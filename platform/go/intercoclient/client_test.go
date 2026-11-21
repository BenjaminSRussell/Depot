package intercoclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const le = "00000000-0000-0000-0000-0000000000e1"

func testSigner(t *testing.T) *tenant.ServiceSigner {
	t.Helper()
	s, err := tenant.ServiceSignerFromEnv(func(k string) string {
		return map[string]string{"DEPOT_SERVICE_NAME": "consolidation", "DEPOT_SERVICE_SIGNING_KEY": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}[k]
	})
	if err != nil || s == nil {
		t.Fatalf("signer: %v %v", s, err)
	}
	return s
}

func principalCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: "00000000-0000-0000-0000-000000000001", UserID: "u", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

func serve(t *testing.T, runs, detail string, status int) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/r2") {
			if r.URL.Query().Get("status") != "Mismatch" {
				t.Errorf("detail read without the Mismatch filter: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(detail))
			return
		}
		_, _ = w.Write([]byte(runs))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, testSigner(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const runsBody = `{"items":[{"id":"r3","period":"2026-07"},{"id":"r2","period":"2026-06"},{"id":"r1","period":"2026-06"}]}`

func TestOpenMismatches_CountsOnlyUnresolvedItemsOfTheNewestRunOfThePeriod(t *testing.T) {
	c := serve(t, runsBody, `{"items":[{"resolution":"open"},{"resolution":"resolved"},{"resolution":"open"}]}`, http.StatusOK)
	got, err := c.OpenMismatches(principalCtx(), le, "2026-06")
	if err != nil || got != 2 {
		t.Fatalf("got %d, %v; want 2 open", got, err)
	}
}

func TestOpenMismatches_PeriodWithoutARunHasNone(t *testing.T) {
	c := serve(t, runsBody, `{"items":[]}`, http.StatusOK)
	got, err := c.OpenMismatches(principalCtx(), le, "2026-05")
	if err != nil || got != 0 {
		t.Fatalf("got %d, %v; want 0", got, err)
	}
}

func TestOpenMismatches_RefusalIsAnError(t *testing.T) {
	c := serve(t, runsBody, "", http.StatusForbidden)
	_, err := c.OpenMismatches(principalCtx(), le, "2026-06")
	var ce *Error
	if !errors.As(err, &ce) || ce.Status != http.StatusForbidden {
		t.Fatalf("want *Error 403, got %v", err)
	}
}

func TestOpenMismatches_NeedsAPrincipal(t *testing.T) {
	c := serve(t, runsBody, "", http.StatusOK)
	if _, err := c.OpenMismatches(context.Background(), le, "2026-06"); err == nil {
		t.Fatal("want an error without a principal")
	}
}

func TestFromEnv_UnsetIsNilAndSetWithoutKeyFails(t *testing.T) {
	if c, err := FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("unset: %v %v", c, err)
	}
	if _, err := FromEnv(func(k string) string {
		if k == "INTERCOMPANY_URL" {
			return "http://x"
		}
		return ""
	}); err == nil {
		t.Fatal("want an error without the signing key")
	}
}
