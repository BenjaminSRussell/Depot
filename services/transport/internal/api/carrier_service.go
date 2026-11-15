package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

// limitParam reads ?limit= (1 to store.ListLimit); an empty value is 0, the store's default.
func limitParam(fields map[string]string, raw string) int {
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > store.ListLimit {
		fields["limit"] = "must be a whole number from 1 to " + strconv.Itoa(store.ListLimit)
		return 0
	}
	return n
}

type carrierServiceRequest struct {
	CarrierID string `json:"carrierId"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
}

func (a *API) createCarrierService(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req carrierServiceRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	svc := domain.CarrierService{CarrierID: req.CarrierID, Code: strings.ToUpper(strings.TrimSpace(req.Code)), Name: req.Name, Mode: req.Mode, Active: true}
	if fields := svc.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, err := a.store.CreateCarrierService(r.Context(), svc, idem)
	if err != nil {
		a.fail(w, r, "create carrier service failed", err)
		return
	}
	httpx.JSON(w, http.StatusCreated, created)
}

// listCarrierServices serves the lookup: q matches the code or name, code is exact.
func (a *API) listCarrierServices(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.CarrierServiceFilter{CarrierID: q.Get("carrierId"), Q: strings.TrimSpace(q.Get("q")), Code: strings.ToUpper(q.Get("code")), ActiveOnly: q.Get("active") == "true"}
	fields := map[string]string{}
	checkOptionalID(fields, "carrierId", f.CarrierID)
	f.Limit = limitParam(fields, q.Get("limit"))
	if len([]rune(f.Q)) > 100 {
		fields["q"] = "must be at most 100 characters"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	list, err := a.store.ListCarrierServices(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list carrier services failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func (a *API) getCarrierService(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("carrier service"))
		return
	}
	svc, err := a.store.GetCarrierService(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get carrier service failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, svc)
}
