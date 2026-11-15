package api

import (
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/store"
)

// DutyAuditPolicyMaintain creates, renames, re-versions and retires audit policies. The run duty reads them.
const DutyAuditPolicyMaintain = "compliance.audit-policy.maintain"

var policyCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,31}$`)

// zeroPolicyID is the placeholder id a spec is validated under: EvaluateAudit rejects a rule that is not configured
// before it looks at a document, so an empty window checks the spec alone.
const zeroPolicyID = "00000000-0000-0000-0000-000000000000"

func (a *API) registerAuditPolicies(r chi.Router) {
	r.With(authz.RequireDuty(DutyAuditPolicyMaintain)).Post("/v1/compliance/audit-policies", a.createAuditPolicy)
	r.With(authz.RequireAnyDuty(DutyAuditPolicyRun, DutyAuditPolicyMaintain)).Get("/v1/compliance/audit-policies", a.listAuditPolicies)
	r.With(authz.RequireAnyDuty(DutyAuditPolicyRun, DutyAuditPolicyMaintain)).Get("/v1/compliance/audit-policies/{id}", a.getAuditPolicy)
	r.With(authz.RequireDuty(DutyAuditPolicyMaintain)).Patch("/v1/compliance/audit-policies/{id}", a.updateAuditPolicy)
}

// validateSpec runs the rule's own configuration checks and the list-size limits of the evaluate route.
func validateSpec(spec domain.AuditSpec) error {
	if len(spec.Keywords) > maxAuditListItems || len(spec.WatchList) > maxAuditListItems ||
		len(spec.Holidays) > maxAuditListItems || len(spec.Limits) > maxAuditListItems {
		return invalid(map[string]string{"spec": "too many keywords, watch-list entries, holidays or limits"})
	}
	if _, err := domain.EvaluateAudit(zeroPolicyID, 1, spec, nil); err != nil {
		return domainProblem(err)
	}
	return nil
}

func (a *API) createAuditPolicy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	var in store.AuditPolicyInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in.Code = strings.ToUpper(strings.TrimSpace(in.Code))
	fields := map[string]string{}
	if !policyCode.MatchString(in.Code) {
		fields["code"] = "must be 2 to 32 characters of A-Z, 0-9, _ or -"
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(in.Name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if in.Spec.Type == "" {
		fields["spec.type"] = "is required"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	if err := validateSpec(in.Spec); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.CreateAuditPolicy(r.Context(), in, key, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusCreated, out)
}

// listAuditPolicies serves the policy lookups: q matches the code or name, code is exact, active=true leaves retired ones out.
func (a *API) listAuditPolicies(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.AuditPolicyFilter{Q: strings.TrimSpace(q.Get("q")), Code: strings.ToUpper(q.Get("code")), ActiveOnly: q.Get("active") == "true", Limit: 100}
	fields := map[string]string{}
	if utf8.RuneCountInString(f.Q) > 100 {
		fields["q"] = "must be at most 100 characters"
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 500 {
			fields["limit"] = "must be a whole number from 1 to 500"
		} else {
			f.Limit = n
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	items, err := a.store.ListAuditPolicies(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getAuditPolicy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetAuditPolicy(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) updateAuditPolicy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var in store.AuditPolicyPatch
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if in.Name == nil && in.Spec == nil && in.Active == nil {
		httpx.Fail(w, r, invalid(map[string]string{"name": "send a name, spec or active to change"}))
		return
	}
	if in.Name != nil {
		if n := utf8.RuneCountInString(strings.TrimSpace(*in.Name)); n < 1 || n > 120 {
			httpx.Fail(w, r, invalid(map[string]string{"name": "must be 1 to 120 characters"}))
			return
		}
	}
	if in.Spec != nil {
		if in.Spec.Type == "" {
			httpx.Fail(w, r, invalid(map[string]string{"spec.type": "is required"}))
			return
		}
		if err := validateSpec(*in.Spec); err != nil {
			httpx.Fail(w, r, err)
			return
		}
	}
	out, err := a.store.UpdateAuditPolicy(r.Context(), id, version, in, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}
