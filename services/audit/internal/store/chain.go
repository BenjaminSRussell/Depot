package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

// MaxVerifyRows caps the rows one GET /v1/audit/records:verify walks. A longer range is answered in pages: the
// verdict has complete false and nextFromSeq, and the client asks again from there, checking that each page's
// startPrevHash is the previous page's head. A legacy-precision row costs up to 999 extra hashes, so this keeps a
// request to seconds; the nightly job walks whole chains.
const MaxVerifyRows = 100_000

// ChangeChain is the verdict of GET /v1/audit/records:verify (AUD-API-4) and of the nightly job. It names rows by
// seq and id only, never by content: the walk spans every legal entity of the tenant.
type ChangeChain struct {
	Verified bool `json:"verified"`
	// Complete is false when the walk stopped at MaxVerifyRows; NextFromSeq is then where the next page starts.
	Complete    bool  `json:"complete"`
	NextFromSeq int64 `json:"nextFromSeq,omitempty"`
	// Entries is the number of rows walked, FromSeq and ToSeq the first and last (0 for an empty range).
	Entries int64 `json:"entries"`
	FromSeq int64 `json:"fromSeq"`
	ToSeq   int64 `json:"toSeq"`
	// StartPrevHash is the prevHash the walk started from (the genesis hash from the tenant's first row, else the first
	// row's own, trusted); Head the hash of the last row walked.
	StartPrevHash string `json:"startPrevHash"`
	Head          string `json:"head"`
	// Breaks counts the breaks; FirstBreak is the first in chain order, absent when verified.
	Breaks      int64          `json:"breaks"`
	FirstBreak  *domain.Break  `json:"firstBreak,omitempty"`
	KnownBreaks KnownBreaks    `json:"knownBreaks"`
	Anchors     AnchorsChecked `json:"anchors"`
}

// KnownBreaks counts the rows from before B-06 whose integrity is limited for a known reason; they do not fail
// verification.
type KnownBreaks struct {
	LegacyPrecision int64 `json:"legacyPrecision"`
	LegacyFork      int64 `json:"legacyFork"`
	LegacyAmbiguous int64 `json:"legacyAmbiguous"`
}

// AnchorsChecked counts the daily anchors whose head lies in the walked range, and those the chain no longer holds.
type AnchorsChecked struct {
	Checked    int64 `json:"checked"`
	Mismatched int64 `json:"mismatched"`
}

func changeChainOf(s domain.ChangeChainStatus) ChangeChain {
	return ChangeChain{
		Verified: s.Verified, Complete: true, Entries: s.Entries, FromSeq: s.FromSeq, ToSeq: s.ToSeq, StartPrevHash: s.StartPrevHash, Head: s.Head,
		Breaks: s.Breaks, FirstBreak: s.FirstBreak,
		KnownBreaks: KnownBreaks{LegacyPrecision: s.LegacyPrecision, LegacyFork: s.LegacyFork, LegacyAmbiguous: s.LegacyAmbiguous},
		Anchors:     AnchorsChecked{Checked: s.AnchorsChecked, Mismatched: s.AnchorsMismatched},
	}
}

// VerifyChanges walks the tenant's change chain from its first row (which must link to the genesis hash), or the seq
// range [fromSeq, toSeq] (0 for an open end; a range that starts later trusts its first row's prevHash), at most
// MaxVerifyRows rows, and checks every daily anchor whose head lies in the walked part. A complete walk to the end of
// the chain also fails on an anchor beyond its last row. The caller decides who may see the verdict: it spans every
// legal entity of the tenant.
func (s *Postgres) VerifyChanges(ctx context.Context, fromSeq, toSeq int64) (ChangeChain, error) {
	var out ChangeChain
	err := s.chainTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = verifyChanges(ctx, tx, fromSeq, toSeq, MaxVerifyRows)
		return err
	})
	return out, err
}

