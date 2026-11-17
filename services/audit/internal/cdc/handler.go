package cdc

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/auditctx"
	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

// ConsumerName is the consumer's service principal (platform/go/tenant), Kafka group and inbox name.
const ConsumerName = "audit-cdc"

// DutyApply is the flag duty of the consumer's principal; the consumer does not start without it.
const DutyApply = "audit.cdc.apply"

const (
	// ContextWindow bounds how far apart in source commit time a depot.ctx message and a change may be and still be
	// joined. Both belong to one transaction and Debezium stamps both with its commit time, so they are normally
	// equal; an hour leaves room for any clock or stamping difference. What it rules out is a message of an older
	// transaction that reused the same 32-bit transaction id: that takes 2^32 transaction ids, about 24 hours at
	// 50,000 writing transactions a second, which a 24-hour window would not have kept apart.
	ContextWindow = time.Hour
	// ContextRetention is how long depot.ctx messages are kept. The consumer purges older ones itself (at most once an
	// hour per tenant), as does the nightly job. A dead-lettered change replayed later than this finds no message
	// and is recorded as unattributed.
	ContextRetention = 7 * 24 * time.Hour
	purgeEvery       = time.Hour
)

// TxContext is the depot.ctx message of one source transaction, keyed by the source service, the source
// transaction id and the message's position, with whether its signature verified (Rejection says why not) and the
// source commit time.
type TxContext struct {
	Source      string
	TxID        int64
	LSN         int64
	Verified    bool
	Rejection   string
	CommittedAt time.Time
	auditctx.Message
}

// Sink is the store side of the consumer. Every call runs in the consumer's inbox transaction, of the record's tenant.
type Sink interface {
	AppendTx(ctx context.Context, tx pgx.Tx, change domain.Change) error
	RecordTxContext(ctx context.Context, tx pgx.Tx, c TxContext) error
	FindTxContext(ctx context.Context, tx pgx.Tx, source string, txID, lsn int64, committedAt time.Time) (TxContext, bool, error)
	PurgeTxContextsTx(ctx context.Context, tx pgx.Tx, cutoff time.Time) (int64, error)
}

// Handler folds CDC records into the change chain.
type Handler struct {
	Registry Registry
	Sink     Sink
	// Verifier checks depot.ctx signatures against the source services' ADR-0010 public keys
	// (tenant.ServiceVerifierFromEnv: DEPOT_SERVICE_NAME, DEPOT_SERVICE_PUBLIC_KEYS). Nil verifies nothing: every
	// message is rejected and its changes are recorded as unattributed-bad-signature.
	Verifier auditctx.Verifier
	Log      *slog.Logger
	// Now is the clock of the purge; nil is time.Now.
	Now func() time.Time

	changes, unattributed, badSignature, contexts, rejected atomic.Int64

	mu     sync.Mutex
	purged map[string]time.Time
}

// Handlers is the consumer's handler table (eventing.Config.Handlers).
func (h *Handler) Handlers() map[string]eventing.Handler {
	return map[string]eventing.Handler{
		TypeContext:   eventing.HandlerFunc(h.context),
		TypeRowChange: eventing.HandlerFunc(h.rowChange),
	}
}

