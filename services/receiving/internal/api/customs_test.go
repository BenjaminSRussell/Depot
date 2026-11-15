package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/customsdata"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store/memstore"
)

// fakeCustoms answers every product with the same customs data, as the product service would for products that carry it.
type fakeCustoms struct {
	partner *customsdata.Partner
	err     error
	vendor  string
}

func (f *fakeCustoms) Lookup(_ context.Context, _, vendor string, products []string) (customsdata.Result, error) {
	f.vendor = vendor
	res := customsdata.Result{Partner: f.partner, Products: map[string]customsdata.Product{}}
	for _, p := range products {
		res.Products[strings.ToLower(p)] = customsdata.Product{CommodityCode: "84137021", OriginCountry: "IT", NetWeightKg: "2.5"}
	}
	return res, f.err
}

type customsLine struct {
	Qty              string `json:"qty"`
	AmountAcct       string `json:"amount_acct"`
	CommodityCode    string `json:"commodity_code"`
	OriginCountry    string `json:"origin_country"`
	NetWeight        string `json:"net_weight"`
	StatisticalValue string `json:"statistical_value"`
}

func postedCustoms(t *testing.T, rg *rig) (partner, vat string, line customsLine) {
	t.Helper()
	var data struct {
		PartnerCountry string        `json:"partner_country"`
		PartnerVATID   string        `json:"partner_vat_id"`
		Lines          []customsLine `json:"lines"`
	}
	evt := rg.receiptPostedEvent(t)
	if err := json.Unmarshal(evt.Data, &data); err != nil || len(data.Lines) != 1 {
		t.Fatalf("receipt.posted %s: %v", evt.Data, err)
	}
	return data.PartnerCountry, data.PartnerVATID, data.Lines[0]
}

// Worked example (#13): 4 units received at 2.50 each (amount 10.00, rate 1 so amount_acct 10.0000), the vendor registered for VAT in
// France, the product 2.5 kg a unit. receipt.posted then carries partner country FR, commodity code and origin, net weight 4 x 2.5 = 10
// kg and statistical value 10.0000. A failed party read still posts, without the country.
func TestPost_ReceiptPostedCarriesTheIntrastatData(t *testing.T) {
	src := &fakeCustoms{partner: &customsdata.Partner{Country: "FR", VATID: "FR123"}}
	rg := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) { p.Customs = src })
	rec := rg.posted(t, "PS-C1", 10, 4)
	partner, vat, line := postedCustoms(t, rg)
	if partner != "FR" || vat != "FR123" || line.CommodityCode != "84137021" || line.OriginCountry != "IT" || line.NetWeight != "10" || line.Qty != "4" ||
		line.StatisticalValue != line.AmountAcct || line.StatisticalValue == "" {
		t.Fatalf("partner %q %q line %+v", partner, vat, line)
	}
	if src.vendor == "" || rec.ID == "" {
		t.Errorf("the vendor of the receipt was not looked up: %q", src.vendor)
	}

	failing := &fakeCustoms{err: errors.New("party answered 503")}
	down := newRigPorts(memstore.New(), domain.PeriodOpen, func(p *api.Ports) { p.Customs = failing })
	down.posted(t, "PS-C2", 10, 4)
	partner, _, line = postedCustoms(t, down)
	if partner != "" || line.CommodityCode != "84137021" {
		t.Errorf("degraded: partner %q line %+v", partner, line)
	}

	plain := newRig(domain.PeriodOpen)
	plain.posted(t, "PS-C3", 10, 4)
	partner, _, line = postedCustoms(t, plain)
	if partner != "" || line.CommodityCode != "" || line.NetWeight != "" || line.StatisticalValue != "" {
		t.Errorf("no source: partner %q line %+v", partner, line)
	}
}
