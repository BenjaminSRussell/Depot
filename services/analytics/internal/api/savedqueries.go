package api

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/store"
)

// registerSavedQueries mounts the saved explorer queries. They are private to their user, so anyone who views analytics or
// authors schedules may save, list and delete their own.
func (a *API) registerSavedQueries(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireAnyDuty(DutyView, DutyAuthor))
		g.Get("/v1/analytics/saved-queries", a.listSavedQueries)
		g.Post("/v1/analytics/saved-queries", a.createSavedQuery)
		g.Get("/v1/analytics/saved-queries/{id}", a.getSavedQuery)
		g.Delete("/v1/analytics/saved-queries/{id}", a.deleteSavedQuery)
	})
}

// SavedQueryRequest is the body of POST /analytics/saved-queries.
type SavedQueryRequest struct {
	Name         string   `json:"name"`
	Measures     []string `json:"measures"`
	Dimensions   []string `json:"dimensions"`
	LookbackDays int      `json:"lookbackDays"`
}

// SavedQueriesResponse lists saved queries.
type SavedQueriesResponse struct {
	Items []domain.SavedQuery `json:"items"`
}

func (a *API) createSavedQuery(w http.ResponseWriter, r *http.Request) {
	key, raw, ok := idempotentBody(w, r)
	if !ok {
		return
	}
	var req SavedQueryRequest
	if err := httpx.Decode(r, &req); err != nil {
		refuse(w, r, "request body is not valid JSON", nil)
		return
	}
	in := domain.SavedQuery{Name: strings.TrimSpace(req.Name), Measures: req.Measures, Dimensions: req.Dimensions, LookbackDays: req.LookbackDays}
	if in.LookbackDays == 0 {
		in.LookbackDays = domain.DefaultLookbackDays
	}
	if verr := domain.ValidateSavedQuery(in, a.catalog); verr != nil {
		fail(w, r, verr)
		return
	}
	out, err := a.store.CreateSavedQuery(r.Context(), key, requestHash(r, raw), in, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCreated(w, out)
}

// listSavedQueries serves the saved query lookup: q matches part of the name, name is exact.
func (a *API) listSavedQueries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.SavedQueryFilter{Q: strings.TrimSpace(q.Get("q")), Name: q.Get("name"), Limit: 100}
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
		refuse(w, r, "request failed validation", fields)
		return
	}
	items, err := a.store.ListSavedQueries(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, SavedQueriesResponse{Items: items})
}

func (a *API) getSavedQuery(w http.ResponseWriter, r *http.Request) {
	out, err := a.store.GetSavedQuery(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) deleteSavedQuery(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteSavedQuery(r.Context(), chi.URLParam(r, "id")); err != nil {
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
