package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/intercoclient"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/consolidation/internal/domain"
	"github.com/BenjaminSRussell/depot/services/consolidation/internal/store"
)

const (
	DutyApprove = "consolidation.approve"

	CodeICMismatchOpen     = "CNS-0901"
	CodeICReconUnreadable  = "CNS-0936"
	CodeICOverrideNotAllow = "CNS-0937"

	maxOverrideReason = 500
)

func (a *API) WithICGate(m intercoclient.Mismatches) *API {
	a.icMismatches = m
	return a
}

func (a *API) icGateEntities(ctx context.Context, groupID string) ([]string, error) {
	if a.groups == nil || !ids.Valid(groupID) {
		slog.WarnContext(ctx, "intercompany gate skipped: the group cannot be looked up", "groupId", groupID)
		return nil, nil
	}
	group, err := a.groups.GetGroup(ctx, groupID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var entities []string
	for _, u := range group.Units {
		if u.LegalEntityID != "" && !seen[u.LegalEntityID] {
			seen[u.LegalEntityID] = true
			entities = append(entities, u.LegalEntityID)
		}
	}
	if len(entities) == 0 {
		slog.WarnContext(ctx, "intercompany gate skipped: no unit of the group has a legal entity", "groupId", groupID)
	}
	return entities, nil
}

// icGate refuses a run while the period's intercompany reconciliation has an open mismatch, unless the caller holds
// consolidation.approve and gives a reason; the approval is returned for the run's own transaction to store.
func (a *API) icGate(w http.ResponseWriter, r *http.Request, groupID, period, reason string) (*store.ICOverride, bool) {
	if a.icMismatches == nil {
		return nil, true
	}
	ctx := r.Context()
	entities, err := a.icGateEntities(ctx, groupID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return nil, false
	}
	open := 0
	for _, entity := range entities {
		n, err := a.icMismatches.OpenMismatches(ctx, entity, period)
		if err != nil {
			slog.ErrorContext(ctx, "intercompany reconciliation could not be read for the consolidation gate", "err", err, "groupId", groupID, "period", period, "legalEntityId", entity)
			httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeICReconUnreadable, "the intercompany reconciliation of "+period+" could not be read, so the run cannot start"))
			return nil, false
		}
		open += n
	}
	if open == 0 {
		return nil, true
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeICMismatchOpen,
			fmt.Sprintf("%d intercompany mismatches of %s are open; resolve them, or override with consolidation.approve and icMismatchOverrideReason", open, period)))
		return nil, false
	}
	p, _ := tenant.FromContext(ctx)
	if !p.HasDuty(DutyApprove) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, CodeICOverrideNotAllow, "overriding open intercompany mismatches needs the duty "+DutyApprove))
		return nil, false
	}
	if len(reason) > maxOverrideReason {
		return nil, validation(w, r, map[string]string{"icMismatchOverrideReason": fmt.Sprintf("must be at most %d characters", maxOverrideReason)})
	}
	slog.WarnContext(ctx, "consolidation run overrides open intercompany mismatches", "groupId", groupID, "period", period, "open", open, "user", p.UserID)
	return &store.ICOverride{GroupID: groupID, Period: period, Reason: reason, Open: open}, true
}

// createRun answers an Idempotency-Key retry from the stored run before the gate is read, then gates and stores the run
// together with its override approval.
func (a *API) createRun(w http.ResponseWriter, r *http.Request, in domain.RunInput, res domain.RunResult, collectionID, key, overrideReason string) (store.RunRecord, bool, bool) {
	if a.icMismatches != nil {
		rec, found, err := a.runs.ReplayRun(r.Context(), in, key)
		if err != nil {
			httpx.Fail(w, r, problemFrom(err))
			return store.RunRecord{}, false, false
		}
		if found {
			return rec, true, true
		}
	}
	override, ok := a.icGate(w, r, in.GroupID, in.Period, overrideReason)
	if !ok {
		return store.RunRecord{}, false, false
	}
	rec, replayed, err := a.runs.CreateRun(r.Context(), in, res, collectionID, key, override)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return store.RunRecord{}, false, false
	}
	return rec, replayed, true
}
