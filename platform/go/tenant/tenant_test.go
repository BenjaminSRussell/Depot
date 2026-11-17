package tenant_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	validTenant = "0190a5f2-7b1c-7d3e-8a4f-1234567890ab"
	validLE     = "0190a5f2-7b1c-7d3e-8a4f-1234567890ac"
	nilUUID     = "00000000-0000-0000-0000-000000000000"
)

func serve(tenantID, legalEntity string) (*httptest.ResponseRecorder, *tenant.Principal) {
	var got *tenant.Principal
	h := tenant.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = &p
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	if tenantID != "" {
		req.Header.Set(tenant.HeaderTenant, tenantID)
	}
	if legalEntity != "" {
		req.Header.Set(tenant.HeaderLegalEntity, legalEntity)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, got
}

func distinctLEs(n int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("0190a5f2-7b1c-7d3e-8a4f-%012x", i+1)
	}
	return strings.Join(out, ",")
}

func TestRejectionIsProblemJSONWithTraceID(t *testing.T) {
	h := middleware.RequestID(tenant.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set("X-Request-Id", "req-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Content-Type") != "application/problem+json" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("status %d headers %v", rec.Code, rec.Header())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["traceId"] != "req-123" || body["code"] != "AUTH-0001" {
		t.Fatalf("body %v", body)
	}
}

func TestMiddlewareTenantValidation(t *testing.T) {
	tests := []struct {
		name, tenant string
		wantStatus   int
		wantTenant   string
		wantDetail   string
	}{
		{"valid", validTenant, http.StatusOK, validTenant, ""},
		{"uppercase canonicalized", strings.ToUpper(validTenant), http.StatusOK, validTenant, ""},
		{"missing", "", http.StatusUnauthorized, "", "missing tenant"},
		{"nil uuid", nilUUID, http.StatusUnauthorized, "", "invalid tenant"},
		{"not a uuid", "not-a-uuid", http.StatusUnauthorized, "", "invalid tenant"},
		{"short word", "tenant-a", http.StatusUnauthorized, "", "invalid tenant"},
		{"no hyphens", strings.ReplaceAll(validTenant, "-", ""), http.StatusUnauthorized, "", "invalid tenant"},
		{"braced", "{" + validTenant + "}", http.StatusUnauthorized, "", "invalid tenant"},
		{"urn prefixed", "urn:uuid:" + validTenant, http.StatusUnauthorized, "", "invalid tenant"},
		{"trailing space", validTenant + " ", http.StatusUnauthorized, "", "invalid tenant"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, p := serve(tc.tenant, "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if tc.wantStatus == http.StatusOK {
				if p == nil || p.TenantID != tc.wantTenant {
					t.Fatalf("principal %+v", p)
				}
				return
			}
			if p != nil || !strings.Contains(rec.Body.String(), `"code":"AUTH-0001"`) || !strings.Contains(rec.Body.String(), tc.wantDetail) {
				t.Fatalf("handler ran or body wrong: %s", rec.Body)
			}
		})
	}
}

func TestMiddlewareLegalEntityValidation(t *testing.T) {
	other := "0190a5f2-7b1c-7d3e-8a4f-1234567890ad"
	tests := []struct {
		name, legalEntity string
		wantStatus        int
		wantLE            string
	}{
		{"absent", "", http.StatusOK, ""},
		{"valid", validLE, http.StatusOK, validLE},
		{"uppercase canonicalized", strings.ToUpper(validLE), http.StatusOK, validLE},
		{"list with spaces", validLE + " , " + strings.ToUpper(other), http.StatusOK, validLE + "," + other},
		{"not a uuid", "le-1", http.StatusBadRequest, ""},
		{"valid then invalid entry", validLE + "," + "x", http.StatusBadRequest, ""},
		{"empty list entry", validLE + ",," + other, http.StatusBadRequest, ""},
		{"trailing comma", validLE + ",", http.StatusBadRequest, ""},
		{"nil entry", validLE + "," + nilUUID, http.StatusBadRequest, ""},
		{"only nil", nilUUID, http.StatusBadRequest, ""},
		{"duplicates collapsed", validLE + "," + strings.ToUpper(validLE) + "," + other, http.StatusOK, validLE + "," + other},
		{"at cap", distinctLEs(100), http.StatusOK, distinctLEs(100)},
		{"over cap", distinctLEs(101), http.StatusBadRequest, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, p := serve(validTenant, tc.legalEntity)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if tc.wantStatus == http.StatusOK {
				if p == nil || p.LegalEntityID != tc.wantLE {
					t.Fatalf("principal %+v", p)
				}
				return
			}
			if p != nil || !strings.Contains(rec.Body.String(), `"code":"COM-0001"`) {
				t.Fatalf("handler ran or body wrong: %s", rec.Body)
			}
		})
	}
}

