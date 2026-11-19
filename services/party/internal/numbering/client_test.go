package numbering_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/party/internal/numbering"
)

const entityID = "00000000-0000-0000-0000-0000000000e1"

func principalContext() context.Context {
	return tenant.With(context.Background(), tenant.Principal{
		TenantID: "00000000-0000-0000-0000-000000000001", LegalEntityID: entityID, UserID: "user-1",
		Duties: map[string]struct{}{"party.write": {}, "party.read": {}},
	})
}

func wantUnavailable(t *testing.T, err error) {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != http.StatusServiceUnavailable || p.Code != numbering.CodeUnavailable {
		t.Fatalf("got %v, want 503 %s", err, numbering.CodeUnavailable)
	}
}

func TestNext_PostsWithPrincipalHeadersAndReturnsFormatted(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		_, _ = w.Write([]byte(`{"value":7,"formatted":"PTY-000007","sequence":"PARTY","legalEntityId":"x"}`))
	}))
	defer srv.Close()
	formatted, err := numbering.New(srv.URL+"/").Next(principalContext(), "PARTY", "key-1")
	if err != nil || formatted != "PTY-000007" {
		t.Fatalf("got %q, %v", formatted, err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/v1/numbering/sequences/PARTY/next" {
		t.Fatalf("got %s %s", got.Method, got.URL.Path)
	}
	for header, want := range map[string]string{
		"Idempotency-Key": "key-1", "X-Depot-Tenant": "00000000-0000-0000-0000-000000000001",
		"X-Depot-Legal-Entity": entityID, "X-Depot-User": "user-1", "X-Depot-Duties": "party.read,party.write",
	} {
		if got.Header.Get(header) != want {
			t.Errorf("%s = %q, want %q", header, got.Header.Get(header), want)
		}
	}
}

func TestNext_FailuresAreUnavailable(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"server error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }},
		{"not found", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }},
		{"malformed body", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) }},
		{"missing formatted", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"value":1}`)) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			_, err := numbering.NewWithTimeout(srv.URL, 100*time.Millisecond).Next(principalContext(), "PARTY", "k")
			wantUnavailable(t, err)
		})
	}
}

func TestNext_TransportErrorIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := numbering.New(url).Next(principalContext(), "PARTY", "k")
	wantUnavailable(t, err)
}

func TestUnavailable_AlwaysFails(t *testing.T) {
	_, err := numbering.Unavailable{}.Next(principalContext(), "PARTY", "k")
	wantUnavailable(t, err)
	if p := problemOf(t, err); !strings.Contains(p.Detail, "not configured") {
		t.Fatalf("detail %q", p.Detail)
	}
}

func TestNext_RequiresASingleLegalEntity(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls++ }))
	defer srv.Close()
	for name, entity := range map[string]string{"none": "", "list": entityID + ",00000000-0000-0000-0000-0000000000e2"} {
		t.Run(name, func(t *testing.T) {
			ctx := tenant.With(context.Background(), tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", LegalEntityID: entity})
			_, err := numbering.New(srv.URL).Next(ctx, "PARTY", "k")
			var p *httpx.Problem
			if !errors.As(err, &p) || p.Status != http.StatusUnprocessableEntity || p.Code != numbering.CodeEntityRequired {
				t.Fatalf("got %v, want 422 %s", err, numbering.CodeEntityRequired)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("numbering called %d times without a legal entity", calls)
	}
}

func TestNext_SignedSendsPartyServiceTokenWithoutUserDuties(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, _ := tenant.NewServiceSigner("party", priv)
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"formatted":"PTY-000007"}`))
	}))
	defer srv.Close()
	if _, err := numbering.New(srv.URL).WithSigner(signer).Next(principalContext(), "PARTY", "key-1"); err != nil {
		t.Fatal(err)
	}
	if got.Get(tenant.HeaderDuties) != "" || got.Get("Idempotency-Key") != "key-1" {
		t.Fatalf("headers %v", got)
	}
	claims, err := tenant.NewServiceVerifier("numbering", map[string]ed25519.PublicKey{"party": pub}).Verify(got.Get(tenant.HeaderServiceToken))
	if err != nil || claims.Le != entityID || claims.Act != "user-1" || !claims.Principal().HasDuty("numbering.sequence.allocate") || claims.Principal().HasDuty("numbering.write") || claims.Principal().HasDuty("party.write") {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

func problemOf(t *testing.T, err error) *httpx.Problem {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) {
		t.Fatalf("got %v, want a problem", err)
	}
	return p
}

