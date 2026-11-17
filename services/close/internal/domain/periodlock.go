package domain

import (
	"sort"
	"time"
)

// LockSourceOther is the period-lock source system that stands for every source system ModuleForSource maps
// to no module of its own.
const LockSourceOther = "*"

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
// plus LockSourceOther for every other source, with the same decision the posting check makes (Decide,
// BR-14.1) for a caller in no on-hold group: a source's own module row, else the ALL row; an unknown source
// meets the most restrictive row of the period.
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
	for _, s := range systems {
		m := sourceModules[s]
		status, _ := Decide(plain, m, "")
		out = append(out, LockSource{SourceSystem: s, Module: m, Status: status, OpenUntil: governingExpiry(rows, Resolve(plain, m).Module)})
	}
	status, _ := Decide(plain, ModuleAll, "")
	out = append(out, LockSource{SourceSystem: LockSourceOther, Module: ModuleAll, Status: status, OpenUntil: earliestExpiry(rows)})
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

func earliestExpiry(rows []LockRow) *time.Time {
	var out *time.Time
	for _, r := range rows {
		if r.ReopenExpiresAt != nil && postable(r.Status) && (out == nil || r.ReopenExpiresAt.Before(*out)) {
			out = r.ReopenExpiresAt
		}
	}
	return out
}
