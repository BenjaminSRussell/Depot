package api

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

// The invoice feed of the payment-prediction service (#47): the paid invoices it trains on and the open ones it scores, read with a
// service token that carries the one duty receivables.prediction.read.
const dutyPredictionRead = "receivables.prediction.read"

// PredictionFeed is what the feed route reads; *store.Postgres implements it.
type PredictionFeed interface {
	ListPredictionInvoices(ctx context.Context, f store.PredictionFeedFilter, limit int) ([]store.PredictionInvoice, error)
}

// WithPredictionFeed sets the source of GET /v1/receivables/prediction-invoices. Without one the route answers 503.
func (a *API) WithPredictionFeed(f PredictionFeed) *API { a.feed = f; return a }

func (a *API) predictionFeedRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyPredictionRead)).Get("/prediction-invoices", a.listPredictionInvoices)
}

func (a *API) listPredictionInvoices(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	if a.feed == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "AR-1590", "the prediction invoice feed is not configured"))
		return
	}
	q := r.URL.Query()
	f := store.PredictionFeedFilter{}
	switch q.Get("state") {
	case "open", "paid", "reversed":
		f.State = q.Get("state")
	default:
		badQuery(w, r, "state", "must be open, paid or reversed")
		return
	}
	if raw := q.Get("updated_since"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			badQuery(w, r, "updated_since", "must be an RFC 3339 time")
			return
		}
		f.UpdatedSince = t
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	if raw := q.Get("cursor"); raw != "" {
		at, id, ok := parseFeedCursor(raw)
		if !ok {
			badQuery(w, r, "cursor", "is not a cursor this list returned")
			return
		}
		f.AfterAt, f.AfterID = at, id
	}
	items, err := a.feed.ListPredictionInvoices(r.Context(), f, limit+1)
	if err != nil {
		a.fail("list prediction invoices", w, r, err)
		return
	}
	body := map[string]any{"asOf": time.Now().UTC().Format(time.RFC3339)}
	if len(items) > limit {
		items = items[:limit]
		last := items[limit-1]
		body["nextCursor"] = base64.RawURLEncoding.EncodeToString([]byte(last.UpdatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID))
	}
	body["items"] = items
	httpx.JSON(w, http.StatusOK, body)
}

func parseFeedCursor(raw string) (time.Time, string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	at, id, found := strings.Cut(string(decoded), "|")
	when, perr := time.Parse(time.RFC3339Nano, at)
	if err != nil || !found || perr != nil || !ids.Valid(id) {
		return time.Time{}, "", false
	}
	return when, id, true
}
