// Package domain is the audit kernel: field-level change records, an
// append-only hash chain, alert matching, standard-text expansion and the
// document checkout state machine.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	CodeInvalid = "AUD-0001"
	// CodeActorNamed rejects a change request that names its actor (userId): the actor is the verified caller of
	// the request. It is the read log's AUD-0002, which refuses a named reader for the same reason (AUD-API-1).
	CodeActorNamed = CodeReaderNamed
	// CodeActorRefused rejects an onBehalfOf or actorKind the verified caller cannot vouch for: onBehalfOf from a
	// person's token or from a service acting on its own, onBehalfOf that is not the user the service token
	// carries, or an actorKind other than the one the verified caller is (AUD-API-1).
	CodeActorRefused = "AUD-0006"
	Genesis          = "0000000000000000000000000000000000000000000000000000000000000000"

	// ActorPerson is a person acting directly (a gateway-verified user).
	ActorPerson = "person"
	// ActorService is a service: on its own (a background service principal), or recording a change it made for
	// the person in OnBehalfOf (the end user its verified service token carries).
	ActorService = "service"
	// ActorAgent is an AI agent runtime acting for the person in OnBehalfOf.
	ActorAgent = "agent"

	MaxChangeReason = 1000
	maxChangeRef    = 256
	// contextHashVersion marks the hash payload of a change that carries actor kind and context (AUD-API-1).
	contextHashVersion = "audit-change-v2"
)

// Error is a stable domain failure.
type Error struct{ Code, Detail string }

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Change is one field mutation, as emitted by a CDC adapter (wave 4) or recorded over HTTP.
//
// UserID is the actor and always comes from the verified caller, never from a request body: the person, or for a
// service the service actor ("service:<name>"). ActorKind says which. OnBehalfOf is the person a service or agent
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
}

// hasContext reports whether c carries any AUD-API-1 field. Rows written before AUD-API-1 carry none and keep
// their original hash payload, so the chain they are on still verifies.
func (c Change) hasContext() bool {
	return c.ActorKind != "" || c.OnBehalfOf != "" || c.Reason != "" || c.ChangeSetID != "" ||
		c.EventType != "" || c.EventID != "" || c.WorkflowInstanceID != ""
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
	switch c.ActorKind {
	case ActorPerson:
		if c.OnBehalfOf != "" {
			fields["onBehalfOf"] = "is only recorded for a service or an agent"
		}
	case ActorService, ActorAgent:
	default:
		fields["actorKind"] = "must be person, service or agent"
	}
	if len(c.OnBehalfOf) > maxChangeRef {
		fields["onBehalfOf"] = fmt.Sprintf("is at most %d characters", maxChangeRef)
	}
	if len(c.Reason) > MaxChangeReason {
		fields["reason"] = fmt.Sprintf("is at most %d characters", MaxChangeReason)
	}
	for name, v := range map[string]string{"changeSetId": c.ChangeSetID, "eventType": c.EventType, "eventId": c.EventID, "workflowInstanceId": c.WorkflowInstanceID} {
		if len(v) > maxChangeRef {
			fields[name] = fmt.Sprintf("is at most %d characters", maxChangeRef)
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

// Hash links c to prevHash (64 hex zeros for the first row). A change that carries the actor kind or any other
// AUD-API-1 field is hashed as a versioned JSON array covering every field, so no value can shift into a
// neighbouring one; a change without them (every row written before AUD-API-1) keeps the original payload.
func Hash(prevHash string, c Change) string {
	if prevHash == "" {
		prevHash = Genesis
	}
	occurred := c.OccurredAt.UTC().Format(time.RFC3339Nano)
	var payload []byte
	if c.hasContext() {
		payload, _ = json.Marshal([]string{
			contextHashVersion, prevHash, c.Entity, c.RecordID, c.Field, c.OldValue, c.NewValue, c.UserID, c.ActorKind, c.OnBehalfOf,
			c.TraceID, occurred, c.Reason, c.ChangeSetID, c.EventType, c.EventID, c.WorkflowInstanceID,
		})
	} else {
		payload = []byte(strings.Join([]string{
			prevHash, c.Entity, c.RecordID, c.Field, c.OldValue, c.NewValue, c.UserID, c.TraceID, occurred,
		}, "|"))
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Link builds the next chain entry.
func Link(prevHash string, c Change) (Entry, error) {
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
		if e.PrevHash != prev || e.Hash != want {
			return fmt.Errorf("chain break at %d", i)
		}
		prev = e.Hash
	}
	return nil
}
