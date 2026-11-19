package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
)

const (
	leA      = "11111111-1111-4111-8111-111111111111"
	leB      = "22222222-2222-4222-8222-222222222222"
	listPath = "/v1/audit/records"
	t0       = "2026-10-24T12:00:00Z"
)

func getAs(f *fakeStore, path, duties, legalEntity string) *httptest.ResponseRecorder {
	r, _ := newRouter(f)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Depot-User", "user-1")
	req.Header.Set("X-Depot-Duties", duties)
	if legalEntity != "" {
		req.Header.Set("X-Depot-Legal-Entity", legalEntity)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func history() []store.Record {
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	return []store.Record{
		{Seq: 1, Entity: "party.vendor", RecordID: "v-1", Field: "name", OldValue: "Acme", NewValue: "Acme Ltd", UserID: "user-1", OccurredAt: at, LegalEntityID: leA},
		{Seq: 2, Entity: "vendor_bank_account", RecordID: "b-1", Field: "account_number", OldValue: "DE0000000001234", NewValue: "DE0000000005678", UserID: "user-2", OccurredAt: at.Add(time.Hour), LegalEntityID: leA},
		{Seq: 3, Entity: "party.vendor", RecordID: "v-1", Field: "name", OldValue: "Acme Ltd", NewValue: "Acme GmbH", UserID: "user-1", OccurredAt: at.Add(2 * time.Hour)},
	}
}

func TestListRecordsFiltersAndPages(t *testing.T) {
	f := &fakeStore{records: history()}
	rec := getAs(f, listPath+"?entity=party.vendor&userId=user-1&field=name&from="+t0+"&to=2026-10-25T00:00:00Z", "audit.read", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered %d %s", rec.Code, rec.Body.String())
	}
	got := f.lastRecordFilter
	if got.Entity != "party.vendor" || got.UserID != "user-1" || got.Field != "name" || got.From == nil || got.To == nil {
		t.Fatalf("filter not forwarded: %+v", got)
	}
	first := getAs(f, listPath+"?limit=2", "audit.read", "")
	if !strings.Contains(first.Body.String(), `"nextCursor"`) || strings.Contains(first.Body.String(), `"seq":3`) {
		t.Fatalf("first page %s", first.Body.String())
	}
	cursor := between(first.Body.String(), `"nextCursor":"`, `"`)
	second := getAs(f, listPath+"?limit=2&cursor="+cursor, "audit.read", "")
	if !strings.Contains(second.Body.String(), `"seq":3`) || strings.Contains(second.Body.String(), `"seq":2`) || strings.Contains(second.Body.String(), "nextCursor") {
		t.Fatalf("second page %s", second.Body.String())
	}
}

func between(s, open, closing string) string {
	i := strings.Index(s, open) + len(open)
	return s[i : i+strings.Index(s[i:], closing)]
}

func TestListRecordsRefusals(t *testing.T) {
	tests := []struct {
		name, query, duties string
		store               *fakeStore
		want                int
		body                string
	}{
		{"no duty", "", "party.read", &fakeStore{}, http.StatusForbidden, "AUTH-0003"},
		{"from not a time", "?from=2026-13-40", "audit.read", &fakeStore{}, http.StatusBadRequest, `"from"`},
		{"to before from", "?from=" + t0 + "&to=" + t0, "audit.read", &fakeStore{}, http.StatusBadRequest, `"to"`},
		{"limit zero", "?limit=0", "audit.read", &fakeStore{}, http.StatusBadRequest, `"limit"`},
		{"limit too big", "?limit=501", "audit.read", &fakeStore{}, http.StatusBadRequest, `"limit"`},
		{"limit text", "?limit=x", "audit.read", &fakeStore{}, http.StatusBadRequest, `"limit"`},
		{"cursor junk", "?cursor=***", "audit.read", &fakeStore{}, http.StatusBadRequest, `"cursor"`},
		{"cursor not a number", "?cursor=YWJj", "audit.read", &fakeStore{}, http.StatusBadRequest, `"cursor"`},
		{"cursor zero", "?cursor=MA", "audit.read", &fakeStore{}, http.StatusBadRequest, `"cursor"`},
		{"field too long", "?field=" + strings.Repeat("x", 257), "audit.read", &fakeStore{}, http.StatusBadRequest, `"field"`},
		{"store down", "", "audit.read", &fakeStore{err: errors.New("secret")}, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := getAs(tc.store, listPath+tc.query, tc.duties, "")
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.body) || strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.want, tc.body)
			}
		})
	}
}

