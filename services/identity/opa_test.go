package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

func TestOPAClientSendsPrincipalAndUsesDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/data/authz/decision" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		inputBody := body["input"].(map[string]any)
		subjectBody := inputBody["subject"].(map[string]any)
		if _, ok := subjectBody["duties"]; ok {
			t.Error("OPA input must not contain caller-provided duties")
		}
		encodedInput, err := json.Marshal(body["input"])
		if err != nil {
			t.Fatal(err)
		}
		var input opaInput
		if err := json.Unmarshal(encodedInput, &input); err != nil {
			t.Fatal(err)
		}
		want := opaInput{
			Subject: opaSubject{TenantID: "tenant-a", LegalEntityID: "le-a", UserID: "user-a"},
			Action:  http.MethodPut, Resource: "/v1/vendors/vendor-a",
		}
		if !reflect.DeepEqual(input, want) {
			t.Errorf("input = %#v, want %#v", input, want)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"allow":true}}`))
	}))
	defer server.Close()

	client, err := NewOPAClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := client.Allow(context.Background(), tenant.Principal{
		TenantID: "tenant-a", LegalEntityID: "le-a", UserID: "user-a",
		Duties: map[string]struct{}{"Forged duty": {}},
	}, http.MethodPut, "/v1/vendors/vendor-a")
	if err != nil || !allowed {
		t.Fatalf("Allow() = %v, %v", allowed, err)
	}
}

func TestOPAClientFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want error
	}{
		{name: "unavailable", code: http.StatusServiceUnavailable, body: `{}`},
		{name: "undefined decision", code: http.StatusOK, body: `{}`, want: ErrInvalidOPAResponse},
		{name: "malformed decision", code: http.StatusOK, body: `not-json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.code)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			client, err := NewOPAClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			allowed, err := client.Allow(context.Background(), tenant.Principal{TenantID: "t", UserID: "u"}, http.MethodPut, "/v1/vendors/v")
			if allowed || err == nil {
				t.Fatalf("Allow() = %v, %v; want deny and error", allowed, err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNewOPAClientRejectsUnsafeURL(t *testing.T) {
	for _, raw := range []string{"", "ftp://opa", "http://user:pass@opa", "http://opa?x=1", "http://opa#fragment", "/relative"} {
		if _, err := NewOPAClient(raw, nil); err == nil {
			t.Errorf("NewOPAClient(%q) succeeded", raw)
		}
	}
}

func TestRequireOPAUsesExpectedHTTPStatuses(t *testing.T) {
	tests := []struct {
		name      string
		principal bool
		opaStatus int
		opaBody   string
		want      int
		wantDuty  string
	}{
		{name: "no principal", want: http.StatusUnauthorized},
		{name: "denied", principal: true, opaStatus: http.StatusOK, opaBody: `{"result":{"allow":false,"duty_missing":"party.vendor.maintain"}}`, want: http.StatusForbidden, wantDuty: "party.vendor.maintain"},
		{name: "opa unavailable", principal: true, opaStatus: http.StatusServiceUnavailable, opaBody: `{}`, want: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.opaStatus)
				_, _ = w.Write([]byte(tt.opaBody))
			}))
			defer server.Close()
			client, err := NewOPAClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			handler := RequireOPA(client, http.MethodPut, "/v1/vendors")(
				http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
			)
			req := httptest.NewRequest(http.MethodPut, "/v1/vendors/1", nil)
			if tt.principal {
				ctx := tenant.With(req.Context(), tenant.Principal{TenantID: "t", UserID: "u"})
				req = req.WithContext(context.WithValue(ctx, verifiedPrincipalKey{}, true))
			}
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != tt.want {
				t.Fatalf("status = %d, want %d; body: %s", resp.Code, tt.want, resp.Body.String())
			}
			if tt.wantDuty != "" && !strings.Contains(resp.Body.String(), `"duty_missing":"`+tt.wantDuty+`"`) {
				t.Fatalf("response missing duty detail: %s", resp.Body.String())
			}
		})
	}
}

// B-03b: the policy grants a legal-entity-scoped role assignment only to a request in that legal entity, so RequireOPA
// tells it the verified token's `le` claim, and leaves the principal (and so the database scope of identity's own routes)
// as the verifier built it.
func TestRequireOPASendsTheVerifiedLegalEntityClaim(t *testing.T) {
	verifier, key, jwks := newTestJWTVerifier(t)
	defer jwks.Close()
	for name, tc := range map[string]struct {
		le   any
		want string
	}{
		"one legal entity":         {le: "00000000-0000-0000-0000-0000000000a1", want: "00000000-0000-0000-0000-0000000000a1"},
		"a comma list":             {le: " a1,b2 ", want: "a1,b2"},
		"an array claim":           {le: []string{"a1", "b2"}, want: ""},
		"no claim":                 {want: ""},
		"a claim of another shape": {le: map[string]string{"x": "y"}, want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			var sent opaSubject
			opa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Input opaInput `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				sent = body.Input.Subject
				_, _ = w.Write([]byte(`{"result":{"allow":true}}`))
			}))
			defer opa.Close()
			client, err := NewOPAClient(opa.URL, opa.Client())
			if err != nil {
				t.Fatal(err)
			}
			claims := testClaims()
			if tc.le != nil {
				claims["le"] = tc.le
			}
			handler := verifier.Middleware(RequireOPAForRequest(client)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if p, _ := tenant.FromContext(r.Context()); p.LegalEntityID != "" {
					t.Errorf("principal legal entity = %q, want it left empty", p.LegalEntityID)
				}
				w.WriteHeader(http.StatusNoContent)
			})))
			req := httptest.NewRequest(http.MethodGet, "/v1/identity/users", nil)
			req.Header.Set("Authorization", "Bearer "+signTestJWT(t, key, "key-1", claims))
			resp := httptest.NewRecorder()
			handler.ServeHTTP(resp, req)
			if resp.Code != http.StatusNoContent {
				t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
			}
			if sent.TenantID != "tenant-a" || sent.UserID != "user-a" || sent.LegalEntityID != tc.want {
				t.Fatalf("OPA subject = %+v, want legal entity %q", sent, tc.want)
			}
		})
	}
}

// B-03b review: RequireOPA tells the policy the verified token's claim even when a middleware between the verifier and
// RequireOPA put another legal entity on the principal.
func TestRequireOPAIgnoresALegalEntityPutOnThePrincipal(t *testing.T) {
	var sent opaSubject
	opa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input opaInput `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent = body.Input.Subject
		_, _ = w.Write([]byte(`{"result":{"allow":true}}`))
	}))
	defer opa.Close()
	client, err := NewOPAClient(opa.URL, opa.Client())
	if err != nil {
		t.Fatal(err)
	}
	handler := RequireOPA(client, http.MethodGet, "/v1/identity/users")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for claim, want := range map[string]string{"le-a": "le-a", "": ""} {
		req := httptest.NewRequest(http.MethodGet, "/v1/identity/users", nil)
		ctx := tenant.With(req.Context(), tenant.Principal{TenantID: "t", UserID: "u", LegalEntityID: "injected"})
		ctx = context.WithValue(context.WithValue(ctx, verifiedPrincipalKey{}, true), verifiedLegalEntityKey{}, claim)
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req.WithContext(ctx))
		if resp.Code != http.StatusNoContent || sent.LegalEntityID != want {
			t.Fatalf("claim %q: status %d, OPA legal entity %q, want %q", claim, resp.Code, sent.LegalEntityID, want)
		}
	}
}
