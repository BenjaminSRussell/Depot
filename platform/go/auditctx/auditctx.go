// Package auditctx writes the depot.ctx message: who made the changes of a transaction, why, and under which trace
// (architecture §7.16 and gap D3, B-07). A service that owns an audited table runs the transaction that changes it
// through WithAuditedTx (or calls Emit on its own transaction). Emit writes one transactional logical-decoding message
// with the prefix depot.ctx (pg_logical_emit_message), so it is decoded in the same transaction, in commit order, ahead
// of the row changes, by the audit CDC connector of that service's database (Debezium pgoutput,
// message.prefix.include.list depot.ctx). The audit consumer joins the message to the row changes of the transaction by
// the source transaction id and records each changed field with the actor, reason and trace id it carries.
//
// The actor is derived from the verified principal of the request (tenant.Principal), never from input:
//
//   - a person signed in through the gateway: actorKind person, actor the user id;
//   - a service token whose act is a service principal, or an in-process service principal (tenant.ServicePrincipal):
//     actorKind service, actor "service:<name>";
//   - a service token whose act is an end user: actorKind service (agent when the issuer is the agent runtime), actor
//     "service:<issuer>", onBehalfOf the end user.
//
// The reason is what the person gave for the change (the X-Depot-Reason header, captured by Capture, which
// httpx.NewRouter mounts, or WithReason). The trace id is the W3C trace id of a traceparent header when the request
// carries one, else the request id.
package auditctx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	// Prefix is the logical-decoding message prefix the audit CDC connectors capture (message.prefix.include.list).
	Prefix = "depot.ctx"
	// Version is the message layout; the audit consumer refuses a version it does not know as missing context.
	Version = 1
	// HeaderReason carries the reason a person gave for a change.
	HeaderReason = "X-Depot-Reason"
	// MaxReason and MaxRef bound the text fields, in characters, as the audit service does.
	MaxReason = 1000
	MaxRef    = 256

	ActorPerson  = "person"
	ActorService = "service"
	ActorAgent   = "agent"

	// agentIssuer is the service whose tokens are an AI agent acting for their user (agents-ts).
	agentIssuer = "agents"
)

// ErrNoActor is returned when the context carries no principal with a tenant and an actor.
var ErrNoActor = errors.New("auditctx: no verified actor in context")

// Message is the depot.ctx payload, JSON encoded. Empty optional fields are left out.
type Message struct {
	V                  int    `json:"v"`
	TenantID           string `json:"tenantId"`
	Actor              string `json:"actor"`
	ActorKind          string `json:"actorKind"`
	OnBehalfOf         string `json:"onBehalfOf,omitempty"`
	Reason             string `json:"reason,omitempty"`
	TraceID            string `json:"traceId,omitempty"`
	ChangeSetID        string `json:"changeSetId,omitempty"`
	EventType          string `json:"eventType,omitempty"`
	EventID            string `json:"eventId,omitempty"`
	WorkflowInstanceID string `json:"workflowInstanceId,omitempty"`
}

// Cause links the changes of a transaction to the business event or workflow run that made them.
type Cause struct {
	EventType          string
	EventID            string
	WorkflowInstanceID string
}

type key int

const (
	reasonKey key = iota
	traceKey
	causeKey
)

// WithReason returns ctx carrying the reason a person gave for the change. It is trimmed and cut to MaxReason
// characters; text that is not valid UTF-8 is dropped.
func WithReason(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, reasonKey, clean(reason, MaxReason))
}

// WithTrace returns ctx carrying the trace id the changes are recorded under.
func WithTrace(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceKey, clean(traceID, MaxRef))
}

// WithCause returns ctx carrying the business event or workflow run the changes come from (a consumer sets it).
func WithCause(ctx context.Context, c Cause) context.Context {
	c.EventType, c.EventID, c.WorkflowInstanceID = clean(c.EventType, MaxRef), clean(c.EventID, MaxRef), clean(c.WorkflowInstanceID, MaxRef)
	return context.WithValue(ctx, causeKey, c)
}

// traceparent is the W3C header: version-traceid-parentid-flags; the trace id is 32 lowercase hex digits, not all zero.
var traceparent = regexp.MustCompile(`^[0-9a-f]{2}-([0-9a-f]{32})-[0-9a-f]{16}-[0-9a-f]{2}$`)

