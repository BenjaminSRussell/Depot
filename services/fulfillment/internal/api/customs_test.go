package api

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/customsdata"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

type fakeCustoms struct {
	res    customsdata.Result
	err    error
	gotLE  string
	gotCus string
	gotPro []string
}

func (f *fakeCustoms) Lookup(_ context.Context, le, partner string, products []string) (customsdata.Result, error) {
	f.gotLE, f.gotCus, f.gotPro = le, partner, products
	return f.res, f.err
}

// The post reads the customer's country and the products' customs data before the transaction and hands them to the store; a failed
// read still posts, with what could be read.
func TestPostPackingSlip_ReadsTheCustomsDataBeforeThePost(t *testing.T) {
	product := ids.New()
	fs := &fakeStore{}
	fs.shipment = store.Shipment{LegalEntityID: ids.New(), CustomerID: ids.New(), Lines: []store.ShipmentLine{{ID: ids.New(), ProductID: product}}}
	src := &fakeCustoms{res: customsdata.Result{Partner: &customsdata.Partner{Country: "DE", VATID: "DE1"},
		Products: map[string]customsdata.Product{product: {CommodityCode: "84137021", OriginCountry: "IT", NetWeightKg: "12.5"}}}}
	r := httpx.NewRouter()
	New(fs, Options{ReleaseHorizonDays: 2, RequireFullATP: true}).WithCustoms(src).Routes(r)
	path := "/v1/fulfillment/shipments/" + ids.New() + ":post-packing-slip"
	body := `{"postingDate":"2026-10-21","currency":"USD","costs":[]}`
	if rec := do(t, r, http.MethodPost, path, body, withKey("c-1", map[string]string{"If-Match": `"3"`})); rec.Code != http.StatusCreated {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	c := fs.lastPost.Customs
	if c == nil || c.PartnerVATID != "DE1" || c.LookupFailed || !c.Products[product].NetWeightKg.Equal(decimal.RequireFromString("12.5")) || c.Products[product].CommodityCode != "84137021" {
		t.Fatalf("customs handed to the store: %+v", c)
	}
	if src.gotLE != fs.shipment.LegalEntityID || src.gotCus != fs.shipment.CustomerID || len(src.gotPro) != 1 || src.gotPro[0] != product {
		t.Errorf("looked up %q %q %v", src.gotLE, src.gotCus, src.gotPro)
	}
	// The party service is down: the post goes ahead with the products' data and no country.
	src.err = errors.New("party answered 503")
	src.res.Partner = nil
	if rec := do(t, r, http.MethodPost, path, body, withKey("c-2", map[string]string{"If-Match": `"3"`})); rec.Code != http.StatusCreated {
		t.Fatalf("post with a failed lookup: %d %s", rec.Code, rec.Body)
	}
	if c := fs.lastPost.Customs; c == nil || !c.LookupFailed || c.PartnerVATID != "" || c.Products[product].CommodityCode != "84137021" {
		t.Errorf("degraded customs: %+v", c)
	}
	// Without a source the post carries none.
	plain := httpx.NewRouter()
	New(fs, Options{ReleaseHorizonDays: 2, RequireFullATP: true}).Routes(plain)
	if rec := do(t, plain, http.MethodPost, path, body, withKey("c-3", map[string]string{"If-Match": `"3"`})); rec.Code != http.StatusCreated || fs.lastPost.Customs != nil {
		t.Errorf("no source: %d customs %+v", rec.Code, fs.lastPost.Customs)
	}
}
