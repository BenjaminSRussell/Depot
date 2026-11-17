package cdc

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync/atomic"

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

// TxContext is the depot.ctx message of one source transaction, keyed by the source service, the source
// transaction id and the message's position.
type TxContext struct {
	Source string
	TxID   int64
	LSN    int64
	auditctx.Message
}

// Sink is the store side of the consumer. Every call runs in the consumer's inbox transaction, of the record's tenant.
type Sink interface {
	AppendTx(ctx context.Context, tx pgx.Tx, change domain.Change) error
	RecordTxContext(ctx context.Context, tx pgx.Tx, c TxContext) error
	FindTxContext(ctx context.Context, tx pgx.Tx, source string, txID, lsn int64) (TxContext, bool, error)
}

// Handler folds CDC records into the change chain.
type Handler struct {
	Registry Registry
	Sink     Sink
	Log      *slog.Logger

	changes, unattributed, contexts atomic.Int64
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

func (h *Handler) context(ctx context.Context, tx pgx.Tx, evt events.Event) error {
	env, err := parse(evt.Data)
	if err != nil {
		return eventing.Permanent(err)
	}
	src, _ := h.Registry.Source(env.Source.Name)
	txID, lsn, _ := env.Source.position()
	msg, err := auditctx.Decode(env.Message.Content)
	if err != nil {
		return eventing.Permanent(err)
	}
	if err := h.Sink.RecordTxContext(ctx, tx, TxContext{Source: src, TxID: txID, LSN: lsn, Message: msg}); err != nil {
		return err
	}
	h.contexts.Add(1)
	return nil
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
	found, attributed, err := h.Sink.FindTxContext(ctx, tx, table.Source, txID, lsn)
	if err != nil {
		return err
	}
	changes := fieldChanges(table, env, evt.Subject)
	for _, c := range changes {
		c = attribute(c, table, txID, found, attributed)
		if fields := domain.Validate(c); attributed && len(fields) > 0 {
			// A depot.ctx message whose actor does not have the shape of its kind is not a verified actor.
			h.log().WarnContext(ctx, "depot.ctx message refused; change recorded as unattributed", "source", table.Source, "txId", txID, "fields", fields)
			c = attribute(c, table, txID, found, false)
		}
		if fields := domain.Validate(c); len(fields) > 0 {
			// The row itself cannot be recorded (a legal entity that is not a UUID): retrying cannot help.
			return eventing.Permanent(fmt.Errorf("cdc: %s %s cannot be recorded: %v", table.Table, evt.Subject, fields))
		}
		if c.Flag == domain.FlagUnattributed {
			h.unattributed.Add(1)
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

// attribute puts the actor on c: the depot.ctx message of the source transaction when there is one, else the
// system actor of the source service with the unattributed flag.
func attribute(c domain.Change, t Table, txID int64, ctx TxContext, attributed bool) domain.Change {
	fallbackTrace := "cdc:" + t.Source + ":" + strconv.FormatInt(txID, 10)
	if !attributed {
		c.UserID, c.ActorKind, c.OnBehalfOf, c.Flag = domain.SystemActorPrefix+t.Source, domain.ActorSystem, "", domain.FlagUnattributed
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
		{"audit_cdc_contexts_recorded_total", "depot.ctx messages the CDC consumer stored.", &h.contexts},
	} {
		_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", m.name, m.help, m.name, m.name, m.v.Load())
	}
}
