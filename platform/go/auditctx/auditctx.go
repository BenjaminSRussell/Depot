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
// carries one, else the request id. Both are client input, not authenticated: they say what the client claimed, and
// neither is an authority field.
//
// The message is signed (B-07a review). Any session on the database can call pg_logical_emit_message, so a message
// is trusted only when the owning service signed it with its ADR-0010 Ed25519 service key (DEPOT_SERVICE_SIGNING_KEY):
// the signature covers the domain "depot.audit-ctx.v1.", the tenant, txid_current() of the emitting transaction and
// the body bytes. The consumer verifies it against the issuing service's public key from the same registry service
// tokens are verified with (DEPOT_SERVICE_PUBLIC_KEYS), requires the issuer to be the service whose database the
// change came from, and requires the signed transaction id to be the change's own, so a copied message does not
// attribute another transaction. Database credentials alone cannot attribute a change to anyone.
package auditctx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/tracectx"
)

const (
	// Prefix is the logical-decoding message prefix the audit CDC connectors capture (message.prefix.include.list).
	Prefix = "depot.ctx"
	// Version is the body layout; the audit consumer refuses a version it does not know as missing context.
	Version = 1
	// EnvelopeVersion is the layout of the signed envelope written as the message content.
	EnvelopeVersion = 2
	// SignatureDomain separates depot.ctx signatures from every other use of a service key (service tokens sign
	// under "depot.service-token.v1.").
	SignatureDomain = "depot.audit-ctx.v1."
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

// Capture puts the X-Depot-Reason header and the trace id of a traceparent header into the request context.
// httpx.NewRouter mounts it after the request id.
func Capture(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if reason := r.Header.Get(HeaderReason); reason != "" {
			ctx = WithReason(ctx, reason)
		}
		if span, ok := tracectx.Parse(r.Header.Get(tracectx.Header)); ok {
			ctx = WithTrace(ctx, span.TraceID)
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
		m.TraceID = clean(tracectx.TraceID(ctx), MaxRef)
	}
	if c, ok := ctx.Value(causeKey).(Cause); ok {
		m.EventType, m.EventID, m.WorkflowInstanceID = c.EventType, c.EventID, c.WorkflowInstanceID
	}
	return m, nil
}

// Envelope is the content of a depot.ctx message: the body (a Message, as the exact JSON bytes that were signed),
// the emitting transaction's txid_current(), and the issuing service's signature over them.
type Envelope struct {
	V    int    `json:"v"`
	Body string `json:"body"`
	TxID string `json:"txid"`
	Iss  string `json:"iss,omitempty"`
	Kid  string `json:"kid,omitempty"`
	Sig  string `json:"sig,omitempty"`
}

// SignedBytes is what a depot.ctx signature covers after SignatureDomain: the tenant, the transaction id and the body,
// separated by newlines (none of the three can hold one: a UUID, a decimal and compact JSON).
func SignedBytes(tenantID, txid, body string) []byte {
	return []byte(tenantID + "\n" + txid + "\n" + body)
}

var (
	signer        atomic.Pointer[tenant.ServiceSigner]
	signerOnce    sync.Once
	signerMissing sync.Once
)

// ConfigureSigner installs the service's ADR-0010 signer from the environment (DEPOT_SERVICE_NAME and
// DEPOT_SERVICE_SIGNING_KEY or DEPOT_SERVICE_SIGNING_KEY_FILE) and fails on a malformed key, so a service stops at
// startup instead of writing unsigned messages. Without a key configured it installs none.
func ConfigureSigner(getenv func(string) string) error {
	s, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return fmt.Errorf("auditctx: %w", err)
	}
	SetSigner(s)
	return nil
}

// SetSigner replaces the signer Emit uses; nil makes Emit write unsigned messages.
func SetSigner(s *tenant.ServiceSigner) {
	signerOnce.Do(func() {})
	signer.Store(s)
}

func currentSigner() *tenant.ServiceSigner {
	signerOnce.Do(func() {
		s, err := tenant.ServiceSignerFromEnv(os.Getenv)
		if err != nil {
			slog.Error("auditctx: service signing key misconfigured; depot.ctx messages are unsigned", "err", err)
		}
		signer.Store(s)
	})
	return signer.Load()
}

