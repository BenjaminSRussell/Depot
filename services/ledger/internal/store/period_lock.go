package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
)

// The period lock (FR-14.1, B-08c) is the ledger's own copy of close's period status, one row per fiscal
// period and source system, in the ledger database. Close writes it with PutPeriodLock and reports a period
// transition only after the ledger has acknowledged it; every posting path reads it inside its own
// transaction. The lock update takes the ledger row FOR UPDATE and then the lock rows; every path that writes
// an entry holds that same ledger row FOR UPDATE (for its gap-free sequence) before it reads the lock, so a
// posting either commits before the lock changes or reads the new status: no posting commits into a period
// after close has reported it closed.
const (
	// LockSourceOther is the source system of the row that governs every source close maps to no module.
	LockSourceOther       = "*"
	CodePeriodLockStale   = "LEDGER_PERIOD_LOCK_STALE"
	CodePeriodLockBusy    = "LEDGER_PERIOD_LOCK_BUSY"
	statusPermanentClosed = "permanently_closed"
	statusNeverOpened     = "never_opened"
	// periodLockWait bounds how long the lock update waits for postings in flight on the ledger; close's
	// client gives up after 3 s and leaves its period unchanged.
	periodLockWait = "2000ms"
)

// PeriodLockSource is the status close resolved for postings from one source system. OpenUntil is the expiry
// of a temporary reopen: from then on the ledger treats an open or on-hold period as closed.
type PeriodLockSource struct {
	SourceSystem string     `json:"sourceSystem"`
	Module       string     `json:"module"`
	Status       string     `json:"status"`
	OpenUntil    *time.Time `json:"openUntil,omitempty"`
}

// PeriodLockInput replaces the period lock of one fiscal period of a ledger. Version orders the updates of a
// period: an update older than the stored one is refused (CodePeriodLockStale).
type PeriodLockInput struct {
	LedgerID       string
	FiscalPeriodID string
	Start, End     time.Time
	Version        int64
	Sources        []PeriodLockSource
	Actor          string
}

// PeriodLock is the stored lock of one fiscal period.
type PeriodLock struct {
	LedgerID       string             `json:"ledgerId"`
	FiscalPeriodID string             `json:"fiscalPeriodId"`
	StartDate      string             `json:"startDate"`
	EndDate        string             `json:"endDate"`
	Version        int64              `json:"version"`
	Sources        []PeriodLockSource `json:"sources"`
}

// PeriodLockStatuses are the statuses a lock source may carry (close's period statuses).
var PeriodLockStatuses = map[string]bool{statusOpen: true, statusOnHold: true, statusClosed: true, statusPermanentClosed: true, statusNeverOpened: true}