// hanging answers until the test ends or the client gives up.
func hanging(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv
}

// A numbering service that does not answer must not hold the request: the client gives up at its timeout with a 503
// that says so, even when the caller's own deadline is later.
func TestNext_SlowNumberingTimesOutWithAClearProblem(t *testing.T) {
	srv := hanging(t)
	ctx, cancel := context.WithTimeout(principalContext(), time.Minute)
	defer cancel()
	start := time.Now()
	_, err := numbering.NewWithTimeout(srv.URL, 100*time.Millisecond).Next(ctx, "PARTY", "k")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Next took %s, want about the 100ms timeout", elapsed)
	}
	p := problemOf(t, err)
	if p.Status != http.StatusServiceUnavailable || p.Code != numbering.CodeTimeout {
		t.Fatalf("got %d %s, want 503 %s", p.Status, p.Code, numbering.CodeTimeout)
	}
	for _, want := range []string{"did not answer within 100ms", "nothing was saved", "Idempotency-Key"} {
		if !strings.Contains(p.Detail, want) {
			t.Errorf("detail %q lacks %q", p.Detail, want)
		}
	}
}

// A response body that stalls counts against the same deadline as the headers.
func TestNext_SlowBodyTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	_, err := numbering.NewWithTimeout(srv.URL, 100*time.Millisecond).Next(principalContext(), "PARTY", "k")
	if p := problemOf(t, err); p.Code != numbering.CodeTimeout {
		t.Fatalf("got %s, want %s", p.Code, numbering.CodeTimeout)
	}
}

func TestNew_UsesTheDefaultTimeout(t *testing.T) {
	if numbering.DefaultTimeout != 3*time.Second {
		t.Fatalf("DefaultTimeout = %s", numbering.DefaultTimeout)
	}
	_, err := numbering.NewWithTimeout(hanging(t).URL, 0).Next(principalContext(), "PARTY", "k")
	if p := problemOf(t, err); p.Code != numbering.CodeTimeout || !strings.Contains(p.Detail, "within 3s") {
		t.Fatalf("got %s %q, want %s within 3s", p.Code, p.Detail, numbering.CodeTimeout)
	}
}

func TestNext_ErrorStatusIsNamedInTheDetail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer srv.Close()
	_, err := numbering.New(srv.URL).Next(principalContext(), "PARTY", "k")
	wantUnavailable(t, err)
	if p := problemOf(t, err); !strings.Contains(p.Detail, "answered HTTP 404") || strings.Contains(p.Detail, srv.URL) {
		t.Fatalf("detail %q", p.Detail)
	}
}

// A caller whose own deadline is earlier than the client's timeout gets the timeout problem without a claim about the
// client's timeout.
func TestNext_CallerDeadlineFirstIsNamedAsSuch(t *testing.T) {
	ctx, cancel := context.WithTimeout(principalContext(), 100*time.Millisecond)
	defer cancel()
	_, err := numbering.NewWithTimeout(hanging(t).URL, time.Second).Next(ctx, "PARTY", "k")
	p := problemOf(t, err)
	if p.Code != numbering.CodeTimeout || !strings.Contains(p.Detail, "request's deadline passed") || strings.Contains(p.Detail, "within") {
		t.Fatalf("got %s %q", p.Code, p.Detail)
	}
	if strings.Contains(p.Detail, "number was given out") {
		t.Fatalf("detail %q claims no number was issued, which a timeout does not show", p.Detail)
	}
}
