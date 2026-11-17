package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/outbox"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/audit/internal/cdc"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

const recordedType = "dev.depot.audit.change.recorded.v1"

// Record is a queryable audit row. UserID is the verified actor; ActorKind is empty only on rows written before
// AUD-API-1, whose actor was not checked.
type Record struct {
	ID                 string    `json:"id"`
	Entity             string    `json:"entity"`
	RecordID           string    `json:"recordId"`
	Field              string    `json:"field"`
	OldValue           string    `json:"oldValue"`
	NewValue           string    `json:"newValue"`
	UserID             string    `json:"userId"`
	ActorKind          string    `json:"actorKind,omitempty"`
	OnBehalfOf         string    `json:"onBehalfOf,omitempty"`
	TraceID            string    `json:"traceId"`
	OccurredAt         time.Time `json:"occurredAt"`
	Reason             string    `json:"reason,omitempty"`
	ChangeSetID        string    `json:"changeSetId,omitempty"`
	EventType          string    `json:"eventType,omitempty"`
	EventID            string    `json:"eventId,omitempty"`
	WorkflowInstanceID string    `json:"workflowInstanceId,omitempty"`
	// LegalEntityID, Source and Flag are the B-07 fields: the changed record's legal entity, the service a captured
	// change came from, and "unattributed" for a captured change without a verified actor.
	LegalEntityID string `json:"legalEntityId,omitempty"`
	Source        string `json:"source,omitempty"`
	Flag          string `json:"flag,omitempty"`
	Hash          string `json:"hash"`
}

// Alert is an inbox item produced from a matching rule.
type Alert struct {
	ID       string `json:"id"`
	RuleID   string `json:"ruleId"`
	Entity   string `json:"entity"`
	RecordID string `json:"recordId"`
	Field    string `json:"field"`
	Message  string `json:"message"`
}

// AlertRule is a persisted matcher.
type AlertRule struct {
	ID     string `json:"id"`
	Entity string `json:"entity"`
	Field  string `json:"field"`
	Kind   string `json:"kind"`
}

// Store is the audit persistence port and the CDC consumer.
type Store interface {
	cdc.Consumer
	Query(ctx context.Context, entity, recordID string) ([]Record, error)
	// VerifyChanges walks the tenant's change chain, or the seq range [fromSeq, toSeq] of it (0 for open), and checks
	// the daily anchors in it (AUD-API-4, B-07).
	VerifyChanges(ctx context.Context, fromSeq, toSeq int64) (ChangeChain, error)
	CreateRule(ctx context.Context, rule AlertRule) (AlertRule, error)
	ListAlerts(ctx context.Context) ([]Alert, error)
	Ping(ctx context.Context) error
	// The PHI read-access log (#606): an append-only, hash-chained record of reads of protected health information.
	RecordRead(ctx context.Context, read domain.Read) (ReadRecord, error)
	ListReads(ctx context.Context, f ReadFilter) ([]ReadRecord, error)
	VerifyReads(ctx context.Context) (ReadChain, error)
	ExportReads(ctx context.Context, f ReadFilter, begin func(ReadChain) error, each func(ReadRecord) error) error
	// Break-glass reads and their review by a second person (#606).
	ListBreakGlass(ctx context.Context, status string, limit int, afterSeq int64) ([]BreakGlassRead, error)
	ReviewRead(ctx context.Context, readID, reviewerID, outcome, note string) (BreakGlassRead, error)
}

// Postgres implements Store.
type Postgres struct{ pool *pgxpool.Pool }

// New wraps pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the pool.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Apply appends a change onto the tenant hash chain and fires matching alerts.
func (s *Postgres) Apply(ctx context.Context, change domain.Change) error {
	if fields := domain.Validate(change); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		p.Errors = fields
		return p
	}
	return s.chainTx(ctx, func(tx pgx.Tx) error { return s.AppendTx(ctx, tx, change) })
}

// chainTx runs fn in a transaction of the context tenant that sees the tenant's whole change chain: the chain is
// one per tenant, so appending to it, verifying it and anchoring it need every legal entity's rows. Only the store
// opens it, and what it returns to a caller is either the caller's own append or a verification verdict.
func (s *Postgres) chainTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("audit: no principal in context")
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('depot.tenant_id', $1, true), set_config('depot.legal_entity_scopes', '*', true)`, p.TenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

// AppendTx appends change onto the tenant hash chain in tx (a transaction of the context tenant, such as the CDC
// consumer's inbox transaction), fires matching alerts and enqueues change.recorded. It widens tx to the tenant's
// whole chain (depot.legal_entity_scopes '*'), as chainTx does.
func (s *Postgres) AppendTx(ctx context.Context, tx pgx.Tx, change domain.Change) error {
	if fields := domain.Validate(change); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		p.Errors = fields
		return p
	}
	p, _ := tenant.FromContext(ctx)
	change = change.Normalize()
	if _, err := tx.Exec(ctx, `select set_config('depot.legal_entity_scopes', '*', true)`); err != nil {
		return err
	}
	// Appends are serialised per tenant, as for the read log: under READ COMMITTED two transactions would read
	// the same head and fork the chain.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('audit_entry:' || $1::text, 0))`, p.TenantID); err != nil {
		return err
	}
	var prev string
	err := tx.QueryRow(ctx, `select hash from audit_entry where tenant_id=$1 order by seq desc limit 1`, p.TenantID).Scan(&prev)
	if errors.Is(err, pgx.ErrNoRows) {
		prev = domain.Genesis
		err = nil
	}
	if err != nil {
		return err
	}
	entry, err := domain.Link(prev, change)
	if err != nil {
		return err
	}
	id := ids.New()
	if _, err := tx.Exec(ctx, `insert into audit_entry
		(tenant_id, id, entity, record_id, field, old_value, new_value, user_id, trace_id, occurred_at, prev_hash, hash,
		 actor_kind, on_behalf_of, reason, change_set_id, event_type, event_id, workflow_instance_id, legal_entity_id, source, flag)
		values ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,nullif($20,'')::uuid,$21,$22)`,
		p.TenantID, id, change.Entity, change.RecordID, change.Field, change.OldValue, change.NewValue, change.UserID, change.TraceID, change.OccurredAt, entry.PrevHash, entry.Hash,
		change.ActorKind, change.OnBehalfOf, change.Reason, change.ChangeSetID, change.EventType, change.EventID, change.WorkflowInstanceID,
		change.LegalEntityID, change.Source, change.Flag); err != nil {
		return err
	}
	if err := fireAlerts(ctx, tx, p.TenantID, change); err != nil {
		return err
	}
	evt, err := events.New(recordedType, "depot/audit", change.RecordID, p.TenantID, recordedPayload(change))
	if err != nil {
		return err
	}
	evt.LegalEntityID = change.LegalEntityID
	return outbox.Enqueue(ctx, tx, "change", id, evt)
}