func TestMiddlewareHealthBypassesValidation(t *testing.T) {
	h := tenant.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
}

func hasAllScope(t *testing.T, method string, headers map[string]string) bool {
	t.Helper()
	var got bool
	h := tenant.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = p.HasDuty(tenant.DutyAllScope)
	}))
	r := httptest.NewRequest(method, "/x", nil)
	r.Header.Set(tenant.HeaderTenant, validTenant)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), r)
	return got
}

func TestMiddlewareAllScopeOnlyOnExplicitRead(t *testing.T) {
	duty := map[string]string{tenant.HeaderDuties: tenant.DutyAllScope}
	withALL := map[string]string{tenant.HeaderDuties: tenant.DutyAllScope, tenant.HeaderLegalEntityScopeAll: tenant.LegalEntityScopeAll}
	if !hasAllScope(t, http.MethodGet, withALL) {
		t.Fatal("GET with ALL and duty must keep the duty")
	}
	if hasAllScope(t, http.MethodGet, duty) {
		t.Fatal("GET without the ALL header must drop the duty")
	}
	if hasAllScope(t, http.MethodPost, withALL) {
		t.Fatal("writes must drop the duty")
	}
	if hasAllScope(t, http.MethodGet, map[string]string{tenant.HeaderLegalEntityScopeAll: tenant.LegalEntityScopeAll}) {
		t.Fatal("ALL without the duty must not grant it")
	}
}

func serveWith(method string, headers map[string]string) (*httptest.ResponseRecorder, *tenant.Principal) {
	var got *tenant.Principal
	h := tenant.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = &p
	}))
	r := httptest.NewRequest(method, "/x", nil)
	r.Header.Set(tenant.HeaderTenant, validTenant)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec, got
}

func TestMiddlewareRejectsWildcardInBothSpellings(t *testing.T) {
	for _, header := range []string{tenant.HeaderLegalEntity, tenant.HeaderWebLegalEntity} {
		for _, value := range []string{"*", "all", "ALL", validLE + ",*"} {
			for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost} {
				rec, p := serveWith(method, map[string]string{header: value})
				if rec.Code != http.StatusBadRequest || p != nil || !strings.Contains(rec.Body.String(), `"code":"COM-0001"`) {
					t.Fatalf("%s %q %s: %d %s", header, value, method, rec.Code, rec.Body)
				}
			}
		}
	}
}

func TestMiddlewareBothLegalEntityHeaders(t *testing.T) {
	other := "0190a5f2-7b1c-7d3e-8a4f-1234567890ad"
	rec, p := serveWith(http.MethodGet, map[string]string{tenant.HeaderLegalEntity: validLE, tenant.HeaderWebLegalEntity: other})
	if rec.Code != http.StatusForbidden || p != nil || !strings.Contains(rec.Body.String(), `"code":"AUTH-0003"`) {
		t.Fatalf("selection outside the claim: %d %s", rec.Code, rec.Body)
	}
	rec, p = serveWith(http.MethodGet, map[string]string{tenant.HeaderLegalEntity: validLE, tenant.HeaderWebLegalEntity: strings.ToUpper(validLE)})
	if rec.Code != http.StatusOK || p.LegalEntityID != validLE {
		t.Fatalf("identical headers: %d %+v", rec.Code, p)
	}
}

