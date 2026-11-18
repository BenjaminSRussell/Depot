package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/sales/internal/store"
)

const dutyOpenLines = "sales.order.open-lines"

func (a *API) listOpenOrderLines(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.OpenLineFilter{LegalEntityID: q.Get("legalEntityId"), SiteID: q.Get("siteId"), DueTo: q.Get("dueTo")}
	fields := map[string]string{}
	for name, value := range map[string]string{"legalEntityId": f.LegalEntityID, "siteId": f.SiteID} {
		if value != "" && !ids.Valid(value) {
			fields[name] = "must be a UUID"
		}
	}
	if _, err := time.Parse(dayLayout, f.DueTo); f.DueTo != "" && err != nil {
		fields["dueTo"] = "must be a date (YYYY-MM-DD)"
	}
	cursor, ok := store.ParseOpenLineCursor(q.Get("cursor"))
	if !ok {
		fields["cursor"] = "is not one this list returned"
	}
	limit, ok := pageLimit(q.Get("limit"))
	if !ok {
		fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if f.LegalEntityID != "" && !inScope(w, r, f.LegalEntityID) {
		return
	}
	f.After, f.Limit = cursor, limit
	page, err := a.store.ListOpenOrderLines(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list open order lines", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}
