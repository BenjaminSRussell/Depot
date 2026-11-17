package store_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/close/internal/domain"
	"github.com/BenjaminSRussell/depot/services/close/internal/store"
)

// recordingLocker stands for the ledger's period-lock endpoint (B-08c).
type recordingLocker struct {
	locks  []domain.PeriodLock
	err    error
	during func(domain.PeriodLock)
}

func (l *recordingLocker) LockPeriod(_ context.Context, lock domain.PeriodLock) error {
	if l.during != nil {
		l.during(lock)
	}
	if l.err != nil {
		return l.err
	}
	l.locks = append(l.locks, lock)
	return nil
}

func committedStatus(t *testing.T, admin *pgxpool.Pool, module domain.Module) string {
	t.Helper()
	var status string
	err := admin.QueryRow(context.Background(), `select status from period_status where fiscal_period_id = $1 and module = $2`, periodID, module).Scan(&status)
	if err != nil {
		return "none"
	}
	return status
}

func sourceStatus(t *testing.T, lock domain.PeriodLock, source string) domain.LockSource {
	t.Helper()
	for _, s := range lock.Sources {
		if s.SourceSystem == source {
			return s
		}
	}
	t.Fatalf("lock has no source %q: %+v", source, lock.Sources)
	return domain.LockSource{}
}

// FR-14.1, B-08c: the ledger's lock is set inside the transition, while close's own change is still
// uncommitted, and the transition reports success only after the ledger acknowledged.
func TestPeriodLock_TransitionLocksTheLedgerBeforeItCommits(t *testing.T) {
	admin, app := migrated(t)
	locker := &recordingLocker{}
	s := store.New(app).WithPeriodLocks(locker)
	ctx := principal(1)
	registerJan(t, s, ctx)
	var seenDuring string
	locker.during = func(domain.PeriodLock) { seenDuring = committedStatus(t, admin, domain.ModuleAP) }
	out, err := s.TransitionPeriod(ctx, domain.PeriodTransition{
		LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAP, To: domain.StatusClosed, Actor: "alice",
	})
	if err != nil || out.Status != domain.StatusClosed {
		t.Fatalf("close ap: %+v %v", out, err)
	}
	if seenDuring != "none" {
		t.Fatalf("the ledger was asked after close committed (ap row %q during the lock)", seenDuring)
	}
	if len(locker.locks) != 1 {
		t.Fatalf("locks %+v", locker.locks)
	}
	lock := locker.locks[0]
	if lock.LegalEntityID != entityID || lock.LedgerID != ledgerID || lock.FiscalPeriodID != periodID || lock.Version <= 0 ||
		lock.Start.Format("2006-01-02") != "2026-01-01" || lock.End.Format("2006-01-02") != "2026-01-31" {
		t.Fatalf("lock %+v", lock)
	}
	if got := sourceStatus(t, lock, "ap"); got.Status != domain.StatusClosed || got.Module != domain.ModuleAP {
		t.Fatalf("ap source %+v", got)
	}
	if got := sourceStatus(t, lock, "manual"); got.Status != domain.StatusOpen || got.Module != domain.ModuleGL {
		t.Fatalf("manual source %+v", got)
	}
	if got := sourceStatus(t, lock, domain.LockSourceOther); got.Status != domain.StatusClosed {
		t.Fatalf("an unknown source meets the most restrictive row: %+v", got)
	}
	again, err := s.TransitionPeriod(ctx, domain.PeriodTransition{
		LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAR, To: domain.StatusClosed, Actor: "alice",
	})
	if err != nil || again.Status != domain.StatusClosed || len(locker.locks) != 2 || locker.locks[1].Version <= lock.Version {
		t.Fatalf("a later transition must send a later version: %+v %v %+v", again, err, locker.locks)
	}
}

