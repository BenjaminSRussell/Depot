package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

// Part 4 (PSG-50): tenant decommission, cost statement PDFs, the licence user-count reporter, the monthly metering job and the
// masked-clone schedules with their job runner.

const (
	// DutyDecommission opens a tenant decommission and reports its steps.
	DutyDecommission = "ops.tenant.decommission"
	// DutyPurge records the verified export of a decommissioned tenant, authorises the purge of its data and reports the result.
	DutyPurge = "ops.tenant.purge"
	// DutyCloneSchedule creates, changes and deletes masked-clone schedules.
	DutyCloneSchedule = "ops.clone.schedule"
	// reporterActor is the service principal ops acts as toward identity and print (platform/go/tenant serviceprincipal.go).
	reporterName = "ops-reporter"
)

// TenantDirectory is identity as ops sees it: a tenant's status and its active named users.
type TenantDirectory interface {
	// Status is the tenant's identity status (active, suspended, deprovisioned) and its active user count.
	Status(ctx context.Context, tenantID string) (status string, activeUsers int, err error)
}

// StatementPrinter renders a statement document to a PDF through print's POST /print/v1/documents:render.
type StatementPrinter interface {
	Render(ctx context.Context, document []byte) ([]byte, error)
}

// CloneRunner runs one masked clone (tools/tenant-restore) and returns what to record.
type CloneRunner interface {
	Clone(ctx context.Context, c domain.CloneSchedule) (detail string, err error)
}

// MeterSource reads a tenant's metered quantities for a month and names the query version they came from.
type MeterSource interface {
	Quantities(ctx context.Context, tenantID, month string) (qty map[string]decimal.Decimal, modelVersion string, err error)
}

// FXSource gives the ECB month-average USD per EUR of a month.
type FXSource interface {
	USDPerEUR(ctx context.Context, month string) (string, error)
}

// ---- identity and print over HTTP ----

// ServiceCaller signs service-token calls as service:ops-reporter in the operator tenant.
type ServiceCaller struct {
	Signer   *tenant.ServiceSigner
	Operator string
	Client   *http.Client
}

