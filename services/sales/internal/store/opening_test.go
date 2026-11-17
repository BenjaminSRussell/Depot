package store

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ids"
)

func openingLines(customer string) []OpeningLine {
	site, wh, product := ids.New(), ids.New(), ids.New()
	mk := func(ref, order string, no int, ordered, delivered, price string) OpeningLine {
		return OpeningLine{Reference: ref, OrderNumber: order, LineNo: no, CustomerID: customer, ProductID: product, SiteID: site, WarehouseID: wh, Unit: "EA", Currency: "USD",
			OrderedQty: decimal.RequireFromString(ordered), DeliveredQty: decimal.RequireFromString(delivered), UnitPrice: decimal.RequireFromString(price), DeliveryDate: "2027-02-01"}
	}
	return []OpeningLine{withInvoiced(mk("L1", "LEG-100", 1, "10", "4", "12.5"), "3"), mk("L2", "LEG-100", 2, "5", "0", "3"), mk("L3", "LEG-101", 1, "2", "0", "100")}
}

func withInvoiced(l OpeningLine, q string) OpeningLine {
	l.InvoicedQty = decimal.RequireFromString(q)
	return l
}

func TestOpeningOrders_ImportIsIdempotentAndReadBack(t *testing.T) {
	s, pool, ctx := migrated(t)
	le, customer := ids.New(), ids.New()
	in := OpeningImport{LegalEntityID: le, BatchKey: "b1", ContentHash: "h1", ImportedBy: "loader", ControlTotals: map[string]string{"USD": "290"}, Lines: openingLines(customer)}
	got, created, err := s.ImportOpeningOrders(ctx, in)
	if err != nil || !created {
		t.Fatalf("import: %v %v", err, created)
	}
	// open values: 6*12.5 + 5*3 + 2*100 = 75 + 15 + 200
	if got.OrderCount != 2 || got.LineCount != 3 || got.OpenQty != "13" || got.OpenValue["USD"] != "290" {
		t.Fatalf("batch: %+v", got)
	}
	again, created, err := s.ImportOpeningOrders(ctx, in)
	if err != nil || created || again.ID != got.ID {
		t.Fatalf("replay: %v %v", err, created)
	}
	changed := in
	changed.ContentHash = "other"
	if _, _, err := s.ImportOpeningOrders(ctx, changed); !errors.Is(err, ErrOpeningKeyConflict) {
		t.Fatalf("same key other content: %v", err)
	}
	// Another batch cannot take the same line references or order numbers.
	other := in
	other.BatchKey, other.ContentHash = "b2", "h2"
	var taken *OpeningTakenError
	if _, _, err := s.ImportOpeningOrders(ctx, other); !errors.As(err, &taken) || taken.References["L1"] != "b1" || !taken.OrderNumbers["LEG-100"] {
		t.Fatalf("taken: %v", err)
	}
	list, err := s.ListOpeningBatches(ctx, le, "", 10, time.Time{}, "")
	if err != nil || len(list) != 1 || list[0].OpenValue["USD"] != "290" {
		t.Fatalf("list: %+v %v", list, err)
	}
	var status, lineStatus string
	var delivered, invoiced string
	if err := pool.QueryRow(ctx, `select o.status, l.line_status, l.delivered_qty::text, l.invoiced_qty::text from sales_order o join sales_order_line l on l.order_id = o.id and l.tenant_id = o.tenant_id
		where o.order_number = 'LEG-100' and l.line_no = 1`).Scan(&status, &lineStatus, &delivered, &invoiced); err != nil {
		t.Fatal(err)
	}
	if status != "PARTIALLY_SHIPPED" || lineStatus != "PICKED" || delivered != "4.000000" || invoiced != "3.000000" {
		t.Fatalf("stored order: %s %s %s", status, lineStatus, delivered)
	}
}