// PutPeriodLock replaces the lock rows of one fiscal period. It waits for the postings in flight on the
// ledger (they hold the ledger row) and returns only once the new status is committed, so close may then
// report its transition.
func (s *Postgres) PutPeriodLock(ctx context.Context, in PeriodLockInput) (PeriodLock, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return PeriodLock{}, err
	}
	var out PeriodLock
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('lock_timeout', $1, true)`, periodLockWait); err != nil {
			return err
		}
		ledger, err := loadLedger(ctx, tx, p.TenantID, in.LedgerID, true)
		if err != nil {
			return err
		}
		var stored int64
		if err := tx.QueryRow(ctx, `select coalesce(max(lock_version), 0) from period_lock where tenant_id = $1 and ledger_id = $2 and fiscal_period_id = $3`,
			p.TenantID, ledger.ID, in.FiscalPeriodID).Scan(&stored); err != nil {
			return err
		}
		if stored > in.Version {
			stale := httpx.NewProblem(http.StatusConflict, CodePeriodLockStale, fmt.Sprintf(
				"the period lock holds version %d, newer than %d; close has already sent a later status", stored, in.Version))
			// The stored version, so close can resend above it (a clock step back on close's side).
			stale.Errors = map[string]string{"version": strconv.FormatInt(stored, 10)}
			return stale
		}
		sources := make([]string, 0, len(in.Sources))
		for _, src := range in.Sources {
			sources = append(sources, src.SourceSystem)
			if _, err := tx.Exec(ctx, `insert into period_lock(tenant_id, legal_entity_id, ledger_id, fiscal_period_id, source_system, module, status, open_until, start_date, end_date, lock_version, changed_by)
				values($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
				on conflict (tenant_id, ledger_id, fiscal_period_id, source_system) do update set
					module = excluded.module, status = excluded.status, open_until = excluded.open_until, start_date = excluded.start_date,
					end_date = excluded.end_date, lock_version = excluded.lock_version, changed_by = excluded.changed_by, changed_at = now()`,
				p.TenantID, ledger.LegalEntityID, ledger.ID, in.FiscalPeriodID, src.SourceSystem, src.Module, src.Status, src.OpenUntil,
				in.Start, in.End, in.Version, in.Actor); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `delete from period_lock where tenant_id = $1 and ledger_id = $2 and fiscal_period_id = $3 and source_system <> all($4::text[])`,
			p.TenantID, ledger.ID, in.FiscalPeriodID, sources); err != nil {
			return err
		}
		out, err = readPeriodLockRows(ctx, tx, p.TenantID, ledger.ID, in.FiscalPeriodID)
		return err
	})
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && (pg.Code == "55P03" || pg.Code == "57014") {
			return PeriodLock{}, httpx.NewProblem(http.StatusServiceUnavailable, CodePeriodLockBusy,
				"postings in flight on the ledger held it past the lock wait; nothing was changed, retry the period transition")
		}
		return PeriodLock{}, err
	}
	return out, nil
}

func readPeriodLockRows(ctx context.Context, tx pgx.Tx, tenantID, ledgerID, periodID string) (PeriodLock, error) {
	out := PeriodLock{LedgerID: ledgerID, FiscalPeriodID: periodID, Sources: []PeriodLockSource{}}
	rows, err := tx.Query(ctx, `select source_system, module, status, open_until, start_date::text, end_date::text, lock_version
		from period_lock where tenant_id = $1 and ledger_id = $2 and fiscal_period_id = $3 order by source_system`, tenantID, ledgerID, periodID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var src PeriodLockSource
		if err := rows.Scan(&src.SourceSystem, &src.Module, &src.Status, &src.OpenUntil, &out.StartDate, &out.EndDate, &out.Version); err != nil {
			return out, err
		}
		out.Sources = append(out.Sources, src)
	}
	return out, rows.Err()
}

// lockedPeriodStatus reads the period lock for a posting date and source inside the caller's transaction
// and reports whether the ledger holds a lock for that date at all (a period close has never locked is
// decided by the close service's answer alone).
//
// forWrite is true on the paths that write an entry. They take the ledger row FOR UPDATE here, which is a
// no-op because every one of them already holds it (postEntryTx, the ledger-set, reversal, clearing and
// realignment paths lock it first for the posting sequence) and keeps the guarantee for any later writer.
// The lock update takes that row FOR UPDATE before it changes a lock row, so under that lock a plain read
// sees either the committed old lock (the update then waits for this commit) or the committed new one;
// row locks on period_lock would add nothing. forWrite is false on validate and batch submit, which write
// nothing: a plain read with no lock, so a dry run in flight never blocks a posting or a close.
func lockedPeriodStatus(ctx context.Context, tx pgx.Tx, tenantID, ledgerID string, postingDate time.Time, sourceSystem string, forWrite bool) (lockDecision, error) {
	if forWrite {
		if _, err := tx.Exec(ctx, `select 1 from ledger where tenant_id = $1 and id = $2 for update`, tenantID, ledgerID); err != nil {
			return lockDecision{}, err
		}
	}
	return readLockDecision(ctx, tx, tenantID, ledgerID, postingDate, sourceSystem)
}

type lockDecision struct {
	found  bool
	status string
	source string
}

// lockRank orders statuses from least to most restrictive; permanently closed and never opened outrank the
// statuses the closed-period duty may override, and a status the ledger does not know outranks them all.
func lockRank(status string) int {
	if r, ok := map[string]int{statusOpen: 0, statusOnHold: 1, statusClosed: 2, statusNeverOpened: 3, statusPermanentClosed: 4}[status]; ok {
		return r
	}
	return 5
}

func readLockDecision(ctx context.Context, tx pgx.Tx, tenantID, ledgerID string, postingDate time.Time, sourceSystem string) (lockDecision, error) {
	rows, err := tx.Query(ctx, `select fiscal_period_id::text, source_system, status, (open_until is not null and open_until <= clock_timestamp())
		from period_lock where tenant_id = $1 and ledger_id = $2 and start_date <= $3 and end_date >= $3 and source_system in ($4, $5)
		order by fiscal_period_id, source_system`, tenantID, ledgerID, postingDate, sourceSystem, LockSourceOther)
	if err != nil {
		return lockDecision{}, err
	}
	defer rows.Close()
	type row struct {
		source, status string
	}
	chosen := map[string]row{}
	for rows.Next() {
		var period, source, status string
		var expired bool
		if err := rows.Scan(&period, &source, &status, &expired); err != nil {
			return lockDecision{}, err
		}
		if expired && (status == statusOpen || status == statusOnHold) {
			status = statusClosed
		}
		// The row for the posting's own source wins over the row for every other source.
		if prior, ok := chosen[period]; !ok || (prior.source == LockSourceOther && source != LockSourceOther) {
			chosen[period] = row{source, status}
		}
	}
	if err := rows.Err(); err != nil {
		return lockDecision{}, err
	}
	d := lockDecision{found: len(chosen) > 0, status: statusOpen}
	for _, r := range chosen {
		if lockRank(r.status) > lockRank(d.status) {
			d.status, d.source = r.status, r.source
		}
	}
	return d, nil
}
