package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/ledger/internal/domain"
)

// The period of testPostingDate (2026-03-10).
var (
	lockStart = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	lockEnd   = time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
)

// putLock writes the period lock as close does when it reports a transition: every source governed by
// all's status, with the given per-source overrides.
func putLock(t *testing.T, e env, period string, version int64, all string, sources ...PeriodLockSource) PeriodLock {
	t.Helper()
	out, err := e.store.PutPeriodLock(e.ctx, lockInput(e, period, version, all, sources...))
	if err != nil {
		t.Fatalf("put period lock: %v", err)
	}
	return out
}

func lockInput(e env, period string, version int64, all string, sources ...PeriodLockSource) PeriodLockInput {
	return PeriodLockInput{LedgerID: e.ledger.ID, FiscalPeriodID: period, Start: lockStart, End: lockEnd, Version: version, Actor: "controller",
		Sources: append([]PeriodLockSource{{SourceSystem: LockSourceOther, Module: "all", Status: all}}, sources...)}
}

// openGate stands for the close service answering open: the refusals below come from the ledger's own lock.
func openGate(ctx context.Context) context.Context {
	return WithPeriodGate(ctx, &fakeGate{status: statusOpen})
}

func TestPostgres_PeriodLockRefusesEveryPostingPathWhileCloseStillAnswersOpen(t *testing.T) {
	e := setupLedgerSet(t)
	putName(t, e.env, "GJ", JournalName{Active: true})
	orig, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "OPEN-1", "100.00"))
	if err != nil {
		t.Fatal(err)
	}
	draft, err := e.store.CreateBatch(e.ctx, batchOf(e.env, "GJ", "55"))
	if err != nil {
		t.Fatal(err)
	}
	validated, err := e.store.SubmitBatch(e.ctx, draft.ID, 1, testToday)
	if err != nil {
		t.Fatal(err)
	}
	putLock(t, e.env, ids.New(), 1, statusClosed)
	before := entryTotal(t, e.env)
	for name, ctx := range map[string]context.Context{"close answers open": openGate(e.ctx), "no close client": e.ctx} {
		t.Run(name, func(t *testing.T) {
			_, err := e.store.PostEntry(ctx, vendorInvoice(e.ledger.ID, "LOCK-1", "10.00"))
			requirePeriodClosed(t, "PostEntry", err)
			res, err := e.store.ValidateEntry(ctx, vendorInvoice(e.ledger.ID, "LOCK-V", "10.00"))
			if err != nil || res.Valid() || res.Violations[0].Code != codePeriodClosed {
				t.Fatalf("ValidateEntry: %+v %v", res, err)
			}
			_, err = e.store.PostSetEntry(ctx, e.set.ID, vendorInvoice("", "LOCK-SET", "10.00"))
			requirePeriodClosed(t, "PostSetEntry", err)
			hash, _ := domain.HashPayload("reverse locked")
			_, err = e.store.ReverseEntry(ctx, ReverseInput{EntryID: orig.Entries[0].ID, Key: "REV-LOCK", Hash: hash, Today: testToday})
			requirePeriodClosed(t, "ReverseEntry", err)
			_, err = postBatch(e.env, ctx, validated, "BATCH-LOCK", true)
			requirePeriodClosed(t, "PostBatch", err)
		})
	}
	if after := entryTotal(t, e.env); after != before {
		t.Fatalf("a refused posting wrote entries: %d before, %d after", before, after)
	}
}

func TestPostgres_PeriodLockSourceRowWinsOverTheCatchAll(t *testing.T) {
	e, _ := setup(t)
	period := ids.New()
	manual := func(key string) PostInput {
		in := vendorInvoice(e.ledger.ID, key, "10.00")
		in.Source.System = domain.ManualSourceSystem
		return in
	}
	putLock(t, e, period, 1, statusOpen, PeriodLockSource{SourceSystem: "ap", Module: "ap", Status: statusClosed})
	_, err := e.store.PostEntry(openGate(e.ctx), vendorInvoice(e.ledger.ID, "AP-1", "10.00"))
	requirePeriodClosed(t, "ap into a period closed for ap", err)
	if _, err := e.store.PostEntry(openGate(e.ctx), manual("GL-1")); err != nil {
		t.Fatalf("a manual posting is governed by the open catch-all: %v", err)
	}
	putLock(t, e, period, 2, statusOnHold, PeriodLockSource{SourceSystem: "ap", Module: "ap", Status: statusOpen})
	if _, err := e.store.PostEntry(openGate(e.ctx), vendorInvoice(e.ledger.ID, "AP-2", "10.00")); err != nil {
		t.Fatalf("ap's own open row wins over all on hold: %v", err)
	}
	_, err = e.store.PostEntry(openGate(e.ctx), manual("GL-2"))
	requirePeriodClosed(t, "manual into a period on hold", err)
}

