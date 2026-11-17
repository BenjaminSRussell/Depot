package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/depot/services/scheduling/internal/store"
)

// Order ids chosen so that uuid order is the reverse of order-number order:
// ORD-0001 gets the larger id. FR-23.5 sorts by (due date, priority, order
// number), so with due date and priority equal ORD-0001 must go first.
const (
	highOrderID = "ffffffff-ffff-4fff-bfff-ffffffffffff"
	lowOrderID  = "00000000-0000-4000-8000-000000000000"
)

// The order number given on POST /operations is stored, read back, and breaks
// the tie between orders with the same due date and priority (FR-23.5).
func TestRun_TiesFallToTheOrderNumberGivenOnTheOperation(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	due := at(4, 16, 0)
	n1, n2 := "ORD-0001", "ORD-0002"
	op2, _, err := s.CreateOperation(ctx, store.Operation{OrderID: lowOrderID, OrderNumber: &n2, OperNumber: 10, Minutes: 60, DueAt: &due, Priority: 100,
		ResourceIDs: []string{m.ID}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	op1, _, err := s.CreateOperation(ctx, store.Operation{OrderID: highOrderID, OrderNumber: &n1, OperNumber: 10, Minutes: 60, DueAt: &due, Priority: 100,
		ResourceIDs: []string{m.ID}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetOperation(ctx, op1.ID); err != nil || got.OrderNumber == nil || *got.OrderNumber != n1 {
		t.Fatalf("GetOperation = %+v err=%v, want order number %s", got, err, n1)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	if b1, b2 := bars[op1.ID], bars[op2.ID]; !b1.Start.Before(b2.Start) {
		t.Fatalf("ORD-0001 starts %v, ORD-0002 starts %v; want ORD-0001 first despite its larger order id", b1.Start, b2.Start)
	}
}

// Imported production orders carry their number onto the operations, and an
// operation written before it carried one takes the imported order's number.
func TestRun_TiesFallToTheImportedOrderNumber(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	importNumbered := func(orderID, number string) {
		t.Helper()
		req := importReq(orderID, 1, pjob(10, 1, "process", m.ID, "1"))
		req.Order.OrderNumber = number
		if res, err := s.ImportProductionOrder(ctx, req); err != nil || res.Created != 1 {
			t.Fatalf("import %s = %+v err=%v, want one operation created", number, res, err)
		}
	}
	importNumbered(lowOrderID, "PO-0002")
	importNumbered(highOrderID, "PO-0001")

	ops, err := s.ListOperations(ctx, store.OperationFilter{OrderID: highOrderID, Limit: 10})
	if err != nil || len(ops) != 1 || ops[0].OrderNumber == nil || *ops[0].OrderNumber != "PO-0001" {
		t.Fatalf("imported operations = %+v err=%v, want one with order number PO-0001", ops, err)
	}
	opHigh := ops[0].ID
	ops, err = s.ListOperations(ctx, store.OperationFilter{OrderID: lowOrderID, Limit: 10})
	if err != nil || len(ops) != 1 {
		t.Fatalf("imported operations = %+v err=%v, want one", ops, err)
	}
	opLow := ops[0].ID

	check := func(when string) {
		t.Helper()
		if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
			t.Fatal(err)
		}
		bars := barsOf(t, ctx, s)
		if hi, lo := bars[opHigh], bars[opLow]; !hi.Start.Before(lo.Start) {
			t.Fatalf("%s: PO-0001 starts %v, PO-0002 starts %v; want PO-0001 first", when, hi.Start, lo.Start)
		}
	}
	check("numbers on the operations")

	// Operations imported before the column existed have no number of their
	// own; the run reads it from the order import.
	if _, err := admin.Exec(context.Background(), `update scheduling_operation set order_number = null`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `delete from scheduled_job`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `update scheduling_operation set status = 'open'`); err != nil {
		t.Fatal(err)
	}
	check("numbers from the order import only")
}
