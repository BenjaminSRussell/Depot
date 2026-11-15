package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/print/internal/delivery"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/statements"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
)

const (
	dutyDeliver      = "print.document.deliver"
	dutyPortalRead   = "print.customer-portal.documents"
	maxScheduleItems = 200
)

// WithMailer sets the e-mail port used for EMAIL destinations. Without one, an EMAIL destination fails with a plain reason.
func WithMailer(m delivery.Mailer) Option { return func(a *API) { a.mailer = m } }

// WithStatementRunner sets the runner behind "run now" on a statement schedule.
func WithStatementRunner(r *statements.Runner) Option { return func(a *API) { a.runner = r } }

// AccountChecker says whether a customer belongs to a legal entity; statements.HTTPSource implements it (party).
type AccountChecker interface {
	CustomerBelongs(ctx context.Context, legalEntityID, customerID string) (bool, error)
}

// WithAccountChecker sets the check that a document's account is a customer of its legal entity before the portal shows it.
func WithAccountChecker(c AccountChecker) Option { return func(a *API) { a.accounts = c } }

// WithStatementTenants lists the tenants the statement runner runs for (PRINT_STATEMENT_TENANT_IDS); a schedule of another tenant is
// reported with runsEnabled false.
func WithStatementTenants(ids []string) Option {
	return func(a *API) {
		a.stmtTenants = map[string]bool{}
		for _, id := range ids {
			a.stmtTenants[id] = true
		}
	}
}

// SetStatementRunner sets the runner behind "run now"; it is set after New because the runner delivers through this API.
func (a *API) SetStatementRunner(r *statements.Runner) { a.runner = r }

// Deliverer returns the deliverer over this API's stores, mailer and printer pipeline, or nil without a delivery store.
func (a *API) Deliverer() *delivery.Deliverer {
	ds, ok := a.store.(store.DeliveryStore)
	if !ok || a.docs == nil {
		return nil
	}
	return &delivery.Deliverer{Docs: a.docs, Log: ds, Mailer: a.mailer, Printer: a.PrintPDF}
}

// PrintPDF sends a PDF to a print queue as a PDF job, once per key.
func (a *API) PrintPDF(ctx context.Context, queueCode string, pdf []byte, key string) error {
	if len(pdf) > domain.MaxDocumentBytes {
		return errors.New("the document is larger than 2 MiB, the most a print job takes")
	}
	p, _ := tenant.FromContext(ctx)
	sum := sha256.Sum256(pdf)
	job := domain.Job{ID: ids.New(), TemplateCode: domain.PDFDocumentTemplate, QueueCode: queueCode, Format: domain.FormatPDF, Status: domain.JobQueued, CreatedBy: p.UserID, ZPL: string(pdf)}
	created, inserted, err := a.store.CreateJob(ctx, store.NewJob{Job: job, RequestHash: hex.EncodeToString(sum[:]), IdempotencyKey: key})
	if err != nil {
		return err
	}
	if !inserted {
		if created.Status != domain.JobPrinted {
			return fmt.Errorf("the print job of an earlier attempt is %s", created.Status)
		}
		return nil
	}
	if err := a.dispatchJob(ctx, &created, nil); err != nil {
		return err
	}
	if created.Status != domain.JobPrinted {
		return fmt.Errorf("no printer of queue %s took the document", queueCode)
	}
	return nil
}

func (a *API) registerDelivery(r chi.Router) {
	admin, read := authz.RequireDuty(dutyPrintAdmin), authz.RequireDuty(dutyPrint)
	r.With(authz.RequireDuty(dutyDeliver)).Post("/documents:deliver", a.deliverDocument)
	r.With(admin).Post("/statement-schedules", a.createSchedule)
	r.With(read).Get("/statement-schedules", a.listSchedules)
	r.With(admin).Patch("/statement-schedules/{id}", a.patchSchedule)
	r.With(admin).Delete("/statement-schedules/{id}", a.deleteSchedule)
	r.With(admin).Post("/statement-schedules/{id}:run", a.runSchedule)
	r.With(read).Get("/statement-schedules/{id}/runs", a.listScheduleRuns)
	r.With(authz.RequireDuty(dutyPortalRead)).Get("/customer-portal/documents", a.listPortalDocuments)
	r.With(authz.RequireDuty(dutyPortalRead)).Get("/customer-portal/documents/{id}/pdf", a.getPortalDocumentPDF)
}

type deliverRequest struct {
	renderRequest
	// SourceKey names the document (an invoice or payment id): delivering it again sends nothing twice.
	SourceKey     string `json:"sourceKey"`
	LegalEntityID string `json:"legalEntityId,omitempty"`
	AccountID     string `json:"accountId,omitempty"`
}

