package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/ledger/internal/store"
)

// dutyPeriodLock opens PUT /ledgers/{ledger}/period-locks/{fiscalPeriodId}. Only the close service holds it
// (service_grants close -> ledger); no role grants it.
const dutyPeriodLock = "ledger.period.lock"

const maxLockSources = 64

func (a *API) registerPeriodLocks(r chi.Router) {
	r.With(authz.RequireDuty(dutyPeriodLock)).Put("/ledgers/{ledger}/period-locks/{fiscalPeriodId}", a.putPeriodLock)
}

type periodLockRequest struct {
	StartDate string                   `json:"startDate"`
	EndDate   string                   `json:"endDate"`
	Version   int64                    `json:"version"`
	Sources   []store.PeriodLockSource `json:"sources"`
}

// putPeriodLock replaces the ledger's period lock for one fiscal period (FR-14.1, B-08c). Close calls it
// inside its own period transition and reports the transition only after a 200, so a posting that commits
// after close has reported a period closed is impossible.
func (a *API) putPeriodLock(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	ledgerID, ok := ledgerParam(w, r)
	if !ok {
		return
	}
	var req periodLockRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	periodID := chi.URLParam(r, "fiscalPeriodId")
	if !ids.Valid(periodID) {
		fields["fiscalPeriodId"] = "must be a UUID"
	}
	start, serr := time.Parse(time.DateOnly, req.StartDate)
	end, eerr := time.Parse(time.DateOnly, req.EndDate)
	switch {
	case serr != nil:
		fields["startDate"] = "must be an ISO date (YYYY-MM-DD)"
	case eerr != nil:
		fields["endDate"] = "must be an ISO date (YYYY-MM-DD)"
	case end.Before(start):
		fields["endDate"] = "must not be before startDate"
	}
	if req.Version <= 0 {
		fields["version"] = "must be a positive integer"
	}
	if msg := validLockSources(req.Sources); msg != "" {
		fields["sources"] = msg
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	periodID, _ = ids.Canonical(periodID)
	out, err := a.store.PutPeriodLock(r.Context(), store.PeriodLockInput{
		LedgerID: ledgerID, FiscalPeriodID: periodID, Start: start, End: end, Version: req.Version, Sources: req.Sources,
		Actor: p.UserID,
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func validLockSources(sources []store.PeriodLockSource) string {
	if len(sources) == 0 || len(sources) > maxLockSources {
		return "send 1 to 64 sources, one of them sourceSystem \"*\""
	}
	seen := map[string]bool{}
	for _, s := range sources {
		switch {
		case len(s.SourceSystem) < 1 || len(s.SourceSystem) > 40:
			return "each sourceSystem must be 1 to 40 characters"
		case seen[s.SourceSystem]:
			return "sourceSystem " + s.SourceSystem + " is listed twice"
		case len(s.Module) < 1 || len(s.Module) > 20:
			return "each module must be 1 to 20 characters"
		case !store.PeriodLockStatuses[s.Status]:
			return "status must be open, on_hold, closed, permanently_closed or never_opened"
		}
		seen[s.SourceSystem] = true
	}
	if !seen[store.LockSourceOther] {
		return "sourceSystem \"*\" (every source without a row of its own) is required"
	}
	return ""
}