func (c ServiceCaller) do(ctx context.Context, method, rawURL, aud string, duty string, body []byte, accept string) (*http.Response, error) {
	if c.Signer == nil {
		return nil, errors.New("no service signing key is configured (PASSAGE_SERVICE_SIGNING_KEY)")
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	p, err := tenant.ServicePrincipal(reporterName, c.Operator, "")
	if err != nil {
		return nil, err
	}
	if _, err := c.Signer.ApplyDuties(req, p, aud, "", []string{duty}); err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return client.Do(req)
}

// IdentityDirectory reads GET /v1/tenants/{id}/user-counts of identity.
type IdentityDirectory struct {
	BaseURL string
	Caller  ServiceCaller
}

// Status implements TenantDirectory.
func (d IdentityDirectory) Status(ctx context.Context, tenantID string) (string, int, error) {
	resp, err := d.Caller.do(ctx, http.MethodGet, strings.TrimRight(d.BaseURL, "/")+"/v1/tenants/"+url.PathEscape(tenantID)+"/user-counts", "identity", "identity.ops.tenant-read", nil, "")
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("identity answered %d for tenant %s", resp.StatusCode, tenantID)
	}
	var out struct {
		Status      string `json:"status"`
		ActiveUsers int    `json:"activeUsers"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", 0, err
	}
	return out.Status, out.ActiveUsers, nil
}

// PrintClient renders through print's POST /print/v1/documents:render with the service-only print.ops.render duty.
type PrintClient struct {
	BaseURL string
	Caller  ServiceCaller
}

// Render implements StatementPrinter.
func (p PrintClient) Render(ctx context.Context, document []byte) ([]byte, error) {
	resp, err := p.Caller.do(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+"/print/v1/documents:render", "print", "print.ops.render", document, "application/pdf")
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("print answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}

// StatementDocument is the print document of a tenant cost statement: one table line per metered dimension, the overhead and the
// totals in USD and EUR. seller names the platform operator on the letterhead.
func StatementDocument(st domain.CostStatement, seller string) map[string]any {
	lines := make([][]string, 0, len(domain.CostDimensions))
	for _, d := range domain.CostDimensions {
		lines = append(lines, []string{d, st.Quantities[d], st.Lines[d]})
	}
	if seller == "" {
		seller = "Passage"
	}
	return map[string]any{
		"documentType": "PERIODIC_STATEMENT",
		"number":       "Cost statement " + st.Month,
		"legalEntity":  map[string]any{"name": seller},
		"fields": []map[string]string{
			{"label": "Tenant", "value": st.TenantID},
			{"label": "Month", "value": st.Month},
			{"label": "Model version", "value": st.ModelVersion},
		},
		"body":    "Metered use of the platform for the month, priced at the published unit rates. Overhead " + st.Overhead + " is added to the subtotal.",
		"columns": []map[string]string{{"header": "Dimension"}, {"header": "Quantity", "align": "R"}, {"header": "Cost USD", "align": "R"}},
		"lines":   lines,
		"totals": []map[string]string{
			{"label": "Total USD", "value": st.CostUSD},
			{"label": "Total EUR (at " + st.USDPerEUR + " USD per EUR, " + st.FXSource + ")", "value": st.CostEUR},
		},
	}
}

// ---- routes ----

func (a *API) registerPart4(g chi.Router) {
	g.With(authz.RequireDuty(DutyDecommission)).Post("/v1/ops/tenants/{id}:decommission", a.decommission)
	g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/tenants/{id}/decommission", a.getDecommission)
	g.With(authz.RequireDuty(DutyDecommission)).Put("/v1/ops/tenants/{id}/decommission-steps/{action}", a.decommissionStep)
	g.With(authz.RequireDuty(DutyPurge)).Put("/v1/ops/tenants/{id}/export-verification", a.verifyExport)
	g.With(authz.RequireDuty(DutyPurge)).Post("/v1/ops/tenants/{id}:purge", a.requestPurge)
	g.With(authz.RequireDuty(DutyPurge)).Put("/v1/ops/tenants/{id}/purge-result", a.purgeResult)
	g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/tenants/{id}/purge", a.getPurge)
	g.With(authz.RequireDuty(DutyCloneSchedule)).Post("/v1/ops/clone-schedules", a.createSchedule)
	g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/clone-schedules", a.listSchedules)
	g.With(authz.RequireDuty(DutyCloneSchedule)).Put("/v1/ops/clone-schedules/{id}", a.setScheduleEnabled)
	g.With(authz.RequireDuty(DutyCloneSchedule)).Delete("/v1/ops/clone-schedules/{id}", a.deleteSchedule)
	g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/clone-schedules/{id}/runs", a.scheduleRuns)
}

type decommissionBody struct {
	Reason string `json:"reason"`
}

// decommission opens the decommission of a tenant: only for a tenant identity reports as suspended (OPS-1731), and audited. A
// repeat returns the first record with 200. The decommission runner (passage-ops decommission) then performs the steps in reverse
// order and reports each with PUT .../decommission-steps/{action}.
func (a *API) decommission(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	var body decommissionBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	reason, err := domain.ValidateDecommissionReason(body.Reason)
	if err != nil {
		httpx.Fail(w, r, store.Problem(err))
		return
	}
	if a.cfg.Directory == nil {
		httpx.Fail(w, r, store.Problem(&domain.Error{Code: domain.CodeDecommissionUnavailable, Detail: "cannot confirm the tenant is suspended: set IDENTITY_URL and the service signing key"}))
		return
	}
	status, _, err := a.cfg.Directory.Status(r.Context(), id)
	if err != nil {
		slog.Error("decommission: identity", "tenant", id, "err", err)
		httpx.Fail(w, r, store.Problem(&domain.Error{Code: domain.CodeDecommissionUnavailable, Detail: "identity did not say whether the tenant is suspended; try again"}))
		return
	}
	if status != "suspended" {
		httpx.Fail(w, r, store.Problem(&domain.Error{Code: domain.CodeDecommissionNotSuspended, Detail: "the tenant is " + status + ": suspend it first, then decommission it"}))
		return
	}
	d, replay, err := a.store.OpenDecommission(r.Context(), id, reason, p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	code := http.StatusCreated
	if replay {
		code = http.StatusOK
	}
	slog.Info("tenant decommission opened", "tenant", id, "requestedBy", p.UserID, "replay", replay)
	httpx.JSON(w, code, d)
}

func (a *API) getDecommission(w http.ResponseWriter, r *http.Request) {
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	d, err := a.store.Decommission(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	trail, err := a.store.Audit(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"decommission": d, "audit": trail})
}

func (a *API) decommissionStep(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	action := chi.URLParam(r, "action")
	if !domain.ValidDecommissionStep(action) {
		httpx.Fail(w, r, validation("action", "must be one of "+strings.Join(domain.DecommissionSteps, ", ")))
		return
	}
	var body provisionStepBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if body.Status != "DONE" && body.Status != "FAILED" {
		httpx.Fail(w, r, validation("status", "must be DONE or FAILED"))
		return
	}
	if len(body.Detail) > 2000 {
		httpx.Fail(w, r, validation("detail", "at most 2000 characters"))
		return
	}
	d, err := a.store.RecordDecommissionStep(r.Context(), id, action, body.Status, body.Detail, p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

// ---- statement PDF ----

// costPDF answers GET /v1/ops/tenants/{id}/costs/{month} for Accept: application/pdf (or ?format=pdf): the statement rendered by print.
func (a *API) costPDF(w http.ResponseWriter, r *http.Request, rec store.CostRecord) {
	if a.cfg.Printer == nil {
		httpx.Fail(w, r, store.Problem(&domain.Error{Code: domain.CodeCostUnavailable, Detail: "PDF statements are rendered by print: set PRINT_URL and the service signing key"}))
		return
	}
	doc, err := json.Marshal(StatementDocument(rec.CostStatement, a.cfg.Seller))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	pdf, err := a.cfg.Printer.Render(r.Context(), doc)
	if err != nil {
		slog.Error("statement pdf", "tenant", rec.TenantID, "month", rec.Month, "err", err)
		httpx.Fail(w, r, store.Problem(&domain.Error{Code: domain.CodeCostUnavailable, Detail: "the statement could not be rendered; try again"}))
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cost-%s-%s.pdf"`, rec.TenantID, rec.Month))
	_, _ = w.Write(pdf)
}

