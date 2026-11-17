package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

// ChangeChain is the verdict of GET /v1/audit/records:verify (AUD-API-4) and of the nightly job. It names rows by
// seq and id only, never by content: the walk spans every legal entity of the tenant.
type ChangeChain struct {
	Verified bool `json:"verified"`
	// Entries is the number of rows walked, FromSeq and ToSeq the first and last (0 for an empty range).
	Entries int64  `json:"entries"`
	FromSeq int64  `json:"fromSeq"`
	ToSeq   int64  `json:"toSeq"`
	Head    string `json:"head"`
	// Breaks counts the tampering breaks; FirstBreak is the first in chain order, absent when verified.
	Breaks      int64          `json:"breaks"`
	FirstBreak  *domain.Break  `json:"firstBreak,omitempty"`
	KnownBreaks KnownBreaks    `json:"knownBreaks"`
	Anchors     AnchorsChecked `json:"anchors"`
}

// KnownBreaks counts the rows from before B-06 that do not recompute for a known reason; they do not fail verification.
type KnownBreaks struct {
	LegacyPrecision int64 `json:"legacyPrecision"`
	LegacyFork      int64 `json:"legacyFork"`
}

// AnchorsChecked counts the daily anchors whose head lies in the walked range, and those the chain no longer holds.
type AnchorsChecked struct {
	Checked    int64 `json:"checked"`
	Mismatched int64 `json:"mismatched"`
}

func changeChainOf(s domain.ChangeChainStatus) ChangeChain {
	return ChangeChain{
		Verified: s.Verified, Entries: s.Entries, FromSeq: s.FromSeq, ToSeq: s.ToSeq, Head: s.Head,
		Breaks: s.Breaks, FirstBreak: s.FirstBreak,
		KnownBreaks: KnownBreaks{LegacyPrecision: s.LegacyPrecision, LegacyFork: s.LegacyFork},
		Anchors:     AnchorsChecked{Checked: s.AnchorsChecked, Mismatched: s.AnchorsMismatched},
	}
}

// VerifyChanges walks the tenant's change chain from its first row (which must link to the genesis hash), or the seq
// range [fromSeq, toSeq] (0 for an open end; a range that starts later trusts its first row's prevHash), and checks
// every daily anchor whose head lies in it. A walk to the end of the chain also fails on an anchor beyond its last row.
func (s *Postgres) VerifyChanges(ctx context.Context, fromSeq, toSeq int64) (ChangeChain, error) {
	var out ChangeChain
	err := s.chainTx(ctx, func(tx pgx.Tx) error {
		st, err := verifyChanges(ctx, tx, fromSeq, toSeq)
		out = changeChainOf(st)
		return err
	})
	return out, err
}

func verifyChanges(ctx context.Context, tx pgx.Tx, fromSeq, toSeq int64) (domain.ChangeChainStatus, error) {
	p, _ := tenant.FromContext(ctx)
	upper := toSeq
	if upper <= 0 {
		upper = 1<<63 - 1
	}
	anchors := map[int64]string{}
	rows, err := tx.Query(ctx, `select head_seq, head_hash from audit_anchor where tenant_id = $1 and head_seq > 0 and head_seq >= $2 and ($3 <= 0 or head_seq <= $3)`,
		p.TenantID, fromSeq, toSeq)
	if err != nil {
		return domain.ChangeChainStatus{}, err
	}
	for rows.Next() {
		var seq int64
		var hash string
		if err := rows.Scan(&seq, &hash); err != nil {
			rows.Close()
			return domain.ChangeChainStatus{}, err
		}
		anchors[seq] = hash
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.ChangeChainStatus{}, err
	}
	walker := domain.NewChangeWalker(fromSeq <= 0, anchors)
	rows, err = tx.Query(ctx, `select seq, id, entity, record_id, field, old_value, new_value, user_id, trace_id, occurred_at, prev_hash, hash,
		actor_kind, on_behalf_of, reason, change_set_id, event_type, event_id, workflow_instance_id, coalesce(legal_entity_id::text, ''), source, flag
		from audit_entry where tenant_id = $1 and seq >= $2 and seq <= $3 order by seq`, p.TenantID, fromSeq, upper)
	if err != nil {
		return domain.ChangeChainStatus{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var r domain.ChainRow
		if err := rows.Scan(&r.Seq, &r.ID, &r.Entity, &r.RecordID, &r.Field, &r.OldValue, &r.NewValue, &r.UserID, &r.TraceID, &r.OccurredAt, &r.PrevHash, &r.Hash,
			&r.ActorKind, &r.OnBehalfOf, &r.Reason, &r.ChangeSetID, &r.EventType, &r.EventID, &r.WorkflowInstanceID, &r.LegalEntityID, &r.Source, &r.Flag); err != nil {
			return domain.ChangeChainStatus{}, err
		}
		walker.Next(r)
	}
	return walker.Status(), rows.Err()
}

// Anchor is one daily anchor of a tenant's change chain.
type Anchor struct {
	Date      string `json:"date"`
	HeadSeq   int64  `json:"headSeq"`
	HeadHash  string `json:"headHash"`
	Entries   int64  `json:"entries"`
	Verified  bool   `json:"verified"`
	Breaks    int64  `json:"breaks"`
	Created   bool   `json:"created"`
	Chain     ChangeChain
	CreatedAt time.Time `json:"-"`
}

// AnchorDay verifies the tenant's whole change chain and stores its head as the anchor of day (UTC), once: a second
// run on the same day keeps the first anchor (Created false) and still returns the verdict of this run.
func (s *Postgres) AnchorDay(ctx context.Context, day time.Time) (Anchor, error) {
	p, _ := tenant.FromContext(ctx)
	date := day.UTC().Format(time.DateOnly)
	var out Anchor
	err := s.chainTx(ctx, func(tx pgx.Tx) error {
		st, err := verifyChanges(ctx, tx, 0, 0)
		if err != nil {
			return err
		}
		chain := changeChainOf(st)
		var firstBreak *int64
		if st.FirstBreak != nil {
			firstBreak = &st.FirstBreak.Seq
		}
		tag, err := tx.Exec(ctx, `insert into audit_anchor (tenant_id, anchor_date, head_seq, head_hash, entries, verified, breaks, first_break_seq, legacy_breaks)
			values ($1, $2::date, $3, $4, $5, $6, $7, $8, $9) on conflict (tenant_id, anchor_date) do nothing`,
			p.TenantID, date, st.ToSeq, st.Head, st.Entries, st.Verified, st.Breaks, firstBreak, st.LegacyPrecision+st.LegacyFork)
		if err != nil {
			return err
		}
		out = Anchor{Date: date, Created: tag.RowsAffected() == 1, Chain: chain}
		return tx.QueryRow(ctx, `select head_seq, head_hash, entries, verified, breaks, created_at from audit_anchor where tenant_id = $1 and anchor_date = $2::date`,
			p.TenantID, date).Scan(&out.HeadSeq, &out.HeadHash, &out.Entries, &out.Verified, &out.Breaks, &out.CreatedAt)
	})
	return out, err
}

// PurgeTxContexts deletes the tenant's depot.ctx messages recorded before cutoff and returns how many went.
func (s *Postgres) PurgeTxContexts(ctx context.Context, cutoff time.Time) (int64, error) {
	p, _ := tenant.FromContext(ctx)
	var n int64
	err := s.chainTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from cdc_tx_context where tenant_id = $1 and recorded_at < $2`, p.TenantID, cutoff)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}
