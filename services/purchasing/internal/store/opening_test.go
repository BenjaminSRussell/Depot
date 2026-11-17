package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/purchasing/internal/store"
)

func TestOpeningOrders_ImportIsIdempotentAndReadBack(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(77)
	le, vendor, product, site, wh := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	mk := func(ref, order string, no int, ordered, received, price string) store.OpeningLine {
		return store.OpeningLine{Reference: ref, OrderNumber: order, LineNo: no, VendorID: vendor, ProductID: product, SiteID: site, WarehouseID: wh, Unit: "EA", Currency: "USD",
			OrderedQty: decimal.RequireFromString(ordered), ReceivedQty: decimal.RequireFromString(received), UnitPrice: decimal.RequireFromString(price), DeliveryDate: "2027-02-01"}
	}
	in := store.OpeningImport{LegalEntityID: le, BatchKey: "b1", ContentHash: "h1", ImportedBy: ids.New(), ControlTotals: map[string]string{"USD": "290"},
		Lines: []store.OpeningLine{mk("L1", "LEG-P1", 1, "10", "4", "12.5"), mk("L2", "LEG-P1", 2, "5", "0", "3"), mk("L3", "LEG-P2", 1, "2", "0", "100")}}
	got, created, err := s.ImportOpeningOrders(ctx, in)
	if err != nil || !created {
		t.Fatalf("import: %v %v", err, created)
	}
	if got.OrderCount != 2 || got.LineCount != 3 || got.OpenQty != "13" || got.OpenValue["USD"] != "290" {
		t.Fatalf("batch: %+v", got)
	}
	again, created, err := s.ImportOpeningOrders(ctx, in)
	if err != nil || created || again.ID != got.ID {
		t.Fatalf("replay: %v %v", err, created)
	}
	changed := in
	changed.ContentHash = "other"
	if _, _, err := s.ImportOpeningOrders(ctx, changed); !errors.Is(err, store.ErrOpeningKeyConflict) {
		t.Fatalf("same key other content: %v", err)
	}
	other := in
	other.BatchKey, other.ContentHash = "b2", "h2"
	var taken *store.OpeningTakenError
	if _, _, err := s.ImportOpeningOrders(ctx, other); !errors.As(err, &taken) || taken.References["L1"] != "b1" || !taken.OrderNumbers["LEG-P1"] {
		t.Fatalf("taken: %v", err)
	}
	list, err := s.ListOpeningBatches(ctx, le, "", 10, time.Time{}, "")
	if err != nil || len(list) != 1 || list[0].OpenValue["USD"] != "290" {
		t.Fatalf("list: %+v %v", list, err)
	}
	var approval, doc, version string
	var received string
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select o.approval_status, o.document_status, l.received_qty::text, (select count(*) from purchase_order_version v where v.purchase_order_id = o.id and v.confirmed_at is not null)::text
		from purchase_order o join purchase_order_line l on l.purchase_order_id = o.id and l.tenant_id = o.tenant_id where o.number = 'LEG-P1' and l.line_no = 1`).Scan(&approval, &doc, &received, &version)
	}); err != nil {
		t.Fatal(err)
	}
	if approval != "CONFIRMED" || doc != "RECEIVED_PARTIAL" || received != "4.000000" || version != "1" {
		t.Fatalf("stored order: %s %s %s %s", approval, doc, received, version)
	}

	// A PO number is unique per tenant: another legal entity's order with the number is reported, not a bare failure.
	otherLE := ids.New()
	ctxB := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(77), LegalEntityID: otherLE, UserID: ids.New()})
	clash := in
	clash.LegalEntityID, clash.BatchKey, clash.ContentHash = otherLE, "b3", "h3"
	if _, _, err := s.ImportOpeningOrders(ctxB, clash); !errors.As(err, &taken) || !taken.OrderNumbers["LEG-P1"] || len(taken.References) != 0 {
		t.Fatalf("number used by another legal entity: %v", err)
	}
}