// deliverDocument renders a document and sends it to the destinations of its output plan (46-2): e-mail, the customer portal
// and printer queues. The e-invoice network and the archive come back as skipped, with the reason.
func (a *API) deliverDocument(w http.ResponseWriter, r *http.Request) {
	ctx, ok := a.docgenStore(w, r)
	if !ok {
		return
	}
	d := a.Deliverer()
	if d == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "PRN-0016", "delivery is not available"))
		return
	}
	var req deliverRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.LegalEntityID, req.AccountID = canonicalID(req.LegalEntityID), canonicalID(req.AccountID)
	fields := map[string]string{}
	if n := len(req.SourceKey); n < 1 || n > 200 {
		fields["sourceKey"] = "is required, at most 200 characters"
	}
	if req.LegalEntityID != "" && !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be an id"
	}
	if req.AccountID != "" && !ids.Valid(req.AccountID) {
		fields["accountId"] = "must be an id"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(ctx)
	if req.LegalEntityID == "" {
		// A caller of one legal entity delivers in it; any other caller names the entity.
		if scopes := tenant.LegalEntityScopeList(p); len(scopes) == 1 {
			req.LegalEntityID = scopes[0]
		} else {
			validationFailed(w, r, map[string]string{"legalEntityId": "is required: the legal entity the document is delivered for"})
			return
		}
	}
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside your scope"))
		return
	}
	verified := false
	if req.AccountID != "" && a.accounts != nil {
		sp, err := tenant.ServicePrincipal(statements.Principal, p.TenantID, req.LegalEntityID)
		if err == nil {
			ok, cerr := a.accounts.CustomerBelongs(tenant.With(ctx, sp), req.LegalEntityID, req.AccountID)
			if cerr != nil {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, "PRN-0021", "the customer could not be checked: "+cerr.Error()))
				return
			}
			if !ok {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "PRN-0022", "the account is not a customer of that legal entity"))
				return
			}
			verified = true
		}
	}
	composed, err := a.compose(r, req.renderRequest)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	doc, err := domain.RenderDocumentReport(composed)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	results, err := d.Deliver(context.WithoutCancel(ctx), delivery.Document{SourceKey: req.SourceKey, Type: composed.DocumentType, Number: composed.Number,
		LegalEntityID: req.LegalEntityID, AccountID: req.AccountID, AccountVerified: verified, PDF: doc.PDF, Pages: doc.Pages})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Pages   int               `json:"pages"`
		Results []delivery.Result `json:"results"`
	}{Pages: doc.Pages, Results: results})
}

// scheduleStore returns the schedule store, or answers 503.
func (a *API) scheduleStore(w http.ResponseWriter, r *http.Request) (store.ScheduleStore, bool) {
	if _, ok := principal(w, r); !ok {
		return nil, false
	}
	ss, ok := a.store.(store.ScheduleStore)
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "PRN-0016", "statement schedules are not available"))
		return nil, false
	}
	return ss, true
}

type scheduleRequest struct {
	Name          string `json:"name"`
	LegalEntityID string `json:"legalEntityId"`
	SenderName    string `json:"senderName"`
	Cadence       string `json:"cadence"`
	RunDay        int    `json:"runDay"`
	Language      string `json:"language"`
	TemplateCode  string `json:"templateCode,omitempty"`
}

// createSchedule adds a statement schedule (46-4). Idempotency-Key is required.
func (a *API) createSchedule(w http.ResponseWriter, r *http.Request) {
	ss, ok := a.scheduleStore(w, r)
	if !ok {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req scheduleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Language == "" {
		req.Language = "en"
	}
	in := domain.StatementSchedule{Name: req.Name, LegalEntityID: canonicalID(req.LegalEntityID), SenderName: req.SenderName, Cadence: req.Cadence, RunDay: req.RunDay,
		Language: req.Language, TemplateCode: req.TemplateCode}
	if fields := in.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if !tenant.InLegalEntityScope(p, in.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside your scope"))
		return
	}
	if in.TemplateCode != "" && a.docs != nil {
		t, err := a.docs.GetTemplate(r.Context(), in.TemplateCode)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if t.DocumentType != domain.DocPeriodicStatement {
			validationFailed(w, r, map[string]string{"templateCode": "must be a template of statements of account"})
			return
		}
	}
	out, inserted, err := ss.CreateSchedule(r.Context(), in, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if !inserted {
		w.Header().Set("Idempotent-Replayed", "true")
		status = http.StatusOK
	}
	httpx.JSON(w, status, out)
}

// scheduleView is a schedule with whether the runner runs for its tenant. A schedule of a tenant the runner is not enabled for is
// stored and shown but never runs by itself (PRINT_STATEMENT_TENANT_IDS), and the list says so.
type scheduleView struct {
	domain.StatementSchedule
	RunsEnabled bool `json:"runsEnabled"`
}

// inScope returns the caller's schedules: those of a legal entity in its scope.
func (a *API) schedulesInScope(r *http.Request, ss store.ScheduleStore) ([]domain.StatementSchedule, error) {
	all, err := ss.ListSchedules(r.Context())
	if err != nil {
		return nil, err
	}
	p, _ := tenant.FromContext(r.Context())
	out := all[:0:0]
	for _, s := range all {
		if tenant.InLegalEntityScope(p, s.LegalEntityID) {
			out = append(out, s)
		}
	}
	return out, nil
}

// scheduleInScope loads one schedule and checks its legal entity against the caller's scope; one outside it does not exist for the caller.
func (a *API) scheduleInScope(w http.ResponseWriter, r *http.Request, ss store.ScheduleStore, id string) (domain.StatementSchedule, bool) {
	all, err := a.schedulesInScope(r, ss)
	if err != nil {
		httpx.Fail(w, r, err)
		return domain.StatementSchedule{}, false
	}
	for _, s := range all {
		if s.ID == id {
			return s, true
		}
	}
	httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeScheduleNotFound, "statement schedule not found"))
	return domain.StatementSchedule{}, false
}

