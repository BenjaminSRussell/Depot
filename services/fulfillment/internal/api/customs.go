package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// shipmentCustoms reads the Intrastat data of a shipment's customer and products from the party and product masters, before the post:
// the call is made outside the post's transaction and a failure only leaves the optional fields of packingslip.posted out (the post is
// never held up by a master data outage). It is nil without a source or when the shipment cannot be read.
func (a *API) shipmentCustoms(r *http.Request, shipmentID string) *store.CustomsInfo {
	if a.customs == nil {
		return nil
	}
	sh, err := a.store.GetShipment(r.Context(), shipmentID)
	if err != nil {
		return nil
	}
	products := make([]string, 0, len(sh.Lines))
	for _, l := range sh.Lines {
		products = append(products, l.ProductID)
	}
	res, err := a.customs.Lookup(r.Context(), sh.LegalEntityID, sh.CustomerID, products)
	if err != nil {
		slog.WarnContext(r.Context(), "customs data not fully read for packingslip.posted", "err", err, "shipmentId", shipmentID, "correlationId", middleware.GetReqID(r.Context()))
	}
	info := &store.CustomsInfo{Products: map[string]store.CustomsProduct{}, LookupFailed: err != nil}
	if res.Partner != nil {
		info.PartnerVATID = res.Partner.VATID
	}
	for id, p := range res.Products {
		cp := store.CustomsProduct{CommodityCode: p.CommodityCode, OriginCountry: p.OriginCountry}
		if w, err := decimal.NewFromString(p.NetWeightKg); err == nil {
			cp.NetWeightKg = w
		}
		info.Products[strings.ToLower(id)] = cp
	}
	return info
}
