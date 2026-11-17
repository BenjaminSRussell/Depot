package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

// systemScopeKey marks a context built by an in-process system code path: the sweeper and the production consumer
// (and the auto clock-out route when a verified service token calls it). It is an unexported context value, so no
// header, token claim or user id can produce it.
type systemScopeKey struct{}

// WithSystemScope marks ctx as a system code path that keeps the tenant's time data consistent whatever legal entity
// employs the worker (B-03b, D17): the auto clock-out of stale shifts and the stop of job starts on an operation
// production finished. Only those code paths call it.
func WithSystemScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, systemScopeKey{}, true)
}

// widenForSystemCaller sets tx's legal-entity scope to every legal entity of the tenant when ctx is marked by
// WithSystemScope and the caller holds no legal-entity claim. A caller with a claim keeps its own scope, and any other
// caller without one keeps the empty scope dbx gave it.
func widenForSystemCaller(ctx context.Context, tx pgx.Tx) error {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("tenant context is required")
	}
	if marked, _ := ctx.Value(systemScopeKey{}).(bool); !marked || p.LegalEntityID != "" {
		return nil
	}
	_, err := tx.Exec(ctx, `select set_config('depot.legal_entity_scopes', '*', true)`)
	return err
}

// SweepActor is the actor the shop floor sweeper runs as. It names the actor in audit columns and logs only; it
// grants nothing (the sweeper's scope comes from WithSystemScope).
const SweepActor = "system:shopfloor-sweeper"

// workerHasTimeOrPay reports whether any time, day, pay, correction or cost-rate row hangs off the worker. Until one
// does, a tenant administrator may still correct the worker's legal entity; after, it never changes.
func workerHasTimeOrPay(ctx context.Context, tx pgx.Tx, tenantID, workerID string) (bool, error) {
	var has bool
	err := tx.QueryRow(ctx, `select exists (select 1 from time_registration where tenant_id = $1 and worker_id = $2)
		or exists (select 1 from worker_day where tenant_id = $1 and worker_id = $2)
		or exists (select 1 from registration_correction where tenant_id = $1 and worker_id = $2)
		or exists (select 1 from worker_cost_rate where tenant_id = $1 and worker_id = $2)`, tenantID, workerID).Scan(&has)
	return has, err
}

// backfillExportBatches gives a payroll export batch without a legal entity the legal entity of its workers once every
// line of it belongs to a worker of legalEntityID (B-03b). It reports how many batches it assigned.
func backfillExportBatches(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string) (int, error) {
	tag, err := tx.Exec(ctx, `update pay_export_batch b set legal_entity_id = $2
		where b.tenant_id = $1 and b.legal_entity_id is null
		  and exists (select 1 from pay_export_line x where x.tenant_id = b.tenant_id and x.batch_id = b.id)
		  and not exists (
		    select 1 from pay_export_line x
		    join pay_line pl on pl.tenant_id = x.tenant_id and pl.id = x.pay_line_id
		    join worker w on w.tenant_id = pl.tenant_id and w.id = pl.worker_id
		    where x.tenant_id = b.tenant_id and x.batch_id = b.id and w.legal_entity_id is distinct from $2::uuid)`, tenantID, legalEntityID)
	return int(tag.RowsAffected()), err
}

// countBackfillableExportBatches is backfillExportBatches as a dry run: how many batches without a legal entity would
// get legalEntityID once the workers in assign have it too.
func countBackfillableExportBatches(ctx context.Context, tx pgx.Tx, tenantID, legalEntityID string, assign []string) (int, error) {
	if assign == nil {
		assign = []string{} // NULL would make "not (w.id = any(...))" unknown and hide a foreign worker
	}
	var n int
	err := tx.QueryRow(ctx, `select count(*) from pay_export_batch b
		where b.tenant_id = $1 and b.legal_entity_id is null
		  and exists (select 1 from pay_export_line x where x.tenant_id = b.tenant_id and x.batch_id = b.id)
		  and not exists (
		    select 1 from pay_export_line x
		    join pay_line pl on pl.tenant_id = x.tenant_id and pl.id = x.pay_line_id
		    join worker w on w.tenant_id = pl.tenant_id and w.id = pl.worker_id
		    where x.tenant_id = b.tenant_id and x.batch_id = b.id
		      and w.legal_entity_id is distinct from $2::uuid and not (w.id = any($3::uuid[])))`, tenantID, legalEntityID, assign).Scan(&n)
	return n, err
}