// recordedPayload is the change.recorded event data. The AUD-API-1 fields are left out when empty, as in Record.
func recordedPayload(c domain.Change) map[string]any {
	data := map[string]any{
		"entity": c.Entity, "recordId": c.RecordID, "field": c.Field,
		"oldValue": c.OldValue, "newValue": c.NewValue, "userId": c.UserID, "traceId": c.TraceID,
	}
	for k, v := range map[string]string{
		"actorKind": c.ActorKind, "onBehalfOf": c.OnBehalfOf, "reason": c.Reason, "changeSetId": c.ChangeSetID,
		"eventType": c.EventType, "eventId": c.EventID, "workflowInstanceId": c.WorkflowInstanceID,
		"legalEntityId": c.LegalEntityID, "source": c.Source, "flag": c.Flag,
	} {
		if v != "" {
			data[k] = v
		}
	}
	return data
}

func fireAlerts(ctx context.Context, tx pgx.Tx, tenantID string, change domain.Change) error {
	rows, err := tx.Query(ctx, `select id, entity, field, kind from alert_rule where tenant_id=$1`, tenantID)
	if err != nil {
		return err
	}
	var rules []domain.AlertRule
	for rows.Next() {
		var rule domain.AlertRule
		if err := rows.Scan(&rule.ID, &rule.Entity, &rule.Field, &rule.Kind); err != nil {
			rows.Close()
			return err
		}
		rules = append(rules, rule)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, rule := range rules {
		if !domain.Match(rule, change) {
			continue
		}
		if _, err := tx.Exec(ctx, `insert into alert_inbox (tenant_id, id, rule_id, entity, record_id, field, message, legal_entity_id)
			values ($1,$2,$3,$4,$5,$6,$7,nullif($8,'')::uuid)`, tenantID, ids.New(), rule.ID, change.Entity, change.RecordID, change.Field,
			change.Field+" changed from "+change.OldValue+" to "+change.NewValue, change.LegalEntityID); err != nil {
			return err
		}
	}
	return nil
}

// Query returns field changes for one record, oldest first.
func (s *Postgres) Query(ctx context.Context, entity, recordID string) ([]Record, error) {
	p, _ := tenant.FromContext(ctx)
	var out []Record
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, entity, record_id, field, old_value, new_value, user_id, trace_id, occurred_at, hash,
			actor_kind, on_behalf_of, reason, change_set_id, event_type, event_id, workflow_instance_id,
			coalesce(legal_entity_id::text, ''), source, flag
			from audit_entry where tenant_id=$1 and entity=$2 and record_id=$3 order by seq`, p.TenantID, entity, recordID)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = []Record{}
		for rows.Next() {
			var rec Record
			if err := rows.Scan(&rec.ID, &rec.Entity, &rec.RecordID, &rec.Field, &rec.OldValue, &rec.NewValue, &rec.UserID, &rec.TraceID, &rec.OccurredAt, &rec.Hash,
				&rec.ActorKind, &rec.OnBehalfOf, &rec.Reason, &rec.ChangeSetID, &rec.EventType, &rec.EventID, &rec.WorkflowInstanceID,
				&rec.LegalEntityID, &rec.Source, &rec.Flag); err != nil {
				return err
			}
			out = append(out, rec)
		}
		return rows.Err()
	})
	return out, err
}

// CreateRule stores an org-wide alert rule.
func (s *Postgres) CreateRule(ctx context.Context, rule AlertRule) (AlertRule, error) {
	p, _ := tenant.FromContext(ctx)
	if rule.Kind == "" {
		rule.Kind = domain.AlertChange
	}
	rule.ID = ids.New()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into alert_rule (tenant_id, id, entity, field, kind) values ($1,$2,$3,$4,$5)`,
			p.TenantID, rule.ID, rule.Entity, rule.Field, rule.Kind)
		return err
	})
	return rule, err
}

// ListAlerts returns the inbox.
func (s *Postgres) ListAlerts(ctx context.Context) ([]Alert, error) {
	p, _ := tenant.FromContext(ctx)
	var out []Alert
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id, rule_id, entity, record_id, field, message from alert_inbox where tenant_id=$1 order by created_at`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		out = []Alert{}
		for rows.Next() {
			var a Alert
			if err := rows.Scan(&a.ID, &a.RuleID, &a.Entity, &a.RecordID, &a.Field, &a.Message); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}