// ---- licence user-count reporter ----

// ReportUsersOnce counts the active named users of every licensed tenant in identity and records them as the licence's usage, as the
// operator (service:ops-reporter). Identity has no licence class on a user yet, so every active user counts as a full user and the
// operational and self-service counts are 0; the hourly licence check compares them with the licence's limits. A tenant identity cannot
// answer for keeps its last counts. It returns how many tenants were reported.
func (a *API) ReportUsersOnce(ctx context.Context) int {
	if a.cfg.Directory == nil {
		return 0
	}
	licences, err := a.store.Licenses(ctx)
	if err != nil {
		slog.Error("user counts", "err", err)
		return 0
	}
	n := 0
	for _, l := range licences {
		id := l.License.TenantID
		_, users, err := a.cfg.Directory.Status(ctx, id)
		if err != nil {
			slog.Warn("user counts: identity did not answer", "tenant", id, "err", err)
			continue
		}
		if _, err := a.store.ReportLicenseUsage(ctx, id, store.LicenseUsage{ActiveFull: users}, "service:"+reporterName); err != nil {
			slog.Error("user counts: record", "tenant", id, "err", err)
			continue
		}
		n++
	}
	return n
}

// ---- monthly metering ----

// MeterOnce produces the cost statement of the month before now for every provisioned tenant that has none yet: the quantities from
// the MeterSource, the rates of OPS_COST_RATES and the month's EUR rate. It is idempotent (a tenant with a statement is skipped, and
// a restatement stays an explicit PUT), so every replica and every check tick may run it. It returns the statements recorded.
func (a *API) MeterOnce(ctx context.Context, now time.Time) int {
	if a.cfg.Meter == nil || a.cfg.FX == nil || a.cfg.CostRates == nil {
		return 0
	}
	month := previousMonth(now)
	provisions, err := a.store.Provisions(ctx)
	if err != nil {
		slog.Error("metering", "err", err)
		return 0
	}
	rate, rateErr := a.cfg.FX.USDPerEUR(ctx, month)
	n := 0
	for _, p := range provisions {
		if p.Status != domain.ProvisionDone {
			continue
		}
		// Only "no statement for this month" lets the job go on: any other read error skips the tenant, so a manually restated
		// statement is never overwritten on a database hiccup.
		if _, err := a.store.Cost(ctx, p.TenantID, month); err == nil {
			continue
		} else if !isCostUnknown(err) {
			slog.Error("metering: reading the statement", "tenant", p.TenantID, "month", month, "err", err)
			continue
		}
		if rateErr != nil {
			slog.Error("metering: no EUR rate", "month", month, "err", rateErr)
			return n
		}
		qty, version, err := a.cfg.Meter.Quantities(ctx, p.TenantID, month)
		if err != nil {
			slog.Error("metering: quantities", "tenant", p.TenantID, "month", month, "err", err)
			continue
		}
		st, err := domain.ComputeCost(p.TenantID, month, qty, *a.cfg.CostRates, rate, version)
		if err != nil {
			slog.Error("metering: statement", "tenant", p.TenantID, "month", month, "err", err)
			continue
		}
		if _, err := a.store.RecordCost(ctx, st, "service:"+reporterName); err != nil {
			slog.Error("metering: record", "tenant", p.TenantID, "month", month, "err", err)
			continue
		}
		n++
	}
	return n
}

