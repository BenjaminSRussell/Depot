package api

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/store"
)

// DutyAutoClockOut runs the auto clock-out sweep. The platform scheduler holds it with
// a service identity, and a supervisor may run it by hand; operators do not hold it.
const DutyAutoClockOut = "shopfloor.time.autoclockout"

const (
	defaultAutoClockBatch = 100
	maxAutoClockBatch     = 500
)

// autoClockOutResponse is what one sweep closed and the threshold it used.
type autoClockOutResponse struct {
	AfterHours int                `json:"afterHours"`
	Items      []store.AutoClosed `json:"items"`
}

// registerAutoClock mounts the auto clock-out routes (D2h, BR-25.3).
func (a *API) registerAutoClock(r chi.Router) {
	r.With(authz.RequireDuty(DutyAutoClockOut)).Post("/v1/shopfloor/registrations:auto-clock-out", a.autoClockOut)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/auto-clock-outs", a.listAutoClockOuts)
}

// autoClockOut is the hourly auto clock-out job. Calling it again is safe: a closed
// shift is no longer open, so it takes no Idempotency-Key.
func (a *API) autoClockOut(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	hours := domain.DefaultAutoClockOutHours
	if raw := r.URL.Query().Get("afterHours"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			validationFailed(w, r, map[string]string{"afterHours": "must be a whole number of hours, 4 to 24"})
			return
		}
		hours = n
	}
	if err := domain.CheckAutoClockOutHours(hours); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeAutoClockOutInvalid, err.(*domain.Error).Detail))
		return
	}
	limit, ok := limitParam(w, r, defaultAutoClockBatch, maxAutoClockBatch)
	if !ok {
		return
	}
	ctx := r.Context()
	// The scheduler calls this with a verified service token and no legal entity: like the sweeper, it closes stale
	// shifts in every legal entity of the tenant (B-03b). Header identities never carry Service, so they cannot widen.
	if p, _ := tenant.FromContext(ctx); p.Service != "" && p.LegalEntityID == "" {
		ctx = store.WithSystemScope(ctx)
	}
	items, err := a.store.AutoClockOut(ctx, a.now().UTC(), hours, limit)
	if err != nil {
		a.fail(w, r, "auto clock-out sweep", err)
		return
	}
	if items == nil {
		items = []store.AutoClosed{}
	}
	a.autoClockedOut.Add(int64(len(items)))
	httpx.JSON(w, http.StatusOK, autoClockOutResponse{AfterHours: hours, Items: items})
}

// listAutoClockOuts is the review list of clock-outs the sweep wrote.
func (a *API) listAutoClockOuts(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	win, ok := queryRange(w, r, false)
	if !ok {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	items, err := a.store.ListAutoClockOuts(r.Context(), win.workerID, win.start, win.end, limit)
	if err != nil {
		a.fail(w, r, "list auto clock-outs", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.AutoClockOutReview]{Items: items})
}
