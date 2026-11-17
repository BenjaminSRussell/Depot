package domain

import (
	"errors"
	"sort"
	"strconv"
	"time"
)

// LockSourceOther is the period-lock source system that stands for every source system ModuleForSource maps
// to no module of its own.
const LockSourceOther = "*"

// ErrLedgerLockBusy is the ledger's 503 LEDGER_PERIOD_LOCK_BUSY: postings in flight held the ledger past its
// 2 s wait. Close retries the lock once.
var ErrLedgerLockBusy = errors.New("ledger: period lock busy")

// StaleLockError is the ledger's 409 LEDGER_PERIOD_LOCK_STALE: it already holds version Stored, newer than the
// one sent. Close resends once above it.
type StaleLockError struct{ Stored int64 }

func (e *StaleLockError) Error() string {
	return "ledger: period lock holds newer version " + strconv.FormatInt(e.Stored, 10)
}

// LockRow is a period_status row with the expiry of a temporary reopen.
type LockRow struct {
	Row
	ReopenExpiresAt *time.Time
}

// PeriodLock is what close sends the ledger inside a period transition (FR-14.1, B-08c): for each source
// system the status a posting from it meets, as the ledger cannot read period_status inside its posting
// transaction (database per service, ADR-0008/ADR-0012). Version orders the locks of one period.
type PeriodLock struct {
	LegalEntityID  string
	LedgerID       string
	FiscalPeriodID string
	Start, End     time.Time
	Version        int64
	Sources        []LockSource
}

// LockSource is the status of postings from one source system. OpenUntil is the expiry of the temporary
// reopen that keeps it open or on hold; the ledger refuses from then on, before the sweeper closes it.
type LockSource struct {
	SourceSystem string     `json:"sourceSystem"`
	Module       Module     `json:"module"`
	Status       Status     `json:"status"`
	OpenUntil    *time.Time `json:"openUntil,omitempty"`
}

// LockSources resolves the period's rows into one lock source per source system close maps to a module,
// plus LockSourceOther for every other source, with the same Resolve the ledger's close check uses
// (GET /v1/close/periods:resolve, BR-14.1): a source's own module row, else the ALL row, else never opened.
// An unmapped source resolves to module all, which is the ALL row alone, so closing AP while ALL is open
// does not stop billing or inventory postings mid-close (the FR-14.2 order). Each source carries the reopen
// expiry of the row that governs it.
func LockSources(rows []LockRow) []LockSource {
	plain := make([]Row, len(rows))
	for i, r := range rows {
		plain[i] = r.Row
	}
	systems := make([]string, 0, len(sourceModules))
	for s := range sourceModules {
		systems = append(systems, s)
	}
	sort.Strings(systems)
	out := make([]LockSource, 0, len(systems)+1)
	add := func(system string, m Module) {
		row := Resolve(plain, m)
		out = append(out, LockSource{SourceSystem: system, Module: m, Status: row.Status, OpenUntil: governingExpiry(rows, row.Module)})
	}
	for _, s := range systems {
		add(s, sourceModules[s])
	}
	add(LockSourceOther, ModuleAll)
	return out
}

func governingExpiry(rows []LockRow, module Module) *time.Time {
	for _, r := range rows {
		if r.Module == module {
			return r.ReopenExpiresAt
		}
	}
	return nil
}
