package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/BenjaminSRussell/depot/services/identity/authztest"
)

const policyDir = "../../../../policies/authz"

// TestGatewayAnswersTheSharedRouteTable runs the B-04 route table through devgateway end to end: a real OPA on
// policies/authz with each case's role assignments, real RS256 tokens, the verifier and RequireOPAForGateway.
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
	environ := []string{}
	for _, prefix := range table.Prefixes() {
		environ = append(environ, "DEPOT_SVC_"+strings.ToUpper(prefix)+"_URL="+upstream.URL)
	}
	env := map[string]string{
		"DEVGATEWAY_ISSUER": issuer.URL, "DEVGATEWAY_JWKS_URL": issuer.JWKSURL, "DEVGATEWAY_TENANT_ID": table.Tenant, "DEVGATEWAY_OPA_URL": opaURL,
	}
	handler, err := buildHandler(func(k string) string { return env[k] }, environ)
	if err != nil {
		t.Fatal(err)
	}

	takeLog := captureJSONLog(t)
	authztest.Run(t, handler, issuer, table, gatewayName, func() bool { return hit.Swap(false) }, takeLog)
}

// captureJSONLog points slog's default logger at a JSON buffer for the test and returns a function that takes what was
// written since its last call.
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
		return s
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

// TestGatewayRecordsALegalEntitySelectionOutsideTheToken: the gateway's own decision writes a deny record too.
func TestGatewayRecordsALegalEntitySelectionOutsideTheToken(t *testing.T) {
	f := newFixture(t)
	takeLog := captureJSONLog(t)
	resp := f.get(t, "/api/v1/party/vendors", f.token(t, adminUser, nil), map[string]string{"X-Depot-LegalEntity": "00000000-0000-0000-0000-0000000000e2"})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	log := takeLog()
	var deny map[string]any
	for _, r := range authztest.Records(t, log) {
		if r["outcome"] == "deny" {
			deny = r
		}
	}
	if deny == nil || deny["reason"] != "legal_entity_selection" || deny["source"] != gatewayName || deny["route"] != "GET /v1/party/**" {
		t.Fatalf("records:\n%s", log)
	}
	if strings.Contains(log, adminUser) {
		t.Fatalf("the raw user id is in the log:\n%s", log)
	}
}

// TestGatewayForwardsItsOwnRequestID: the service gets the id the gateway's record carries, never the client's.
func TestGatewayForwardsItsOwnRequestID(t *testing.T) {
	f := newFixture(t)
	takeLog := captureJSONLog(t)
	resp := f.get(t, "/api/v1/party/vendors", f.token(t, adminUser, nil), map[string]string{"X-Request-Id": "client-chosen"})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	forwarded := (<-f.seen).Get("X-Request-Id")
	<-f.seenPath
	recs := authztest.Records(t, takeLog())
	if forwarded == "" || forwarded == "client-chosen" || len(recs) != 1 || recs[0]["request_id"] != forwarded {
		t.Fatalf("forwarded %q, records %v", forwarded, recs)
	}
}
