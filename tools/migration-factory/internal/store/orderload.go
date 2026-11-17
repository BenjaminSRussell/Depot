package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/tools/migration-factory/internal/domain"
	mfevents "github.com/BenjaminSRussell/depot/tools/migration-factory/internal/events"
)

// ErrNoOrderLoad means the run has not been loaded: it has no order_load row yet.
var ErrNoOrderLoad = errors.New("the run has no order load")

const orderLoadColumns = `run_id::text, legal_entity_id::text, status, staged, loaded, rejected, pending, attempts, last_error, started_by, started_at, finished_at, version`

func scanOrderLoad(row pgx.Row) (domain.OrderLoad, error) {
	var l domain.OrderLoad
	err := row.Scan(&l.RunID, &l.LegalEntityID, &l.Status, &l.Staged, &l.Loaded, &l.Rejected, &l.Pending, &l.Attempts, &l.LastError, &l.StartedBy, &l.StartedAt, &l.FinishedAt, &l.Version)
	return l, err
}

// GetOrderLoad returns the run's load or ErrNoOrderLoad.
func (s *Postgres) GetOrderLoad(ctx context.Context, runID string) (domain.OrderLoad, error) {
	var l domain.OrderLoad
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		l, err = scanOrderLoad(tx.QueryRow(ctx, `select `+orderLoadColumns+` from order_load where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoOrderLoad
		}
		return err
	})
	return l, err
}

// OrderLoadRun names what the load needs of the run.
type OrderLoadRun struct {
	System  string
	Company string
}

// PendingOrderLine is a staged open order line no target holds yet.
type PendingOrderLine struct {
	LineID string
	Line   domain.OpenOrderLine
}

// ClaimOrderLoad starts a pass over the run's staged order lines: it creates or reopens the run's order_load, refuses it while
// another pass holds the lease or a run of another legal entity owns it, and returns the staged lines of the run that no target
// holds yet, in order type, order number and line number order.
func (s *Postgres) ClaimOrderLoad(ctx context.Context, runID, legalEntityID, startedBy string, lease time.Duration) (domain.OrderLoad, OrderLoadRun, []PendingOrderLine, error) {
	var load domain.OrderLoad
	var run OrderLoadRun
	var pending []PendingOrderLine
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var pack, status string
		err := tx.QueryRow(ctx, `select pack, source_system, source_company, status from pack_run where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1 for update`, runID).
			Scan(&pack, &run.System, &run.Company, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if domain.Pack(pack) != domain.PackOpenOrders {
			return fmt.Errorf("%w: pack %s has no order load; only open_orders runs are loaded into sales and purchasing", ErrNotLoadable, pack)
		}
		if domain.RunStatus(status) != domain.StatusReconciled && domain.RunStatus(status) != domain.StatusLoaded {
			return fmt.Errorf("%w: run %s is %s; only a LOADED or RECONCILED run is loaded", ErrNotLoadable, runID, status)
		}
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('orderload|' || current_setting('depot.tenant_id', true) || '|' || $1 || '|' || $2, 0))`, run.System, run.Company); err != nil {
			return fmt.Errorf("lock order load: %w", err)
		}
		var staged int
		if err := tx.QueryRow(ctx, `select count(*) from open_order_opening_line where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&staged); err != nil {
			return err
		}
		var leaseUntil *time.Time
		var held, heldStatus string
		err = tx.QueryRow(ctx, `select status, lease_until, legal_entity_id::text from order_load where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1 for update`, runID).Scan(&heldStatus, &leaseUntil, &held)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			if _, err := tx.Exec(ctx, `insert into order_load (tenant_id, run_id, legal_entity_id, status, staged) values (current_setting('depot.tenant_id', true)::uuid, $1, $2, 'STAGED', $3)`,
				runID, legalEntityID, staged); err != nil {
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
		if err := tx.QueryRow(ctx, `select coalesce(bool_or(o.status = 'LOADING' and o.lease_until > now()), false) from order_load o join pack_run p on p.tenant_id = o.tenant_id and p.run_id = o.run_id
			where o.tenant_id = current_setting('depot.tenant_id', true)::uuid and o.run_id <> $1 and p.source_system = $2 and p.source_company = $3`, runID, run.System, run.Company).Scan(&busy); err != nil {
			return err
		}
		if busy {
			return ErrLoadBusy
		}
		rows, err := tx.Query(ctx, `select o.line_id::text, o.legacy_id, o.order_type, o.order_number, o.line_number, o.item, o.party, o.unit, o.currency,
				o.ordered_qty::text, o.delivered_qty::text, o.unit_price::text, coalesce(to_char(o.delivery_date, 'YYYY-MM-DD'), '')
			from open_order_opening_line o
			where o.tenant_id = current_setting('depot.tenant_id', true)::uuid and o.run_id = $1
			  and not exists (select 1 from order_load_row l where l.tenant_id = o.tenant_id and l.line_id = o.line_id and l.status = 'LOADED')
			order by o.order_type, o.order_number, o.line_number`, runID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p PendingOrderLine
			var ordered, delivered, price string
			if err := rows.Scan(&p.LineID, &p.Line.LegacyID, &p.Line.OrderType, &p.Line.OrderNumber, &p.Line.LineNumber, &p.Line.Item, &p.Line.Party, &p.Line.Unit, &p.Line.Currency,
				&ordered, &delivered, &price, &p.Line.DeliveryDate); err != nil {
				rows.Close()
				return err
			}
			for _, f := range []struct {
				dst *decimal.Decimal
				raw string
			}{{&p.Line.OrderedQty, ordered}, {&p.Line.DeliveredQty, delivered}, {&p.Line.UnitPrice, price}} {
				if *f.dst, err = decimal.NewFromString(f.raw); err != nil {
					rows.Close()
					return err
				}
			}
			pending = append(pending, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update order_load set status = 'LOADING', staged = $2, lease_until = now() + $3::int * interval '1 second', started_by = $4, started_at = now(), finished_at = null,
				attempts = attempts + 1, last_error = '', version = version + 1
			where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID, staged, int32(lease.Seconds()), startedBy); err != nil {
			return err
		}
		load, err = scanOrderLoad(tx.QueryRow(ctx, `select `+orderLoadColumns+` from order_load where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID))
		return err
	})
	return load, run, pending, err
}

