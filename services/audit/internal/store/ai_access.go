package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

type AIAccessRecord struct {
	ID       string `json:"id"`
	Seq      int64  `json:"seq"`
	PrevHash string `json:"prevHash"`
	Hash     string `json:"hash"`
}

func (s *Postgres) RecordAIAccess(ctx context.Context, access domain.AIAccess) (AIAccessRecord, error) {
	p, _ := tenant.FromContext(ctx)
	var out AIAccessRecord
	err := s.chainTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('ai_data_access:' || $1::text, 0))`, p.TenantID); err != nil {
			return err
		}
		var prev string
		err := tx.QueryRow(ctx, `select hash from ai_data_access where tenant_id=$1 order by seq desc limit 1`, p.TenantID).Scan(&prev)
		if errors.Is(err, pgx.ErrNoRows) {
			prev, err = domain.Genesis, nil
		}
		if err != nil {
			return err
		}
		e, err := domain.LinkAIAccess(prev, access, time.Now())
		if err != nil {
			return err
		}
		id := ids.New()
		var seq int64
		if err := tx.QueryRow(ctx, `insert into ai_data_access
			(tenant_id, id, kind, legal_entity_id, actor, user_id, agent_id, run_id, tool, entity_type, record_ids, record_classes,
			 highest_class, route_id, region, redaction_counts, excluded_counts, policy_version, decision, reason, trace_id,
			 occurred_at, prev_hash, hash)
			values ($1,$2,$3,nullif($4,'')::uuid,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
			returning seq`,
			p.TenantID, id, e.Kind, e.LegalEntityID, e.Actor, e.UserID, e.AgentID, e.RunID, e.Tool, e.EntityType, e.RecordIDs, e.RecordClasses,
			e.HighestClass, e.RouteID, e.Region, e.RedactionCounts, e.ExcludedCounts, e.PolicyVersion, e.Decision, e.Reason, e.TraceID,
			e.OccurredAt, e.PrevHash, e.Hash).Scan(&seq); err != nil {
			return err
		}
		out = AIAccessRecord{ID: id, Seq: seq, PrevHash: e.PrevHash, Hash: e.Hash}
		return nil
	})
	return out, err
}
