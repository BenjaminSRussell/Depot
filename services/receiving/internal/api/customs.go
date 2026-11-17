package api

import (
	"context"
	"log/slog"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/customsdata"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

// receiptCustoms reads the Intrastat data of a receipt's vendor and products from the party and product masters, before the post
// transaction (#13): a failure leaves the optional fields of receipt.posted out and never holds the receipt up. It is nil without a source
// or when the receipt cannot be read.
func (a *API) receiptCustoms(ctx context.Context, receiptID, shipFrom string) *receiptCustoms {
	if a.ports.Customs == nil {
		return nil
	}
	if _, ok := tenant.FromContext(ctx); !ok {
		return nil
	}
	rec, err := a.store.GetReceipt(ctx, receiptID)
	if err != nil {
		return nil
	}
	products := make([]string, 0, len(rec.Lines))
	for _, l := range rec.Lines {
		products = append(products, l.ProductID)
	}
	res, err := a.ports.Customs.Lookup(ctx, rec.LegalEntityID, rec.VendorID, products)
	if err != nil {
		slog.WarnContext(ctx, "customs data not fully read for receipt.posted", "err", err, "receiptId", receiptID, "correlationId", middleware.GetReqID(ctx))
	}
	return &receiptCustoms{res: res, shipFrom: shipFrom, failed: err != nil}
}

// receiptCustoms is the Intrastat input of receipt.posted: what the masters said, the consignment country the post named, and whether a
// master read failed.
type receiptCustoms struct {
	res      customsdata.Result
	shipFrom string
	failed   bool
}

// postRequest is the optional body of POST /receipts/{id}:post.
type postRequest struct {
	// ShipFromCountry is the member state the goods were consigned from (ISO 3166 alpha-2), the Intrastat partner country of an arrival.
	ShipFromCountry string `json:"shipFromCountry"`
}