func extendOrderLease(ctx context.Context, tx pgx.Tx, runID string, lease time.Duration) error {
	tag, err := tx.Exec(ctx, `update order_load set lease_until = now() + $2::int * interval '1 second'
		where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1 and status = 'LOADING'`, runID, int32(lease.Seconds()))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrLoadBusy
	}
	return nil
}

// RecordOrderBatch marks the lines of an accepted batch LOADED and extends the lease.
func (s *Postgres) RecordOrderBatch(ctx context.Context, runID, legalEntityID string, lease time.Duration, batchKey string, lines []PendingOrderLine) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := extendOrderLease(ctx, tx, runID, lease); err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := tx.Exec(ctx, `insert into order_load_row (tenant_id, run_id, line_id, legal_entity_id, order_type, status, batch_key)
				values (current_setting('depot.tenant_id', true)::uuid, $1, $2, $3, $4, 'LOADED', $5)
				on conflict (tenant_id, line_id) do update set status = 'LOADED', batch_key = excluded.batch_key, code = '', detail = '', updated_at = now()`,
				runID, l.LineID, legalEntityID, l.Line.OrderType, batchKey); err != nil {
				return err
			}
		}
		return nil
	})
}

// OrderReject is a staged line the factory or a target refused, with the reason.
type OrderReject struct {
	Line   PendingOrderLine
	Code   string
	Detail string
}

// RejectOrderLines marks lines REJECTED; a later load retries them (their mapping may have been added).
func (s *Postgres) RejectOrderLines(ctx context.Context, runID, legalEntityID string, lease time.Duration, rejects []OrderReject) error {
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := extendOrderLease(ctx, tx, runID, lease); err != nil {
			return err
		}
		for _, r := range rejects {
			if _, err := tx.Exec(ctx, `insert into order_load_row (tenant_id, run_id, line_id, legal_entity_id, order_type, status, code, detail)
				values (current_setting('depot.tenant_id', true)::uuid, $1, $2, $3, $4, 'REJECTED', $5, $6)
				on conflict (tenant_id, line_id) do update set status = 'REJECTED', code = excluded.code, detail = excluded.detail, updated_at = now()
				where order_load_row.status <> 'LOADED'`, runID, r.Line.LineID, legalEntityID, r.Line.Line.OrderType, r.Code, r.Detail); err != nil {
				return err
			}
		}
		return nil
	})
}

// OrderRejectRow is one rejected line with its legacy id.
type OrderRejectRow struct {
	LegacyID  string
	OrderType string
	Code      string
	Detail    string
}

