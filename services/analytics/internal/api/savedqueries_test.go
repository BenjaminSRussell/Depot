package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/analytics/internal/domain"
)

func asUser(user string, duties ...string) map[string]string {
	return map[string]string{tenant.HeaderUser: user, tenant.HeaderDuties: strings.Join(duties, ",")}
}

const savedBody = `{"name":"Revenue by entity","measures":["revenue","cogs"],"dimensions":["legal_entity_id"],"lookbackDays":90}`

func TestSavedQueries_DutiesAndValidationBeforeTheStore(t *testing.T) {
	h := newTestServer()
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/analytics/saved-queries"}, {http.MethodPost, "/v1/analytics/saved-queries"},
		{http.MethodGet, "/v1/analytics/saved-queries/0193d6a0-0000-7000-8000-000000000001"}, {http.MethodDelete, "/v1/analytics/saved-queries/0193d6a0-0000-7000-8000-000000000001"},
	} {
		if rec := do(t, h, c.method, c.path, "{}", with(map[string]string{}, tenant.HeaderDuties, DutyExport)); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without the view or author duty: %d", c.method, c.path, rec.Code)
		}
	}
	keyed := with(asUser("u1", DutyView), headerIdempotency, "k")
	for name, body := range map[string]string{
		"no measures":    `{"name":"x","measures":[]}`,
		"unknown metric": `{"name":"x","measures":["bogus"]}`,
		"mixed grains":   `{"name":"x","measures":["revenue","otif_pct"]}`,
		"bad dimension":  `{"name":"x","measures":["inventory_turns"],"dimensions":["site_id"]}`,
		"no name":        `{"name":" ","measures":["revenue"]}`,
		"bad lookback":   `{"name":"x","measures":["revenue"],"lookbackDays":4000}`,
	} {
		rec := do(t, h, http.MethodPost, "/v1/analytics/saved-queries", body, keyed)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := do(t, h, http.MethodPost, "/v1/analytics/saved-queries", savedBody, asUser("u1", DutyView)); rec.Code != http.StatusBadRequest {
		t.Errorf("no Idempotency-Key: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/analytics/saved-queries?limit=0", "", asUser("u1", DutyView)); rec.Code != http.StatusBadRequest {
		t.Errorf("limit 0: %d", rec.Code)
	}
}

func TestSavedQueries_PrivateToTheirUserSearchableAndDeletable(t *testing.T) {
	h := dbServer(t, time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
	ana := func(extra ...string) map[string]string { return with(asUser("ana", DutyView), extra...) }
	rec := do(t, h, http.MethodPost, "/v1/analytics/saved-queries", savedBody, ana(headerIdempotency, "k1"))
	var made domain.SavedQuery
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &made) != nil || made.ID == "" || made.LookbackDays != 90 || len(made.Measures) != 2 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if replay := do(t, h, http.MethodPost, "/v1/analytics/saved-queries", savedBody, ana(headerIdempotency, "k1")); replay.Code != http.StatusCreated || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay: %d %v", replay.Code, replay.Header())
	}
	// Another user sending ana's key and body gets no replay of ana's query.
	if other := do(t, h, http.MethodPost, "/v1/analytics/saved-queries", savedBody, with(asUser("bo", DutyView), headerIdempotency, "k1")); other.Code != http.StatusUnprocessableEntity || problemCode(t, other.Body.String()).Code != domain.CodeIdempotencyReuse {
		t.Fatalf("another user's replay of the key: %d %s", other.Code, other.Body)
	}
	if dup := do(t, h, http.MethodPost, "/v1/analytics/saved-queries", savedBody, ana(headerIdempotency, "k2")); dup.Code != http.StatusConflict || problemCode(t, dup.Body.String()).Code != domain.CodeSavedQueryDupe {
		t.Fatalf("same name again: %d %s", dup.Code, dup.Body)
	}
	// Defaults: no dimensions and a 30 day window.
	if rec := do(t, h, http.MethodPost, "/v1/analytics/saved-queries", `{"name":"Margin","measures":["gross_margin_pct"]}`, ana(headerIdempotency, "k3")); rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"dimensions":[]`) || !strings.Contains(rec.Body.String(), `"lookbackDays":30`) {
		t.Fatalf("defaults: %d %s", rec.Code, rec.Body)
	}
	list := do(t, h, http.MethodGet, "/v1/analytics/saved-queries?q=ENTITY", "", ana())
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "Revenue by entity") || strings.Contains(list.Body.String(), "Margin") {
		t.Fatalf("search: %d %s", list.Code, list.Body)
	}
	if exact := do(t, h, http.MethodGet, "/v1/analytics/saved-queries?name=Margin", "", ana()); !strings.Contains(exact.Body.String(), "Margin") || strings.Contains(exact.Body.String(), "Revenue") {
		t.Fatalf("exact name: %s", exact.Body)
	}
	// Another user of the tenant, and an author, see none of ana's queries.
	if other := do(t, h, http.MethodGet, "/v1/analytics/saved-queries", "", asUser("bo", DutyAuthor)); other.Code != http.StatusOK || strings.Contains(other.Body.String(), "Revenue") {
		t.Fatalf("another user: %d %s", other.Code, other.Body)
	}
	path := "/v1/analytics/saved-queries/" + made.ID
	if rec := do(t, h, http.MethodGet, path, "", asUser("bo", DutyView)); rec.Code != http.StatusNotFound {
		t.Fatalf("another user's get: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodDelete, path, "", asUser("bo", DutyView)); rec.Code != http.StatusNotFound {
		t.Fatalf("another user's delete: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, path, "", ana()); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), made.ID) {
		t.Fatalf("own get: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodDelete, path, "", ana()); rec.Code != http.StatusNoContent {
		t.Fatalf("own delete: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, path, "", ana()); rec.Code != http.StatusNotFound {
		t.Fatalf("after delete: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/analytics/saved-queries", "", map[string]string{tenant.HeaderDuties: DutyView}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no user: %d", rec.Code)
	}
}
