package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// DutyPICategoryMaintain maintains the PI category master that groups step templates (FR-25.15, D7).
const DutyPICategoryMaintain = "shopfloor.picategory.maintain"

// registerPICategories mounts the PI category master routes.
func (a *API) registerPICategories(r chi.Router) {
	r.With(authz.RequireDuty(DutyPICategoryMaintain)).Post("/v1/shopfloor/pi-categories", a.createPICategory)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pi-categories", a.listPICategories)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pi-categories/{id}", a.getPICategory)
	r.With(authz.RequireDuty(DutyPICategoryMaintain)).Put("/v1/shopfloor/pi-categories/{id}", a.updatePICategory)
}

type piCategoryRequest struct {
	Code        string `json:"code"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Active      *bool  `json:"active"`
}

func (req piCategoryRequest) input(creating bool) (store.PICategoryInput, map[string]string) {
	f := map[string]string{}
	in := store.PICategoryInput{Code: strings.TrimSpace(req.Code), Name: strings.TrimSpace(req.Name), Description: strings.TrimSpace(req.Description), Active: req.Active == nil || *req.Active}
	if creating && !piCode.MatchString(in.Code) {
		f["code"] = "must be 1 to 40 characters: upper-case letters, digits, '_', '.' or '-'"
	}
	if n := len(in.Name); n < 1 || n > 120 {
		f["name"] = "must be 1 to 120 characters"
	}
	if len(in.Description) > 500 {
		f["description"] = "must be at most 500 characters"
	}
	return in, f
}

func (a *API) createPICategory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a PI category")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req piCategoryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, f := req.input(true)
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.CreatePICategory(r.Context(), in, user, idem)
	if err != nil {
		a.fail(w, r, "create PI category", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) updatePICategory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a PI category")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodePICategoryNotFound, "PI category")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req piCategoryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, f := req.input(false)
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	out, err := a.store.UpdatePICategory(r.Context(), id, version, in, user)
	if err != nil {
		a.fail(w, r, "update PI category", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getPICategory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodePICategoryNotFound, "PI category")
	if !ok {
		return
	}
	out, err := a.store.GetPICategory(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get PI category", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listPICategories(w http.ResponseWriter, r *http.Request) {
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
	items, err := a.store.ListPICategories(r.Context(), active, limit)
	if err != nil {
		a.fail(w, r, "list PI categories", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.PICategory]{Items: items})
}