func TestListRecordsRequiresATenant(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	rec := do(r, http.MethodGet, listPath, "", "audit.read", false)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "AUTH-0001") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestListRecordsMasksSensitiveValuesAndKeepsRowsWithoutActorKind(t *testing.T) {
	f := &fakeStore{records: history()}
	body := getAs(f, listPath, "audit.read", "").Body.String()
	if strings.Contains(body, "DE0000000001234") || !strings.Contains(body, "****1234") || !strings.Contains(body, `"newValue":"Acme GmbH"`) {
		t.Fatalf("masked list %s", body)
	}
	if body := getAs(f, listPath, "audit.read,audit.export", "").Body.String(); !strings.Contains(body, "DE0000000001234") {
		t.Fatalf("export duty must see the value: %s", body)
	}
}

func TestListEntitiesCoversEntitiesWithoutChanges(t *testing.T) {
	last := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	f := &fakeStore{entities: []store.EntitySummary{{Entity: "party.vendor", Changes: 4, LastChangeAt: last}}}
	rec := getAs(f, "/v1/audit/entities", "audit.read", "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"entity":"party.vendor","changes":4,"lastChangeAt":"2026-10-24T12:00:00Z","recorded":true`) ||
		!strings.Contains(body, `"entity":"worker","changes":0,"recorded":false`) {
		t.Fatalf("entities %d %s", rec.Code, body)
	}
	if rec := getAs(f, "/v1/audit/entities", "party.read", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("no duty %d", rec.Code)
	}
	if rec := getAs(&fakeStore{err: errors.New("down")}, "/v1/audit/entities", "audit.read", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("store down %d", rec.Code)
	}
}

func TestPerRecordRouteAuthorizesByTheRecordReadDuty(t *testing.T) {
	const bank = "/v1/audit/records/vendor_bank_account/b-1"
	tests := []struct {
		name, path, duties, le string
		want                   int
	}{
		{"audit.read", bank, "audit.read", "", http.StatusOK},
		{"own read duty in scope", bank, "party.read", leA, http.StatusOK},
		{"own read duty, other legal entity", bank, "party.read", leB, http.StatusForbidden},
		{"own read duty without a scope", bank, "party.read", "", http.StatusForbidden},
		{"another record's duty", bank, "product.read", leA, http.StatusForbidden},
		{"no duty", bank, "", leA, http.StatusForbidden},
		{"unmapped entity without audit.read", "/v1/audit/records/mystery/x-1", "party.read", leA, http.StatusForbidden},
		{"unmapped entity with audit.read", "/v1/audit/records/mystery/x-1", "audit.read", "", http.StatusOK},
		{"unknown order", bank + "?order=up", "audit.read", "", http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if rec := getAs(&fakeStore{records: history()[1:2]}, tc.path, tc.duties, tc.le); rec.Code != tc.want {
				t.Fatalf("got %d %s; want %d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}

// A tenant-level row has no legal entity: it is master data for the whole tenant, so the record's own read
// duty is enough. Without the duty it is still refused.
func TestPerRecordRouteShowsATenantLevelRowToARecordDutyHolder(t *testing.T) {
	f := &fakeStore{records: history()[2:3]}
	if rec := getAs(f, "/v1/audit/records/party.vendor/v-1", "party.read", leA); rec.Code != http.StatusOK {
		t.Fatalf("with the read duty: %d %s", rec.Code, rec.Body.String())
	}
	if rec := getAs(f, "/v1/audit/records/party.vendor/v-1", "product.read", leA); rec.Code != http.StatusForbidden {
		t.Fatalf("with another record's duty: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPerRecordRouteMasksAndOrders(t *testing.T) {
	f := &fakeStore{records: history()[:2]}
	masked := getAs(f, "/v1/audit/records/vendor_bank_account/b-1", "party.read", leA).Body.String()
	if strings.Contains(masked, "DE0000000001234") || !strings.Contains(masked, "****5678") {
		t.Fatalf("not masked: %s", masked)
	}
	clear := getAs(f, "/v1/audit/records/vendor_bank_account/b-1", "party.read,audit.export", leA).Body.String()
	if !strings.Contains(clear, "DE0000000005678") {
		t.Fatalf("export duty must see the value: %s", clear)
	}
	f = &fakeStore{records: []store.Record{{Seq: 1, Entity: "e", RecordID: "r", Field: "a"}, {Seq: 2, Entity: "e", RecordID: "r", Field: "b"}}}
	if body := getAs(f, "/v1/audit/records/e/r?order=desc", "audit.read", "").Body.String(); strings.Index(body, `"seq":2`) > strings.Index(body, `"seq":1`) {
		t.Fatalf("not newest first: %s", body)
	}
}

func TestRecordQueriesAreCounted(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	req := httptest.NewRequest(http.MethodGet, listPath, nil)
	req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Depot-Duties", "audit.read")
	r.ServeHTTP(httptest.NewRecorder(), req)
	w := httptest.NewRecorder()
	a.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), "audit_record_queries_total 1") {
		t.Fatalf("metrics %s", w.Body.String())
	}
}
