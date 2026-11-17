package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/tools/migration-factory/internal/domain"
	mfevents "github.com/BenjaminSRussell/depot/tools/migration-factory/internal/events"
)

// Target load of the staged packs (#49): the same lease, claim, record and finish cycle as the open orders load, over staged_load.

// ErrNoStagedLoad means the run has not been loaded: it has no staged_load row yet.
var ErrNoStagedLoad = errors.New("the run has no staged load")

const stagedLoadColumns = `run_id::text, pack, legal_entity_id::text, status, staged, loaded, rejected, pending, attempts, last_error, started_by, started_at, finished_at, version`

func scanStagedLoad(row pgx.Row) (domain.StagedLoad, error) {
	var l domain.StagedLoad
	var pack string
	err := row.Scan(&l.RunID, &pack, &l.LegalEntityID, &l.Status, &l.Staged, &l.Loaded, &l.Rejected, &l.Pending, &l.Attempts, &l.LastError, &l.StartedBy, &l.StartedAt, &l.FinishedAt, &l.Version)
	l.Pack = domain.Pack(pack)
	return l, err
}

// GetStagedLoad returns the run's load or ErrNoStagedLoad.
func (s *Postgres) GetStagedLoad(ctx context.Context, runID string) (domain.StagedLoad, error) {
	var l domain.StagedLoad
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		l, err = scanStagedLoad(tx.QueryRow(ctx, `select `+stagedLoadColumns+` from staged_load where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoStagedLoad
		}
		return err
	})
	return l, err
}

// StagedLoadRun names what the load needs of the run.
type StagedLoadRun struct {
	Pack     domain.Pack
	System   string
	Company  string
	CutoffTS time.Time
}

// PendingStagedLine is a staged line no target holds yet.
type PendingStagedLine struct {
	LineID string
	Line   domain.StagedLine
}

// ClaimStagedLoad starts a pass over the run's staged lines: it creates or reopens the run's staged_load, refuses it while another pass holds the
// lease or a run of another legal entity owns it, and returns the staged lines of the run no target holds yet, in legacy id order.
func (s *Postgres) ClaimStagedLoad(ctx context.Context, runID, legalEntityID, startedBy string, lease time.Duration) (domain.StagedLoad, StagedLoadRun, []PendingStagedLine, error) {
	var load domain.StagedLoad
	var run StagedLoadRun
	var pending []PendingStagedLine
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var pack, status string
		err := tx.QueryRow(ctx, `select pack, source_system, source_company, status, cutoff_ts from pack_run where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1 for update`, runID).
			Scan(&pack, &run.System, &run.Company, &status, &run.CutoffTS)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		run.Pack = domain.Pack(pack)
		if domain.StagedTargetOfPack(run.Pack) == "" {
			return fmt.Errorf("%w: pack %s has no staged load; only fixed_assets, wip_prod and payroll_ytd runs are loaded into assets, production and payroll", ErrNotLoadable, pack)
		}
		if domain.RunStatus(status) != domain.StatusReconciled && domain.RunStatus(status) != domain.StatusLoaded {
			return fmt.Errorf("%w: run %s is %s; only a LOADED or RECONCILED run is loaded", ErrNotLoadable, runID, status)
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('stagedload|' || current_setting('depot.tenant_id', true) || '|' || $1 || '|' || $2 || '|' || $3, 0))`, pack, run.System, run.Company); err != nil {
			return fmt.Errorf("lock staged load: %w", err)
		}
		var staged int
		if err := tx.QueryRow(ctx, `select count(*) from staged_opening_line where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&staged); err != nil {
			return err
		}
		var leaseUntil *time.Time
		var held, heldStatus string
		err = tx.QueryRow(ctx, `select status, lease_until, legal_entity_id::text from staged_load where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1 for update`, runID).Scan(&heldStatus, &leaseUntil, &held)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if _, err := tx.Exec(ctx, `insert into staged_load (tenant_id, run_id, pack, legal_entity_id, status, staged) values (current_setting('depot.tenant_id', true)::uuid, $1, $2, $3, 'STAGED', $4)`,
				runID, pack, legalEntityID, staged); err != nil {
				return scopeErr(err)
			}
		case err != nil:
			return err
		default:
			if held != legalEntityID {
				return fmt.Errorf("%w: run %s is loaded into legal entity %s", domain.ErrInvalid, runID, held)
			}
			if heldStatus == domain.LoadLoading && leaseUntil != nil && leaseUntil.After(time.Now()) {
				return ErrLoadBusy
			}
		}
		var busy bool
		if err := tx.QueryRow(ctx, `select coalesce(bool_or(o.status = 'LOADING' and o.lease_until > now()), false) from staged_load o join pack_run p on p.tenant_id = o.tenant_id and p.run_id = o.run_id
			where o.tenant_id = current_setting('depot.tenant_id', true)::uuid and o.run_id <> $1 and o.pack = $4 and p.source_system = $2 and p.source_company = $3`, runID, run.System, run.Company, pack).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return ErrLoadBusy
		}
		rows, err := tx.Query(ctx, `select o.line_id::text, o.legacy_id, o.natural_key, o.currency, o.grouping, o.measures
			from staged_opening_line o
			where o.tenant_id = current_setting('depot.tenant_id', true)::uuid and o.run_id = $1
			  and not exists (select 1 from staged_load_row l where l.tenant_id = o.tenant_id and l.line_id = o.line_id and l.status = 'LOADED')
			order by o.legacy_id`, runID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p PendingStagedLine
			var grouping, measures []byte
			if err := rows.Scan(&p.LineID, &p.Line.LegacyID, &p.Line.NaturalKey, &p.Line.Currency, &grouping, &measures); err != nil {
				rows.Close()
				return err
			}
			if p.Line.Group, p.Line.Measures, err = decodeStaged(grouping, measures); err != nil {
				rows.Close()
				return err
			}
			pending = append(pending, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update staged_load set status = 'LOADING', staged = $2, lease_until = now() + $3::int * interval '1 second', started_by = $4, started_at = now(), finished_at = null,
				attempts = attempts + 1, last_error = '', version = version + 1
			where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID, staged, int32(lease.Seconds()), startedBy); err != nil {
			return err
		}
		load, err = scanStagedLoad(tx.QueryRow(ctx, `select `+stagedLoadColumns+` from staged_load where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID))
		return err
	})
	return load, run, pending, err
}