// ListOrderRejects returns the run's REJECTED lines in order type and legacy id order.
func (s *Postgres) ListOrderRejects(ctx context.Context, runID string, limit int) ([]OrderRejectRow, error) {
	var out []OrderRejectRow
	err := s.tx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.legacy_id, o.order_type, l.code, l.detail from order_load_row l
			join open_order_opening_line o on o.tenant_id = l.tenant_id and o.line_id = l.line_id
			where l.tenant_id = current_setting('depot.tenant_id', true)::uuid and l.run_id = $1 and l.status = 'REJECTED'
			order by o.order_type, o.legacy_id limit $2`, runID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r OrderRejectRow
			if err := rows.Scan(&r.LegacyID, &r.OrderType, &r.Code, &r.Detail); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// OrderFinish is how a pass ended: Fatal stops the load (a target refused the factory), Unavailable leaves the rest pending,
// ActualErr says a target's totals could not be read, else Actual is what each target (SALES, PURCHASE) holds.
type OrderFinish struct {
	Fatal       string
	Unavailable string
	ActualErr   string
	Actual      map[string]domain.OrderTotals
}

// FinishOrderLoad ends a pass: it recounts the rows, compares the batches each target holds with what the factory loaded for the
// open orders of the company, stores the comparison as control totals and the status, and writes
// dev.depot.migration.pack.loaded.v1.
func (s *Postgres) FinishOrderLoad(ctx context.Context, runID string, fin OrderFinish) (domain.OrderLoad, []domain.ControlTotal, error) {
	var load domain.OrderLoad
	var totals []domain.ControlTotal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		var err error
		load, err = scanOrderLoad(tx.QueryRow(ctx, `select `+orderLoadColumns+` from order_load where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1 for update`, runID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoOrderLoad
		}
		if err != nil {
			return err
		}
		var system, company string
		if err := tx.QueryRow(ctx, `select source_system, source_company from pack_run where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&system, &company); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `select count(*) filter (where status = 'LOADED'), count(*) filter (where status = 'REJECTED') from order_load_row
			where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID).Scan(&load.Loaded, &load.Rejected); err != nil {
			return err
		}
		load.Pending = max(load.Staged-load.Loaded-load.Rejected, 0)
		load.LastError = ""
		switch {
		case fin.Fatal != "":
			load.Status, load.LastError = domain.LoadFailed, fin.Fatal
		case load.Pending > 0 || fin.Unavailable != "":
			// A target that did not answer leaves its lines unsent: the pass is PARTIAL even when every line already has a verdict
			// (a rejected line that was sent again keeps its old one until the answer comes).
			load.Status, load.LastError = domain.LoadPartial, fin.Unavailable
		case fin.ActualErr != "":
			load.Status, load.LastError = domain.LoadUnreconciled, fin.ActualErr
		default:
			expected, err := expectedOrderTotals(ctx, tx, system, company, load.LegalEntityID)
			if err != nil {
				return err
			}
			for _, ot := range []string{domain.OrderSales, domain.OrderPurchase} {
				exp, act := expected[ot], fin.Actual[ot]
				if exp.Value == nil {
					exp = domain.NewOrderTotals()
				}
				if act.Value == nil {
					act = domain.NewOrderTotals()
				}
				if exp.Lines == 0 && act.Lines == 0 {
					continue
				}
				part, err := domain.ReconcileOrders(load.LegalEntityID, ot, exp, act)
				if err != nil {
					return err
				}
				totals = append(totals, part...)
			}
			if err := upsertTargetTotals(ctx, tx, runID, totals); err != nil {
				return err
			}
			load.Status = domain.LoadReconciled
			if _, mismatch := domain.CountTotals(totals); mismatch > 0 {
				load.Status = domain.LoadMismatch
			}
		}
		if _, err := tx.Exec(ctx, `update order_load set status = $2, loaded = $3, rejected = $4, pending = $5, last_error = $6, lease_until = null, finished_at = now(), version = version + 1
			where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID, load.Status, load.Loaded, load.Rejected, load.Pending, load.LastError); err != nil {
			return err
		}
		match, mismatch := domain.CountTotals(totals)
		if err := enqueue(ctx, tx, mfevents.PackLoaded, "pack_run", runID, mfevents.PackLoadedData{
			RunID: runID, Pack: string(domain.PackOpenOrders), Target: "sales+purchasing", ImportSource: string(domain.PackOpenOrders) + "/" + company, LegalEntityID: load.LegalEntityID, Status: load.Status,
			Created: load.Loaded, Rejected: load.Rejected, Pending: load.Pending, TotalsMatch: match, TotalsMismatch: mismatch,
		}); err != nil {
			return fmt.Errorf("enqueue pack.loaded: %w", err)
		}
		load, err = scanOrderLoad(tx.QueryRow(ctx, `select `+orderLoadColumns+` from order_load where tenant_id = current_setting('depot.tenant_id', true)::uuid and run_id = $1`, runID))
		return err
	})
	return load, totals, err
}

// expectedOrderTotals sums, per order type, every staged line of the company across all runs: the line count, the open quantity and
// the open value per currency. A line that was rejected or held back is part of the open orders the targets should hold, so it shows
// as a difference instead of dropping out of both sides.
func expectedOrderTotals(ctx context.Context, tx pgx.Tx, system, company, legalEntityID string) (map[string]domain.OrderTotals, error) {
	rows, err := tx.Query(ctx, `select o.order_type, o.currency, count(*), sum(o.ordered_qty - o.delivered_qty)::text, sum(round((o.ordered_qty - o.delivered_qty) * o.unit_price, 6))::text
		from open_order_opening_line o
		where o.tenant_id = current_setting('depot.tenant_id', true)::uuid and o.source_system = $1 and o.source_company = $2
		  and $3::uuid is not null
		group by o.order_type, o.currency`, system, company, legalEntityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]domain.OrderTotals{}
	for rows.Next() {
		var ot, cur, qty, value string
		var n int
		if err := rows.Scan(&ot, &cur, &n, &qty, &value); err != nil {
			return nil, err
		}
		t, ok := out[ot]
		if !ok {
			t = domain.NewOrderTotals()
		}
		q, err := decimal.NewFromString(qty)
		if err != nil {
			return nil, err
		}
		v, err := decimal.NewFromString(value)
		if err != nil {
			return nil, err
		}
		t.Lines += n
		t.Qty = t.Qty.Add(q)
		t.Value[cur] = v
		out[ot] = t
	}
	return out, rows.Err()
}
