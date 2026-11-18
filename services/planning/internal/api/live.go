package api

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/planning/internal/openinputs"
)

const (
	codeLiveUnavailable = "PLAN-LIVE-UNAVAILABLE"
	codeLiveEmpty       = "PLAN-LIVE-EMPTY"
)

type OpenInputs interface {
	OpenSalesLines(ctx context.Context, tenantID, legalEntityID, siteID, dueTo string) ([]openinputs.SalesLine, error)
	OnHand(ctx context.Context, tenantID, legalEntityID, siteID string) (map[string]decimal.Decimal, error)
}

func (a *API) WithOpenInputs(o OpenInputs) *API {
	a.openInputs = o
	return a
}

func (a *API) liveRefused(w http.ResponseWriter, r *http.Request, counter *atomic.Int64, status int, code, detail string) bool {
	counter.Add(1)
	httpx.Fail(w, r, httpx.NewProblem(status, code, detail))
	return false
}

func (a *API) loadLiveItems(w http.ResponseWriter, r *http.Request, code string, req *runRequest) bool {
	ctx := r.Context()
	if a.openInputs == nil {
		return a.liveRefused(w, r, &a.liveUnavailable, http.StatusServiceUnavailable, codeLiveUnavailable, "sales and inventory are not configured as plan inputs; send items")
	}
	entity, err := a.store.PlanLegalEntity(ctx, code, req.LegalEntityID)
	if err != nil {
		httpx.Fail(w, r, err)
		return false
	}
	if entity == "" {
		validationFailed(w, r, map[string]string{"legalEntityId": "the plan has no legal entity; name one"})
		return false
	}
	caller, _ := tenant.FromContext(ctx)
	asOf, _ := time.Parse(dateLayout, req.AsOf)
	dueTo := ""
	if horizon := cmp.Or(req.HorizonDays, a.horizonDays); horizon > 0 {
		dueTo = asOf.AddDate(0, 0, horizon).Format(dateLayout)
	}
	lines, err := a.openInputs.OpenSalesLines(ctx, caller.TenantID, entity, req.SiteID, dueTo)
	if err != nil {
		return a.liveUnreadable(w, r, code, err)
	}
	if len(lines) == 0 {
		return a.liveRefused(w, r, &a.liveEmpty, http.StatusUnprocessableEntity, codeLiveEmpty, "the site has no open sales demand to plan; send items to plan anything else")
	}
	onHand, err := a.openInputs.OnHand(ctx, caller.TenantID, entity, req.SiteID)
	if err != nil {
		return a.liveUnreadable(w, r, code, err)
	}
	demand := map[string][]requirementDTO{}
	for _, l := range lines {
		demand[l.ProductID] = append(demand[l.ProductID], requirementDTO{Ref: l.Ref, RefType: "sales-line", Date: l.Date, Qty: l.Qty})
	}
	products := make([]string, 0, len(demand))
	for product := range demand {
		products = append(products, product)
	}
	slices.Sort(products)
	req.Items = make([]itemDTO, 0, len(products))
	for _, product := range products {
		req.Items = append(req.Items, itemDTO{ItemID: product, OnHand: decimal.Max(decimal.Zero, onHand[product]), Demand: demand[product],
			Receipts: []requirementDTO{}, Components: []bomDTO{}})
	}
	a.liveLoaded.Add(1)
	return true
}

func (a *API) replayedBeforeLoad(w http.ResponseWriter, r *http.Request, key, inputHash string) bool {
	if prefersAsync(r) {
		run, found, err := a.store.ReplayPlanRun(r.Context(), key, inputHash)
		switch {
		case err != nil:
			httpx.Fail(w, r, err)
		case found:
			w.Header().Set("Idempotent-Replayed", "true")
			respondRun(w, http.StatusOK, run)
		}
		return err != nil || found
	}
	v, found, err := a.store.ReplayVersion(r.Context(), key, inputHash)
	switch {
	case err != nil:
		httpx.Fail(w, r, err)
	case found:
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, v)
	}
	return err != nil || found
}

func (a *API) liveUnreadable(w http.ResponseWriter, r *http.Request, code string, err error) bool {
	slog.ErrorContext(r.Context(), "live plan inputs unavailable", "err", err, "planCode", code, "correlationId", middleware.GetReqID(r.Context()))
	return a.liveRefused(w, r, &a.liveUnavailable, http.StatusServiceUnavailable, codeLiveUnavailable, "open sales demand and on-hand could not be read; no plan was run")
}