func extendStagedLease(ctx context.Context, tx pgx.Tx, runID string, lease time.Duration) error {
	tag, err := tx.Exec(ctx, `update staged_load set lease_until = now() + $2::int * interval '1 second'
		where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1 and status = 'LOADING'`, runID, int32(lease.Seconds()))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLoadBusy
	}
	return nil
}

// RecordStagedBatch marks the lines of an accepted batch LOADED and extends the lease.
func (s *Postgres) RecordStagedBatch(ctx context.Context, runID, legalEntityID string, lease time.Duration, batchKey string, lines []PendingStagedLine) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := extendStagedLease(ctx, tx, runID, lease); err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := tx.Exec(ctx, `insert into staged_load_row (tenant_id, run_id, line_id, legal_entity_id, status, batch_key)
				values (current_setting('depot.tenant_id', true)::uuid, $1, $2, $3, 'LOADED', $4)
				on conflict (tenant_id, line_id) do update set status = 'LOADED', batch_key = excluded.batch_key, code = '', detail = '', updated_at = now()`,
				runID, l.LineID, legalEntityID, batchKey); err != nil {
				return err
			}
		}
		return nil
	})
}

// StagedReject is a staged line the factory or a target refused, with the reason.
type StagedReject struct {
	Line   PendingStagedLine
	Code   string
	Detail string
}

// RejectStagedLines marks lines REJECTED; a later load retries them (their mapping may have been added).
func (s *Postgres) RejectStagedLines(ctx context.Context, runID, legalEntityID string, lease time.Duration, rejects []StagedReject) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := extendStagedLease(ctx, tx, runID, lease); err != nil {
			return err
		}
		for _, r := range rejects {
			if _, err := tx.Exec(ctx, `insert into staged_load_row (tenant_id, run_id, line_id, legal_entity_id, status, code, detail)
				values (current_setting('depot.tenant_id', true)::uuid, $1, $2, $3, 'REJECTED', $4, $5)
				on conflict (tenant_id, line_id) do update set status = 'REJECTED', code = excluded.code, detail = excluded.detail, updated_at = now()
				where staged_load_row.status <> 'LOADED'`, runID, r.Line.LineID, legalEntityID, r.Code, r.Detail); err != nil {
				return err
			}
		}
		return nil
	})
}

// StagedRejectRow is one rejected line with its legacy id.
type StagedRejectRow struct {
	LegacyID string
	Code     string
	Detail   string
}