// RR-01 (B-03a, D33, ADR-0006 rule 4): the shell's selection can only narrow the gateway-asserted claim. With no claim it
// is refused, except for a tenant administrator, who is entitled to every entity of the tenant.
func TestMiddlewareLegalEntitySelection(t *testing.T) {
	a, b, c := "0190a5f2-7b1c-7d3e-8a4f-00000000000a", "0190a5f2-7b1c-7d3e-8a4f-00000000000b", "0190a5f2-7b1c-7d3e-8a4f-00000000000c"
	admin := tenant.DutyTenantAdmin + ",org.legalentity.read"
	tests := []struct {
		name                     string
		claim, selection, duties string
		wantStatus               int
		wantCode                 string
		wantLE                   string
	}{
		{name: "no claim, selection: refused", selection: a, duties: "party.read", wantStatus: http.StatusForbidden, wantCode: "AUTH-0003"},
		{name: "no claim, selection, all-scope duty but not tenant admin: refused", selection: a, duties: tenant.DutyAllScope, wantStatus: http.StatusForbidden, wantCode: "AUTH-0003"},
		{name: "no claim, comma selection: refused", selection: a + "," + b, wantStatus: http.StatusForbidden, wantCode: "AUTH-0003"},
		{name: "no claim, tenant admin, selection: narrowed as before", selection: strings.ToUpper(a), duties: admin, wantStatus: http.StatusOK, wantLE: a},
		{name: "no claim, tenant admin, comma selection: as before", selection: a + ", " + b, duties: admin, wantStatus: http.StatusOK, wantLE: a + "," + b},
		{name: "claim list, selection inside: narrowed", claim: a + "," + b + "," + c, selection: b, wantStatus: http.StatusOK, wantLE: b},
		{name: "claim list, two selected inside: narrowed to both", claim: a + "," + b + "," + c, selection: c + "," + a, wantStatus: http.StatusOK, wantLE: c + "," + a},
		{name: "claim list, selection outside: refused", claim: a + "," + b, selection: c, wantStatus: http.StatusForbidden, wantCode: "AUTH-0003"},
		{name: "claim list, selection partly outside: refused", claim: a + "," + b, selection: a + "," + c, wantStatus: http.StatusForbidden, wantCode: "AUTH-0003"},
		{name: "single claim, other selection, tenant admin duty: refused", claim: a, selection: b, duties: admin, wantStatus: http.StatusForbidden, wantCode: "AUTH-0003"},
		{name: "claim list, no selection: claim unchanged", claim: a + "," + b, wantStatus: http.StatusOK, wantLE: a + "," + b},
		{name: "no claim, no selection: unscoped as before", duties: "party.read", wantStatus: http.StatusOK, wantLE: ""},
		{name: "no claim, tenant admin, no selection: unscoped as before", duties: admin, wantStatus: http.StatusOK, wantLE: ""},
		{name: "no claim, malformed selection: 400 as before", selection: "not-a-uuid", wantStatus: http.StatusBadRequest, wantCode: "COM-0001"},
	}
	for _, tc := range tests {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			headers := map[string]string{}
			if tc.claim != "" {
				headers[tenant.HeaderLegalEntity] = tc.claim
			}
			if tc.selection != "" {
				headers[tenant.HeaderWebLegalEntity] = tc.selection
			}
			if tc.duties != "" {
				headers[tenant.HeaderDuties] = tc.duties
			}
			rec, p := serveWith(method, headers)
			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s: status %d, want %d: %s", tc.name, method, rec.Code, tc.wantStatus, rec.Body)
			}
			if tc.wantCode != "" {
				if p != nil || !strings.Contains(rec.Body.String(), `"code":"`+tc.wantCode+`"`) || rec.Header().Get("Content-Type") != "application/problem+json" {
					t.Fatalf("%s %s: want problem %s, got %s (principal %+v)", tc.name, method, tc.wantCode, rec.Body, p)
				}
				continue
			}
			if p == nil || p.LegalEntityID != tc.wantLE {
				t.Fatalf("%s %s: principal %+v, want legal entity %q", tc.name, method, p, tc.wantLE)
			}
		}
	}
}