// PromMeter reads the six quantities of a tenant's month from Prometheus. Each dimension has a query template in which {tenant} is the
// tenant id and {range} the month as a range selector; the query returns one value (the first sample is used). ModelVersion names the
// query set, and is recorded on the statement so a month stays reproducible.
type PromMeter struct {
	Prom         PromSource
	Queries      map[string]string
	ModelVersion string
}

// ParseMeterQueries reads OPS_METER_QUERIES: {"cpu_core_hours":"sum(...{tenant=\"{tenant}\"}[{range}])/3600",...}. Every dimension is required.
func ParseMeterQueries(raw string) (map[string]string, error) {
	var q map[string]string
	if err := json.Unmarshal([]byte(raw), &q); err != nil {
		return nil, fmt.Errorf("must be a JSON object of dimension to query: %w", err)
	}
	for _, d := range domain.CostDimensions {
		if !strings.Contains(q[d], "{tenant}") {
			return nil, fmt.Errorf("%s needs a query that uses {tenant}", d)
		}
	}
	return q, nil
}

// Quantities implements MeterSource. The range selector is the month's length in hours.
func (m PromMeter) Quantities(ctx context.Context, tenantID, month string) (map[string]decimal.Decimal, string, error) {
	start, err := time.Parse("2006-01", month)
	if err != nil {
		return nil, "", err
	}
	hours := int(start.AddDate(0, 1, 0).Sub(start) / time.Hour)
	qty := map[string]decimal.Decimal{}
	for _, d := range domain.CostDimensions {
		q := strings.NewReplacer("{tenant}", tenantID, "{range}", fmt.Sprintf("%dh", hours)).Replace(m.Queries[d])
		at := start.AddDate(0, 1, 0)
		v, err := m.Prom.scalar(ctx, q, at)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", d, err)
		}
		qty[d] = v
	}
	return qty, m.ModelVersion, nil
}

// StaticFX gives one USD-per-EUR rate for every month (OPS_USD_PER_EUR). The ECB month average is not looked up: whoever sets the
// rate sets it for the month being metered.
type StaticFX string

// USDPerEUR implements FXSource.
func (f StaticFX) USDPerEUR(context.Context, string) (string, error) {
	if f == "" {
		return "", errors.New("OPS_USD_PER_EUR is not set")
	}
	return string(f), nil
}

// ---- masked-clone schedules ----

type scheduleBody struct {
	Name          string `json:"name"`
	SourceTenant  string `json:"sourceTenant"`
	Schema        string `json:"schema"`
	MasksFile     string `json:"masksFile"`
	IntervalHours int    `json:"intervalHours"`
}

func (a *API) createSchedule(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	var body scheduleBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	source, ok := ids.Canonical(body.SourceTenant)
	if !ok || source == ids.Nil {
		httpx.Fail(w, r, validation("sourceTenant", "must be the tenant to clone (a UUID)"))
		return
	}
	c, err := domain.ValidateCloneSchedule(domain.CloneSchedule{Name: body.Name, SourceTenant: source, Schema: body.Schema, MasksFile: body.MasksFile, IntervalHours: body.IntervalHours})
	if err != nil {
		httpx.Fail(w, r, store.Problem(err))
		return
	}
	made, err := a.store.CreateCloneSchedule(r.Context(), c, p.UserID, a.now())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, made)
}

