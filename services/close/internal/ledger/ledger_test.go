package ledger

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/close/internal/domain"
)

const (
	testEntity = "00000000-0000-0000-0000-0000000000c1"
	testLedger = "00000000-0000-0000-0000-0000000000a1"
	testPeriod = "00000000-0000-0000-0000-0000000000b1"
)

func lockFixture(t *testing.T, h http.HandlerFunc) (*Client, context.Context, ed25519.PublicKey) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	pub, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("close", priv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: testEntity, UserID: "alice", Duties: testkit.ScopeDuties(testEntity)})
	return New(srv.URL, nil, signer), ctx, pub
}

func testLock() domain.PeriodLock {
	return domain.PeriodLock{LegalEntityID: testEntity, LedgerID: testLedger, FiscalPeriodID: testPeriod, Version: 42,
		Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC),
		Sources: []domain.LockSource{{SourceSystem: "*", Module: domain.ModuleAll, Status: domain.StatusClosed}}}
}

func TestLockPeriod_SendsASignedPutWithTheLockDutyOnly(t *testing.T) {
	var got *http.Request
	var body map[string]any
	c, ctx, pub := lockFixture(t, func(w http.ResponseWriter, r *http.Request) {
		got = r
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.LockPeriod(ctx, testLock()); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPut || got.URL.Path != "/v1/ledger/ledgers/"+testLedger+"/period-locks/"+testPeriod {
		t.Fatalf("request %s %s", got.Method, got.URL.Path)
	}
	if body["startDate"] != "2026-01-01" || body["endDate"] != "2026-01-31" || body["version"] != float64(42) || len(body["sources"].([]any)) != 1 {
		t.Fatalf("body %+v", body)
	}
	claims, err := tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"close": pub}).Verify(got.Header.Get(tenant.HeaderServiceToken))
	if err != nil {
		t.Fatal(err)
	}
	if len(claims.Duties) != 1 || claims.Duties[0] != dutyPeriodLock || claims.Le != testEntity || claims.Act != "alice" {
		t.Fatalf("token claims %+v", claims)
	}
}

func TestLockPeriod_FailsUnlessTheLedgerAnswers200(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusServiceUnavailable, http.StatusForbidden, http.StatusNotFound, http.StatusFound} {
		c, ctx, _ := lockFixture(t, func(w http.ResponseWriter, r *http.Request) {
			if status == http.StatusFound {
				http.Redirect(w, r, "/elsewhere", status)
				return
			}
			w.WriteHeader(status)
		})
		if err := c.LockPeriod(ctx, testLock()); err == nil {
			t.Errorf("status %d must fail the lock", status)
		}
	}
	c, ctx, _ := lockFixture(t, func(http.ResponseWriter, *http.Request) {})
	c.baseURL = "http://127.0.0.1:1"
	if err := c.LockPeriod(ctx, testLock()); err == nil {
		t.Error("an unreachable ledger must fail the lock")
	}
	c.signer = nil
	if err := c.LockPeriod(ctx, testLock()); err == nil {
		t.Error("a missing signing key must fail the lock")
	}
	if err := c.LockPeriod(context.Background(), testLock()); err == nil {
		t.Error("no principal must fail the lock")
	}
}
