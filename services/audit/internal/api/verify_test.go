package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
)

// wide is a caller with tenant-wide authority: no legal-entity claim and the all-scope duty.
const wide = "audit.verify,org.switcher.all-scope"

func TestVerifyChangesRoute(t *testing.T) {
	broken := store.ChangeChain{Verified: false, Entries: 4, FromSeq: 1, ToSeq: 4, Breaks: 1,
		FirstBreak: &domain.Break{Seq: 3, ID: "e3", Kind: domain.BreakHash}, KnownBreaks: store.KnownBreaks{LegacyPrecision: 1}}
	for _, tc := range []struct {
		name, path, duties string
		store              *fakeStore
		status             int
		want               string
		bounds             [2]int64
	}{
		{"whole chain", "/v1/audit/records:verify", wide, &fakeStore{chain: store.ChangeChain{Verified: true, Entries: 2}}, http.StatusOK, `"verified":true`, [2]int64{}},
		{"range with a break", "/v1/audit/records:verify?fromSeq=1&toSeq=4", wide, &fakeStore{chain: broken}, http.StatusOK, `"firstBreak":{"seq":3,"id":"e3","kind":"hash-mismatch"}`, [2]int64{1, 4}},
		{"read duty does not verify", "/v1/audit/records:verify", "audit.read", &fakeStore{}, http.StatusForbidden, "AUTH-0003", [2]int64{}},
		{"bad fromSeq", "/v1/audit/records:verify?fromSeq=0", wide, &fakeStore{}, http.StatusBadRequest, domain.CodeVerifyRange, [2]int64{}},
		{"reversed range", "/v1/audit/records:verify?fromSeq=5&toSeq=2", wide, &fakeStore{}, http.StatusBadRequest, domain.CodeVerifyRange, [2]int64{}},
		{"store error", "/v1/audit/records:verify", wide, &fakeStore{err: errors.New("down")}, http.StatusInternalServerError, "COM-0500", [2]int64{}},
		// Review finding 5: the verdict spans every legal entity, so a caller without tenant-wide authority is refused.
		{"no tenant-wide authority", "/v1/audit/records:verify", "audit.verify", &fakeStore{}, http.StatusForbidden, domain.CodeVerifyScope, [2]int64{}},
		{"tenant administrator", "/v1/audit/records:verify", "audit.verify,tenant.administer", &fakeStore{chain: store.ChangeChain{Verified: true}}, http.StatusOK, `"verified":true`, [2]int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			// The all-scope duty is kept only on an explicit ALL-scope read (tenant.Middleware).
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
			req.Header.Set("X-Depot-User", "auditor")
			req.Header.Set("X-Depot-Duties", tc.duties)
			req.Header.Set(tenant.HeaderLegalEntityScopeAll, tenant.LegalEntityScopeAll)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if tc.status == http.StatusOK {
				if tc.store.verifyRange != tc.bounds {
					t.Fatalf("range %v, want %v", tc.store.verifyRange, tc.bounds)
				}
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				for _, k := range []string{"verified", "entries", "fromSeq", "toSeq", "head", "breaks", "knownBreaks", "anchors"} {
					if _, ok := body[k]; !ok {
						t.Errorf("verdict has no %s: %s", k, rec.Body.String())
					}
				}
			}
		})
	}
}