func TestPostgres_PeriodLockTreatsAnExpiredReopenAsClosed(t *testing.T) {
	e, _ := setup(t)
	period := ids.New()
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	putLock(t, e, period, 1, statusClosed, PeriodLockSource{SourceSystem: "ap", Module: "ap", Status: statusOpen, OpenUntil: &future})
	if _, err := e.store.PostEntry(openGate(e.ctx), vendorInvoice(e.ledger.ID, "REOPENED-1", "10.00")); err != nil {
		t.Fatalf("post into a reopened period: %v", err)
	}
	putLock(t, e, period, 2, statusClosed, PeriodLockSource{SourceSystem: "ap", Module: "ap", Status: statusOpen, OpenUntil: &past})
	_, err := e.store.PostEntry(openGate(e.ctx), vendorInvoice(e.ledger.ID, "REOPENED-2", "10.00"))
	requirePeriodClosed(t, "post after the reopen expired, before close's sweeper ran", err)
}

func TestPostgres_PeriodLockHonoursTheClosedPeriodDuty(t *testing.T) {
	for status, allowed := range map[string]bool{statusClosed: true, statusOnHold: true, statusPermanentClosed: false, statusNeverOpened: false} {
		t.Run(status, func(t *testing.T) {
			e, _ := setup(t)
			putLock(t, e, ids.New(), 1, status)
			_, err := e.store.PostEntry(openGate(withDuty(e, DutyPostClosedPeriod)), vendorInvoice(e.ledger.ID, "ADJ-1", "10.00"))
			if allowed && err != nil {
				t.Fatalf("holder post into %s: %v", status, err)
			}
			if !allowed {
				requirePeriodClosed(t, "holder post into "+status, err)
			}
		})
	}
}

func TestPostgres_PeriodLockAppliesToItsDatesOnly(t *testing.T) {
	e, _ := setup(t)
	in := lockInput(e, ids.New(), 1, statusClosed)
	in.Start, in.End = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	if _, err := e.store.PutPeriodLock(e.ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := e.store.PostEntry(openGate(e.ctx), vendorInvoice(e.ledger.ID, "MARCH-1", "10.00")); err != nil {
		t.Fatalf("a February lock must not refuse a March posting: %v", err)
	}
}

func TestPostgres_PutPeriodLockOrdersUpdatesAndReplacesTheSources(t *testing.T) {
	e, _ := setup(t)
	period := ids.New()
	first := putLock(t, e, period, 10, statusOpen, PeriodLockSource{SourceSystem: "ap", Module: "ap", Status: statusClosed})
	if len(first.Sources) != 2 || first.Version != 10 || first.StartDate != "2026-03-01" || first.EndDate != "2026-03-31" {
		t.Fatalf("stored lock %+v", first)
	}
	if _, err := e.store.PutPeriodLock(e.ctx, lockInput(e, period, 9, statusClosed)); !isProblem(err, http.StatusConflict, CodePeriodLockStale) {
		t.Fatalf("an older update must be refused as stale, got %v", err)
	}
	replay := putLock(t, e, period, 10, statusOpen, PeriodLockSource{SourceSystem: "ap", Module: "ap", Status: statusClosed})
	if len(replay.Sources) != 2 {
		t.Fatalf("a repeated update must be accepted: %+v", replay)
	}
	next := putLock(t, e, period, 11, statusClosed)
	if len(next.Sources) != 1 || next.Sources[0].SourceSystem != LockSourceOther || next.Sources[0].Status != statusClosed {
		t.Fatalf("a source close no longer sends must be dropped: %+v", next)
	}
}

func TestPostgres_PutPeriodLockStaysInsideTheLegalEntity(t *testing.T) {
	e, _ := setup(t)
	other := principalCtx(e.tenant, ids.New())
	if _, err := e.store.PutPeriodLock(other, lockInput(e, ids.New(), 1, statusClosed)); !isProblem(err, http.StatusNotFound, "LEDGER_NOT_FOUND") {
		t.Fatalf("a caller of another legal entity must not lock the ledger: %v", err)
	}
	foreign := principalCtx(testkit.TenantID(2), e.le)
	if _, err := e.store.PutPeriodLock(foreign, lockInput(e, ids.New(), 1, statusClosed)); !isProblem(err, http.StatusNotFound, "LEDGER_NOT_FOUND") {
		t.Fatalf("another tenant must not lock the ledger: %v", err)
	}
}

// A posting that is inside its transaction when close's lock update arrives commits first; the lock update
// waits for it and returns only after it, so close reports the period closed only once that posting is in.
// The posting is held after its period check by an admin transaction locking the balance row it updates.
func TestPostgres_PostingInFlightCommitsBeforeTheLockUpdateReturns(t *testing.T) {
	e, _ := setup(t)
	if _, err := e.store.PostEntry(e.ctx, vendorInvoice(e.ledger.ID, "SEED-1", "1.00")); err != nil {
		t.Fatal(err)
	}
	hold, err := e.admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(context.Background()) }()
	if _, err := hold.Exec(context.Background(), `select 1 from balance where tenant_id = $1 and ledger_id = $2 for update`, e.tenant, e.ledger.ID); err != nil {
		t.Fatal(err)
	}
	var order []string
	var mu sync.Mutex
	mark := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	posted := make(chan error, 1)
	go func() {
		_, err := e.store.PostEntry(openGate(e.ctx), vendorInvoice(e.ledger.ID, "INFLIGHT-1", "10.00"))
		mark("posting committed")
		posted <- err
	}()
	waitForLockWait(t, e, "insert into balance")
	locked := make(chan error, 1)
	go func() {
		_, err := e.store.PutPeriodLock(e.ctx, lockInput(e, ids.New(), 1, statusClosed))
		mark("lock returned")
		locked <- err
	}()
	waitForLockWait(t, e, "from ledger where")
	select {
	case err := <-locked:
		t.Fatalf("the lock update returned while a posting was in flight: %v", err)
	default:
	}
	if err := hold.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-posted; err != nil {
		t.Fatalf("the posting that started before the close must commit: %v", err)
	}
	if err := <-locked; err != nil {
		t.Fatalf("lock update: %v", err)
	}
	if order[0] != "posting committed" {
		t.Fatalf("order %v: the lock update must return after the posting in flight", order)
	}
	_, err = e.store.PostEntry(openGate(e.ctx), vendorInvoice(e.ledger.ID, "AFTER-1", "10.00"))
	requirePeriodClosed(t, "post after the lock update", err)
}