// A ledger that does not confirm the lock rolls the transition back: the period keeps its status and no
// status-changed event is written.
func TestPeriodLock_UnconfirmedLockLeavesThePeriodUnchanged(t *testing.T) {
	admin, app := migrated(t)
	locker := &recordingLocker{err: errors.New("ledger: connection refused")}
	s := store.New(app).WithPeriodLocks(locker)
	ctx := principal(1)
	registerJan(t, s, ctx)
	_, err := s.TransitionPeriod(ctx, domain.PeriodTransition{
		LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll, To: domain.StatusClosed, Actor: "alice",
	})
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Status != http.StatusServiceUnavailable || p.Code != store.CodeLedgerLockUnavailable {
		t.Fatalf("want 503 %s, got %v", store.CodeLedgerLockUnavailable, err)
	}
	if got := committedStatus(t, admin, domain.ModuleAll); got != string(domain.StatusOpen) {
		t.Fatalf("all is %q after an unconfirmed close, want open", got)
	}
	var changes, events int
	if err := admin.QueryRow(context.Background(), `select (select count(*) from period_status_change), (select count(*) from outbox where type = $1)`,
		domain.EventPeriodStatusChanged).Scan(&changes, &events); err != nil || changes != 0 || events != 0 {
		t.Fatalf("an unconfirmed close left %d changes and %d events (%v)", changes, events, err)
	}
}

// A reopen approval loosens the period: the ledger's lock opens it again, with the reopen's expiry, so the
// ledger refuses from the expiry on without waiting for close's sweeper (which sends nothing).
func TestPeriodLock_ReopenApprovalSendsTheExpiry(t *testing.T) {
	admin, app := migrated(t)
	locker := &recordingLocker{}
	s := store.New(app).WithPeriodLocks(locker)
	ctx := principal(1)
	registerJan(t, s, ctx)
	if _, err := s.TransitionPeriod(ctx, domain.PeriodTransition{
		LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll, To: domain.StatusClosed, Actor: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if got := sourceStatus(t, locker.locks[0], domain.LockSourceOther); got.Status != domain.StatusClosed {
		t.Fatalf("closed all: %+v", got)
	}
	req, err := s.RequestReopen(ctx, domain.StoredReopen{LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll,
		Reason: "audit adjustment after close", RequestedBy: "alice", Required: 2, ExpiresDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveReopen(ctx, req.ID, "bob"); err != nil || len(locker.locks) != 1 {
		t.Fatalf("a first approval changes no status and sends no lock: %v %d", err, len(locker.locks))
	}
	if _, err := s.ApproveReopen(ctx, req.ID, "cara"); err != nil || len(locker.locks) != 2 {
		t.Fatalf("final approval: %v, %d locks", err, len(locker.locks))
	}
	for _, src := range []string{domain.LockSourceOther, "manual", "ap"} {
		if got := sourceStatus(t, locker.locks[1], src); got.Status != domain.StatusOpen || got.OpenUntil == nil {
			t.Fatalf("reopened %s: %+v", src, got)
		}
	}
	if _, err := admin.Exec(context.Background(), `update period_status set reopen_expires_at = now() - interval '1 hour' where fiscal_period_id = $1`, periodID); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ExpireReopened(context.Background()); err != nil || n != 1 || len(locker.locks) != 2 {
		t.Fatalf("sweep closed %d (%v) and sent %d locks; the ledger already refuses from the expiry", n, err, len(locker.locks))
	}
}

// A reopen approval the ledger does not confirm stays pending: the period stays closed.
func TestPeriodLock_UnconfirmedReopenStaysPending(t *testing.T) {
	admin, app := migrated(t)
	locker := &recordingLocker{}
	s := store.New(app).WithPeriodLocks(locker)
	ctx := principal(1)
	registerJan(t, s, ctx)
	if _, err := s.TransitionPeriod(ctx, domain.PeriodTransition{
		LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll, To: domain.StatusClosed, Actor: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	req, err := s.RequestReopen(ctx, domain.StoredReopen{LegalEntityID: entityID, LedgerID: ledgerID, FiscalPeriodID: periodID, Module: domain.ModuleAll,
		Reason: "audit adjustment after close", RequestedBy: "alice", Required: 2, ExpiresDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveReopen(ctx, req.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	locker.err = errors.New("ledger: 503")
	if _, err := s.ApproveReopen(ctx, req.ID, "cara"); err == nil {
		t.Fatal("an unconfirmed reopen must fail")
	}
	if got := committedStatus(t, admin, domain.ModuleAll); got != string(domain.StatusClosed) {
		t.Fatalf("all is %q after an unconfirmed reopen, want closed", got)
	}
	locker.err = nil
	if done, err := s.ApproveReopen(ctx, req.ID, "cara"); err != nil || done.Status != "approved" {
		t.Fatalf("the approval can be repeated once the ledger answers: %+v %v", done, err)
	}
}
