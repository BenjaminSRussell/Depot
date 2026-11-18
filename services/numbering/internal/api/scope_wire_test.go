package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/numbering/internal/api"
	"github.com/BenjaminSRussell/depot/services/numbering/internal/domain"
	"github.com/BenjaminSRussell/depot/services/numbering/internal/store"
	"github.com/BenjaminSRussell/depot/services/numbering/migrations"
)

func TestAllocateAndListOverHTTPUnderAppRole(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	entity := testkit.TenantID(9)
	seeder := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: entity})
	if err := dbx.Migrate(seeder, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	if _, err := s.Create(seeder, store.Sequence{Code: "PARTY", Name: "Party", Format: "PTY-{######}", LegalEntityID: entity, Mode: domain.ModeGapFree, StartFrom: 1}, ""); err != nil {
		t.Fatal(err)
	}
	r := httpx.NewRouter()
	api.New(s).Register(r)

	allocate := func(headers map[string]string) int {
		req := httptest.NewRequest(http.MethodPost, "/v1/numbering/sequences/PARTY/next", nil)
		req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
		req.Header.Set(tenant.HeaderDuties, "numbering.write")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	tests := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"scoped caller", map[string]string{tenant.HeaderLegalEntity: entity}, http.StatusOK},
		{"all-scope caller naming the entity", map[string]string{
			tenant.HeaderLegalEntity: entity, tenant.HeaderDuties: "numbering.write," + tenant.DutyAllScope,
		}, http.StatusOK},
		{"all-scope caller without an entity", map[string]string{
			tenant.HeaderDuties: "numbering.write," + tenant.DutyAllScope, tenant.HeaderLegalEntityScopeAll: tenant.LegalEntityScopeAll,
		}, http.StatusForbidden},
		{"caller without an entity", nil, http.StatusForbidden},
		{"multi-entity claim allocates from the entity that owns the sequence", map[string]string{
			tenant.HeaderLegalEntity: testkit.TenantID(8) + "," + entity,
		}, http.StatusOK},
		{"caller scoped to another entity", map[string]string{tenant.HeaderLegalEntity: testkit.TenantID(8)}, http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := allocate(tc.headers); got != tc.want {
				t.Fatalf("status %d, want %d", got, tc.want)
			}
		})
	}

	list := func(headers map[string]string) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/v1/numbering/sequences", nil)
		req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
		req.Header.Set(tenant.HeaderDuties, "numbering.read")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	lists := []struct {
		name    string
		headers map[string]string
		want    int
		body    string
	}{
		{"list for a scoped caller shows the next number", map[string]string{tenant.HeaderLegalEntity: entity}, http.StatusOK, `"code":"PARTY"`},
		{"list for a caller scoped to another entity is empty", map[string]string{tenant.HeaderLegalEntity: testkit.TenantID(8)}, http.StatusOK, `"items":[]`},
		{"list for a caller without an entity", nil, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range lists {
		t.Run(tc.name, func(t *testing.T) {
			if got, body := list(tc.headers); got != tc.want || !strings.Contains(body, tc.body) {
				t.Fatalf("status %d %s, want %d containing %s", got, body, tc.want, tc.body)
			}
		})
	}
}