// The tenant administrator principal (no le claim, tenant.administer) keeps working on its routes: it stays a tenant
// administrator without a selection, keeps an ALL-scope read, and still loses the all-scope duty on a write.
func TestMiddlewareTenantAdminWithoutClaimUnaffected(t *testing.T) {
	duties := tenant.DutyTenantAdmin + "," + tenant.DutyAllScope
	rec, p := serveWith(http.MethodGet, map[string]string{tenant.HeaderDuties: duties, tenant.HeaderLegalEntityScopeAll: tenant.LegalEntityScopeAll})
	if rec.Code != http.StatusOK || !tenant.IsTenantAdmin(*p) || !p.HasDuty(tenant.DutyAllScope) || !tenant.InLegalEntityScope(*p, validLE) {
		t.Fatalf("ALL-scope read: %d %+v", rec.Code, p)
	}
	rec, p = serveWith(http.MethodPost, map[string]string{tenant.HeaderDuties: duties})
	if rec.Code != http.StatusOK || !tenant.IsTenantAdmin(*p) || p.HasDuty(tenant.DutyAllScope) {
		t.Fatalf("tenant-level write: %d %+v", rec.Code, p)
	}
	rec, p = serveWith(http.MethodPost, map[string]string{tenant.HeaderDuties: duties, tenant.HeaderWebLegalEntity: validLE})
	if rec.Code != http.StatusOK || p.LegalEntityID != validLE || tenant.IsTenantAdmin(*p) || !tenant.InLegalEntityScope(*p, validLE) {
		t.Fatalf("write in a selected entity: %d %+v", rec.Code, p)
	}
}

// A forged service actor in the user header stays a 401 even when the selection would also be refused.
func TestMiddlewareSelectionRefusalAfterUserCheck(t *testing.T) {
	rec, _ := serveWith(http.MethodGet, map[string]string{tenant.HeaderUser: tenant.ServiceActorPrefix + "ledger", tenant.HeaderWebLegalEntity: validLE})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("service actor in the user header must stay 401: %d %s", rec.Code, rec.Body)
	}
}

// The refusal is logged with the tenant and the user, never with the legal-entity values.
func TestMiddlewareSelectionRefusalLog(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)
	user := "0190a5f2-7b1c-7d3e-8a4f-0000000000f1"
	rec, _ := serveWith(http.MethodGet, map[string]string{tenant.HeaderUser: user, tenant.HeaderWebLegalEntity: validLE})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log %q: %v", buf.String(), err)
	}
	if entry["msg"] != "legal entity selection refused" || entry["tenantId"] != validTenant || entry["user"] != user {
		t.Fatalf("log entry %v", entry)
	}
	if strings.Contains(buf.String(), validLE) {
		t.Fatalf("log names the legal entity: %s", buf.String())
	}
}

func TestReadOnlyPOSTKeepsAllScopeOnlyWhereDeclared(t *testing.T) {
	duty := map[string]string{tenant.HeaderDuties: tenant.DutyAllScope}
	withALL := map[string]string{tenant.HeaderDuties: tenant.DutyAllScope, tenant.HeaderLegalEntityScopeAll: tenant.LegalEntityScopeAll}
	noDuty := map[string]string{tenant.HeaderLegalEntityScopeAll: tenant.LegalEntityScopeAll}
	probe := func(method string, headers map[string]string, readOnly bool) bool {
		var got bool
		var inner http.Handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			p, _ := tenant.FromContext(r.Context())
			got = p.HasDuty(tenant.DutyAllScope)
		})
		if readOnly {
			inner = tenant.ReadOnlyPOST(inner)
		}
		r := httptest.NewRequest(method, "/x", nil)
		r.Header.Set(tenant.HeaderTenant, validTenant)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		tenant.Middleware(inner).ServeHTTP(httptest.NewRecorder(), r)
		return got
	}
	if !probe(http.MethodPost, withALL, true) {
		t.Fatal("allowlisted POST with ALL and duty must keep the duty, like a GET")
	}
	if probe(http.MethodPost, withALL, false) {
		t.Fatal("a non-allowlisted POST must still drop the duty")
	}
	if probe(http.MethodPost, duty, true) {
		t.Fatal("allowlisted POST without the ALL header must drop the duty")
	}
	if probe(http.MethodPost, noDuty, true) {
		t.Fatal("ALL without the duty must grant nothing")
	}
	for _, m := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if probe(m, withALL, true) {
			t.Fatalf("%s must never carry the duty, even under ReadOnlyPOST", m)
		}
	}
}

