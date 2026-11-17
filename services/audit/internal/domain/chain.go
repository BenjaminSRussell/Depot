// Package domain is the audit kernel: field-level change records, an
// append-only hash chain, alert matching, standard-text expansion and the
// document checkout state machine.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BenjaminSRussell/depot/platform/ids"
)

const (
	CodeInvalid = "AUD-0001"
	// CodeActorNamed rejects a change request that names its actor (userId): the actor is the verified caller of
	// the request. It is the read log's AUD-0002, which refuses a named reader for the same reason (AUD-API-1).
	CodeActorNamed = CodeReaderNamed
	// CodeActorRefused rejects an onBehalfOf or actorKind the verified caller cannot vouch for: onBehalfOf from a
	// service acting on its own, onBehalfOf that is not the user the service token carries, or an actorKind other
	// than the one the verified caller is (AUD-API-1).
	CodeActorRefused = "AUD-0006"
	// CodeServiceOnly rejects a change recorded without a verified service token: identity headers are not proof
	// of who is calling, so only a service that signs its call may record a change (AUD-API-1).
	CodeServiceOnly = "AUD-0007"
	// CodeVerifyRange rejects a change-chain verification range that is not two positive seqs in order (AUD-API-4).
	CodeVerifyRange = "AUD-0008"
	// CodeVerifyScope refuses a change-chain verification to a caller without tenant-wide authority: the verdict
	// describes every legal entity of the tenant.
	CodeVerifyScope = "AUD-0009"
	Genesis         = "0000000000000000000000000000000000000000000000000000000000000000"

	// ActorPerson is a person acting directly. The HTTP route never records it (it accepts verified services
	// only); it is kept for the CDC feed (B-07), which carries the person from the source transaction.
	ActorPerson = "person"
	// ActorService is a service: on its own (a background service principal), or recording a change it made for
	// the person in OnBehalfOf (the end user its verified service token carries).
	ActorService = "service"
	// ActorAgent is an AI agent runtime acting for the person in OnBehalfOf.
	ActorAgent = "agent"
	// ActorSystem is a change captured by CDC without a depot.ctx message (B-07): no verified actor is known. It is
	// recorded as "system:<source service>" and always carries FlagUnattributed or FlagBadSignature.
	ActorSystem = "system"

	// FlagUnattributed marks a captured change whose transaction carried no usable depot.ctx message.
	FlagUnattributed = "unattributed"
	// FlagBadSignature marks a captured change whose transaction carried a depot.ctx message that is not authenticated:
	// unsigned, signed by an unknown key or by another service than the source, altered, or signed for another
	// transaction. The actor it names is not recorded as the actor.
	FlagBadSignature = "unattributed-bad-signature"

	MaxChangeReason = 1000
	maxChangeRef    = 256
	maxSource       = 64
	// contextHashVersion marks the hash payload of a change that carries actor kind and context (AUD-API-1).
	contextHashVersion = "audit-change-v2"
	// placedHashVersion marks the hash payload of a change that also carries its legal entity, source or flag (B-07).
	placedHashVersion = "audit-change-v3"
	// serviceActorPrefix is tenant.ServiceActorPrefix: the recorded id of a service is "service:<name>".
	serviceActorPrefix = "service:"
	// SystemActorPrefix starts the recorded id of an unattributed captured change: "system:<source service>".
	SystemActorPrefix = "system:"
)

