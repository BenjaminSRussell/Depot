package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/BenjaminSRussell/depot/services/identity/authztest"
)

const policyDir = "../../policies/authz"

// TestGatewayAnswersTheSharedRouteTable runs the B-04 route table through depot-server's gateway with the guard
// buildGuard builds in production (the verifier and RequireOPAForGateway), a real OPA on policies/authz with each case's
// role assignments and real RS256 tokens.
func TestGatewayAnswersTheSharedRouteTable(t *testing.T) {
	table := authztest.Load(t, policyDir)
	opaURL := authztest.StartOPA(t, policyDir, table)
	issuer := authztest.NewIssuer(t)

	var hit atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	u, _ := url.Parse(upstream.URL)
	upstreams := map[string]*url.URL{}
	for _, prefix := range table.Prefixes() {
		upstreams[prefix] = u
	}
	guard, err := buildGuard(Config{OIDCIssuer: issuer.URL, OIDCAudience: "depot-api", TenantID: table.Tenant, OIDCJWKSURL: issuer.JWKSURL, OPAURL: opaURL})
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway(guard, upstreams, nil, func() (bool, []Status) { return true, nil }, "gw-secret", nil, nil)

	takeLog := captureJSONLog(t)
	authztest.Run(t, gateway, issuer, table, gatewayName, func() bool { return hit.Swap(false) }, takeLog)
}

// TestGatewayRecordsItsOwnDenials: a path that is not canonical and a legal-entity selection outside the token are
// decisions of the gateway itself; each writes one deny record without a raw path or subject.
func TestGatewayRecordsItsOwnDenials(t *testing.T) {
	g, _ := testGateway(t, true)
	takeLog := captureJSONLog(t)
	if rec := do(g, "GET", "/api/v1/party/../ledger/x", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-canonical = %d", rec.Code)
	}
	recs := authztest.Records(t, takeLog())
	if len(recs) != 1 || recs[0]["reason"] != "not_canonical" || recs[0]["route"] != "GET unmatched" || recs[0]["source"] != gatewayName {
		t.Fatalf("records = %v", recs)
	}
	if rec := do(g, "GET", "/api/v1/party/vendors", map[string]string{"X-Depot-LegalEntity": "le-2"}); rec.Code != http.StatusForbidden {
		t.Fatalf("selection = %d", rec.Code)
	}
	recs = authztest.Records(t, takeLog())
	if len(recs) != 1 || recs[0]["reason"] != "legal_entity_selection" || recs[0]["route"] != "GET /v1/party/**" {
		t.Fatalf("records = %v", recs)
	}
}

// TestGatewayForwardsItsOwnRequestID: the service receives the gateway's request id, not one the client chose.
func TestGatewayForwardsItsOwnRequestID(t *testing.T) {
	g, seen := testGateway(t, true)
	if rec := do(g, "GET", "/api/v1/party/vendors", map[string]string{"X-Request-Id": "client-chosen"}); rec.Code != 200 {
		t.Fatalf("api = %d", rec.Code)
	}
	if got := (*seen)[0].Get("X-Request-Id"); got == "" || got == "client-chosen" {
		t.Fatalf("forwarded request id = %q", got)
	}
}

func captureJSONLog(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(lockedWriter{&mu, &buf}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		s := buf.String()
		buf.Reset()
		return strings.TrimSpace(s)
	}
}

type lockedWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
