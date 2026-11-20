package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/fulfillment/internal/store"
)

func (a *API) routesSlipGL(r chi.Router) {
	r.With(authz.RequireDuty("fulfillment.slip-gl.release")).Post("/v1/fulfillment/packing-slips/{id}:release-gl", a.releaseSlipGL)
}

func (a *API) releaseSlipGL(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSlipGLNotFound, "no packing slip journal found for this slip"))
		return
	}
	_, idem, err := command(r, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	g, err := a.store.ReleaseSlipGL(r.Context(), id, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, g)
}
