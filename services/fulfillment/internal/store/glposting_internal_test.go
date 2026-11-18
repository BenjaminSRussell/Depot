package store

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/issueclaim"
)

const (
	testProduct = "0190f0a0-0000-7000-8000-0000000000b1"
	testSlip    = "0190f0a0-0000-7000-8000-0000000000c1"
)

func problem(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestSlipEntry_OriginalAndMirrorAreBalancedUnderTheirOwnKeys(t *testing.T) {
	journal := []JournalEntry{{Account: "1405", Amount: dec("37.5"), Currency: "USD"}, {Account: "1400", Amount: dec("-37.5"), Currency: "USD"}}
	orig := PackingSlip{ID: testSlip, Number: "PS-0000001", PostingDate: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), Currency: "USD", Journal: journal}
	e := slipEntry(orig, "le", "ledger")
	if e.IdempotencyKey != "post:fulfillment:packingslip:"+testSlip+":issue" || e.DocumentID != testSlip || e.PostingDate != "2026-10-21" || e.SourceType != "fulfillment-packing-slip" {
		t.Fatalf("original entry %+v", e)
	}
	if e.Lines[0].Account != "1405" || e.Lines[0].Credit || e.Lines[1].Account != "1400" || !e.Lines[1].Credit || !e.Lines[1].Amount.Equal(dec("37.5")) {
		t.Fatalf("original lines %+v", e.Lines)
	}
	if _, err := e.Rounded(); err != nil {
		t.Fatalf("original entry is not postable: %v", err)
	}

	reversal := PackingSlip{ID: "rev", Number: "PS-0000002", ReversalOfID: &orig.ID, PostingDate: orig.PostingDate, Currency: "USD",
		Journal: []JournalEntry{{Account: "1405", Amount: dec("-37.5")}, {Account: "1400", Amount: dec("37.5")}}}
	m := slipEntry(reversal, "le", "ledger")
	if m.IdempotencyKey != "post:fulfillment:packingslip:rev:cancel" || !m.Lines[0].Credit || m.Lines[1].Credit {
		t.Fatalf("mirror entry %+v", m)
	}
	if _, err := m.Rounded(); err != nil {
		t.Fatalf("mirror entry is not postable: %v", err)
	}
}

func valuedShipment() Shipment {
	return Shipment{ID: "sh", LegalEntityID: "le", WarehouseID: "wh", Lines: []ShipmentLine{
		{ID: "line", ProductID: testProduct, Packed: dec("3"), Shipped: decimal.Zero}, {ID: "done", ProductID: testProduct, Packed: dec("2"), Shipped: dec("2")}}}
}

func TestValuedCosts_InventoryPricesAndTheRequestCostIsIgnored(t *testing.T) {
	claims := &issueclaim.Fake{}
	claims.SetCost(testProduct, "USD", dec("12.5"))
	s := &Postgres{claims: claims}
	date := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	got, err := s.valuedCosts(context.Background(), valuedShipment(), "USD", date,
		map[string]Cost{"line": {UnitCost: dec("99"), CostSnapshotID: "from-request", Weight: dec("7")}, "done": {UnitCost: dec("1")}})
	if err != nil {
		t.Fatal(err)
	}
	c := got["line"]
	if !c.UnitCost.Equal(dec("12.5")) || c.CostSnapshotID != "inventory:"+testProduct+":2026-10-21" || !c.Weight.Equal(dec("7")) {
		t.Fatalf("line cost %+v: want inventory's 12.5, its snapshot and the requested catch weight", c)
	}
	if _, priced := got["done"]; priced || len(got) != 1 {
		t.Fatalf("a line with nothing left to ship was priced: %v", got)
	}
}

func TestValuedCosts_FallsBackToTheRequestOnlyWhereInventoryHoldsNoValue(t *testing.T) {
	s := &Postgres{claims: &issueclaim.Fake{}}
	asked := map[string]Cost{"line": {UnitCost: dec("4"), CostSnapshotID: "s1"}}
	got, err := s.valuedCosts(context.Background(), valuedShipment(), "USD", time.Now(), asked)
	if err != nil || !got["line"].UnitCost.Equal(dec("4")) {
		t.Fatalf("no valued stock: %v, %v", got, err)
	}
	if got, err = s.valuedCosts(context.Background(), valuedShipment(), "USD", time.Now(), nil); err != nil || len(got) != 0 {
		t.Fatalf("no valuation and no request cost must leave the line unpriced: %v, %v", got, err)
	}
}

func TestValuedCosts_RefusalsAndOutage(t *testing.T) {
	claims := &issueclaim.Fake{}
	claims.SetCost(testProduct, "EUR", dec("1"))
	s := &Postgres{claims: claims}
	if _, err := s.valuedCosts(context.Background(), valuedShipment(), "USD", time.Now(), nil); problem(err) != CodeCostCurrency {
		t.Fatalf("stock valued in EUR for a USD slip: %v, want %s", err, CodeCostCurrency)
	}
	claims.FailCosts(errors.New("inventory down"))
	_, err := s.valuedCosts(context.Background(), valuedShipment(), "USD", time.Now(), nil)
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != "COM-0503" || p.Status != http.StatusServiceUnavailable {
		t.Fatalf("valuation outage: %v, want 503 COM-0503", err)
	}
	claims.FailCosts(issueclaim.ErrNotProduct)
	if _, err := s.valuedCosts(context.Background(), valuedShipment(), "USD", time.Now(), nil); problem(err) != CodeQualityHold {
		t.Fatalf("an item that is no product: %v, want %s", err, CodeQualityHold)
	}
}

func TestValuedCosts_WithoutAValuerTheRequestCostsStand(t *testing.T) {
	asked := map[string]Cost{"line": {UnitCost: dec("4")}}
	got, err := (&Postgres{}).valuedCosts(context.Background(), valuedShipment(), "USD", time.Now(), asked)
	if err != nil || !got["line"].UnitCost.Equal(dec("4")) {
		t.Fatalf("no claimer: %v, %v", got, err)
	}
}

func TestPostSlipsToGL_WithoutALedgerPostsNothing(t *testing.T) {
	if n, err := (&Postgres{}).PostSlipsToGL(context.Background()); n != 0 || err != nil {
		t.Fatalf("no ledger: %d, %v", n, err)
	}
}
