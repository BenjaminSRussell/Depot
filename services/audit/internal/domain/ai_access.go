package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"time"
)

const (
	AIAccessKindRead   = "READ"
	AIAccessKindExport = "EXPORT"

	MaxAIAccessRecordIDs = 200
	maxAIAccessText      = 128
	maxAIAccessCounts    = 32
)

var (
	aiAccessIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
	aiAccessRecordID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/#@+=~%-]*$`)
	aiAccessAgentID    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	aiAccessKinds      = []string{AIAccessKindRead, AIAccessKindExport}
	aiAccessDecisions  = []string{"ALLOWED", "REDACTED", "WITHHELD", "BLOCKED"}
	aiAccessClasses    = []string{"P0", "P1", "P2", "P3"}
)

type AIAccess struct {
	Kind            string
	LegalEntityID   string
	Actor           string
	UserID          string
	AgentID         string
	RunID           string
	Tool            string
	EntityType      string
	RecordIDs       []string
	RecordClasses   []string
	HighestClass    string
	RouteID         string
	Region          string
	RedactionCounts map[string]int
	ExcludedCounts  map[string]int
	PolicyVersion   int
	Decision        string
	Reason          string
	TraceID         string
	OccurredAt      time.Time
}

type AIAccessEntry struct {
	AIAccess
	PrevHash string
	Hash     string
}

func (a AIAccess) Normalize() AIAccess {
	a.RecordIDs = nonNil(a.RecordIDs)
	a.RecordClasses = nonNil(a.RecordClasses)
	if a.RedactionCounts == nil {
		a.RedactionCounts = map[string]int{}
	}
	if a.ExcludedCounts == nil {
		a.ExcludedCounts = map[string]int{}
	}
	a.OccurredAt = a.OccurredAt.UTC().Truncate(time.Microsecond)
	return a
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func ValidateAIAccess(a AIAccess, now time.Time) map[string]string {
	problems := map[string]string{}
	identifier := func(field, v string, required bool) {
		if v == "" && !required {
			return
		}
		if len(v) == 0 || len(v) > maxAIAccessText || !aiAccessIdentifier.MatchString(v) {
			problems[field] = fmt.Sprintf("must be 1 to %d letters, digits, _ . : - with no spaces", maxAIAccessText)
		}
	}
	oneOf := func(field, v string, allowed []string) {
		if !slices.Contains(allowed, v) {
			problems[field] = fmt.Sprintf("must be one of %v", allowed)
		}
	}
	oneOf("kind", a.Kind, aiAccessKinds)
	oneOf("decision", a.Decision, aiAccessDecisions)
	oneOf("highestClass", a.HighestClass, aiAccessClasses)
	identifier("runId", a.RunID, true)
	identifier("tool", a.Tool, true)
	identifier("entityType", a.EntityType, true)
	identifier("routeId", a.RouteID, true)
	identifier("traceId", a.TraceID, true)
	identifier("region", a.Region, false)
	identifier("reason", a.Reason, false)
	if a.UserID == "" || a.Actor == "" {
		problems["userId"] = "the caller is unknown"
	}
	if a.AgentID != "" && !aiAccessAgentID.MatchString(a.AgentID) {
		problems["agentId"] = "must be lowercase letters, digits, _ - and at most 64 characters"
	}
	if len(a.RecordIDs) > MaxAIAccessRecordIDs {
		problems["recordIds"] = fmt.Sprintf("holds at most %d ids per row", MaxAIAccessRecordIDs)
	} else {
		for _, id := range a.RecordIDs {
			if len(id) > maxAIAccessText || !aiAccessRecordID.MatchString(id) {
				problems["recordIds"] = "must be ids of letters, digits, _ . : / # @ + = ~ % - with no spaces, at most 128 characters each"
				break
			}
		}
	}
	if len(a.RecordClasses) != len(a.RecordIDs) {
		problems["recordClasses"] = "needs one class per record id"
	} else {
		for _, c := range a.RecordClasses {
			if len(c) > 16 || !aiAccessIdentifier.MatchString(c) {
				problems["recordClasses"] = "must be class codes of letters and digits"
				break
			}
		}
	}
	if a.PolicyVersion < 1 {
		problems["policyVersion"] = "must be at least 1"
	}
	counts("redactionCounts", a.RedactionCounts, problems)
	counts("excludedCounts", a.ExcludedCounts, problems)
	if a.OccurredAt.IsZero() {
		problems["occurredAt"] = "is required"
	} else if a.OccurredAt.After(now.Add(maxFutureSkew)) {
		problems["occurredAt"] = "is in the future"
	}
	return problems
}

func counts(field string, m map[string]int, problems map[string]string) {
	if len(m) > maxAIAccessCounts {
		problems[field] = fmt.Sprintf("holds at most %d categories", maxAIAccessCounts)
		return
	}
	for k, v := range m {
		if v < 0 || len(k) > 64 || !aiAccessIdentifier.MatchString(k) {
			problems[field] = "must map category names to non-negative counts"
			return
		}
	}
}

func AIAccessHash(prevHash string, a AIAccess) string {
	if prevHash == "" {
		prevHash = Genesis
	}
	a = a.Normalize()
	payload, _ := json.Marshal([]any{
		prevHash, a.Kind, a.LegalEntityID, a.Actor, a.UserID, a.AgentID, a.RunID, a.Tool, a.EntityType, a.RecordIDs, a.RecordClasses,
		a.HighestClass, a.RouteID, a.Region, a.RedactionCounts, a.ExcludedCounts, a.PolicyVersion, a.Decision,
		a.Reason, a.TraceID, a.OccurredAt.Format(time.RFC3339Nano),
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func LinkAIAccess(prevHash string, a AIAccess, now time.Time) (AIAccessEntry, error) {
	a = a.Normalize()
	if problems := ValidateAIAccess(a, now); len(problems) > 0 {
		return AIAccessEntry{}, &Error{Code: CodeInvalid, Detail: "invalid ai data access"}
	}
	if prevHash == "" {
		prevHash = Genesis
	}
	return AIAccessEntry{AIAccess: a, PrevHash: prevHash, Hash: AIAccessHash(prevHash, a)}, nil
}
