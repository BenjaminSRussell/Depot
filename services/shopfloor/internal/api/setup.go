package api

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/registry"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/store"
)

const maxCodeLen = 40

func (a *API) registerSetup(r chi.Router) {
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/time-profiles", a.createProfile)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/time-profiles", a.listProfiles)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/time-profiles/{id}", a.getProfile)
	r.With(authz.RequireDuty(DutySetup)).Put("/v1/shopfloor/time-profiles/{id}", a.updateProfile)
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/pay-agreements", a.createAgreement)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pay-agreements", a.listAgreements)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pay-agreements/{id}", a.getAgreement)
	r.With(authz.RequireDuty(DutySetup)).Put("/v1/shopfloor/pay-agreements/{id}", a.updateAgreement)
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/workers/{id}:assign-setup", a.assignSetup)
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/workers/{id}:assign-resource", a.assignResource)
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/workers/{id}:assign-legal-entity", a.assignLegalEntity)
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/workers:assign-legal-entity", a.assignLegalEntities)
}

func etag(version int) string { return `"` + strconv.Itoa(version) + `"` }

// ifMatch reads If-Match as the version the caller last read. The header is required.
func ifMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	n, err := strconv.Atoi(strings.Trim(raw, `"`))
	if raw == "" || err != nil || n < 1 || !strings.HasPrefix(raw, `"`) || !strings.HasSuffix(raw, `"`) {
		validationFailed(w, r, map[string]string{"If-Match": `header is required: the version you read as an entity tag, for example "3"`})
		return 0, false
	}
	return n, true
}

func pathID(w http.ResponseWriter, r *http.Request, code, what string) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, code, what+" not found"))
		return "", false
	}
	return id, true
}

func activeFilter(w http.ResponseWriter, r *http.Request) (bool, bool) {
	switch r.URL.Query().Get("active") {
	case "", "false":
		return false, true
	case "true":
		return true, true
	}
	validationFailed(w, r, map[string]string{"active": "must be true or false"})
	return false, false
}

type profileRequest struct {
	Code   string                   `json:"code"`
	Name   string                   `json:"name"`
	Active *bool                    `json:"active"`
	Days   []domain.ProfileDayInput `json:"days"`
}