// waitForLockWait waits until a backend of the test database is blocked on a lock while running a
// statement that mentions table.
func waitForLockWait(t *testing.T, e env, table string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := e.admin.QueryRow(context.Background(), `select count(*) from pg_stat_activity
			where datname = current_database() and wait_event_type = 'Lock' and query ilike '%' || $1 || '%'`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no statement on %s ever waited for a lock", table)
}

// Concurrent postings racing a close: once the lock update has returned (close then reports the period
// closed), no posting commits, although the close service still answers open.
func TestPostgres_ConcurrentPostingsNeverCommitAfterTheLockUpdate(t *testing.T) {
	e, _ := setup(t)
	const workers = 8
	var closedAt atomic.Int64 // UnixNano at which the lock update returned; 0 while open
	var wg sync.WaitGroup
	stop := make(chan struct{})
	failures := make(chan error, workers*64)
	var committed atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				started := time.Now().UnixNano()
				_, err := e.store.PostEntry(openGate(e.ctx), vendorInvoice(e.ledger.ID, fmt.Sprintf("RACE-%d-%d", w, i), "1.00"))
				closed := closedAt.Load()
				var rej *domain.RejectedError
				switch {
				case err == nil:
					committed.Add(1)
					if closed != 0 && started > closed {
						failures <- fmt.Errorf("worker %d: a posting started after the close committed", w)
					}
				case errors.As(err, &rej) && rej.Violations[0].Code == codePeriodClosed:
					if closed == 0 {
						// Refused before the lock update returned: it took the ledger lock after the update.
						continue
					}
				default:
					failures <- fmt.Errorf("worker %d: %v", w, err)
				}
			}
		}(w)
	}
	for committed.Load() < workers*2 {
		time.Sleep(5 * time.Millisecond)
	}
	putLock(t, e, ids.New(), 1, statusClosed)
	closedAt.Store(time.Now().UnixNano())
	atClose := entryTotal(t, e)
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if after := entryTotal(t, e); after != atClose {
		t.Fatalf("%d entries at close, %d after: postings committed after the close completed", atClose, after)
	}
}
