package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/numbering/internal/api"
	"github.com/BenjaminSRussell/depot/services/numbering/internal/store"
)

type fakeStore struct {
	err    error
	ping   error
	seq    store.Sequence
	issued store.Issued
	items  []store.Sequence
	res    store.Reservation
	voided []store.VoidedNumber
}

func (f *fakeStore) Create(_ context.Context, in store.Sequence, _ string) (store.Sequence, error) {
	if f.err != nil {
		return store.Sequence{}, f.err
	}
	in.ID = "id-1"
	f.seq = in
	return in, nil
}
func (f *fakeStore) Get(context.Context, string) (store.Sequence, error) {
	if f.err != nil {
		return store.Sequence{}, f.err
	}
	return f.seq, nil
}
func (f *fakeStore) List(_ context.Context, after string, limit int) ([]store.Sequence, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}
func (f *fakeStore) Allocate(context.Context, string, string) (store.Issued, error) {
	if f.err != nil {
		return store.Issued{}, f.err
	}
	return f.issued, nil
}
func (f *fakeStore) Reserve(context.Context, string, string, time.Duration) (store.Reservation, error) {
	return f.res, f.err
}
func (f *fakeStore) Confirm(context.Context, string, string) (store.Reservation, error) {
	return f.res, f.err
}
func (f *fakeStore) Void(context.Context, string, string) (store.Reservation, error) {
	return f.res, f.err
}
func (f *fakeStore) Voided(context.Context, string, string, int) ([]store.VoidedNumber, error) {
	return f.voided, f.err
}
func (f *fakeStore) Ping(context.Context) error { return f.ping }

func newRouter(f *fakeStore) (*chi.Mux, *api.API) {
	a := api.New(f)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func do(r http.Handler, method, path, body, duties string, withTenant bool, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if withTenant {
		req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Depot-Legal-Entity", testkit.TenantID(9))
		req.Header.Set("X-Depot-Duties", duties)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const (
	createPath = "/v1/numbering/sequences"
	nextPath   = "/v1/numbering/sequences/INV/next"
	allDuties  = "numbering.read,numbering.write"
	validBody  = `{"code":"INV","name":"Invoices","format":"INV-{######}","legalEntityId":"00000000-0000-0000-0000-000000000009","mode":"gap_free","startFrom":1}`
)

func TestSequences(t *testing.T) {
	ok := &fakeStore{issued: store.Issued{Value: 1, Formatted: "INV-000001", Sequence: "INV", LegalEntityID: testkit.TenantID(9)}}
	tests := []struct {
		name, method, path, body, duties string
		store                            *fakeStore
		withTenant                       bool
		wantStatus                       int
		wantBody                         string
	}{
		{"create ok", http.MethodPost, createPath, validBody, allDuties, ok, true, http.StatusCreated, `"code":"INV"`},
		{"create without tenant", http.MethodPost, createPath, validBody, "", ok, false, http.StatusUnauthorized, "AUTH-0001"},
		{"create malformed", http.MethodPost, createPath, `{`, allDuties, ok, true, http.StatusBadRequest, "COM-0001"},
		{"create unknown field", http.MethodPost, createPath, `{"code":"INV","x":1}`, allDuties, ok, true, http.StatusBadRequest, "COM-0001"},
		{"create bad mode", http.MethodPost, createPath, `{"code":"INV","name":"n","format":"INV-{######}","legalEntityId":"00000000-0000-0000-0000-000000000009","mode":"other","startFrom":1}`, allDuties, ok, true, http.StatusBadRequest, `"mode"`},
		{"create without duty", http.MethodPost, createPath, validBody, "numbering.read", ok, true, http.StatusForbidden, "AUTH-0003"},
		{"create store error", http.MethodPost, createPath, validBody, allDuties, &fakeStore{err: errors.New("pg secret")}, true, http.StatusInternalServerError, "COM-0500"},
		{"allocate ok", http.MethodPost, nextPath, "", allDuties, ok, true, http.StatusOK, `"formatted":"INV-000001"`},
		{"allocate with the narrow service duty", http.MethodPost, nextPath, "", "numbering.sequence.allocate", ok, true, http.StatusOK, `"formatted":"INV-000001"`},
		{"create with the narrow service duty", http.MethodPost, createPath, validBody, "numbering.sequence.allocate", ok, true, http.StatusForbidden, "AUTH-0003"},
		{"allocate without duty", http.MethodPost, nextPath, "", "numbering.read", ok, true, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestListSequences(t *testing.T) {
	one := store.Sequence{ID: testkit.TenantID(5), Code: "INV", Name: "Invoices", Format: "INV-{######}", LegalEntityID: testkit.TenantID(9), Mode: "gap_free", StartFrom: 1}
	tests := []struct {
		name, query, duties string
		store               *fakeStore
		withTenant          bool
		wantStatus          int
		wantBody            string
	}{
		{"last page has no next", "", allDuties, &fakeStore{items: []store.Sequence{one}}, true, http.StatusOK, `"next":""`},
		{"full page returns the last id as next", "?limit=1", allDuties, &fakeStore{items: []store.Sequence{one}}, true, http.StatusOK, `"next":"` + one.ID + `"`},
		{"empty list is an empty array", "", allDuties, &fakeStore{}, true, http.StatusOK, `"items":[]`},
		{"without tenant", "", "", &fakeStore{}, false, http.StatusUnauthorized, "AUTH-0001"},
		{"without read duty", "", "numbering.write", &fakeStore{}, true, http.StatusForbidden, "AUTH-0003"},
		{"limit out of range", "?limit=0", allDuties, &fakeStore{}, true, http.StatusBadRequest, `"limit"`},
		{"limit not a number", "?limit=x", allDuties, &fakeStore{}, true, http.StatusBadRequest, `"limit"`},
		{"cursor not a uuid", "?after=nope", allDuties, &fakeStore{}, true, http.StatusBadRequest, `"after"`},
		{"store error", "", allDuties, &fakeStore{err: errors.New("pg secret")}, true, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, http.MethodGet, createPath+tc.query, "", tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "pg secret") {
				t.Fatalf("store error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestReadyAndMetrics(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	rec := do(r, http.MethodGet, "/readyz", "", "", false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ready") {
		t.Fatalf("ready: %d %s", rec.Code, rec.Body.String())
	}
	down, _ := newRouter(&fakeStore{ping: errors.New("down")})
	rec = do(down, http.MethodGet, "/readyz", "", "", false)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("down ready: %d", rec.Code)
	}
	do(r, http.MethodPost, nextPath, "", allDuties, true)
	w := httptest.NewRecorder()
	a.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), "numbering_allocated_total 1") {
		t.Fatalf("metrics: %s", w.Body.String())
	}
}