// Emit writes the signed depot.ctx message for ctx into tx. Call it in every transaction that changes an audited
// table, before the transaction's first change to one: the consumer attaches a message to the changes that follow it
// in the transaction (a change written before it is recorded as unattributed). A transaction that changes nothing
// audited need not call it. It assigns the transaction an id, so do not call it in read-only transactions.
//
// Without a signing key the message is written unsigned, and the consumer records the changes as unattributed
// (flag unattributed-bad-signature); Emit logs that once. The business change is never refused for it.
func Emit(ctx context.Context, tx pgx.Tx) error {
	m, err := From(ctx)
	if err != nil {
		return err
	}
	var txid string
	if err := tx.QueryRow(ctx, `select txid_current()::text`).Scan(&txid); err != nil {
		return fmt.Errorf("auditctx: txid: %w", err)
	}
	s := currentSigner()
	if s == nil {
		signerMissing.Do(func() {
			slog.Warn("auditctx: no service signing key; depot.ctx messages are unsigned and their changes are recorded as unattributed")
		})
	}
	content, err := Seal(s, m, txid)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `select pg_logical_emit_message(true, $1::text, $2::text)`, Prefix, string(content)); err != nil {
		return fmt.Errorf("auditctx: emit %s: %w", Prefix, err)
	}
	return nil
}

// Seal builds the message content for m emitted in transaction txid, signed by s (unsigned when s is nil).
func Seal(s *tenant.ServiceSigner, m Message, txid string) ([]byte, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	env := Envelope{V: EnvelopeVersion, Body: string(body), TxID: txid}
	if s != nil {
		iss, kid, sig, err := s.SignDetached(SignatureDomain, SignedBytes(m.TenantID, env.TxID, env.Body))
		if err != nil {
			return nil, fmt.Errorf("auditctx: sign: %w", err)
		}
		env.Iss, env.Kid, env.Sig = iss, kid, base64.StdEncoding.EncodeToString(sig)
	}
	return json.Marshal(env)
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

// Open parses a depot.ctx message content: the envelope and its body. It refuses an unknown envelope or body
// version, a tenant that is not a UUID, a missing actor or an unknown actor kind. It does not check the signature
// (Verify does).
func Open(content []byte) (Envelope, Message, error) {
	var env Envelope
	if err := json.Unmarshal(content, &env); err != nil {
		return Envelope{}, Message{}, fmt.Errorf("auditctx: %w", err)
	}
	if env.V != EnvelopeVersion {
		return Envelope{}, Message{}, fmt.Errorf("auditctx: envelope version %d", env.V)
	}
	var m Message
	if err := json.Unmarshal([]byte(env.Body), &m); err != nil {
		return Envelope{}, Message{}, fmt.Errorf("auditctx: body: %w", err)
	}
	switch {
	case m.V != Version:
		return Envelope{}, Message{}, fmt.Errorf("auditctx: version %d", m.V)
	case !ids.Valid(m.TenantID):
		return Envelope{}, Message{}, errors.New("auditctx: tenantId is not a UUID")
	case m.Actor == "":
		return Envelope{}, Message{}, errors.New("auditctx: no actor")
	case m.ActorKind != ActorPerson && m.ActorKind != ActorService && m.ActorKind != ActorAgent:
		return Envelope{}, Message{}, fmt.Errorf("auditctx: actorKind %q", m.ActorKind)
	}
	return env, m, nil
}

// Verifier checks a detached signature: tenant.ServiceVerifier, built from DEPOT_SERVICE_PUBLIC_KEYS.
type Verifier interface {
	VerifyDetached(domain, iss, kid string, msg, sig []byte) bool
}

// Reasons Verify refuses a message.
var (
	ErrUnsigned       = errors.New("auditctx: message is not signed")
	ErrWrongIssuer    = errors.New("auditctx: message is signed by another service than the one whose database the change came from")
	ErrBadSignature   = errors.New("auditctx: signature does not verify (wrong key, unknown key id or altered message)")
	ErrTxIDMismatch   = errors.New("auditctx: message is signed for another transaction")
	ErrNoVerifierKeys = errors.New("auditctx: no service public keys configured")
)

// Verify checks that env is source's signature over m and that it was signed in the transaction the decoder saw
// it in. sourceTxID is Debezium's source.txId, the 32-bit transaction id from the transaction's BEGIN; the signed
// txid_current() carries the epoch in its high 32 bits, so the low 32 bits are compared.
func Verify(v Verifier, env Envelope, m Message, source string, sourceTxID int64) error {
	if env.Sig == "" || env.Iss == "" {
		return ErrUnsigned
	}
	if v == nil {
		return ErrNoVerifierKeys
	}
	if env.Iss != source {
		return ErrWrongIssuer
	}
	sig, err := base64.StdEncoding.DecodeString(env.Sig)
	if err != nil || !v.VerifyDetached(SignatureDomain, env.Iss, env.Kid, SignedBytes(m.TenantID, env.TxID, env.Body), sig) {
		return ErrBadSignature
	}
	signed, err := strconv.ParseUint(env.TxID, 10, 64)
	if err != nil || uint32(signed) != uint32(sourceTxID) {
		return ErrTxIDMismatch
	}
	return nil
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
