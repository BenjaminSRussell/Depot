package api_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/close/internal/api"
	"github.com/BenjaminSRussell/depot/services/close/internal/domain"
	"github.com/BenjaminSRussell/depot/services/close/internal/ledger"
	"github.com/BenjaminSRussell/depot/services/close/internal/store"
	"github.com/BenjaminSRussell/depot/services/close/migrations"
)

// lockedPeriods serves the period PATCH from the Postgres store with the real ledger client pointed at
// ledgerURL, as cmd/close wires it, for one registered period whose ALL row is open (B-08c).
func lockedPeriods(t *testing.T, ledgerURL string) (moveFunc, *store.Postgres, context.Context, string, http.Handler) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("close", priv)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(app).WithPeriodLocks(ledger.New(ledgerURL, nil, signer))
	entity, ledgerID, period := testkit.TenantID(9), testkit.TenantID(7), testkit.TenantID(8)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	if _, err := st.RegisterPeriod(ctx, domain.PeriodRegistration{
		LegalEntityID: entity, LedgerID: ledgerID, FiscalPeriodID: period, Actor: "test",
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	a := api.New(st).WithLedgers(fakeLedger{owner: entity}).WithSliceB(st, st, st, st, st, nil)
	r := httpx.NewRouter()
	a.Register(r)
	move := func(module, status string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, "/v1/close/periods/"+period+"/modules/"+module, strings.NewReader(`{"ledgerId":"`+ledgerID+`","status":"`+status+`"}`))
		req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Depot-Duties", "period.maintain")
		req.Header.Set("X-Depot-Legal-Entity", entity)
		req.Header.Set("X-Depot-User", "alice")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	return move, st, ctx, entity, r
}

func allStatus(t *testing.T, st *store.Postgres, ctx context.Context, entity string) domain.Status {
	t.Helper()
	rows, err := st.PeriodMatrix(ctx, entity, testkit.TenantID(7))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Module == domain.ModuleAll {
			return r.Status
		}
	}
	t.Fatalf("no all row in %+v", rows)
	return ""
}

// B-08c: close's PATCH fails cleanly, and the period stays open, when the ledger cannot be reached to set
// its period lock.
func TestPatch_LedgerUnreachableFailsCleanlyAndLeavesThePeriodOpen(t *testing.T) {
	move, st, ctx, entity, _ := lockedPeriods(t, "http://127.0.0.1:1")
	rec := move("all", "closed")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), store.CodeLedgerLockUnavailable) {
		t.Fatalf("want 503 %s, got %d %s", store.CodeLedgerLockUnavailable, rec.Code, rec.Body)
	}
	if got := allStatus(t, st, ctx, entity); got != domain.StatusOpen {
		t.Fatalf("all is %s after a close the ledger never confirmed, want open", got)
	}
}

func TestPatch_LedgerRefusalFailsCleanlyAndLeavesThePeriodOpen(t *testing.T) {
	ledgerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(ledgerSrv.Close)
	move, st, ctx, entity, _ := lockedPeriods(t, ledgerSrv.URL)
	if rec := move("all", "closed"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d %s", rec.Code, rec.Body)
	}
	if got := allStatus(t, st, ctx, entity); got != domain.StatusOpen {
		t.Fatalf("all is %s, want open", got)
	}
}

// The PATCH answers 200 only after the ledger has acknowledged the lock, and the lock carries the period.
func TestPatch_ReportsTheCloseOnlyAfterTheLedgerAcknowledged(t *testing.T) {
	var acked atomic.Bool
	var path, body atomic.Value
	ledgerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		path.Store(r.Method + " " + r.URL.Path)
		body.Store(string(raw[:n]))
		acked.Store(true)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ledgerSrv.Close)
	move, st, ctx, entity, _ := lockedPeriods(t, ledgerSrv.URL)
	rec := move("all", "closed")
	if rec.Code != http.StatusOK || !acked.Load() {
		t.Fatalf("close: %d %s, ledger acknowledged %v", rec.Code, rec.Body, acked.Load())
	}
	if want := "PUT /v1/ledger/ledgers/" + testkit.TenantID(7) + "/period-locks/" + testkit.TenantID(8); path.Load() != want {
		t.Fatalf("ledger called with %v, want %s", path.Load(), want)
	}
	if b := body.Load().(string); !strings.Contains(b, `"sourceSystem":"*","module":"all","status":"closed"`) || !strings.Contains(b, `"startDate":"2026-03-01"`) {
		t.Fatalf("lock body %s", b)
	}
	if got := allStatus(t, st, ctx, entity); got != domain.StatusClosed {
		t.Fatalf("all is %s, want closed", got)
	}
}

// Review of B-08c (minor 4): POST /v1/close/periods/{id}:resync-ledger-lock resends the current matrix to
// the ledger with a newer version and changes nothing in close.
func TestResync_ResendsTheMatrixToTheLedger(t *testing.T) {
	var calls atomic.Int64
	ledgerSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ledgerSrv.Close)
	move, st, ctx, entity, router := lockedPeriods(t, ledgerSrv.URL)
	if rec := move("all", "closed"); rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body)
	}
	resync := func(duty string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/close/periods/"+testkit.TenantID(8)+":resync-ledger-lock", strings.NewReader(`{"ledgerId":"`+testkit.TenantID(7)+`"}`))
		req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Depot-Duties", duty)
		req.Header.Set("X-Depot-Legal-Entity", entity)
		req.Header.Set("X-Depot-User", "alice")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := resync("period.reopen"); rec.Code != http.StatusForbidden {
		t.Fatalf("resync needs period.maintain: %d %s", rec.Code, rec.Body)
	}
	rec := resync("period.maintain")
	if rec.Code != http.StatusOK || calls.Load() != 2 || !strings.Contains(rec.Body.String(), `"version"`) {
		t.Fatalf("resync: %d %s, ledger calls %d", rec.Code, rec.Body, calls.Load())
	}
	if got := allStatus(t, st, ctx, entity); got != domain.StatusClosed {
		t.Fatalf("all is %s after a resync, want closed", got)
	}
}

// Review of B-08c (nit): without LEDGER_URL a reopen approval is refused like a PATCH, since the approval
// that opens a period must set the ledger's period lock.
func TestApproveReopen_WithoutTheLedgerIsRefused(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(app)
	a := api.New(st).WithSliceB(st, st, st, st, st, nil)
	r := httpx.NewRouter()
	a.Register(r)
	req := httptest.NewRequest(http.MethodPost, "/v1/close/reopen-requests/"+testkit.TenantID(5)+":approve", strings.NewReader(`{}`))
	req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Depot-Duties", "period.reopen")
	req.Header.Set("X-Depot-Legal-Entity", testkit.TenantID(9))
	req.Header.Set("X-Depot-User", "bob")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 without the ledger, got %d %s", rec.Code, rec.Body)
	}
}