func (a *API) listSchedules(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.CloneSchedules(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func scheduleParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := ids.Canonical(chi.URLParam(r, "id"))
	if !ok || id == ids.Nil {
		httpx.Fail(w, r, validation("id", "must be a schedule id (a UUID)"))
		return "", false
	}
	return id, true
}

func (a *API) setScheduleEnabled(w http.ResponseWriter, r *http.Request) {
	id, ok := scheduleParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if body.Enabled == nil {
		httpx.Fail(w, r, validation("enabled", "is required"))
		return
	}
	c, err := a.store.SetCloneScheduleEnabled(r.Context(), id, *body.Enabled, a.now(), actorOf(r))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

func (a *API) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, ok := scheduleParam(w, r)
	if !ok {
		return
	}
	if err := a.store.DeleteCloneSchedule(r.Context(), id, actorOf(r)); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) scheduleRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := scheduleParam(w, r)
	if !ok {
		return
	}
	runs, err := a.store.CloneRuns(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": runs})
}

// CloneOnce claims the schedules that are due and runs each through the CloneRunner, recording the outcome. A schedule is claimed by
// one replica per period (ClaimDueClones), and a failed run is recorded and retried at the next period, not in a loop. It returns the
// runs made.
func (a *API) CloneOnce(ctx context.Context) int {
	if a.cfg.Cloner == nil {
		return 0
	}
	lease := a.cloneLease()
	due, err := a.store.ClaimDueClones(ctx, a.now(), lease)
	if err != nil {
		slog.Error("clone schedules", "err", err)
		return 0
	}
	for _, c := range due {
		started := a.now()
		runCtx, cancel := context.WithTimeout(ctx, lease-time.Minute)
		detail, err := a.cfg.Cloner.Clone(runCtx, c)
		cancel()
		status := "OK"
		if err != nil {
			status, detail = "FAILED", err.Error()
			slog.Error("masked clone failed", "schedule", c.Name, "err", err)
		}
		if len(detail) > 2000 {
			detail = detail[len(detail)-2000:]
		}
		if err := a.store.RecordCloneRun(ctx, c.ID, started, a.now(), status, detail); err != nil {
			slog.Error("clone run record", "schedule", c.Name, "err", err)
		}
	}
	return len(due)
}

// CommandCloner runs tools/tenant-restore's `clone` command: `tenant-restore clone --tenant <id> --schema <schema> --masks <file> --apply`.
// The source and target database URLs come from the process environment (PASSAGE_CLONE_SOURCE_URL, PASSAGE_CLONE_TARGET_URL), which
// the child inherits; they are never put on the command line.
type CommandCloner struct {
	// Command is the tenant-restore executable (OPS_CLONE_COMMAND, default tenant-restore).
	Command string
	// Run starts the command and returns its combined output; nil means exec.CommandContext.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Clone implements CloneRunner.
func (c CommandCloner) Clone(ctx context.Context, s domain.CloneSchedule) (string, error) {
	name := c.Command
	if name == "" {
		name = "tenant-restore"
	}
	args := []string{"clone", "--tenant", s.SourceTenant, "--schema", s.Schema, "--masks", s.MasksFile, "--apply"}
	run := c.Run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	out, err := run(ctx, name, args...)
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("%s clone: %w", name, err)
	}
	return text, nil
}

// previousMonth is the calendar month before now's, as YYYY-MM. It starts from the first of the month: AddDate on the 29th to 31st
// would overflow into the current month (2026-03-30 minus one month is "March 2").
func previousMonth(now time.Time) string {
	n := now.UTC()
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Format("2006-01")
}

func isCostUnknown(err error) bool {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code == domain.CodeCostUnknown
	}
	var rule *domain.Error
	return errors.As(err, &rule) && rule.Code == domain.CodeCostUnknown
}

func actorOf(r *http.Request) string {
	p, _ := tenant.FromContext(r.Context())
	return p.UserID
}

// cloneLease is how long a claimed clone may run before another replica may claim the schedule again (Config.CloneLease, default 6 h).
func (a *API) cloneLease() time.Duration {
	if a.cfg.CloneLease > 2*time.Minute {
		return a.cfg.CloneLease
	}
	return 6 * time.Hour
}

// ---- purge of a decommissioned tenant's data ----

type exportBody struct {
	ExportID string `json:"exportId"`
	Note     string `json:"note"`
}

// verifyExport records that the tenant's export was verified (who, when, which export). Needs the decommission DECOMMISSIONED.
func (a *API) verifyExport(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	var body exportBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rec, err := a.store.VerifyExport(r.Context(), id, strings.TrimSpace(body.ExportID), strings.TrimSpace(body.Note), p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rec)
}

// requestPurge authorises the purge (decommission DECOMMISSIONED, export verified) and audits the request. `passage-ops tenant purge`
// calls it, then deletes with its own purge credentials, then reports with PUT .../purge-result.
func (a *API) requestPurge(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	rec, err := a.store.RequestPurge(r.Context(), id, p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	slog.Info("tenant purge requested", "tenant", id, "requestedBy", p.UserID)
	httpx.JSON(w, http.StatusOK, rec)
}

func (a *API) purgeResult(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	var body provisionStepBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(body.Detail) > 4000 {
		httpx.Fail(w, r, validation("detail", "at most 4000 characters"))
		return
	}
	rec, err := a.store.RecordPurgeResult(r.Context(), id, body.Status, body.Detail, p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rec)
}

func (a *API) getPurge(w http.ResponseWriter, r *http.Request) {
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	rec, err := a.store.Purge(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rec)
}
