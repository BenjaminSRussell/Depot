package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

type fakeFeed struct{ got store.PredictionFeedFilter }

func (f *fakeFeed) ListPredictionInvoices(_ context.Context, fl store.PredictionFeedFilter, limit int) ([]store.PredictionInvoice, error) {
	f.got = fl
	out := []store.PredictionInvoice{}
	for i := 0; i < limit; i++ {
		out = append(out, store.PredictionInvoice{ID: "00000000-0000-7000-8000-00000000000" + string(rune('1'+i)), UpdatedAt: time.Date(2027, 1, 1, 0, 0, i, 0, time.UTC)})
	}
	return out, nil
}

func feedRequest(a *API, query string, duties ...string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), tenant.Principal{TenantID: "00000000-0000-7000-8000-000000000001", UserID: "service:prediction", Duties: dutySet(duties)})))
		})
	})
	a.Register(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/receivables/prediction-invoices"+query, nil))
	return rec
}

func TestPredictionFeed_NeedsItsDutyAndAStateAndPages(t *testing.T) {
	feed := &fakeFeed{}
	a := New(&fakeStore{}).WithPredictionFeed(feed)
	if rec := feedRequest(a, "?state=open", "receivables.view"); rec.Code != http.StatusForbidden {
		t.Fatalf("without the duty: %d", rec.Code)
	}
	if rec := feedRequest(a, "", dutyPredictionRead); rec.Code != http.StatusBadRequest {
		t.Fatalf("without a state: %d", rec.Code)
	}
	rec := feedRequest(a, "?state=paid&limit=2&updated_since=2027-01-01T00:00:00Z", dutyPredictionRead)
	if rec.Code != http.StatusOK || !feed.got.Paid || feed.got.UpdatedSince.IsZero() || !strings.Contains(rec.Body.String(), `"nextCursor"`) {
		t.Fatalf("paged read: %d %s %+v", rec.Code, rec.Body, feed.got)
	}
	if rec := feedRequest(New(&fakeStore{}), "?state=open", dutyPredictionRead); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no feed: %d", rec.Code)
	}
}

func dutySet(in []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, d := range in {
		out[d] = struct{}{}
	}
	return out
}
