package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// DutyPIExecutionVerify recomputes the hash chain of a PI execution record. The records themselves are written by the
// service when a kiosk completes a sheet and are read with the view duty; nothing edits them.
const DutyPIExecutionVerify = "shopfloor.piexecution.verify"

func (a *API) registerPIExecutions(r chi.Router) {
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pi-executions", a.listPIExecutions)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pi-executions/{id}", a.getPIExecution)
	r.With(authz.RequireDuty(DutyPIExecutionVerify)).Post("/v1/shopfloor/pi-executions/{id}:verify", a.verifyPIExecution)
}

func (a *API) listPIExecutions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 100, 500)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := map[string]string{}
	for _, k := range []string{"orderId", "recipeId"} {
		if v := q.Get(k); v != "" && !ids.Valid(v) {
			f[k] = "must be a UUID"
		}
	}
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	out, err := a.store.ListPIExecutions(r.Context(), store.PIExecutionFilter{OrderID: strings.ToLower(q.Get("orderId")), RecipeID: strings.ToLower(q.Get("recipeId")), Limit: limit})
	if err != nil {
		a.fail(w, r, "list PI executions", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.PIExecution]{Items: out})
}

func (a *API) getPIExecution(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodePIExecutionNotFound, "PI execution record")
	if !ok {
		return
	}
	out, err := a.store.GetPIExecution(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get PI execution", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) verifyPIExecution(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodePIExecutionNotFound, "PI execution record")
	if !ok {
		return
	}
	out, err := a.store.VerifyPIExecution(r.Context(), id)
	if err != nil {
		a.fail(w, r, "verify PI execution", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
