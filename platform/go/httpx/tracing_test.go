package httpx_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/httpx"
)

const incomingTrace = "4bf92f3577b34da6a3ce929d0e0e4736"

func failingRouter() http.Handler {
	r := httpx.NewRouterWithoutTenant()
	r.Get("/boom", func(w http.ResponseWriter, r *http.Request) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, "COM-0409", "taken"))
	})
	return r
}

func problemTraceID(t *testing.T, req *http.Request) string {
	t.Helper()
	rec := httptest.NewRecorder()
	failingRouter().ServeHTTP(rec, req)
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusConflict {
		t.Fatalf("status %d body %s err %v", rec.Code, rec.Body.String(), err)
	}
	return p.TraceID
}

func TestProblemCarriesTheIncomingW3CTraceID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.Header.Set("traceparent", "00-"+incomingTrace+"-00f067aa0ba902b7-01")
	req.Header.Set("X-Request-Id", "req-1")
	if got := problemTraceID(t, req); got != incomingTrace {
		t.Fatalf("traceId %q, want %q", got, incomingTrace)
	}
}

func TestProblemWithoutTraceparentCarriesAMintedW3CTraceID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.Header.Set("X-Request-Id", "req-1")
	got := problemTraceID(t, req)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(got) {
		t.Fatalf("traceId %q is not a 32-hex W3C trace id", got)
	}
}

func TestRequestLogCarriesTheTraceID(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.Header.Set("traceparent", "00-"+incomingTrace+"-00f067aa0ba902b7-01")
	req.Header.Set("X-Request-Id", "req-1")
	failingRouter().ServeHTTP(httptest.NewRecorder(), req)
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("log %q: %v", buf.String(), err)
	}
	if line["traceId"] != incomingTrace || line["requestId"] != "req-1" {
		t.Fatalf("log line %v", line)
	}
}
