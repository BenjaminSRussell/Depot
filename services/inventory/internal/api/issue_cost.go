package api

import (
	"net/http"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
)

const (
	issueCostPath = "/v1/inventory/issue-cost"
	issueCostUnit = 6
)

type issueCostResponse struct {
	Currency string `json:"currency"`
	UnitCost string `json:"unitCost"`
}

func (a *API) getIssueCost(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	legalEntityID, productID, warehouse := q.Get("legalEntityId"), q.Get("productId"), q.Get("warehouse")
	fields := map[string]string{}
	if !ids.Valid(legalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(productID) {
		fields["productId"] = "must be a UUID"
	}
	if n := len(warehouse); n < 1 || n > maxScopeText {
		fields["warehouse"] = "must be 1 to 120 characters"
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	currency, cost, err := a.store.WarehouseIssueCost(r.Context(), legalEntityID, productID, warehouse)
	if err != nil {
		a.failed(w, r, "issue cost", err)
		return
	}
	httpx.JSON(w, http.StatusOK, issueCostResponse{Currency: currency, UnitCost: cost.StringFixed(issueCostUnit)})
}
