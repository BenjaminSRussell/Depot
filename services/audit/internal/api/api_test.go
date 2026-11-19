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
	"github.com/BenjaminSRussell/depot/services/audit/internal/api"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
)

type fakeStore struct {
	err     error
	ping    error
	applied []domain.Change
	records []store.Record
	alerts  []store.Alert
	readStore
	lastFilter store.ReadFilter
	entities   []store.EntitySummary

	lastRecordFilter store.RecordFilter
	chain            store.ChangeChain
	verifyRange      [2]int64
}

func (f *fakeStore) Apply(_ context.Context, change domain.Change) error {
	if f.err != nil {
		return f.err
	}
	f.applied = append(f.applied, change)
	return nil
}
func (f *fakeStore) Query(context.Context, string, string) ([]store.Record, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.records == nil {
		return []store.Record{}, nil
	}
	return f.records, nil
}
func (f *fakeStore) ListRecords(_ context.Context, flt store.RecordFilter) ([]store.Record, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.lastRecordFilter = flt
	out := []store.Record{}
	for _, rec := range f.records {
		match := (flt.Entity == "" || rec.Entity == flt.Entity) && (flt.RecordID == "" || rec.RecordID == flt.RecordID) &&
			(flt.UserID == "" || rec.UserID == flt.UserID) && (flt.Field == "" || rec.Field == flt.Field) &&
			(flt.From == nil || !rec.OccurredAt.Before(*flt.From)) && (flt.To == nil || rec.OccurredAt.Before(*flt.To)) && rec.Seq > flt.AfterSeq
		if match {
			out = append(out, rec)
		}
		if flt.Limit > 0 && len(out) == flt.Limit {
			break
		}
	}
	return out, nil
}
func (f *fakeStore) ListEntities(context.Context) ([]store.EntitySummary, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entities, nil
}
func (f *fakeStore) CreateRule(_ context.Context, rule store.AlertRule) (store.AlertRule, error) {
	if f.err != nil {
		return store.AlertRule{}, f.err
	}
	rule.ID = "rule-1"
	return rule, nil
}
func (f *fakeStore) ListAlerts(context.Context) ([]store.Alert, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.alerts == nil {
		return []store.Alert{}, nil
	}
	return f.alerts, nil
}
func (f *fakeStore) Ping(context.Context) error { return f.ping }
func (f *fakeStore) VerifyChanges(_ context.Context, fromSeq, toSeq int64) (store.ChangeChain, error) {
	if f.err != nil {
		return store.ChangeChain{}, f.err
	}
	f.verifyRange = [2]int64{fromSeq, toSeq}
	return f.chain, nil
}

func newRouter(f *fakeStore) (*chi.Mux, *api.API) {
	a := api.New(f)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func do(r http.Handler, method, path, body, duties string, withTenant bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if withTenant {
		req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Depot-User", "user-1")
		req.Header.Set("X-Depot-Duties", duties)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const (
	changes = "/v1/audit/changes"
	record  = "/v1/audit/records/party.vendor/v-1"
	duties  = "audit.read,audit.write"
	valid   = `{"entity":"party.vendor","recordId":"v-1","field":"name","oldValue":"Acme","newValue":"Acme Ltd","traceId":"trace-abc"}`
)

func TestAuditHTTP(t *testing.T) {
	recAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	viewer := &fakeStore{records: []store.Record{{
		ID: "e1", Entity: "party.vendor", RecordID: "v-1", Field: "name",
		OldValue: "Acme", NewValue: "Acme Ltd", UserID: "user-1", TraceID: "trace-abc", OccurredAt: recAt,
	}}}
	tests := []struct {
		name, method, path, body, duty string
		store                          *fakeStore
		withTenant                     bool
		wantStatus                     int
		wantBody                       string
	}{
		// Identity headers are not proof of the caller on this route: only a verified service records a change.
		{"ingest from identity headers", http.MethodPost, changes, valid, duties, &fakeStore{}, true, http.StatusForbidden, "AUD-0007"},
		{"ingest malformed from identity headers", http.MethodPost, changes, `{`, duties, &fakeStore{}, true, http.StatusForbidden, "AUD-0007"},
		{"ingest without tenant", http.MethodPost, changes, valid, "", &fakeStore{}, false, http.StatusUnauthorized, "AUTH-0001"},
		{"ingest without duty", http.MethodPost, changes, valid, "audit.read", &fakeStore{}, true, http.StatusForbidden, "AUTH-0003"},
		{"query viewer", http.MethodGet, record, "", duties, viewer, true, http.StatusOK, `"traceId":"trace-abc"`},
		{"query empty", http.MethodGet, record, "", duties, &fakeStore{}, true, http.StatusOK, `"items":[]`},
		{"expand", http.MethodPost, "/v1/audit/texts/expand", `{"body":"Hi {name}","vars":{"name":"Ada"}}`, "", &fakeStore{}, true, http.StatusOK, `"Hi Ada"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duty, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if len(tc.store.applied) != 0 {
				t.Fatalf("stored %+v", tc.store.applied)
			}
		})
	}
}

func TestIngestFromAVerifiedService(t *testing.T) {
	tests := []struct {
		name, body string
		store      *fakeStore
		wantStatus int
		wantBody   string
	}{
		{"ok", valid, &fakeStore{}, http.StatusAccepted, `"recorded"`},
		{"malformed", `{`, &fakeStore{}, http.StatusBadRequest, "COM-0001"},
		{"missing field", `{"entity":"party.vendor","recordId":"v-1","oldValue":"a","newValue":"b","traceId":"t"}`, &fakeStore{}, http.StatusBadRequest, `"field"`},
		{"store error", valid, &fakeStore{err: errors.New("secret")}, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(verifiedRouter(tc.store, viaService()), tc.body)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestIngestThenQueryIsTheTwoSecondViewerPath(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	start := time.Now()
	rec := post(verifiedRouter(f, viaService()), valid)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("ingest %d %s", rec.Code, rec.Body.String())
	}
	f.records = []store.Record{{
		Entity: "party.vendor", RecordID: "v-1", Field: "name",
		OldValue: "Acme", NewValue: "Acme Ltd", UserID: "user-1", TraceID: "trace-abc", OccurredAt: time.Now().UTC(),
	}}
	rec = do(r, http.MethodGet, record, "", duties, true)
	if time.Since(start) > 2*time.Second {
		t.Fatal("viewer slower than 2s")
	}
	for _, need := range []string{`"userId":"user-1"`, `"oldValue":"Acme"`, `"newValue":"Acme Ltd"`, `"traceId":"trace-abc"`} {
		if !strings.Contains(rec.Body.String(), need) {
			t.Fatalf("viewer missing %s in %s", need, rec.Body.String())
		}
	}
}

func TestReadyAndMetrics(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	if rec := do(r, http.MethodGet, "/readyz", "", "", false); rec.Code != http.StatusOK {
		t.Fatalf("ready %d", rec.Code)
	}
	down, _ := newRouter(&fakeStore{ping: errors.New("down")})
	if rec := do(down, http.MethodGet, "/readyz", "", "", false); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("down %d", rec.Code)
	}
	svc, a := verifiedAPI(&fakeStore{}, viaService())
	post(svc, valid)
	w := httptest.NewRecorder()
	a.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), "audit_changes_applied_total 1") {
		t.Fatalf("metrics %s", w.Body.String())
	}
}