// Capture puts the X-Depot-Reason header and the trace id of a traceparent header into the request context.
// httpx.NewRouter mounts it after the request id.
func Capture(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if reason := r.Header.Get(HeaderReason); reason != "" {
			ctx = WithReason(ctx, reason)
		}
		if m := traceparent.FindStringSubmatch(strings.TrimSpace(r.Header.Get("traceparent"))); m != nil && strings.Trim(m[1], "0") != "" {
			ctx = WithTrace(ctx, m[1])
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// From builds the depot.ctx message for the principal and request in ctx. Each call gets a new changeSetId: one
// transaction is one save.
func From(ctx context.Context) (Message, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok || !ids.Valid(p.TenantID) || strings.TrimSpace(p.UserID) == "" {
		return Message{}, ErrNoActor
	}
	m := Message{V: Version, TenantID: p.TenantID, ChangeSetID: ids.New()}
	switch {
	case tenant.IsServiceActor(p.UserID):
		// A service acting on its own: a service token whose act is a service principal, or an in-process consumer.
		m.Actor, m.ActorKind = p.UserID, ActorService
	case p.Service != "":
		// A verified service token carrying an end user: the service recorded the change for that user.
		m.Actor, m.ActorKind, m.OnBehalfOf = tenant.ServiceActor(p.Service), ActorService, p.UserID
		if p.Service == agentIssuer {
			m.ActorKind = ActorAgent
		}
	default:
		m.Actor, m.ActorKind = p.UserID, ActorPerson
	}
	m.Reason, _ = ctx.Value(reasonKey).(string)
	if t, _ := ctx.Value(traceKey).(string); t != "" {
		m.TraceID = t
	} else {
		m.TraceID = clean(middleware.GetReqID(ctx), MaxRef)
	}
	if c, ok := ctx.Value(causeKey).(Cause); ok {
		m.EventType, m.EventID, m.WorkflowInstanceID = c.EventType, c.EventID, c.WorkflowInstanceID
	}
	return m, nil
}

// Emit writes the depot.ctx message for ctx into tx. Call it in every transaction that changes an audited table,
// before the transaction's first change to one: the consumer attaches a message to the changes that follow it in the
// transaction (a change written before it is recorded as unattributed). A transaction that changes nothing audited
// need not call it. It assigns the transaction an id, so do not call it in read-only transactions.
func Emit(ctx context.Context, tx pgx.Tx) error {
	m, err := From(ctx)
	if err != nil {
		return err
	}
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `select pg_logical_emit_message(true, $1::text, $2::text)`, Prefix, string(body)); err != nil {
		return fmt.Errorf("auditctx: emit %s: %w", Prefix, err)
	}
	return nil
}

// WithAuditedTx is dbx.WithTenantTx with the depot.ctx message written first: use it for the transactions that
// change an audited table.
func WithAuditedTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	return dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		if err := Emit(ctx, tx); err != nil {
			return err
		}
		return fn(tx)
	})
}

// Decode parses a depot.ctx message body. It refuses an unknown version, a tenant that is not a UUID, a missing
// actor or an unknown actor kind; the audit consumer then records the changes as unattributed.
func Decode(body []byte) (Message, error) {
	var m Message
	if err := json.Unmarshal(body, &m); err != nil {
		return Message{}, fmt.Errorf("auditctx: %w", err)
	}
	switch {
	case m.V != Version:
		return Message{}, fmt.Errorf("auditctx: version %d", m.V)
	case !ids.Valid(m.TenantID):
		return Message{}, errors.New("auditctx: tenantId is not a UUID")
	case m.Actor == "":
		return Message{}, errors.New("auditctx: no actor")
	case m.ActorKind != ActorPerson && m.ActorKind != ActorService && m.ActorKind != ActorAgent:
		return Message{}, fmt.Errorf("auditctx: actorKind %q", m.ActorKind)
	}
	return m, nil
}

func clean(s string, limit int) string {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return ""
	}
	if utf8.RuneCountInString(s) > limit {
		s = string([]rune(s)[:limit])
	}
	return s
}
