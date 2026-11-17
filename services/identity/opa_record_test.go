package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

// B-04: every RequireOPA decision writes one authz.decision record (pseudonymized subject, tenant, duty, route, outcome,
// reason, request id), and a deny counts in depot_authz_denied_total.
func TestRequireOPAForGatewayRecordsEveryDecision(t *testing.T) {
	const rawUser = "8a6b0c1d-2e3f-4a5b-8c6d-7e8f9a0b1c2d"
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	for _, tc := range []struct {
		name, opaBody, wantOutcome, wantReason, wantDuty string
		opaStatus                                        int
	}{
		{"allowed", `{"result":{"allow":true,"duty":"party.vendor.view"}}`, "allow", "policy_allowed", "party.vendor.view", http.StatusOK},
		{"denied", `{"result":{"allow":false,"duty_missing":"party.vendor.maintain"}}`, "deny", "policy_denied", "party.vendor.maintain", http.StatusOK},
		{"policy down", `{}`, "deny", "policy_unavailable", "", http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.opaStatus)
				_, _ = w.Write([]byte(tc.opaBody))
			}))
			defer server.Close()
			client, err := NewOPAClient(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			before := authz.Denied("test-gateway", tc.wantDuty, tc.wantReason)
			handler := RequireOPAForGateway(client, "test-gateway")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodPut, "/v1/party/vendors/v-77", nil)
			ctx := tenant.With(req.Context(), tenant.Principal{TenantID: "tenant-a", UserID: rawUser})
			ctx = authz.WithGatewayRoutes(ctx, func(p string) bool { return p == "party" })
			req = req.WithContext(context.WithValue(ctx, verifiedPrincipalKey{}, true))
			handler.ServeHTTP(httptest.NewRecorder(), req)

			var rec map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
				t.Fatalf("want one JSON record, got %q", buf.String())
			}
			if rec["msg"] != authz.Event || rec["source"] != "test-gateway" || rec["outcome"] != tc.wantOutcome ||
				rec["reason"] != tc.wantReason || rec["duty"] != tc.wantDuty || rec["tenant"] != "tenant-a" ||
				rec["route"] != "PUT /v1/party/**" {
				t.Fatalf("record = %v", rec)
			}
			if strings.Contains(buf.String(), rawUser) || strings.Contains(buf.String(), "v-77") {
				t.Fatalf("raw user id or raw path in the record: %s", buf.String())
			}
			want := before
			if tc.wantOutcome == "deny" {
				want++
			}
			if got := authz.Denied("test-gateway", tc.wantDuty, tc.wantReason); got != want {
				t.Fatalf("depot_authz_denied_total = %d, want %d", got, want)
			}
		})
	}

	// A prefix the gateway does not route is "unmatched", so a client cannot write a name of its choosing into the record.
	buf.Reset()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"allow":false}}`))
	}))
	defer server.Close()
	client, err := NewOPAClient(server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/9f2a0c11-chosen-by-client/x", nil)
	ctx := tenant.With(req.Context(), tenant.Principal{TenantID: "tenant-a", UserID: rawUser})
	ctx = authz.WithGatewayRoutes(context.WithValue(ctx, verifiedPrincipalKey{}, true), func(p string) bool { return p == "party" })
	RequireOPAForGateway(client, "test-gateway")(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))
	if !strings.Contains(buf.String(), `"route":"GET unmatched"`) || strings.Contains(buf.String(), "chosen-by-client") {
		t.Fatalf("unrouted prefix record: %s", buf.String())
	}
}
