package domain_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

func TestAIAccessHashCoversUserAndAgent(t *testing.T) {
	base := domain.AIAccess{
		Kind: "READ", Actor: "agent:copilot@user:u-1", UserID: "u-1", AgentID: "copilot", RunID: "run-1", Tool: "depot.ar.invoice.list",
		EntityType: "invoice", RecordIDs: []string{"i-1"}, RecordClasses: []string{"NONE"}, HighestClass: "P1", RouteID: "r-1",
		PolicyVersion: 1, Decision: "ALLOWED", TraceID: "run-1", OccurredAt: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC),
	}
	want := domain.AIAccessHash("", base)
	otherUser, otherAgent := base, base
	otherUser.UserID = "u-2"
	otherAgent.AgentID = "other"
	if domain.AIAccessHash("", otherUser) == want {
		t.Fatal("an edited user_id must change the row hash")
	}
	if domain.AIAccessHash("", otherAgent) == want {
		t.Fatal("an edited agent_id must change the row hash")
	}
}