func (a *API) listSchedules(w http.ResponseWriter, r *http.Request) {
	ss, ok := a.scheduleStore(w, r)
	if !ok {
		return
	}
	items, err := a.schedulesInScope(r, ss)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	views := make([]scheduleView, 0, len(items))
	for _, s := range items {
		views = append(views, scheduleView{StatementSchedule: s, RunsEnabled: a.stmtTenants[p.TenantID]})
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []scheduleView `json:"items"`
	}{Items: views})
}

// patchSchedule pauses or resumes a schedule.
func (a *API) patchSchedule(w http.ResponseWriter, r *http.Request) {
	ss, ok := a.scheduleStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, domain.CodeScheduleNotFound, "statement schedule")
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Enabled == nil {
		validationFailed(w, r, map[string]string{"enabled": "is required"})
		return
	}
	if _, ok := a.scheduleInScope(w, r, ss, id); !ok {
		return
	}
	out, err := ss.SetScheduleEnabled(r.Context(), id, *req.Enabled)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	ss, ok := a.scheduleStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, domain.CodeScheduleNotFound, "statement schedule")
	if !ok {
		return
	}
	if _, ok := a.scheduleInScope(w, r, ss, id); !ok {
		return
	}
	if err := ss.DeleteSchedule(r.Context(), id); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runSchedule runs one schedule for today now. A statement already delivered today is not sent again.
func (a *API) runSchedule(w http.ResponseWriter, r *http.Request) {
	ss, ok := a.scheduleStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, domain.CodeScheduleNotFound, "statement schedule")
	if !ok {
		return
	}
	sc, ok := a.scheduleInScope(w, r, ss, id)
	if !ok {
		return
	}
	if a.runner == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "PRN-0016", "statement runs are not available: the receivables and party addresses are not configured"))
		return
	}
	p, _ := tenant.FromContext(r.Context())
	sum, err := a.runner.RunNow(context.WithoutCancel(r.Context()), p.TenantID, sc)
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, "PRN-0021", "the statements could not be read: "+err.Error()))
		return
	}
	httpx.JSON(w, http.StatusOK, sum)
}

func (a *API) listScheduleRuns(w http.ResponseWriter, r *http.Request) {
	ss, ok := a.scheduleStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, domain.CodeScheduleNotFound, "statement schedule")
	if !ok {
		return
	}
	if _, ok := a.scheduleInScope(w, r, ss, id); !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := ss.ListStatementRuns(r.Context(), id, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []store.StatementRun `json:"items"`
	}{Items: items})
}

// portalStore returns the delivery store and the customer the call is for, or answers 403/503. The customer is the one in the
// signed token of the customer portal and nothing a caller sends: a document of another customer does not exist for it.
func (a *API) portalStore(w http.ResponseWriter, r *http.Request) (store.DeliveryStore, string, bool) {
	if _, ok := principal(w, r); !ok {
		return nil, "", false
	}
	acct, ok := tenant.PortalCustomer(r.Context())
	if !ok || !ids.Valid(acct) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "only the customer portal, acting for a customer, reads these documents"))
		return nil, "", false
	}
	ds, ok := a.store.(store.DeliveryStore)
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "PRN-0016", "portal documents are not available"))
		return nil, "", false
	}
	return ds, acct, true
}

func (a *API) listPortalDocuments(w http.ResponseWriter, r *http.Request) {
	ds, acct, ok := a.portalStore(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit > maxScheduleItems {
		limit = maxScheduleItems
	}
	items, err := ds.ListPortalDocuments(r.Context(), acct, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, struct {
		Items []store.PortalDocument `json:"items"`
	}{Items: items})
}

func (a *API) getPortalDocumentPDF(w http.ResponseWriter, r *http.Request) {
	ds, acct, ok := a.portalStore(w, r)
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodePortalDocumentNotFound, "document")
	if !ok {
		return
	}
	d, err := ds.GetPortalDocument(r.Context(), id, acct)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="document.pdf"`)
	w.Header().Set("X-Passage-Document-Pages", strconv.Itoa(d.Pages))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(d.PDF)
}
