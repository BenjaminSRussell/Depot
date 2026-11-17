package ledger

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/close/internal/domain"
)

// The ledger's busy and stale answers come back typed, so close can retry once (B-08c review).
func TestLockPeriod_TypesTheBusyAndStaleAnswers(t *testing.T) {
	c, ctx, _ := lockFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":503,"code":"LEDGER_PERIOD_LOCK_BUSY"}`))
	})
	if err := c.LockPeriod(ctx, testLock()); !errors.Is(err, domain.ErrLedgerLockBusy) {
		t.Fatalf("want ErrLedgerLockBusy, got %v", err)
	}
	c, ctx, _ = lockFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"status":409,"code":"LEDGER_PERIOD_LOCK_STALE","errors":{"version":"1234"}}`))
	})
	var stale *domain.StaleLockError
	if err := c.LockPeriod(ctx, testLock()); !errors.As(err, &stale) || stale.Stored != 1234 {
		t.Fatalf("want a stale error with the stored version, got %v", err)
	}
	c, ctx, _ = lockFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":503,"code":"COM-0503"}`))
	})
	if err := c.LockPeriod(ctx, testLock()); err == nil || errors.Is(err, domain.ErrLedgerLockBusy) {
		t.Fatalf("another 503 is a plain failure, got %v", err)
	}
}

// The reopen-expiry sweeper signs as the service principal close-reopen-expiry, which the ledger accepts
// with ledger.period.lock alone.
func TestLockPeriod_SignsAsTheReopenExpiryPrincipal(t *testing.T) {
	var token string
	c, _, pub := lockFixture(t, func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get(tenant.HeaderServiceToken)
		_, _ = w.Write([]byte(`{}`))
	})
	p, err := tenant.ServicePrincipal("close-reopen-expiry", testkit.TenantID(1), testEntity)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.LockPeriod(tenant.With(t.Context(), p), testLock()); err != nil {
		t.Fatal(err)
	}
	claims, err := tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"close": pub}).Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Act != "service:close-reopen-expiry" || len(claims.Duties) != 1 || claims.Duties[0] != dutyPeriodLock || claims.Le != testEntity {
		t.Fatalf("claims %+v", claims)
	}
}