func (req profileRequest) input(creating bool) (store.TimeProfileInput, map[string]string) {
	_, fields := domain.ProfileFromDays(req.Days)
	code, name := strings.TrimSpace(req.Code), strings.TrimSpace(req.Name)
	if creating && (len(code) < 1 || len(code) > maxCodeLen) {
		fields["code"] = "must be 1 to 40 characters"
	}
	if len(name) < 1 || len(name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	active := req.Active == nil || *req.Active
	return store.TimeProfileInput{Code: code, Name: name, Active: active, Days: req.Days}, fields
}

func (a *API) createProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a time profile")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req profileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(true)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.CreateTimeProfile(r.Context(), in, user, idem)
	if err != nil {
		a.fail(w, r, "create time profile", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) updateProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a time profile")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeProfileNotFound, "time profile")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req profileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.UpdateTimeProfile(r.Context(), id, version, in, user)
	if err != nil {
		a.fail(w, r, "update time profile", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeProfileNotFound, "time profile")
	if !ok {
		return
	}
	out, err := a.store.GetTimeProfile(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get time profile", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listProfiles(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	active, ok := activeFilter(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListTimeProfiles(r.Context(), active, limit)
	if err != nil {
		a.fail(w, r, "list time profiles", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.TimeProfile]{Items: items})
}

type agreementRequest struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Active *bool  `json:"active"`
	domain.AgreementInput
}

func (req agreementRequest) input(creating bool) (store.PayAgreementInput, map[string]string) {
	rules, fields := req.AgreementInput.Rules()
	code, name := strings.TrimSpace(req.Code), strings.TrimSpace(req.Name)
	if creating && (len(code) < 1 || len(code) > maxCodeLen) {
		fields["code"] = "must be 1 to 40 characters"
	}
	if len(name) < 1 || len(name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	return store.PayAgreementInput{Code: code, Name: name, Active: req.Active == nil || *req.Active, Rules: rules}, fields
}

func (a *API) createAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a pay agreement")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req agreementRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(true)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.CreatePayAgreement(r.Context(), in, user, idem)
	if err != nil {
		a.fail(w, r, "create pay agreement", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) updateAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a pay agreement")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeAgreementNotFound, "pay agreement")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req agreementRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.UpdatePayAgreement(r.Context(), id, version, in, user)
	if err != nil {
		a.fail(w, r, "update pay agreement", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeAgreementNotFound, "pay agreement")
	if !ok {
		return
	}
	out, err := a.store.GetPayAgreement(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get pay agreement", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listAgreements(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	active, ok := activeFilter(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListPayAgreements(r.Context(), active, limit)
	if err != nil {
		a.fail(w, r, "list pay agreements", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.PayAgreement]{Items: items})
}

type assignRequest struct {
	ProfileID      *string `json:"profileId"`
	PayAgreementID *string `json:"payAgreementId"`
}

func (a *API) assignSetup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "assigning a pay setup")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeWorkerNotFound, "worker")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req assignRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.ProfileID != nil && !ids.Valid(*req.ProfileID) {
		fields["profileId"] = "must be a UUID or null"
	}
	if req.PayAgreementID != nil && !ids.Valid(*req.PayAgreementID) {
		fields["payAgreementId"] = "must be a UUID or null"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.AssignWorkerSetup(r.Context(), id, version, req.ProfileID, req.PayAgreementID, user, idem)
	if err != nil {
		a.fail(w, r, "assign worker setup", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

type assignResourceRequest struct {
	ResourceID *string `json:"resourceId"`
}

// assignResource sets or clears the worker's home resource, whose jobs the worker's
// dispatch list shows (GET jobs?workerId=).
func (a *API) assignResource(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "assigning a home resource")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeWorkerNotFound, "worker")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req assignResourceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.ResourceID != nil && !ids.Valid(*req.ResourceID) {
		validationFailed(w, r, map[string]string{"resourceId": "must be a UUID or null"})
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.AssignWorkerResource(r.Context(), id, version, req.ResourceID, user, idem)
	if err != nil {
		a.fail(w, r, "assign worker resource", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

type assignLegalEntityRequest struct {
	LegalEntityID string `json:"legalEntityId"`
}

// assignLegalEntity sets the employing legal entity of a worker created before workers carried one, or corrects it
// while the worker has no time or pay (B-03b, D17). Only a tenant administrator may (no legal-entity claim,
// tenant.administer and the setup duty): no scoped caller can see such a worker. A worker with time or pay in another
// legal entity is SF-0200.
func (a *API) assignLegalEntity(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "assigning a legal entity")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeWorkerNotFound, "worker")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req assignLegalEntityRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	entity, valid := ids.Canonical(strings.TrimSpace(req.LegalEntityID))
	if !valid || entity == ids.Nil {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	if !a.legalEntityUsable(w, r, entity) {
		return
	}
	out, err := a.store.AssignWorkerLegalEntity(r.Context(), id, entity, DutySetup, user, idem)
	if errors.Is(err, dbx.ErrNotTenantWide) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "only a tenant administrator may assign a worker's legal entity"))
		return
	}
	if err != nil {
		a.fail(w, r, "assign worker legal entity", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

type assignLegalEntitiesRequest struct {
	LegalEntityID string   `json:"legalEntityId"`
	WorkerIDs     []string `json:"workerIds"`
	AllUnassigned bool     `json:"allUnassigned"`
	DryRun        bool     `json:"dryRun"`
}

// assignLegalEntities assigns one legal entity to many workers created before workers carried one: the listed
// workerIds (at most store.MaxBulkLegalEntityWorkers), or with allUnassigned every worker of the tenant that has none
// (B-03b, the deploy-time backfill). Tenant administrators only, as for one worker. Workers already in another legal
// entity are reported as conflicts and left alone; export batches whose lines all belong to the legal entity's workers
// get it too.
func (a *API) assignLegalEntities(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "assigning a legal entity")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req assignLegalEntitiesRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	entity, valid := ids.Canonical(strings.TrimSpace(req.LegalEntityID))
	if !valid || entity == ids.Nil {
		fields["legalEntityId"] = "must be a UUID"
	}
	workers := make([]string, 0, len(req.WorkerIDs))
	seen := map[string]bool{}
	for _, raw := range req.WorkerIDs {
		id, ok := ids.Canonical(strings.TrimSpace(raw))
		if !ok || id == ids.Nil {
			fields["workerIds"] = "must be UUIDs"
			break
		}
		if !seen[id] {
			seen[id] = true
			workers = append(workers, id)
		}
	}
	switch {
	case req.AllUnassigned && len(req.WorkerIDs) > 0:
		fields["workerIds"] = "must be empty with allUnassigned"
	case !req.AllUnassigned && len(req.WorkerIDs) == 0:
		fields["workerIds"] = "name at least one worker, or set allUnassigned"
	case len(workers) > store.MaxBulkLegalEntityWorkers:
		fields["workerIds"] = "at most " + strconv.Itoa(store.MaxBulkLegalEntityWorkers) + " workers per call"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !req.DryRun && a.replayed(w, r, idem) {
		return
	}
	if !a.legalEntityUsable(w, r, entity) {
		return
	}
	out, err := a.store.AssignWorkersLegalEntity(r.Context(), store.BulkLegalEntityAssignment{LegalEntityID: entity, WorkerIDs: workers, AllUnassigned: req.AllUnassigned, DryRun: req.DryRun}, DutySetup, user, idem)
	if errors.Is(err, dbx.ErrNotTenantWide) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "only a tenant administrator may assign workers' legal entity"))
		return
	}
	if err != nil {
		a.fail(w, r, "assign workers legal entity", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// Legal-entity check codes (B-03b).
const (
	// CodeLegalEntityUnknown: identity knows no active legal entity with this id in the tenant (422).
	CodeLegalEntityUnknown = "SF-0201"
	// CodeLegalEntityCheckUnavailable: identity could not confirm the legal entity, or the check is not configured
	// (IDENTITY_URL); nothing was assigned (503).
	CodeLegalEntityCheckUnavailable = "SF-0202"
)

// legalEntityUsable refuses a legal entity identity does not confirm as an active one of the tenant, before anything is
// written: an assignment freezes once a worker has time or pay, so a mistyped id must never be stored (B-03b). Only a
// tenant administrator gets this far (the store refuses anyone else, and the check's service token needs one), so the
// caller is checked first. An identity outage is 503, never a pass.
func (a *API) legalEntityUsable(w http.ResponseWriter, r *http.Request, entity string) bool {
	p, _ := tenant.FromContext(r.Context())
	if !dbx.TenantWide(p, DutySetup) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "only a tenant administrator may assign workers' legal entity"))
		return false
	}
	if a.legalEntities == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeLegalEntityCheckUnavailable, "the legal-entity check against identity is not configured (IDENTITY_URL)"))
		return false
	}
	active, err := a.legalEntities.Active(r.Context(), entity)
	switch {
	case errors.Is(err, registry.ErrLegalEntityUnknown), err == nil && !active:
		p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeLegalEntityUnknown, "the tenant has no active legal entity with this id")
		p.Errors = map[string]string{"legalEntityId": "must be an active legal entity of the tenant"}
		httpx.Fail(w, r, p)
		return false
	case err != nil:
		slog.ErrorContext(r.Context(), "legal-entity check failed", "err", err)
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeLegalEntityCheckUnavailable, "identity could not confirm the legal entity; nothing was assigned"))
		return false
	}
	return true
}
