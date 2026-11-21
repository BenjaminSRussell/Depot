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
)

const (
	DutyApprove = "consolidation.approve"

	CodeICMismatchOpen     = "CNS-0901"
	CodeICReconUnreadable  = "CNS-0936"
	CodeICOverrideNotAllow = "CNS-0937"

	maxOverrideReason = 500
)

type ICOverrideAudit interface {
	RecordICOverride(ctx context.Context, groupID, period, reason string, open int) error
}

func (a *API) WithICGate(m intercoclient.Mismatches, audit ICOverrideAudit) *API {
	a.icMismatches = m
	a.icAudit = audit
	return a
}

func (a *API) icGateEntities(ctx context.Context, groupID string) ([]string, error) {
	if a.groups == nil || !ids.Valid(groupID) {
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
	return entities, nil
}

// icGate refuses a run while the period's intercompany reconciliation has an open mismatch, unless the caller holds
// consolidation.approve and gives a reason, which is stored before the run starts.
func (a *API) icGate(w http.ResponseWriter, r *http.Request, groupID, period, reason string) bool {
	if a.icMismatches == nil {
		return true
	}
	ctx := r.Context()
	entities, err := a.icGateEntities(ctx, groupID)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return false
	}
	open := 0
	for _, entity := range entities {
		n, err := a.icMismatches.OpenMismatches(ctx, entity, period)
		if err != nil {
			slog.ErrorContext(ctx, "intercompany reconciliation could not be read for the consolidation gate", "err", err, "groupId", groupID, "period", period, "legalEntityId", entity)
			httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeICReconUnreadable, "the intercompany reconciliation of "+period+" could not be read, so the run cannot start"))
			return false
		}
		open += n
	}
	if open == 0 {
		return true
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeICMismatchOpen,
			fmt.Sprintf("%d intercompany mismatches of %s are open; resolve them, or override with consolidation.approve and icMismatchOverrideReason", open, period)))
		return false
	}
	p, _ := tenant.FromContext(ctx)
	if !p.HasDuty(DutyApprove) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, CodeICOverrideNotAllow, "overriding open intercompany mismatches needs the duty "+DutyApprove))
		return false
	}
	if len(reason) > maxOverrideReason {
		return validation(w, r, map[string]string{"icMismatchOverrideReason": fmt.Sprintf("must be at most %d characters", maxOverrideReason)})
	}
	if a.icAudit == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "override audit storage is not configured"))
		return false
	}
	if err := a.icAudit.RecordICOverride(ctx, groupID, period, reason, open); err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return false
	}
	slog.WarnContext(ctx, "consolidation run overrides open intercompany mismatches", "groupId", groupID, "period", period, "open", open, "user", p.UserID)
	return true
}