// Error is a stable domain failure.
type Error struct{ Code, Detail string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Change is one field mutation, as emitted by a CDC adapter (wave 4) or recorded over HTTP.
//
// UserID is the actor and always comes from the verified caller, never from a request body: for a service or an
// agent the service actor ("service:<name>"), for a person (CDC only, B-07) the person. ActorKind says which. OnBehalfOf is the person a service or agent
// recorded the change for, taken from the user its verified service token carries.
type Change struct {
	Entity     string
	RecordID   string
	Field      string
	OldValue   string
	NewValue   string
	UserID     string
	ActorKind  string
	OnBehalfOf string
	TraceID    string
	OccurredAt time.Time
	// Reason is why the change was made, as the person gave it.
	Reason string
	// ChangeSetID groups the field changes of one save.
	ChangeSetID string
	// EventType and EventID link the change to the business event that carried it.
	EventType string
	EventID   string
	// WorkflowInstanceID links the change to the workflow run that made it.
	WorkflowInstanceID string
	// LegalEntityID is the legal entity of the changed record (a UUID), empty for a tenant-level record (B-07, D17).
	LegalEntityID string
	// Source is the service whose database the change was captured from by CDC; empty for a change recorded over HTTP.
	Source string
	// Flag is FlagUnattributed or FlagBadSignature for a captured change without a verified actor, else empty.
	Flag string
}

// hasContext reports whether c carries any AUD-API-1 field. Rows written before AUD-API-1 carry none and keep
// their original hash payload, so the chain they are on still verifies.
func (c Change) hasContext() bool {
	return c.ActorKind != "" || c.OnBehalfOf != "" || c.Reason != "" || c.ChangeSetID != "" ||
		c.EventType != "" || c.EventID != "" || c.WorkflowInstanceID != ""
}

// hasPlacement reports whether c carries any B-07 field. Rows without them keep the v2 (or legacy) payload.
func (c Change) hasPlacement() bool {
	return c.LegalEntityID != "" || c.Source != "" || c.Flag != ""
}

// IsLegacy reports whether c is hashed with the legacy payload (written before AUD-API-1).
func (c Change) IsLegacy() bool { return !c.hasContext() && !c.hasPlacement() }

// Normalize returns c with OccurredAt in UTC and truncated to microseconds, the precision Postgres timestamptz
// stores (pgx truncates, it does not round), so the hash of a stored row can be recomputed from the database.
func (c Change) Normalize() Change {
	c.OccurredAt = c.OccurredAt.UTC().Truncate(time.Microsecond)
	// The legal entity is hashed in the form Postgres returns a uuid in.
	if le, ok := ids.Canonical(c.LegalEntityID); ok {
		c.LegalEntityID = le
	}
	return c
}

// Entry is an immutable audit row.
type Entry struct {
	Change
	PrevHash string
	Hash     string
}

// Validate reports missing change fields.
func Validate(c Change) map[string]string {
	fields := map[string]string{}
	if c.Entity == "" {
		fields["entity"] = "is required"
	}
	if c.RecordID == "" {
		fields["recordId"] = "is required"
	}
	if c.Field == "" {
		fields["field"] = "is required"
	}
	if c.UserID == "" {
		fields["userId"] = "is required"
	}
	serviceActor := strings.HasPrefix(c.UserID, serviceActorPrefix)
	systemActor := strings.HasPrefix(c.UserID, SystemActorPrefix)
	switch c.ActorKind {
	case ActorPerson:
		if c.OnBehalfOf != "" {
			fields["onBehalfOf"] = "is only recorded for a service or an agent"
		}
		if serviceActor || systemActor {
			fields["userId"] = "a person is never a service or system actor"
		}
	case ActorService, ActorAgent:
		if c.UserID != "" && !serviceActor {
			fields["userId"] = "a service or an agent is recorded as " + serviceActorPrefix + "<name>"
		}
	case ActorSystem:
		if c.OnBehalfOf != "" {
			fields["onBehalfOf"] = "is only recorded for a service or an agent"
		}
		if c.UserID != "" && !systemActor {
			fields["userId"] = "an unattributed change is recorded as " + SystemActorPrefix + "<source>"
		}
	default:
		fields["actorKind"] = "must be person, service, agent or system"
	}
	// A system actor is exactly an unattributed captured change: it is never set without the flag, nor the flag without it.
	switch {
	case c.Flag != "" && c.Flag != FlagUnattributed && c.Flag != FlagBadSignature:
		fields["flag"] = "must be empty, " + FlagUnattributed + " or " + FlagBadSignature
	case (c.Flag != "") != (c.ActorKind == ActorSystem):
		fields["flag"] = "an unattributed flag goes with actorKind system, and only with it"
	}
	if le, ok := ids.Canonical(c.LegalEntityID); c.LegalEntityID != "" && (!ok || le == ids.Nil) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if len(c.Source) > maxSource || strings.Trim(c.Source, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
		fields["source"] = fmt.Sprintf("is a service name: lowercase letters, digits and '-', at most %d", maxSource)
	}
	// Lengths count characters, as the OpenAPI maxLength and Postgres length() do.
	for name, limit := range map[string]struct {
		v   string
		max int
	}{
		"onBehalfOf": {c.OnBehalfOf, maxChangeRef}, "reason": {c.Reason, MaxChangeReason}, "changeSetId": {c.ChangeSetID, maxChangeRef},
		"eventType": {c.EventType, maxChangeRef}, "eventId": {c.EventID, maxChangeRef}, "workflowInstanceId": {c.WorkflowInstanceID, maxChangeRef},
	} {
		if utf8.RuneCountInString(limit.v) > limit.max {
			fields[name] = fmt.Sprintf("is at most %d characters", limit.max)
		}
	}
	// The hash payload is RFC 8785 JSON, which is defined for valid UTF-8 only (Postgres text refuses the rest too).
	for name, v := range map[string]string{
		"entity": c.Entity, "recordId": c.RecordID, "field": c.Field, "oldValue": c.OldValue, "newValue": c.NewValue, "userId": c.UserID,
		"onBehalfOf": c.OnBehalfOf, "traceId": c.TraceID, "reason": c.Reason, "changeSetId": c.ChangeSetID, "eventType": c.EventType,
		"eventId": c.EventID, "workflowInstanceId": c.WorkflowInstanceID,
	} {
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			fields[name] = "must be valid UTF-8 text without NUL characters"
		}
	}
	if c.TraceID == "" {
		fields["traceId"] = "is required"
	}
	if c.OccurredAt.IsZero() {
		fields["occurredAt"] = "is required"
	}
	return fields
}

