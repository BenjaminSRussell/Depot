package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

const dutyAIAccessWrite = "audit.ai-access.write"

type aiAccessRequest struct {
	Kind            string         `json:"kind"`
	AgentID         string         `json:"agentId"`
	RunID           string         `json:"runId"`
	Tool            string         `json:"tool"`
	EntityType      string         `json:"entityType"`
	RecordIDs       []string       `json:"recordIds"`
	RecordClasses   []string       `json:"recordClasses"`
	HighestClass    string         `json:"highestClass"`
	RouteID         string         `json:"routeId"`
	Region          string         `json:"region"`
	RedactionCounts map[string]int `json:"redactionCounts"`
	ExcludedCounts  map[string]int `json:"excludedCounts"`
	PolicyVersion   int            `json:"policyVersion"`
	Decision        string         `json:"decision"`
	Reason          string         `json:"reason"`
	TraceID         string         `json:"traceId"`
	OccurredAt      time.Time      `json:"occurredAt"`
	UserID          *string        `json:"userId"`
	Actor           *string        `json:"actor"`
}

func (a *API) recordAIAccess(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if !verifiedService(p) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, domain.CodeServiceOnly,
			"AI data access is recorded only by a service that signs its call with a service token; identity headers are not accepted on this route"))
		return
	}
	var req aiAccessRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.UserID != nil || req.Actor != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeReaderNamed,
			"userId and actor must not be sent: the actor is the verified caller of the request"))
		return
	}
	if tenant.IsServiceActor(p.UserID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeActorRefused,
			"AI data access is recorded for an end user: this service token carries no user"))
		return
	}
	now := time.Now().UTC()
	if req.OccurredAt.IsZero() {
		req.OccurredAt = now
	}
	access := domain.AIAccess{
		Kind: req.Kind, LegalEntityID: tokenLegalEntity(p), Actor: "user:" + p.UserID, UserID: p.UserID,
		RunID: req.RunID, Tool: req.Tool, EntityType: req.EntityType, RecordIDs: req.RecordIDs, RecordClasses: req.RecordClasses,
		HighestClass: req.HighestClass, RouteID: req.RouteID, Region: req.Region, RedactionCounts: req.RedactionCounts,
		ExcludedCounts: req.ExcludedCounts, PolicyVersion: req.PolicyVersion, Decision: req.Decision, Reason: req.Reason,
		TraceID: req.TraceID, OccurredAt: req.OccurredAt,
	}
	if p.Service == agentIssuer {
		access.AgentID = req.AgentID
		access.Actor = "agent:" + req.AgentID + "@user:" + p.UserID
	}
	access = access.Normalize()
	problems := domain.ValidateAIAccess(access, now)
	if access.Kind != domain.AIAccessKindRead {
		problems["kind"] = "must be READ"
	}
	if p.Service == agentIssuer && req.AgentID == "" {
		problems["agentId"] = "is required"
	}
	if len(problems) > 0 {
		pr := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		pr.Errors = problems
		httpx.Fail(w, r, pr)
		return
	}
	rec, err := a.store.RecordAIAccess(r.Context(), access)
	if err != nil {
		a.aiAccessFailed.Add(1)
		slog.Error("ai data access record failed", "err", err, "correlationId", middleware.GetReqID(r.Context()), "traceId", req.TraceID)
		httpx.Fail(w, r, err)
		return
	}
	a.aiAccess.Add(1)
	httpx.JSON(w, http.StatusCreated, rec)
}
