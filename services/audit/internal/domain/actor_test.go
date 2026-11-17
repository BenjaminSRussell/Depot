package domain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

func serviceChange() domain.Change {
	c := change("bankAccount", "DE01", "DE02", "trace-1")
	c.ActorKind, c.UserID, c.OnBehalfOf = domain.ActorService, "service:billing", "ana"
	c.Reason, c.ChangeSetID, c.EventType, c.EventID, c.WorkflowInstanceID = "vendor letter 12", "cs-1", "dev.depot.party.vendor.updated.v1", "evt-1", "wf-1"
	return c
}

// TestLegacyRowsKeepTheirHash: rows written before AUD-API-1 carry no actor kind or context, so their hash payload
// must be the original one, or every existing tenant chain would stop verifying.
func TestLegacyRowsKeepTheirHash(t *testing.T) {
	legacy := change("name", "Acme", "Acme Ltd", "trace-1")
	legacy.ActorKind = ""
	sum := sha256.Sum256([]byte(strings.Join([]string{
		domain.Genesis, "party.vendor", "v-1", "name", "Acme", "Acme Ltd", "user-1", "trace-1", "2026-10-24T12:00:00Z",
	}, "|")))
	if got := domain.Hash("", legacy); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("legacy hash changed: %s", got)
	}
	first := domain.Entry{Change: legacy, PrevHash: domain.Genesis, Hash: domain.Hash(domain.Genesis, legacy)}
	next, err := domain.Link(first.Hash, change("city", "Austin", "Dallas", "trace-2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.Verify([]domain.Entry{first, next}); err != nil {
		t.Fatalf("a chain of a legacy row and a new row must verify: %v", err)
	}
}

// TestV2HashKnownAnswer pins the v2 payload for outside verifiers: the expected digest was computed independently
// (Python json.dumps(values, ensure_ascii=False, separators=(",", ":")), which for an array of strings is RFC 8785)
// over the array documented on domain.Hash. The values exercise HTML characters, short and \u00xx escapes, U+2028,
// a quote, a backslash, a non-ASCII letter and a nanosecond time truncated to microseconds.
func TestV2HashKnownAnswer(t *testing.T) {
	c := domain.Change{
		Entity: "party.vendor", RecordID: "v-1", Field: "note", OldValue: "a<b>&c",
		NewValue: "line\nnext\ttab   \x01 \"q\" \\ é", UserID: "service:billing", ActorKind: domain.ActorService, OnBehalfOf: "ana",
		TraceID: "trace-1", OccurredAt: time.Date(2026, 11, 1, 10, 0, 0, 123456789, time.FixedZone("CET", 3600)),
		Reason: "why", ChangeSetID: "cs-1", EventType: "dev.depot.party.vendor.updated.v1", EventID: "evt-1", WorkflowInstanceID: "wf-1",
	}
	const want = "74873d78ba793a4b0aaaf50dff8e0245a00b29762d7fda2ee37052689d048d5f"
	if got := domain.Hash(domain.Genesis, c); got != want {
		t.Fatalf("v2 hash %s; want %s", got, want)
	}
	e, err := domain.Link("", c)
	if err != nil {
		t.Fatal(err)
	}
	if e.Hash != want || e.OccurredAt.Nanosecond() != 123456000 || e.OccurredAt.Location() != time.UTC {
		t.Fatalf("linked %s at %s", e.Hash, e.OccurredAt)
	}
}

func TestContextFieldsAreOnTheChain(t *testing.T) {
	e, err := domain.Link("", serviceChange())
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.Verify([]domain.Entry{e}); err != nil {
		t.Fatal(err)
	}
	tamper := []func(*domain.Change){
		func(c *domain.Change) { c.ActorKind = domain.ActorAgent },
		func(c *domain.Change) { c.OnBehalfOf = "mallory" },
		func(c *domain.Change) { c.UserID = "mallory" },
		func(c *domain.Change) { c.Reason = "other" },
		func(c *domain.Change) { c.ChangeSetID = "cs-2" },
		func(c *domain.Change) { c.EventType = "x" },
		func(c *domain.Change) { c.EventID = "evt-2" },
		func(c *domain.Change) { c.WorkflowInstanceID = "wf-2" },
		func(c *domain.Change) { c.OccurredAt = c.OccurredAt.Add(time.Microsecond) },
		// A value that moves into its neighbour must not hash the same.
		func(c *domain.Change) { c.UserID, c.ActorKind = "service:billing|service", "" },
		func(c *domain.Change) { c.UserID, c.ActorKind = `service:billing","service`, "" },
	}
	for i, f := range tamper {
		bad := e
		f(&bad.Change)
		if domain.Verify([]domain.Entry{bad}) == nil {
			t.Errorf("tamper %d not detected", i)
		}
	}
}

