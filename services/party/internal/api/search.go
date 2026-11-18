package api

import (
	"encoding/base64"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/party/internal/store"
)

const maxSearchText = 100

func (a *API) WithSearch(search store.SearchStore) *API {
	a.search = search
	return a
}

func (a *API) registerSearch(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/parties:search", a.searchRoute(store.SearchParties))
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/vendors:search", a.searchRoute(store.SearchVendors))
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/customers:search", a.searchRoute(store.SearchCustomers))
}

type searchResponse struct {
	Items      []store.SearchHit `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

func badSearchParam(w http.ResponseWriter, r *http.Request, field, rule string) {
	p := httpx.NewProblem(http.StatusBadRequest, codeInvalid, "search parameters failed validation")
	p.Errors = map[string]string{field: rule}
	httpx.Fail(w, r, p)
}

func encodeSearchCursor(key store.SearchKey) string {
	score := strconv.FormatFloat(float64(key.Score), 'g', -1, 32)
	return base64.RawURLEncoding.EncodeToString([]byte(key.Name + "\x00" + key.ID + "\x00" + score))
}

func decodeSearchCursor(cursor string) (store.SearchKey, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	parts := strings.Split(string(raw), "\x00")
	if err != nil || len(parts) != 3 || !ids.Valid(parts[1]) {
		return store.SearchKey{}, false
	}
	score, err := strconv.ParseFloat(parts[2], 32)
	if err != nil || math.IsNaN(score) || score < 0 || score > 1 {
		return store.SearchKey{}, false
	}
	return store.SearchKey{Name: parts[0], ID: parts[1], Score: float32(score)}, true
}

func (a *API) searchRoute(kind store.SearchKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		query := r.URL.Query()
		text := strings.TrimSpace(query.Get("q"))
		if len([]rune(text)) > maxSearchText {
			badSearchParam(w, r, "q", "must be at most 100 characters")
			return
		}
		limit := store.MaxSearchPage
		if raw := query.Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > store.MaxSearchPage {
				badSearchParam(w, r, "limit", "must be a whole number from 1 to 50")
				return
			}
			limit = n
		}
		legalEntityID := query.Get("legalEntityId")
		if legalEntityID != "" && !ids.Valid(legalEntityID) {
			badSearchParam(w, r, "legalEntityId", "must be a UUID")
			return
		}
		var after store.SearchKey
		if cursor := query.Get("cursor"); cursor != "" {
			key, ok := decodeSearchCursor(cursor)
			if !ok {
				badSearchParam(w, r, "cursor", "is invalid")
				return
			}
			after = key
		}
		page, err := a.search.Search(r.Context(), kind, text, legalEntityID, after, limit)
		if err != nil {
			failParty(w, r, err)
			return
		}
		body := searchResponse{Items: page.Items}
		if page.Next != nil {
			body.NextCursor = encodeSearchCursor(*page.Next)
		}
		httpx.JSON(w, http.StatusOK, body)
	}
}
