package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/auditctx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/audit/internal/cdc"
)

// RecordTxContext stores the depot.ctx message of a source transaction, verified or not, in tx (the consumer's inbox
// transaction, of the message's tenant). A second message at the same position is ignored.
func (s *Postgres) RecordTxContext(ctx context.Context, tx pgx.Tx, c cdc.TxContext) error {
	p, _ := tenant.FromContext(ctx)
	_, err := tx.Exec(ctx, `insert into cdc_tx_context (tenant_id, source, tx_id, lsn, actor, actor_kind, on_behalf_of, reason, trace_id,
		change_set_id, event_type, event_id, workflow_instance_id, verified, rejection, committed_at)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		on conflict (tenant_id, source, tx_id, lsn) do nothing`,
		p.TenantID, c.Source, c.TxID, c.LSN, c.Actor, c.ActorKind, c.OnBehalfOf, c.Reason, c.TraceID, c.ChangeSetID, c.EventType, c.EventID, c.WorkflowInstanceID,
		c.Verified, c.Rejection, c.CommittedAt)
	return err
}

// FindTxContext returns the latest depot.ctx message of source transaction txID at or before position lsn, committed
// within cdc.ContextWindow of the change (committedAt), for the context tenant: a message of another tenant is
// invisible, and one of an older transaction that reused the transaction id is out of the window.
func (s *Postgres) FindTxContext(ctx context.Context, tx pgx.Tx, source string, txID, lsn int64, committedAt time.Time) (cdc.TxContext, bool, error) {
	p, _ := tenant.FromContext(ctx)
	c := cdc.TxContext{Source: source, TxID: txID}
	err := tx.QueryRow(ctx, `select lsn, actor, actor_kind, on_behalf_of, reason, trace_id, change_set_id, event_type, event_id, workflow_instance_id,
			verified, rejection, committed_at
		from cdc_tx_context where tenant_id = $1 and source = $2 and tx_id = $3 and lsn <= $4
			and committed_at between $5::timestamptz - $6::interval and $5::timestamptz + $6::interval
		order by lsn desc limit 1`,
		p.TenantID, source, txID, lsn, committedAt, cdc.ContextWindow).Scan(&c.LSN, &c.Actor, &c.ActorKind, &c.OnBehalfOf, &c.Reason, &c.TraceID,
		&c.ChangeSetID, &c.EventType, &c.EventID, &c.WorkflowInstanceID, &c.Verified, &c.Rejection, &c.CommittedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return cdc.TxContext{}, false, nil
	}
	if err != nil {
		return cdc.TxContext{}, false, err
	}
	c.TenantID, c.V = p.TenantID, auditctx.Version
	return c, true, nil
}

// PurgeTxContextsTx deletes, in tx, the context tenant's depot.ctx messages recorded before cutoff (the consumer
// purges as it goes, so it does not depend on the nightly job).
func (s *Postgres) PurgeTxContextsTx(ctx context.Context, tx pgx.Tx, cutoff time.Time) (int64, error) {
	p, _ := tenant.FromContext(ctx)
	tag, err := tx.Exec(ctx, `delete from cdc_tx_context where tenant_id = $1 and recorded_at < $2`, p.TenantID, cutoff)
	return tag.RowsAffected(), err
}