func (h *Handler) log() *slog.Logger {
	if h.Log == nil {
		return slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return h.Log
}

func (h *Handler) now() time.Time {
	if h.Now == nil {
		return time.Now()
	}
	return h.Now()
}

// context stores a depot.ctx message with the verdict on its signature: the source service's key, over the tenant, the
// transaction id the message was signed in (which must be the transaction Debezium saw it in) and the body.
func (h *Handler) context(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	env, err := parse(evt.Data)
	if err != nil {
		return eventing.Permanent(err)
	}
	src, _ := h.Registry.Source(env.Source.Name)
	txID, lsn, _ := env.Source.position()
	sealed, msg, err := auditctx.Open(env.Message.Content)
	if err != nil {
		return eventing.Permanent(err)
	}
	c := TxContext{Source: src, TxID: txID, LSN: lsn, CommittedAt: env.occurredAt(), Message: msg, Verified: true}
	if err := auditctx.Verify(h.Verifier, sealed, msg, src, txID); err != nil {
		c.Verified, c.Rejection = false, err.Error()
		h.rejected.Add(1)
		h.log().WarnContext(ctx, "depot.ctx message rejected; its changes are recorded as unattributed-bad-signature",
			"source", src, "txId", txID, "issuer", sealed.Iss, "keyId", sealed.Kid, "reason", err.Error())
	}
	if err := h.Sink.RecordTxContext(ctx, tx, c); err != nil {
		return err
	}
	h.contexts.Add(1)
	return h.purge(ctx, tx, evt.TenantID)
}

// purge deletes the tenant's expired depot.ctx messages, at most once per purgeEvery per tenant and process.
func (h *Handler) purge(ctx context.Context, tx pgx.Tx, tenantID string) error {
	now := h.now()
	h.mu.Lock()
	if h.purged == nil {
		h.purged = map[string]time.Time{}
	}
	due := now.Sub(h.purged[tenantID]) >= purgeEvery
	if due {
		h.purged[tenantID] = now
	}
	h.mu.Unlock()
	if !due {
		return nil
	}
	_, err := h.Sink.PurgeTxContextsTx(ctx, tx, now.Add(-ContextRetention))
	return err
}

func (h *Handler) rowChange(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	env, err := parse(evt.Data)
	if err != nil {
		return eventing.Permanent(err)
	}
	table, ok := h.Registry.Lookup(env.Source.Name, env.Source.Schema+"."+env.Source.Table)
	if !ok {
		return eventing.Permanent(ErrUnregistered)
	}
	txID, lsn, _ := env.Source.position()
	found, present, err := h.Sink.FindTxContext(ctx, tx, table.Source, txID, lsn, env.occurredAt())
	if err != nil {
		return err
	}
	flag := domain.FlagUnattributed
	if present && !found.Verified {
		flag = domain.FlagBadSignature
	}
	attributed := present && found.Verified
	for _, c := range fieldChanges(table, env, evt.Subject) {
		c = attribute(c, table, txID, found, attributed, flag)
		if fields := domain.Validate(c); attributed && len(fields) > 0 {
			// A signed message whose actor does not have the shape of its kind is a defect of the emitting service,
			// not a verified actor.
			h.log().WarnContext(ctx, "depot.ctx actor refused; change recorded as unattributed", "source", table.Source, "txId", txID, "fields", fields)
			c = attribute(c, table, txID, found, false, domain.FlagUnattributed)
		}
		if fields := domain.Validate(c); len(fields) > 0 {
			// The row itself cannot be recorded (a legal entity that is not a UUID): retrying cannot help.
			return eventing.Permanent(fmt.Errorf("cdc: %s %s cannot be recorded: %v", table.Table, evt.Subject, fields))
		}
		switch c.Flag {
		case domain.FlagUnattributed:
			h.unattributed.Add(1)
		case domain.FlagBadSignature:
			h.badSignature.Add(1)
		}
		if err := h.Sink.AppendTx(ctx, tx, c); err != nil {
			return err
		}
		h.changes.Add(1)
	}
	return nil
}

// fieldChanges turns one captured row change into field changes, in the table's field order: an insert records each
// non-empty field from "" to its value, a delete each non-empty field from its value to "", an update each field
// whose value differs (a column Debezium does not have, an unchanged TOASTed value, is skipped). The changes carry
// no actor yet (see attribute).
func fieldChanges(t Table, env envelope, recordID string) []domain.Change {
	row := env.After
	if env.Op == "d" {
		row = env.Before
	}
	le, _ := text(row[t.LegalEntityColumn])
	if t.LegalEntityColumn == "" {
		le = ""
	}
	var out []domain.Change
	for _, f := range t.Fields {
		var oldV, newV string
		switch env.Op {
		case "c":
			v, ok := render(env.After[f.Column], f)
			if !ok || v == "" {
				continue
			}
			newV = v
		case "d":
			v, ok := render(env.Before[f.Column], f)
			if !ok || v == "" {
				continue
			}
			oldV = v
		default:
			n, okN := render(env.After[f.Column], f)
			o, okO := render(env.Before[f.Column], f)
			if !okN || !okO || o == n {
				continue
			}
			oldV, newV = o, n
		}
		out = append(out, domain.Change{
			Entity: t.Entity, RecordID: recordID, Field: f.Name, OldValue: oldV, NewValue: newV,
			OccurredAt: env.occurredAt(), LegalEntityID: le, Source: t.Source,
		})
	}
	return out
}

// attribute puts the actor on c: the verified depot.ctx message of the source transaction when there is one, else the
// system actor of the source service with flag (unattributed, or unattributed-bad-signature for a message that did
// not verify; the actor such a message names is not recorded).
func attribute(c domain.Change, t Table, txID int64, ctx TxContext, attributed bool, flag string) domain.Change {
	fallbackTrace := "cdc:" + t.Source + ":" + strconv.FormatInt(txID, 10)
	if !attributed {
		c.UserID, c.ActorKind, c.OnBehalfOf, c.Flag = domain.SystemActorPrefix+t.Source, domain.ActorSystem, "", flag
		c.Reason, c.TraceID, c.ChangeSetID, c.EventType, c.EventID, c.WorkflowInstanceID = "", fallbackTrace, fallbackTrace, "", "", ""
		return c
	}
	c.UserID, c.ActorKind, c.OnBehalfOf, c.Flag = ctx.Actor, ctx.ActorKind, ctx.OnBehalfOf, ""
	c.Reason, c.TraceID, c.ChangeSetID = ctx.Reason, ctx.TraceID, ctx.ChangeSetID
	c.EventType, c.EventID, c.WorkflowInstanceID = ctx.EventType, ctx.EventID, ctx.WorkflowInstanceID
	if c.TraceID == "" {
		c.TraceID = fallbackTrace
	}
	return c
}

// WriteMetrics renders the consumer's own counters in Prometheus text.
func (h *Handler) WriteMetrics(w io.Writer) {
	for _, m := range []struct {
		name, help string
		v          *atomic.Int64
	}{
		{"audit_cdc_changes_recorded_total", "Field changes the CDC consumer appended to the change chain.", &h.changes},
		{"audit_cdc_changes_unattributed_total", "Captured field changes recorded without a depot.ctx message (actor kind system, flag unattributed).", &h.unattributed},
		{"audit_cdc_changes_bad_signature_total", "Captured field changes whose depot.ctx message did not verify (actor kind system, flag unattributed-bad-signature).", &h.badSignature},
		{"audit_cdc_contexts_recorded_total", "depot.ctx messages the CDC consumer stored.", &h.contexts},
		{"audit_cdc_contexts_rejected_total", "depot.ctx messages whose signature did not verify: unsigned, unknown key, another service, altered or another transaction.", &h.rejected},
	} {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", m.name, m.help, m.name, m.name, m.v.Load())
	}
}
