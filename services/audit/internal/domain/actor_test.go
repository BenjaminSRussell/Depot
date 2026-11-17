package domain_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

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

func TestContextFieldsAreOnTheChain(t *testing.T) {
	c := change("bankAccount", "DE01", "DE02", "trace-1")
	c.ActorKind, c.UserID, c.OnBehalfOf = domain.ActorService, "service:billing", "ana"
	c.Reason, c.ChangeSetID, c.EventType, c.EventID, c.WorkflowInstanceID = "vendor letter 12", "cs-1", "dev.depot.party.vendor.updated.v1", "evt-1", "wf-1"
	e, err := domain.Link("", c)
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
		// A value that moves into its neighbour must not hash the same.
		func(c *domain.Change) { c.UserID, c.ActorKind = "service:billing|service", "" },
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
		name      string
		kind, obo string
		wantField string
	}{
		{"person", domain.ActorPerson, "", ""},
		{"service for a person", domain.ActorService, "ana", ""},
		{"agent for a person", domain.ActorAgent, "ana", ""},
		{"service on its own", domain.ActorService, "", ""},
		{"missing kind", "", "", "actorKind"},
		{"unknown kind", "robot", "", "actorKind"},
		{"person on behalf of someone", domain.ActorPerson, "ana", "onBehalfOf"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := change("name", "a", "b", "t")
			c.ActorKind, c.OnBehalfOf = tc.kind, tc.obo
			fields := domain.Validate(c)
			if (tc.wantField == "" && len(fields) > 0) || (tc.wantField != "" && fields[tc.wantField] == "") {
				t.Fatalf("fields %v", fields)
			}
		})
	}
	c := change("name", "a", "b", "t")
	c.Reason = strings.Repeat("x", domain.MaxChangeReason+1)
	c.EventID = strings.Repeat("x", 257)
	if f := domain.Validate(c); f["reason"] == "" || f["eventId"] == "" {
		t.Fatalf("length limits: %v", f)
	}
}
