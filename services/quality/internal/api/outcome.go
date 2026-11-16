package api

import (
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
	"github.com/BenjaminSRussell/passage/services/quality/internal/store"
)

type outcomeRequest struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

func (a *API) createOutcome(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requireKey(w, r)
	if !ok {
		return
	}
	var req outcomeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, err := domain.NewOutcome(req.Code, req.Name)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	out, inserted, err := a.store.CreateOutcome(r.Context(), in, key)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, statusFor(inserted), out)
}

// listOutcomes serves the outcome lookups: q matches the code or name, code is exact, active=true leaves retired ones out.
func (a *API) listOutcomes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, err := limitParam(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.OutcomeFilter{Q: strings.TrimSpace(q.Get("q")), Code: strings.ToUpper(q.Get("code")), ActiveOnly: q.Get("active") == "true", Limit: limit}
	if utf8.RuneCountInString(f.Q) > 100 {
		httpx.Fail(w, r, validation(map[string]string{"q": "must be at most 100 characters"}))
		return
	}
	outcomes, err := a.store.ListOutcomes(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list[domain.Outcome]{Items: outcomes})
}

func (a *API) getOutcome(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeOutcomeMissing, "outcome not found"))
		return
	}
	out, err := a.store.GetOutcome(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

type outcomePatch struct {
	Name   *string `json:"name"`
	Active *bool   `json:"active"`
}

// updateOutcome renames or retires an outcome with If-Match. A retired outcome stays on the results and test groups that
// name it and is left out of pick lists.
func (a *API) updateOutcome(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeOutcomeMissing, "outcome not found"))
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req outcomePatch
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Name == nil && req.Active == nil {
		httpx.Fail(w, r, validation(map[string]string{"name": "send a name or active to change"}))
		return
	}
	if req.Name != nil {
		if err := domain.ValidateOutcomeName(*req.Name); err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return
		}
	}
	out, err := a.store.UpdateOutcome(r.Context(), id, version, req.Name, req.Active)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

// resolveOutcomeCodes fills OutcomeID from OutcomeCode for the inputs that name an outcome by code; an unregistered
// code is QM-0192. Inputs that carry an id are left as they are.
func (a *API) resolveOutcomeCodes(r *http.Request, inputs []resultInput) ([]resultInput, error) {
	var codes []string
	for _, in := range inputs {
		if in.OutcomeID == "" && in.OutcomeCode != "" {
			codes = append(codes, strings.ToUpper(strings.TrimSpace(in.OutcomeCode)))
		}
	}
	if len(codes) == 0 {
		return inputs, nil
	}
	found, err := a.store.OutcomeIDsByCode(r.Context(), codes)
	if err != nil {
		return nil, err
	}
	out := make([]resultInput, len(inputs))
	copy(out, inputs)
	for i, in := range out {
		if in.OutcomeID != "" || in.OutcomeCode == "" {
			continue
		}
		code := strings.ToUpper(strings.TrimSpace(in.OutcomeCode))
		id, ok := found[code]
		if !ok {
			return nil, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeOutcomeUnknown,
				fmt.Sprintf("results[%d].outcomeCode %q is not a registered outcome", i, code))
		}
		out[i].OutcomeID = id
	}
	return out, nil
}

// resolvePassingOutcomes replaces the passing outcome references of every attribute test (a code or an outcome id) with the
// ids of registered outcomes; a reference the master does not hold is a field error.
func (a *API) resolvePassingOutcomes(r *http.Request, tests []domain.TestLine) error {
	var refs []string
	for _, t := range tests {
		refs = append(refs, t.PassingOutcomes...)
	}
	if len(refs) == 0 {
		return nil
	}
	found, err := a.store.ResolveOutcomeRefs(r.Context(), refs)
	if err != nil {
		return err
	}
	fields := map[string]string{}
	for i := range tests {
		for j, ref := range tests[i].PassingOutcomes {
			id, ok := found[ref]
			if !ok {
				fields[fmt.Sprintf("tests[%d].passingOutcomeIds", i)] = fmt.Sprintf("%q is not a registered outcome id or code", ref)
				continue
			}
			tests[i].PassingOutcomes[j] = id
		}
	}
	if len(fields) > 0 {
		return validation(fields)
	}
	return nil
}
