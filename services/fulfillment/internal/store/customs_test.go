package store_test

import (
	"testing"

	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// Worked example (#13): 40 units are picked, packed and posted at a unit cost of 2.00 to the customer's warehouse in Austria (countryCode AT;
// the customer itself may be VAT registered elsewhere, say in Germany). The product master says commodity code 84137021, origin Italy and
// 12.5 kg a unit. packingslip.posted then carries partner country AT (the ship-to), the VAT id of the registration, and
// the line's net weight 40 x 12.5 = 500 kg and statistical value 80.00 (40 x 2.00). Without customs data the event carries none of it.
func TestPostPackingSlip_EventCarriesTheIntrastatData(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(6, "")
	f := released(t, s, ctx, 40)
	line := f.shipment.Lines[0]
	picked, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: line.ID, Picked: d("40")}}, idem("pick"))
	if err != nil {
		t.Fatal(err)
	}
	pr, err := s.PackContainer(ctx, picked.ID, picked.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: line.ID, Qty: d("40")}}, idem("pack"))
	if err != nil {
		t.Fatal(err)
	}
	info := &store.CustomsInfo{PartnerVATID: "DE123456789", Products: map[string]store.CustomsProduct{
		line.ProductID: {CommodityCode: "84137021", OriginCountry: "IT", NetWeightKg: d("12.5")}}}
	if _, err := s.PostPackingSlip(ctx, picked.ID, pr.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(picked, "2.00"), Customs: info, CountryCode: "AT"}, idem("post")); err != nil {
		t.Fatalf("post: %v", err)
	}
	for path, want := range map[string]string{"{partner_country}": "AT", "{partner_vat_id}": "DE123456789", "{lines,0,commodity_code}": "84137021",
		"{lines,0,origin_country}": "IT", "{lines,0,net_weight}": "500", "{lines,0,statistical_value}": "80"} {
		if got := outboxData(t, admin, fulevents.PackingSlipPosted, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}

func TestPostPackingSlip_WithoutCustomsDataTheEventHasNone(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(7, "")
	f := released(t, s, ctx, 10)
	line := f.shipment.Lines[0]
	picked, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: line.ID, Picked: d("10")}}, idem("pick"))
	if err != nil {
		t.Fatal(err)
	}
	pr, err := s.PackContainer(ctx, picked.ID, picked.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: line.ID, Qty: d("10")}}, idem("pack"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostPackingSlip(ctx, picked.ID, pr.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(picked, "2.00")}, idem("post")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"{partner_country}", "{lines,0,commodity_code}", "{lines,0,net_weight}", "{lines,0,statistical_value}"} {
		if got := outboxData(t, admin, fulevents.PackingSlipPosted, path); got != "" {
			t.Errorf("%s = %q, want none", path, got)
		}
	}
}

// A catch-weight line reports its actual shipped weight, not quantity times the nominal kilograms per unit; a failed lookup is marked on
// the event, and a post that names no ship-to country carries no partner country.
func TestPostPackingSlip_CatchWeightLineAndFailedLookup(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(8, "")
	f := released(t, s, ctx, 10)
	line := f.shipment.Lines[0]
	picked, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: line.ID, Picked: d("10")}}, idem("pick"))
	if err != nil {
		t.Fatal(err)
	}
	pr, err := s.PackContainer(ctx, picked.ID, picked.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: line.ID, Qty: d("10")}}, idem("pack"))
	if err != nil {
		t.Fatal(err)
	}
	cs := costs(picked, "2.00")
	c := cs[line.ID]
	c.Weight = d("48.7")
	cs[line.ID] = c
	info := &store.CustomsInfo{LookupFailed: true, Products: map[string]store.CustomsProduct{line.ProductID: {CommodityCode: "04069021", OriginCountry: "NL", NetWeightKg: d("5")}}}
	if _, err := s.PostPackingSlip(ctx, picked.ID, pr.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: cs, Customs: info}, idem("post")); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"{lines,0,net_weight}": "48.7", "{customs_lookup_failed}": "true", "{partner_country}": ""} {
		if got := outboxData(t, admin, fulevents.PackingSlipPosted, path); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
}