func TestReadOnlyPOSTDoesNotLetASwappedPrincipalInheritTheGrant(t *testing.T) {
	var got bool
	inner := tenant.ReadOnlyPOST(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = p.HasDuty(tenant.DutyAllScope)
	}))
	swap := func(p tenant.Principal) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inner.ServeHTTP(w, r.WithContext(tenant.With(r.Context(), p)))
		})
	}
	for name, p := range map[string]tenant.Principal{
		"other user":    {TenantID: validTenant, UserID: "someone-else"},
		"other tenant":  {TenantID: "00000000-0000-7000-8000-000000000002", UserID: "u1"},
		"service token": {TenantID: validTenant, UserID: "u1", Service: "billing"},
	} {
		got = false
		r := httptest.NewRequest(http.MethodPost, "/x", nil)
		r.Header.Set(tenant.HeaderTenant, validTenant)
		r.Header.Set(tenant.HeaderUser, "u1")
		r.Header.Set(tenant.HeaderDuties, tenant.DutyAllScope)
		r.Header.Set(tenant.HeaderLegalEntityScopeAll, tenant.LegalEntityScopeAll)
		tenant.Middleware(swap(p)).ServeHTTP(httptest.NewRecorder(), r)
		if got {
			t.Fatalf("%s: swapped principal inherited the all-scope grant", name)
		}
	}
}

func TestMiddlewareGatewaySecret(t *testing.T) {
	t.Setenv(tenant.GatewaySecretEnv, "s3cret-per-boot")
	called := false
	h := tenant.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	do := func(secret string) int {
		called = false
		req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
		req.Header.Set(tenant.HeaderTenant, validTenant)
		if secret != "" {
			req.Header.Set(tenant.HeaderGatewaySecret, secret)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := do(""); code != http.StatusUnauthorized || called {
		t.Fatalf("no secret: %d called=%v", code, called)
	}
	if code := do("wrong"); code != http.StatusUnauthorized || called {
		t.Fatalf("wrong secret: %d called=%v", code, called)
	}
	if code := do("s3cret-per-boot"); code != http.StatusOK || !called {
		t.Fatalf("right secret: %d called=%v", code, called)
	}
	// Health probes never carry the secret.
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz: %d", rec.Code)
	}
}

func TestForwardGatewaySecret(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	tenant.ForwardGatewaySecret(req)
	if req.Header.Get(tenant.HeaderGatewaySecret) != "" {
		t.Fatal("must not set a header when the secret is not configured")
	}
	t.Setenv(tenant.GatewaySecretEnv, "s3cret-per-boot")
	tenant.ForwardGatewaySecret(req)
	if got := req.Header.Get(tenant.HeaderGatewaySecret); got != "s3cret-per-boot" {
		t.Fatalf("header = %q", got)
	}
	// A request forwarded with the helper is accepted by a peer that shares the secret.
	called := false
	peer := tenant.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	req.Header.Set(tenant.HeaderTenant, validTenant)
	rec := httptest.NewRecorder()
	peer.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("peer answered %d called=%v", rec.Code, called)
	}
}

func TestAuthAge(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	at := func(v string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		if v != "" {
			r.Header.Set(tenant.HeaderAuthTime, v)
		}
		return r
	}
	if age, ok := tenant.AuthAge(at("1799999940"), now); !ok || age != time.Minute {
		t.Fatalf("a minute ago: %v %v", age, ok)
	}
	if age, ok := tenant.AuthAge(at("1800000030"), now); !ok || age != 0 {
		t.Fatalf("thirty seconds of clock skew reads as now: %v %v", age, ok)
	}
	for _, bad := range []string{"", "soon", "0", "-5", "1800000300"} {
		if _, ok := tenant.AuthAge(at(bad), now); ok {
			t.Errorf("%q must read as not fresh", bad)
		}
	}
}
