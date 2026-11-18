package store

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/sales/migrations"
)

func TestPostgres_ListOpenOrderLinesIsOpenDemandOfTheSiteAndEntity(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("planner")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(appOf(t, pool))
	le, otherLE, site, otherSite, product := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	create := func(entity, atSite, hash, shipDate string) Order {
		line := Line{LineNo: 1, ProductID: product, Quantity: d(10), Unit: "EA", SiteID: atSite, WarehouseID: ids.New(), UnitPrice: d(10), DiscPct: d(0), LineNet: d(100), RequestedShipDate: shipDate}
		o, _, err := s.CreateOrder(ctx, NewOrder{LegalEntityID: entity, CustomerID: ids.New(), InvoiceAccountID: ids.New(), OrderType: "ORDER", Currency: "USD", RequestedShipDate: shipDate, Channel: "UI", RequestHash: hash, Lines: []Line{line}})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	confirm := func(o Order) {
		if res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "planner", time.Now()); err != nil || !res.Confirmed {
			t.Fatalf("confirm %s: %v", o.ID, err)
		}
	}
	exec := func(sql string, args ...any) {
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	open, partial, delivered := create(le, site, "open", "2027-03-04"), create(le, site, "partial", "2027-03-10"), create(le, site, "delivered", "2027-03-11")
	late, cancelled, lineCancelled := create(le, site, "late", "2027-09-01"), create(le, site, "cancelled", "2027-03-06"), create(le, site, "line-cancelled", "2027-03-07")
	create(le, site, "draft", "2027-03-05")
	elsewhere, otherEntity := create(le, otherSite, "elsewhere", "2027-03-08"), create(otherLE, site, "other-entity", "2027-03-09")
	for _, o := range []Order{open, partial, delivered, late, cancelled, lineCancelled, elsewhere, otherEntity} {
		confirm(o)
	}
	exec(`update sales_order_line set delivered_qty = 4 where order_id = $1`, partial.ID)
	exec(`update sales_order_line set delivered_qty = quantity where order_id = $1`, delivered.ID)
	exec(`update sales_order set status = 'CANCELLED' where id = $1`, cancelled.ID)
	exec(`update sales_order_line set line_status = 'CANCELLED' where order_id = $1`, lineCancelled.ID)
	exec(`update sales_order_line set confirmed_ship_date = '2027-03-01' where order_id = $1`, partial.ID)

	filter := OpenLineFilter{LegalEntityID: le, SiteID: site, DueTo: "2027-06-30", Limit: 50}
	page, err := s.ListOpenOrderLines(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Next != "" {
		t.Fatalf("open lines %+v next %q, want the open and the partly delivered line", page.Items, page.Next)
	}
	first, second := page.Items[0], page.Items[1]
	if first.OrderID != partial.ID || !first.OpenQty.Equal(d(6)) || first.DueDate != "2027-03-01" {
		t.Fatalf("partly delivered line %+v, want 6 open due on its confirmed date", first)
	}
	if second.OrderID != open.ID || !second.OpenQty.Equal(d(10)) || second.DueDate != "2027-03-04" || second.ProductID != product || second.SiteID != site || second.LineNo != 1 {
		t.Fatalf("open line %+v", second)
	}

	filter.Limit = 1
	one, err := s.ListOpenOrderLines(ctx, filter)
	if err != nil || len(one.Items) != 1 || one.Next == "" {
		t.Fatalf("first page %+v err %v", one, err)
	}
	cursor, ok := ParseOpenLineCursor(one.Next)
	if !ok {
		t.Fatalf("cursor %q is not parsed back", one.Next)
	}
	filter.After = cursor
	rest, err := s.ListOpenOrderLines(ctx, filter)
	if err != nil || len(rest.Items) != 1 || rest.Items[0].OrderID != open.ID || rest.Next != "" {
		t.Fatalf("second page %+v err %v", rest, err)
	}

	all, err := s.ListOpenOrderLines(ctx, OpenLineFilter{Limit: 50})
	if err != nil || len(all.Items) != 5 {
		t.Fatalf("unfiltered open lines %d err %v, want the five open lines of every site, entity and date", len(all.Items), err)
	}
	if none, err := s.ListOpenOrderLines(ctx, OpenLineFilter{LegalEntityID: ids.New(), Limit: 50}); err != nil || len(none.Items) != 0 {
		t.Fatalf("an entity with no orders sees %+v err %v", none.Items, err)
	}
}

func TestParseOpenLineCursor(t *testing.T) {
	if c, ok := ParseOpenLineCursor(""); c != nil || !ok {
		t.Fatalf("empty cursor = %v %v, want none and valid", c, ok)
	}
	id := ids.New()
	if c, ok := ParseOpenLineCursor("2027-03-04|" + id); !ok || c.LineID != id || c.String() != "2027-03-04|"+id {
		t.Fatalf("round trip = %+v %v", c, ok)
	}
	for _, bad := range []string{"x", "2027-03-04", "2027-03-04|nope", "2027-13-04|" + id, "|" + id} {
		if _, ok := ParseOpenLineCursor(bad); ok {
			t.Errorf("cursor %q accepted", bad)
		}
	}
}