// verifyChanges walks at most limit rows (0 for all).
func verifyChanges(ctx context.Context, tx pgx.Tx, fromSeq, toSeq int64, limit int64) (ChangeChain, error) {
	p, _ := tenant.FromContext(ctx)
	upper := toSeq
	if upper <= 0 {
		upper = 1<<63 - 1
	}
	anchors := map[int64]string{}
	rows, err := tx.Query(ctx, `select head_seq, head_hash from audit_anchor where tenant_id = $1 and head_seq > 0 and head_seq >= $2 and ($3 <= 0 or head_seq <= $3)`,
		p.TenantID, fromSeq, toSeq)
	if err != nil {
		return ChangeChain{}, err
	}
	for rows.Next() {
		var seq int64
		var hash string
		if err := rows.Scan(&seq, &hash); err != nil {
			rows.Close()
			return ChangeChain{}, err
		}
		anchors[seq] = hash
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ChangeChain{}, err
	}
	walker := domain.NewChangeWalker(fromSeq <= 0, anchors)
	fetch := limit + 1
	if limit <= 0 {
		fetch = 1<<63 - 1
	}
	rows, err = tx.Query(ctx, `select seq, id, entity, record_id, field, old_value, new_value, user_id, trace_id, occurred_at, prev_hash, hash,
		actor_kind, on_behalf_of, reason, change_set_id, event_type, event_id, workflow_instance_id, coalesce(legal_entity_id::text, ''), source, flag
		from audit_entry where tenant_id = $1 and seq >= $2 and seq <= $3 order by seq limit $4`, p.TenantID, fromSeq, upper, fetch)
	if err != nil {
		return ChangeChain{}, err
	}
	defer rows.Close()
	var walked, next int64
	for rows.Next() {
		var r domain.ChainRow
		if err := rows.Scan(&r.Seq, &r.ID, &r.Entity, &r.RecordID, &r.Field, &r.OldValue, &r.NewValue, &r.UserID, &r.TraceID, &r.OccurredAt, &r.PrevHash, &r.Hash,
			&r.ActorKind, &r.OnBehalfOf, &r.Reason, &r.ChangeSetID, &r.EventType, &r.EventID, &r.WorkflowInstanceID, &r.LegalEntityID, &r.Source, &r.Flag); err != nil {
			return ChangeChain{}, err
		}
		if limit > 0 && walked == limit {
			// The next page trusts this row's prevHash, so this page checks it links to the last row walked; an anchor
			// whose head lies between the two (a removed row) stays this page's to report.
			next = r.Seq
			walker.Boundary(r)
			break
		}
		walker.Next(r)
		walked++
	}
	if err := rows.Err(); err != nil {
		return ChangeChain{}, err
	}
	if next > 0 {
		walker.DropAnchorsAfter(next - 1)
	}
	out := changeChainOf(walker.Status())
	if next > 0 {
		out.Complete, out.NextFromSeq = false, next
	}
	return out, nil
}

// Anchor is one daily anchor of a tenant's change chain.
type Anchor struct {
	Date      string
	HeadSeq   int64
	HeadHash  string
	Entries   int64
	Verified  bool
	Breaks    int64
	Created   bool
	Chain     ChangeChain
	CreatedAt time.Time
}

// anchorAttempts bounds how often AnchorDay verifies again because rows were appended between its walk and the anchor.
const anchorAttempts = 5

// AnchorDay verifies the tenant's whole change chain and stores its head as the anchor of day (UTC: today or
// yesterday), once, through audit_anchor_record, which reads the head itself under the append lock and refuses a seq
// that is no longer the head (40001): then rows were appended during the walk, and AnchorDay walks again. A second
// run on the same day keeps the first anchor (Created false) and still returns the verdict of this run.
func (s *Postgres) AnchorDay(ctx context.Context, day time.Time) (Anchor, error) {
	var out Anchor
	var err error
	for range anchorAttempts {
		out, err = s.anchorDay(ctx, day)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "40001" {
			return out, err
		}
	}
	return out, err
}

func (s *Postgres) anchorDay(ctx context.Context, day time.Time) (Anchor, error) {
	p, _ := tenant.FromContext(ctx)
	date := day.UTC().Format(time.DateOnly)
	var out Anchor
	err := s.chainTx(ctx, func(tx pgx.Tx) error {
		chain, err := verifyChanges(ctx, tx, 0, 0, 0)
		if err != nil {
			return err
		}
		var firstBreak *int64
		if chain.FirstBreak != nil {
			firstBreak = &chain.FirstBreak.Seq
		}
		legacy := chain.KnownBreaks.LegacyPrecision + chain.KnownBreaks.LegacyFork + chain.KnownBreaks.LegacyAmbiguous
		out = Anchor{Date: date, Chain: chain}
		if err := tx.QueryRow(ctx, `select audit_anchor_record($1::date, $2, $3, $4, $5, $6, $7)`,
			date, chain.ToSeq, chain.Entries, chain.Verified, chain.Breaks, firstBreak, legacy).Scan(&out.Created); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select head_seq, head_hash, entries, verified, breaks, created_at from audit_anchor where tenant_id = $1 and anchor_date = $2::date`,
			p.TenantID, date).Scan(&out.HeadSeq, &out.HeadHash, &out.Entries, &out.Verified, &out.Breaks, &out.CreatedAt)
	})
	return out, err
}

// PurgeTxContexts deletes the tenant's depot.ctx messages recorded before cutoff and returns how many went.
func (s *Postgres) PurgeTxContexts(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := s.chainTx(ctx, func(tx pgx.Tx) error {
		var err error
		n, err = s.PurgeTxContextsTx(ctx, tx, cutoff)
		return err
	})
	return n, err
}