func TestValidateActorKind(t *testing.T) {
	tests := []struct {
		name            string
		kind, user, obo string
		wantField       string
	}{
		{"person", domain.ActorPerson, "ana", "", ""},
		{"service for a person", domain.ActorService, "service:billing", "ana", ""},
		{"agent for a person", domain.ActorAgent, "service:agents", "ana", ""},
		{"service on its own", domain.ActorService, "service:search-index", "", ""},
		{"missing kind", "", "ana", "", "actorKind"},
		{"unknown kind", "robot", "ana", "", "actorKind"},
		{"person on behalf of someone", domain.ActorPerson, "ana", "bob", "onBehalfOf"},
		{"person as a service actor", domain.ActorPerson, "service:billing", "", "userId"},
		{"service as a person id", domain.ActorService, "ana", "", "userId"},
		{"agent as a person id", domain.ActorAgent, "ana", "bob", "userId"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := change("name", "a", "b", "t")
			c.ActorKind, c.UserID, c.OnBehalfOf = tc.kind, tc.user, tc.obo
			fields := domain.Validate(c)
			if (tc.wantField == "" && len(fields) > 0) || (tc.wantField != "" && fields[tc.wantField] == "") {
				t.Fatalf("fields %v", fields)
			}
		})
	}
}

func TestValidateTextLimitsCountCharacters(t *testing.T) {
	c := serviceChange()
	c.Reason = strings.Repeat("é", domain.MaxChangeReason)
	c.EventID = strings.Repeat("ü", 256)
	if f := domain.Validate(c); len(f) > 0 {
		t.Fatalf("limits at the boundary in characters: %v", f)
	}
	c.Reason += "é"
	c.EventID += "ü"
	if f := domain.Validate(c); f["reason"] == "" || f["eventId"] == "" {
		t.Fatalf("over the limit: %v", f)
	}
	c = serviceChange()
	c.NewValue = "bad \xff"
	c.Reason = "nul \x00"
	if f := domain.Validate(c); f["newValue"] == "" || f["reason"] == "" {
		t.Fatalf("invalid UTF-8 and NUL: %v", f)
	}
}

// TestHashRefusesInvalidUTF8: two v2 changes that differ only in invalid bytes would encode to the same U+FFFD and
// collide, so Hash gives neither a hash and Verify reports the row as a break, whatever hash it carries.
func TestHashRefusesInvalidUTF8(t *testing.T) {
	a, b := serviceChange(), serviceChange()
	a.NewValue, b.NewValue = "x\xff", "x\xfe"
	if ha, hb := domain.Hash("", a), domain.Hash("", b); ha != "" || hb != "" {
		t.Fatalf("invalid UTF-8 hashed: %q %q", ha, hb)
	}
	for _, stored := range []string{"", domain.Hash("", serviceChange())} {
		if err := domain.Verify([]domain.Entry{{Change: a, PrevHash: domain.Genesis, Hash: stored}}); err == nil {
			t.Fatalf("a row with invalid UTF-8 verified against hash %q", stored)
		}
	}
	if _, err := domain.Link("", a); err == nil {
		t.Fatal("Link accepted invalid UTF-8")
	}
	// The legacy payload hashes raw bytes, so it never decodes and cannot collide this way.
	legacy := change("name", "x\xff", "y", "t")
	legacy.ActorKind = ""
	if domain.Hash("", legacy) == "" {
		t.Fatal("legacy rows keep hashing their bytes")
	}
}