// Hash links c to prevHash (64 hex zeros for the first row). It is SHA-256, as lowercase hex, over one of three
// payloads, chosen by the row itself:
//
//   - v3, when any B-07 field (legalEntityId, source, flag) is non-empty: the v2 array with its first element
//     "audit-change-v3" and legalEntityId, source and flag appended, in that order.
//
//   - v2, when any AUD-API-1 field (actorKind, onBehalfOf, reason, changeSetId, eventType, eventId,
//     workflowInstanceId) is non-empty: the RFC 8785 (JCS) serialization of the JSON array of strings
//     ["audit-change-v2", prevHash, entity, recordId, field, oldValue, newValue, userId, actorKind, onBehalfOf,
//     traceId, occurredAt, reason, changeSetId, eventType, eventId, workflowInstanceId], where occurredAt is the
//     time in UTC truncated to microseconds and written as RFC 3339 with trailing fractional zeros removed (Go
//     time.RFC3339Nano, e.g. 2026-11-01T09:00:00Z, 2026-11-01T09:00:00.1234Z). No value can shift into its
//     neighbour.
//
//   - legacy, for rows written before AUD-API-1 (all of those fields empty): prevHash, entity, recordId, field,
//     oldValue, newValue, userId, traceId and occurredAt (UTC, time.RFC3339Nano, not truncated) joined by "|".
//
// RFC 8785 is defined for valid UTF-8 only, and decoding invalid bytes would map different values to the same
// U+FFFD. So a v2 change with any value that is not valid UTF-8 has no hash: Hash returns "" (never a SHA-256 hex
// string) and Verify reports the row as a break. Validate refuses such values, so Link never produces one.
func Hash(prevHash string, c Change) string {
	if prevHash == "" {
		prevHash = Genesis
	}
	var payload []byte
	if c.hasContext() || c.hasPlacement() {
		occurred := c.OccurredAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
		values := []string{
			contextHashVersion, prevHash, c.Entity, c.RecordID, c.Field, c.OldValue, c.NewValue, c.UserID, c.ActorKind, c.OnBehalfOf,
			c.TraceID, occurred, c.Reason, c.ChangeSetID, c.EventType, c.EventID, c.WorkflowInstanceID,
		}
		if c.hasPlacement() {
			values[0] = placedHashVersion
			values = append(values, c.LegalEntityID, c.Source, c.Flag)
		}
		for _, v := range values {
			if !utf8.ValidString(v) {
				return ""
			}
		}
		payload = canonicalStrings(values)
	} else {
		payload = []byte(strings.Join([]string{
			prevHash, c.Entity, c.RecordID, c.Field, c.OldValue, c.NewValue, c.UserID, c.TraceID, c.OccurredAt.UTC().Format(time.RFC3339Nano),
		}, "|"))
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// canonicalStrings is the RFC 8785 (JCS) serialization of an array of strings: no whitespace; in each string '"'
// and '\' are escaped, U+0008, U+0009, U+000A, U+000C and U+000D use their short escapes, the other characters
// below U+0020 are \u00xx with lowercase hex, and every other character, U+2028, U+2029, '<', '>' and '&'
// included, is written as itself in UTF-8. The caller (Hash) passes valid UTF-8 only.
func canonicalStrings(values []string) []byte {
	const hexDigits = "0123456789abcdef"
	var b strings.Builder
	b.WriteByte('[')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range v {
			switch r {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\b':
				b.WriteString(`\b`)
			case '\t':
				b.WriteString(`\t`)
			case '\n':
				b.WriteString(`\n`)
			case '\f':
				b.WriteString(`\f`)
			case '\r':
				b.WriteString(`\r`)
			default:
				if r < 0x20 {
					b.WriteString(`\u00`)
					b.WriteByte(hexDigits[r>>4])
					b.WriteByte(hexDigits[r&0xf])
				} else {
					b.WriteRune(r)
				}
			}
		}
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return []byte(b.String())
}

// Link builds the next chain entry from c normalized (see Normalize); the entry carries the normalized change,
// which is what the store writes.
func Link(prevHash string, c Change) (Entry, error) {
	c = c.Normalize()
	if fields := Validate(c); len(fields) > 0 {
		return Entry{}, &Error{Code: CodeInvalid, Detail: "invalid change"}
	}
	if prevHash == "" {
		prevHash = Genesis
	}
	return Entry{Change: c, PrevHash: prevHash, Hash: Hash(prevHash, c)}, nil
}

// Verify reports whether entries form a contiguous hash chain from Genesis.
func Verify(entries []Entry) error {
	prev := Genesis
	for i, e := range entries {
		want := Hash(prev, e.Change)
		if want == "" || e.PrevHash != prev || e.Hash != want {
			return fmt.Errorf("chain break at %d", i)
		}
		prev = e.Hash
	}
	return nil
}
