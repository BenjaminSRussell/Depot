package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/depot/services/scheduling/internal/store"
	"github.com/BenjaminSRussell/depot/services/scheduling/migrations"
)

// Order ids chosen so that uuid order is the reverse of order-number order:
// ORD-0001 gets the larger id. FR-23.5 sorts by (due date, priority, order
// number), so with due date and priority equal ORD-0001 must go first.
const (
	highOrderID = "ffffffff-ffff-4fff-bfff-ffffffffffff"
	lowOrderID  = "00000000-0000-4000-8000-000000000000"
)

// The order number given on POST /operations is stored, read back, and breaks
// the tie between orders with the same due date and priority (FR-23.5),
// compared naturally: PO-999999 before PO-1000000.
func TestRun_TiesFallToTheOrderNumberGivenOnTheOperation(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	due := at(4, 16, 0)
	op2, _, err := s.CreateOperation(ctx, store.Operation{OrderID: lowOrderID, OrderNumber: strp("PO-1000000"), OperNumber: 10, Minutes: 60, DueAt: &due, Priority: 100,
		ResourceIDs: []string{m.ID}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	op1, _, err := s.CreateOperation(ctx, store.Operation{OrderID: highOrderID, OrderNumber: strp("PO-999999"), OperNumber: 10, Minutes: 60, DueAt: &due, Priority: 100,
		ResourceIDs: []string{m.ID}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetOperation(ctx, op1.ID); err != nil || got.OrderNumber == nil || *got.OrderNumber != "PO-999999" {
		t.Fatalf("GetOperation = %+v err=%v, want order number PO-999999", got, err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	if b1, b2 := bars[op1.ID], bars[op2.ID]; !b1.Start.Before(b2.Start) {
		t.Fatalf("PO-999999 starts %v, PO-1000000 starts %v; want PO-999999 first despite its larger order id", b1.Start, b2.Start)
	}
}

// One order has one number: a POST without a number takes the order's, a POST
// or PATCH with a different one is refused, and a PATCH of the number only
// orders the next run (the jobs stay, also on a firmed operation).
func TestOperationOrderNumber_OneNumberPerOrder(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	order := ids.New()
	if _, _, err := s.CreateOperation(ctx, store.Operation{OrderID: order, OrderNumber: strp("WO-87"), OperNumber: 10, Minutes: 60, Priority: 100, ResourceIDs: []string{m.ID}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	op20, _, err := s.CreateOperation(ctx, store.Operation{OrderID: order, OperNumber: 20, Minutes: 60, Priority: 100, ResourceIDs: []string{m.ID}}, store.Idem{})
	if err != nil || op20.OrderNumber == nil || *op20.OrderNumber != "WO-87" {
		t.Fatalf("op20 = %+v err=%v, want it to take the order's number WO-87", op20, err)
	}
	if _, _, err := s.CreateOperation(ctx, store.Operation{OrderID: order, OrderNumber: strp("WO-88"), OperNumber: 30, Minutes: 60, Priority: 100,
		ResourceIDs: []string{m.ID}}, store.Idem{}); problemCode(err) != store.CodeOrderNumberMismatch {
		t.Fatalf("POST with another number: err=%v, want SCHED-0155", err)
	}
	if _, _, err := s.UpdateOperation(ctx, op20.ID, 1, store.OperationPatch{OrderNumber: strp("WO-88")}, idem("n1")); problemCode(err) != store.CodeOrderNumberMismatch {
		t.Fatalf("PATCH to another number while op10 has WO-87: err=%v, want SCHED-0155", err)
	}
	same, _, err := s.UpdateOperation(ctx, op20.ID, 1, store.OperationPatch{OrderNumber: strp("WO-87")}, idem("n2"))
	if err != nil || same.Version != 2 {
		t.Fatalf("PATCH to the same number = %+v err=%v, want version 2", same, err)
	}

	// A single-operation order can be renumbered; a firmed job stays put.
	solo, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OrderNumber: strp("WO-1"), OperNumber: 10, Minutes: 60, Priority: 100, ResourceIDs: []string{m.ID}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FirmJob(ctx, solo.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	upd, _, err := s.UpdateOperation(ctx, solo.ID, 1, store.OperationPatch{OrderNumber: strp("WO-2")}, idem("n3"))
	if err != nil || upd.JobsRemoved || upd.OrderNumber == nil || *upd.OrderNumber != "WO-2" || upd.Version != 2 {
		t.Fatalf("renumber = %+v err=%v, want WO-2, version 2, jobs kept", upd, err)
	}
	if n := jobCount(t, ctx, s, solo.ID); n != 1 {
		t.Fatalf("firmed operation has %d jobs after the renumber, want 1", n)
	}
}

// A re-import writes the order's number onto its existing operations, a
// firmed one included, without moving their versions or jobs; an event
// without a number keeps the one on record.
func TestImportProductionOrder_NumberOnExistingOperations(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	order := ids.New()
	req := importReq(order, 1, pjob(10, 1, "process", m.ID, "1"), pjob(20, 1, "process", m.ID, "1"))
	req.Order.OrderNumber = ""
	if _, err := s.ImportProductionOrder(ctx, req); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
	if err != nil || len(ops) != 2 || ops[0].OrderNumber != nil {
		t.Fatalf("operations = %+v err=%v, want two without a number", ops, err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FirmJob(ctx, ops[0].ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	before := map[string]store.Operation{}
	for _, op := range ops {
		got, err := s.GetOperation(ctx, op.ID)
		if err != nil {
			t.Fatal(err)
		}
		before[op.ID] = got
	}

	again := importReq(order, 2, pjob(10, 1, "process", m.ID, "1"), pjob(20, 1, "process", m.ID, "1"))
	again.Order.OrderNumber = "PO-000042"
	if res, err := s.ImportProductionOrder(ctx, again); err != nil || res.Unchanged != 1 || res.KeptFirmed != 1 {
		t.Fatalf("re-import = %+v err=%v, want one unchanged and one kept firmed", res, err)
	}
	for id, b := range before {
		got, err := s.GetOperation(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.OrderNumber == nil || *got.OrderNumber != "PO-000042" || got.Version != b.Version || got.UpdatedAt == nil ||
			(b.UpdatedAt != nil && !got.UpdatedAt.After(*b.UpdatedAt)) {
			t.Fatalf("operation %d after re-import = %+v (before %+v), want PO-000042, the same version and a later updated_at", got.OperNumber, got, b)
		}
		if n := jobCount(t, ctx, s, id); n != 1 {
			t.Fatalf("operation %d has %d jobs after the re-import, want 1", got.OperNumber, n)
		}
	}

	// An event without a number keeps the one on record, also for an
	// operation the event adds.
	third := importReq(order, 3, pjob(10, 1, "process", m.ID, "1"), pjob(20, 1, "process", m.ID, "1"), pjob(30, 1, "process", m.ID, "1"))
	third.Order.OrderNumber = ""
	if res, err := s.ImportProductionOrder(ctx, third); err != nil || res.Created != 1 {
		t.Fatalf("third import = %+v err=%v, want one created", res, err)
	}
	var distinct int
	var number string
	if err := admin.QueryRow(context.Background(), `select count(distinct coalesce(order_number, '')), min(coalesce(order_number, '')) from scheduling_operation where order_id = $1`,
		order).Scan(&distinct, &number); err != nil || distinct != 1 || number != "PO-000042" {
		t.Fatalf("numbers after an import without one: %d distinct, %q, err=%v; want all PO-000042", distinct, number, err)
	}
}

// Migration 0026 copies the import's number onto operations imported before
// they carried one, so GET and the run read the same value.
func TestOrderNumberBackfill_GetAndRunAgree(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	importNumbered := func(orderID, number string) string {
		t.Helper()
		req := importReq(orderID, 1, pjob(10, 1, "process", m.ID, "1"))
		req.Order.OrderNumber = number
		if res, err := s.ImportProductionOrder(ctx, req); err != nil || res.Created != 1 {
			t.Fatalf("import %s = %+v err=%v, want one operation created", number, res, err)
		}
		ops, err := s.ListOperations(ctx, store.OperationFilter{OrderID: orderID, Limit: 10})
		if err != nil || len(ops) != 1 {
			t.Fatalf("operations of %s = %+v err=%v", number, ops, err)
		}
		return ops[0].ID
	}
	opLow := importNumbered(lowOrderID, "PO-1000000")
	opHigh := importNumbered(highOrderID, "PO-999999")
	// As if imported before 0025: no number on the operations.
	if _, err := admin.Exec(context.Background(), `update scheduling_operation set order_number = null`); err != nil {
		t.Fatal(err)
	}
	sql, err := migrations.FS.ReadFile("0026_operation_order_number_backfill.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), "begin;\n"+string(sql)+"\ncommit;"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetOperation(ctx, opHigh); err != nil || got.OrderNumber == nil || *got.OrderNumber != "PO-999999" {
		t.Fatalf("GET after backfill = %+v err=%v, want PO-999999", got, err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	if hi, lo := bars[opHigh], bars[opLow]; !hi.Start.Before(lo.Start) {
		t.Fatalf("PO-999999 starts %v, PO-1000000 starts %v; want PO-999999 first", hi.Start, lo.Start)
	}
}

// BR-23.12: nearest-neighbour sequencing breaks a tie on changeover and due
// date by order number (natural order), read from the orders' operations.
func TestSequenceCampaign_TieFallsToOrderNumber(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	br2312Matrix(t, ctx, s, r.ID, "2027-01-01")
	for id, number := range map[string]string{highOrderID: "PO-999999", lowOrderID: "PO-1000000"} {
		if _, _, err := s.CreateOperation(ctx, store.Operation{OrderID: id, OrderNumber: strp(number), OperNumber: 10, Minutes: 60, Priority: 100,
			ResourceIDs: []string{r.ID}}, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	ord := func(id string) store.CampaignOrder {
		return store.CampaignOrder{OrderID: id, SetupGroup: "B", Quantity: decimal.RequireFromString("10"), RunMinutes: 60, RouteSetupMinutes: 15}
	}
	current := "A"
	c, _, err := s.CreateCampaign(ctx, store.Campaign{ResourceID: r.ID, CampaignKey: "FAMILY-1", PlannedStart: at(0, 6, 0),
		SequenceRule: domain.SequenceNearestNeighbor, CurrentSetupGroup: &current, Orders: []store.CampaignOrder{ord(lowOrderID), ord(highOrderID)}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	seq, _, err := s.SequenceCampaign(ctx, store.CampaignCommand{CampaignID: c.ID, Version: c.Version}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if seq.Orders[0].OrderID != highOrderID || seq.Orders[1].OrderID != lowOrderID {
		t.Fatalf("sequence = [%s %s], want PO-999999 (%s) first", seq.Orders[0].OrderID, seq.Orders[1].OrderID, highOrderID)
	}
}
