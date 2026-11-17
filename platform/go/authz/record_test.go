package authz_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/logpseudo"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	rawUser   = "3f1c2b9a-7d4e-4f60-9a1b-5c6d7e8f9a0b"
	rawTenant = "00000000-0000-0000-0000-000000000001"
)

type testMAC struct{}

func (testMAC) MAC(tenantID string, message []byte) ([]byte, error) {
	m := hmac.New(sha256.New, []byte("authz-test-key|"+tenantID))
	m.Write(message)
	return m.Sum(nil), nil
}

// captureLog swaps slog's default for a JSON handler into a buffer (wrapped by wrap when not nil) for one test.
func captureLog(t *testing.T, wrap func(slog.Handler) slog.Handler) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	var h slog.Handler = slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	if wrap != nil {
		h = wrap(h)
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func records(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		if m["msg"] == authz.Event {
			out = append(out, m)
		}
	}
	return out
}

// serve runs mw on a chi route behind chi's request id, as a service router does, for a principal holding duties.
func serve(mw func(http.Handler) http.Handler, duties ...string) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.With(mw).Get("/v1/things/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodGet, "/v1/things/thing-42", nil)
	p := tenant.Principal{TenantID: rawTenant, UserID: rawUser, Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	req = req.WithContext(tenant.With(req.Context(), p))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestDeniedCallWritesOneRecordWithAPseudonymAndNoRawUserID(t *testing.T) {
	logpseudo.SetSubjectKey(testMAC{})
	t.Cleanup(func() { logpseudo.SetSubjectKey(nil) })
	buf := captureLog(t, nil)

	if rec := serve(authz.RequireDuty("things.write"), "things.read"); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d", rec.Code)
	}
	recs := records(t, buf)
	if len(recs) != 1 {
		t.Fatalf("want one record, got %d: %s", len(recs), buf)
	}
	got := recs[0]
	want := map[string]any{
		"event": authz.Event, "schema": float64(authz.SchemaVersion), "log_class": authz.LogClass, "source": "service",
		"tenant": rawTenant, "duty": "things.write", "route": "GET /v1/things/{id}", "outcome": "deny", "reason": "duty_missing",
		"level": "WARN", "subject": logpseudo.PseudonymFor(testMAC{}, rawTenant, rawUser),
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if id, _ := got["request_id"].(string); id == "" {
		t.Error("request_id is empty")
	}
	if !strings.HasPrefix(got["subject"].(string), logpseudo.Prefix) {
		t.Errorf("subject %q is not a pseudonym", got["subject"])
	}
	if strings.Contains(buf.String(), rawUser) {
		t.Fatalf("the raw user id is in the record: %s", buf)
	}
	if strings.Contains(buf.String(), "thing-42") {
		t.Fatalf("the raw path is in the record: %s", buf)
	}
}

func TestRecordWithoutAKeyRedactsTheSubject(t *testing.T) {
	logpseudo.SetSubjectKey(nil)
	buf := captureLog(t, nil)
	serve(authz.RequireDuty("things.write"))
	recs := records(t, buf)
	if len(recs) != 1 || recs[0]["subject"] != logpseudo.Redacted {
		t.Fatalf("records = %v", recs)
	}
	if strings.Contains(buf.String(), rawUser) {
		t.Fatalf("the raw user id is in the record: %s", buf)
	}
}

// A service that also runs the whole-log pseudonymizer (eu-data-boundary) logs the same pseudonym, not a pseudonym of it.
func TestRecordKeepsItsPseudonymUnderTheLogPseudonymizer(t *testing.T) {
	logpseudo.SetSubjectKey(testMAC{})
	t.Cleanup(func() { logpseudo.SetSubjectKey(nil) })
	buf := captureLog(t, func(h slog.Handler) slog.Handler { return logpseudo.New(h, testMAC{}) })
	serve(authz.RequireDuty("things.write"))
	recs := records(t, buf)
	if len(recs) != 1 || recs[0]["subject"] != logpseudo.PseudonymFor(testMAC{}, rawTenant, rawUser) {
		t.Fatalf("records = %v", recs)
	}
}

func TestAllowedCallWritesAnAllowRecord(t *testing.T) {
	buf := captureLog(t, nil)
	if rec := serve(authz.RequireDuty("things.read"), "things.read"); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
	recs := records(t, buf)
	if len(recs) != 1 || recs[0]["outcome"] != "allow" || recs[0]["reason"] != "duty_held" || recs[0]["level"] != "INFO" {
		t.Fatalf("records = %v", recs)
	}
}

func TestRequireAnyDutyRecordsTheDutyThatGranted(t *testing.T) {
	buf := captureLog(t, nil)
	serve(authz.RequireAnyDuty("things.scoped", "things.broad"), "things.broad")
	serve(authz.RequireAnyDuty("things.scoped", "things.broad"))
	recs := records(t, buf)
	if len(recs) != 2 || recs[0]["duty"] != "things.broad" || recs[0]["outcome"] != "allow" ||
		recs[1]["duty"] != "things.scoped|things.broad" || recs[1]["reason"] != "duty_missing" {
		t.Fatalf("records = %v", recs)
	}
}

func TestNoPrincipalIsADenyRecord(t *testing.T) {
	buf := captureLog(t, nil)
	rec := call(authz.RequireDuty("things.read"), nil)
	recs := records(t, buf)
	if rec.Code != http.StatusUnauthorized || len(recs) != 1 || recs[0]["reason"] != "no_principal" || recs[0]["route"] != "GET unmatched" {
		t.Fatalf("status %d, records = %v", rec.Code, recs)
	}
}

func TestDeniedCounterIncrementsAndIsExportedWithTheSharedSeries(t *testing.T) {
	captureLog(t, nil)
	before := authz.Denied("service", "things.approve", "duty_missing")
	serve(authz.RequireDuty("things.approve"))
	serve(authz.RequireDuty("things.approve"))
	serve(authz.RequireDuty("things.approve"), "things.approve") // allowed: not counted
	if got := authz.Denied("service", "things.approve", "duty_missing"); got != before+2 {
		t.Fatalf("depot_authz_denied_total = %d, want %d", got, before+2)
	}
	var out bytes.Buffer
	httpx.WriteMetrics(&out)
	if !strings.Contains(out.String(), "# TYPE depot_authz_denied_total counter") ||
		!strings.Contains(out.String(), `depot_authz_denied_total{source="service",duty="things.approve",reason="duty_missing"} `) {
		t.Fatalf("metrics lack the counter:\n%s", out.String())
	}
	if strings.Contains(out.String(), rawUser) || strings.Contains(out.String(), rawTenant) {
		t.Fatal("the counter has a user or tenant label")
	}
}

func TestGatewayRouteIsThePrefixNeverTheRawPath(t *testing.T) {
	for path, want := range map[string]string{
		"/v1/ledger/journal-entries/9f2a": "/v1/ledger/**",
		"/v1/ledger":                      "/v1/ledger/**",
		"/scim/v2/Users/abc":              "/scim/v2/**",
		"/v1/Ledger/x":                    "unmatched",
		"/v1/a@b.example/x":               "unmatched",
		"/healthz":                        "unmatched",
		"/":                               "unmatched",
	} {
		if got := authz.GatewayRoute(path); got != want {
			t.Errorf("GatewayRoute(%q) = %q, want %q", path, got, want)
		}
	}
}