// ListStagedRejects returns the run's REJECTED lines in legacy id order.
func (s *Postgres) ListStagedRejects(ctx context.Context, runID string, limit int) ([]StagedRejectRow, error) {
	var out []StagedRejectRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.legacy_id, l.code, l.detail from staged_load_row l
			join staged_opening_line o on o.tenant_id = l.tenant_id and o.line_id = l.line_id
			where l.tenant_id = current_setting('depot.tenant_id', true)::uuid and l.run_id = $1 and l.status = 'REJECTED'
			order by o.legacy_id limit $2`, runID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r StagedRejectRow
			if err := rows.Scan(&r.LegacyID, &r.Code, &r.Detail); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// StagedFinish is how a pass ended: Fatal stops the load (the target refused the factory), Unavailable leaves the rest pending, ActualErr says the
// target's totals could not be read (or its journal could not be posted), else Actual is what the target holds.
type StagedFinish struct {
	Fatal       string
	Unavailable string
	ActualErr   string
	Actual      domain.StagedTotals
}

// FinishStagedLoad ends a pass: it recounts the rows, compares the batches the target holds with what the factory loaded for the staged pack of the
// company, stores the comparison as control totals and the status, and writes dev.depot.migration.pack.loaded.v1.
func (s *Postgres) FinishStagedLoad(ctx context.Context, runID string, fin StagedFinish) (domain.StagedLoad, []domain.ControlTotal, error) {
	var load domain.StagedLoad
	var totals []domain.ControlTotal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		load, err = scanStagedLoad(tx.QueryRow(ctx, `select `+stagedLoadColumns+` from staged_load where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1 for update`, runID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoStagedLoad
		}
		if err != nil {
			return err
		}
		var system, company string
		if err := tx.QueryRow(ctx, `select source_system, source_company from pack_run where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&system, &company); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select count(*) filter (where status = 'LOADED'), count(*) filter (where status = 'REJECTED') from staged_load_row
			where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&load.Loaded, &load.Rejected); err != nil {
			return err
		}
		load.Pending = max(load.Staged-load.Loaded-load.Rejected, 0)
		load.LastError = ""
		switch {
		case fin.Fatal != "":
			load.Status, load.LastError = domain.LoadFailed, fin.Fatal
		case load.Pending > 0 || fin.Unavailable != "":
			load.Status, load.LastError = domain.LoadPartial, fin.Unavailable
		case fin.ActualErr != "":
			load.Status, load.LastError = domain.LoadUnreconciled, fin.ActualErr
		default:
			expected, err := expectedStagedTotals(ctx, tx, load.Pack, system, company)
			if err != nil {
				return err
			}
			act := fin.Actual
			if act.Sums == nil {
				act = domain.NewStagedTotals()
			}
			if totals, err = domain.ReconcileStagedTarget(load.Pack, load.LegalEntityID, expected, act); err != nil {
				return err
			}
			if err := upsertTargetTotals(ctx, tx, runID, totals); err != nil {
				return err
			}
			load.Status = domain.LoadReconciled
			if _, mismatch := domain.CountTotals(totals); mismatch > 0 {
				load.Status = domain.LoadMismatch
			}
		}
		if _, err := tx.Exec(ctx, `update staged_load set status = $2, loaded = $3, rejected = $4, pending = $5, last_error = $6, lease_until = null, finished_at = now(), version = version + 1
			where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID, load.Status, load.Loaded, load.Rejected, load.Pending, load.LastError); err != nil {
			return err
		}
		match, mismatch := domain.CountTotals(totals)
		if err := enqueue(ctx, tx, mfevents.PackLoaded, "pack_run", runID, mfevents.PackLoadedData{
			RunID: runID, Pack: string(load.Pack), Target: domain.StagedTargetOfPack(load.Pack), ImportSource: string(load.Pack) + "/" + company, LegalEntityID: load.LegalEntityID, Status: load.Status,
			Created: load.Loaded, Rejected: load.Rejected, Pending: load.Pending, TotalsMatch: match, TotalsMismatch: mismatch,
		}); err != nil {
			return fmt.Errorf("enqueue pack.loaded: %w", err)
		}
		load, err = scanStagedLoad(tx.QueryRow(ctx, `select `+stagedLoadColumns+` from staged_load where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID))
		return err
	})
	return load, totals, err
}

// expectedStagedTotals sums, over every staged line of the pack for the company across all runs, the target lines and amounts each line must have put
// in its target. A line that was rejected or held back is part of what the target should hold, so it shows as a difference instead of dropping out
// of both sides.
func expectedStagedTotals(ctx context.Context, tx pgx.Tx, pack domain.Pack, system, company string) (domain.StagedTotals, error) {
	rows, err := tx.Query(ctx, `select legacy_id, natural_key, currency, grouping, measures from staged_opening_line
		where tenant_id = current_setting('depot.tenant_id', true)::uuid and pack = $1 and source_system = $2 and source_company = $3`, string(pack), system, company)
	if err != nil {
		return domain.StagedTotals{}, err
	}
	defer rows.Close()
	out := domain.NewStagedTotals()
	for rows.Next() {
		var l domain.StagedLine
		var grouping, measures []byte
		if err := rows.Scan(&l.LegacyID, &l.NaturalKey, &l.Currency, &grouping, &measures); err != nil {
			return domain.StagedTotals{}, err
		}
		if l.Group, l.Measures, err = decodeStaged(grouping, measures); err != nil {
			return domain.StagedTotals{}, err
		}
		n, sums := domain.StagedExpected(pack, l)
		out.Add(n, sums)
	}
	return out, rows.Err()
}

// StagedBook is a depreciation book and currency a fixed_assets run staged lines for.
type StagedBook struct{ Book, Currency string }

// ListStagedBooks returns the distinct books and currencies of the run's staged lines, which the opening journal is booked per.
func (s *Postgres) ListStagedBooks(ctx context.Context, runID string) ([]StagedBook, error) {
	var out []StagedBook
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select distinct grouping->>'book', currency from staged_opening_line
			where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1 order by 1, 2`, runID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b StagedBook
			if err := rows.Scan(&b.Book, &b.Currency); err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}
