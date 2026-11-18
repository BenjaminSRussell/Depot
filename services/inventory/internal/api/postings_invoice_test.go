package api

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/depot/services/inventory/internal/store"
)

const (
	flowDir      = "../../testdata/receipt-flow/"
	flowEntity   = "0199f480-0000-7000-8000-00000000001a"
	flowReceipt  = "0199f480-0000-7000-8000-000000000011"
	flowProduct  = "0199f480-0000-7000-8000-000000000015"
	flowSite     = "0199f480-0000-7000-8000-000000000017"
	flowWarehous = "0199f480-0000-7000-8000-000000000018"
	flowLedger   = "0199f480-0000-7000-8000-000000000022"
)

type stockBooks struct {
	rows      []*store.TransactionRow
	bySource  map[string][]*store.TransactionRow
	corrects  map[string]string
	changes   map[string]store.StatusChange
	steps     []store.StatusStepInput
	ledgerIDs map[string]string
	stepErr   error
}

func newStockBooks() *stockBooks {
	return &stockBooks{bySource: map[string][]*store.TransactionRow{}, corrects: map[string]string{}, changes: map[string]store.StatusChange{}, ledgerIDs: map[string]string{}}
}

func (b *stockBooks) CreateTransaction(_ context.Context, in store.CreateTransactionInput) (store.StatusChange, bool, error) {
	if prev, ok := b.changes[in.IdempotencyKey]; ok {
		return prev, false, nil
	}
	cost := in.Qty.Mul(in.CostPrice)
	if in.Value != nil {
		cost = *in.Value
	}
	row := &store.TransactionRow{
		ID: "t" + strconv.Itoa(len(b.rows)+1), LegalEntityID: in.LegalEntityID, SourceType: in.SourceType, SourceID: in.SourceID, ProductID: in.ProductID,
		Dims: in.Dims, Qty: in.Qty, EnteredQty: in.EnteredQty, EnteredUnit: in.EnteredUnit, Currency: in.Currency, CostAmountPhysical: cost,
	}
	if in.Status == domain.StatusDeducted {
		row.IssueStatus = string(in.Status)
	} else {
		row.ReceiptStatus = string(in.Status)
	}
	b.rows = append(b.rows, row)
	b.bySource[in.SourceID] = append(b.bySource[in.SourceID], row)
	if in.LineSourceID != "" {
		b.bySource[in.LineSourceID] = append(b.bySource[in.LineSourceID], row)
	}
	b.corrects[row.ID] = in.Corrects
	change := store.StatusChange{ID: "c" + row.ID, TransactionID: row.ID}
	b.changes[in.IdempotencyKey] = change
	return change, true, nil
}

func (b *stockBooks) IssueBasis(context.Context, string, string, domain.Dimensions) (string, decimal.Decimal, error) {
	return "USD", decimal.Zero, nil
}

func (b *stockBooks) LineBookings(_ context.Context, _, _ string, lineSourceIDs []string) (map[string][]store.TransactionRow, error) {
	out := map[string][]store.TransactionRow{}
	for _, id := range lineSourceIDs {
		for _, r := range b.bySource[id] {
			out[id] = append(out[id], *r)
		}
	}
	return out, nil
}

func (b *stockBooks) ApplyStatusStep(_ context.Context, in store.StatusStepInput) (store.StatusChange, bool, error) {
	if b.stepErr != nil {
		return store.StatusChange{}, false, b.stepErr
	}
	if prev, ok := b.changes[in.IdempotencyKey]; ok {
		return prev, false, nil
	}
	b.steps = append(b.steps, in)
	for _, r := range b.rows {
		if r.ID != in.TransactionID {
			continue
		}
		if r.ReceiptStatus != string(in.From) {
			return store.StatusChange{}, false, httpx.NewProblem(412, store.CodeConflictVersion, "transaction is at "+r.ReceiptStatus)
		}
		r.ReceiptStatus = string(in.To)
		r.CostAmountPosted = in.CostAmount
		b.ledgerIDs[r.ID] = in.LedgerEntryID
		change := store.StatusChange{ID: "s" + r.ID, TransactionID: r.ID}
		b.changes[in.IdempotencyKey] = change
		return change, true, nil
	}
	return store.StatusChange{}, false, httpx.NewProblem(404, domain.CodeTransactionNotFound, "not found")
}

func (b *stockBooks) onHand(site, warehouse string) decimal.Decimal {
	total := decimal.Zero
	for _, r := range b.rows {
		if r.Dims[domain.DimSite] == site && r.Dims[domain.DimWarehouse] == warehouse {
			total = total.Add(r.Qty)
		}
	}
	return total
}

func (b *stockBooks) status(t *testing.T, receiptID string, lineNo int) store.TransactionRow {
	t.Helper()
	rows := b.bySource[receiptLineSourceID(postTenant, receiptID, lineNo)]
	if len(rows) != 1 {
		t.Fatalf("%d bookings for line %d, want 1", len(rows), lineNo)
	}
	return *rows[0]
}

func flowEvent(t *testing.T, typ, source, name string) events.Event {
	t.Helper()
	raw, err := os.ReadFile(flowDir + name)
	if err != nil {
		t.Fatal(err)
	}
	return postingEvent(t, typ, source, string(raw))
}

func flowHandlers(b *stockBooks) map[string]eventing.Handler {
	return PostingsConsumerHandlers(b, &fakeUnits{}, domain.NegativePolicy{})
}

func TestInvoicePosted_StepsReceivedToPurchasedWithLedgerEntry(t *testing.T) {
	b, ctx := newStockBooks(), postingsCtx(t)
	h := flowHandlers(b)
	if err := h[ReceiptPostedType].Handle(ctx, nil, flowEvent(t, ReceiptPostedType, ReceiptSource, "receipt-posted.json")); err != nil {
		t.Fatal(err)
	}
	if got := b.onHand(flowSite, flowWarehous); !got.Equal(d("10")) {
		t.Fatalf("on hand %s after the receipt, want 10 at the receipt's site and warehouse", got)
	}
	if row := b.status(t, flowReceipt, 1); row.ReceiptStatus != "RECEIVED" {
		t.Fatalf("receipt line at %s, want RECEIVED", row.ReceiptStatus)
	}
	invoice := flowEvent(t, InvoicePostedType, ReceiptSource, "invoice-posted.json")
	if err := h[InvoicePostedType].Handle(ctx, nil, invoice); err != nil {
		t.Fatal(err)
	}
	row := b.status(t, flowReceipt, 1)
	if row.ReceiptStatus != "PURCHASED" || row.CostAmountPosted == nil || !row.CostAmountPosted.Equal(d("105")) || b.ledgerIDs[row.ID] != flowLedger {
		t.Fatalf("after the invoice: %s posted %v ledger %q, want PURCHASED at 105 with %s", row.ReceiptStatus, row.CostAmountPosted, b.ledgerIDs[row.ID], flowLedger)
	}
	if err := h[InvoicePostedType].Handle(ctx, nil, invoice); err != nil || len(b.steps) != 1 {
		t.Fatalf("redelivery: %v, %d steps, want one step in total", err, len(b.steps))
	}
}

func TestReceiptCorrection_BooksMirrorTransactionAndReducesOnHand(t *testing.T) {
	b, ctx := newStockBooks(), postingsCtx(t)
	h := flowHandlers(b)
	if err := h[ReceiptPostedType].Handle(ctx, nil, flowEvent(t, ReceiptPostedType, ReceiptSource, "receipt-posted.json")); err != nil {
		t.Fatal(err)
	}
	if err := h[ReceiptCorrectedType].Handle(ctx, nil, flowEvent(t, ReceiptCorrectedType, ReceiptSource, "receipt-corrected.json")); err != nil {
		t.Fatal(err)
	}
	if len(b.rows) != 2 {
		t.Fatalf("%d transactions, want the receipt line and its mirror", len(b.rows))
	}
	mirror := b.rows[1]
	if !mirror.Qty.Equal(d("-2")) || b.corrects[mirror.ID] != b.rows[0].ID || mirror.IssueStatus != "DEDUCTED" {
		t.Fatalf("mirror %+v corrects %q, want -2 linked to %s", mirror, b.corrects[mirror.ID], b.rows[0].ID)
	}
	if got := b.onHand(flowSite, flowWarehous); !got.Equal(d("8")) {
		t.Fatalf("on hand %s after the -2 correction, want 8", got)
	}
}

func TestInvoicePosted_ThousandLineReceiptMovesEveryLineToPurchased(t *testing.T) {
	const lines = 1000
	var receipt, invoice strings.Builder
	receipt.WriteString(`{"receipt_id":"` + flowReceipt + `","receipt_number":"RCV-1000","legal_entity_id":"` + flowEntity + `","posting_date":"2026-10-25","currency":"USD","lines":[`)
	invoice.WriteString(`{"invoice_id":"` + postReceipt + `","legal_entity_id":"` + flowEntity + `","posting_date":"2026-10-26","currency":"USD","ledger_entry_id":"` + flowLedger + `","lines":[`)
	for i := 1; i <= lines; i++ {
		sep := ","
		if i == 1 {
			sep = ""
		}
		receipt.WriteString(fmt.Sprintf(`%s{"line_no":%d,"product_id":%q,"unit":"EA","qty":"2","serials":[],"amount":"20","unit_price":"10","site_id":%q,"warehouse_id":%q}`, sep, i, flowProduct, flowSite, flowWarehous))
		invoice.WriteString(fmt.Sprintf(`%s{"line_no":%d,"receipt_id":%q,"receipt_line_no":%d,"qty":"2","unit_price":"10","amount":"20"}`, sep, i, flowReceipt, i))
	}
	receipt.WriteString("]}")
	invoice.WriteString("]}")
	b, ctx := newStockBooks(), postingsCtx(t)
	h := flowHandlers(b)
	if err := h[ReceiptPostedType].Handle(ctx, nil, postingEvent(t, ReceiptPostedType, ReceiptSource, receipt.String())); err != nil {
		t.Fatal(err)
	}
	if len(b.rows) != lines || !b.onHand(flowSite, flowWarehous).Equal(d("2000")) {
		t.Fatalf("%d bookings, on hand %s, want %d and 2000", len(b.rows), b.onHand(flowSite, flowWarehous), lines)
	}
	if err := h[InvoicePostedType].Handle(ctx, nil, postingEvent(t, InvoicePostedType, ReceiptSource, invoice.String())); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= lines; i++ {
		if row := b.status(t, flowReceipt, i); row.ReceiptStatus != "PURCHASED" {
			t.Fatalf("line %d at %s, want PURCHASED", i, row.ReceiptStatus)
		}
	}
	if len(b.steps) != lines {
		t.Fatalf("%d steps, want %d", len(b.steps), lines)
	}
}

func TestInvoicePosted_SplitsTheAmountOverTheSerialsOfALine(t *testing.T) {
	receipt := `{"receipt_id":"` + flowReceipt + `","receipt_number":"RCV-S","legal_entity_id":"` + flowEntity + `","posting_date":"2026-10-25","currency":"USD","lines":[` +
		`{"line_no":1,"product_id":"` + flowProduct + `","unit":"EA","qty":"3","serials":["A","B","C"],"amount":"30","unit_price":"10","site_id":"` + flowSite + `"}]}`
	invoice := `{"invoice_id":"` + postReceipt + `","legal_entity_id":"` + flowEntity + `","posting_date":"2026-10-26","currency":"USD","ledger_entry_id":"` + flowLedger + `","lines":[` +
		`{"line_no":1,"receipt_id":"` + flowReceipt + `","receipt_line_no":1,"qty":"3","unit_price":"10","amount":"100"}]}`
	b, ctx := newStockBooks(), postingsCtx(t)
	h := flowHandlers(b)
	if err := h[ReceiptPostedType].Handle(ctx, nil, postingEvent(t, ReceiptPostedType, ReceiptSource, receipt)); err != nil {
		t.Fatal(err)
	}
	if err := h[InvoicePostedType].Handle(ctx, nil, postingEvent(t, InvoicePostedType, ReceiptSource, invoice)); err != nil {
		t.Fatal(err)
	}
	total := decimal.Zero
	for _, s := range b.steps {
		total = total.Add(*s.CostAmount)
	}
	if len(b.steps) != 3 || !total.Equal(d("100")) || !b.steps[0].CostAmount.Equal(d("33.33")) || !b.steps[2].CostAmount.Equal(d("33.34")) {
		t.Fatalf("%d steps adding to %s, want three adding to 100 with the remainder on the last", len(b.steps), total)
	}
}

func invoiceLine(receiptLineNo, qty, amount string) string {
	return `{"line_no":1,"receipt_id":"` + flowReceipt + `","receipt_line_no":` + receiptLineNo + `,"qty":"` + qty + `","unit_price":"10.5","amount":"` + amount + `"}`
}

func invoiceWith(ledgerEntry string, lines ...string) string {
	return `{"invoice_id":"` + postReceipt + `","legal_entity_id":"` + flowEntity + `","posting_date":"2026-10-26","currency":"USD","ledger_entry_id":"` + ledgerEntry +
		`","lines":[` + strings.Join(lines, ",") + `]}`
}

func TestInvoicePosted_ParksRetriesAndSkipsWhatItCannotStep(t *testing.T) {
	received := func(t *testing.T) (*stockBooks, eventing.Handler) {
		b := newStockBooks()
		h := flowHandlers(b)
		if err := h[ReceiptPostedType].Handle(postingsCtx(t), nil, flowEvent(t, ReceiptPostedType, ReceiptSource, "receipt-posted.json")); err != nil {
			t.Fatal(err)
		}
		return b, h[InvoicePostedType]
	}
	handle := func(h eventing.Handler, t *testing.T, source, data string) error {
		return h.Handle(postingsCtx(t), nil, postingEvent(t, InvoicePostedType, source, data))
	}
	permanent := map[string]string{
		"not json":              `{"lines":"x"}`,
		"no ledger entry":       invoiceWith("", invoiceLine("1", "10", "105")),
		"ledger entry not uuid": invoiceWith("entry-1", invoiceLine("1", "10", "105")),
		"zero qty":              invoiceWith(flowLedger, invoiceLine("1", "0", "105")),
		"text qty":              invoiceWith(flowLedger, invoiceLine("1", "abc", "105")),
		"negative amount":       invoiceWith(flowLedger, invoiceLine("1", "10", "-1")),
		"line zero":             invoiceWith(flowLedger, invoiceLine("0", "10", "105")),
	}
	for name, data := range permanent {
		b, h := received(t)
		if err := handle(h, t, ReceiptSource, data); !eventing.IsPermanent(err) || len(b.steps) != 0 {
			t.Errorf("%s: %v with %d steps, want permanent and none", name, err, len(b.steps))
		}
	}
	b, h := received(t)
	if err := handle(h, t, "/depot/other/other", invoiceWith(flowLedger, invoiceLine("1", "10", "105"))); !eventing.IsPermanent(err) || len(b.steps) != 0 {
		t.Errorf("foreign source: %v, want permanent", err)
	}
	if err := h.Handle(context.Background(), nil, postingEvent(t, InvoicePostedType, ReceiptSource, invoiceWith(flowLedger, invoiceLine("1", "10", "105")))); !eventing.IsPermanent(err) {
		t.Errorf("no principal: %v, want permanent", err)
	}

	err := handle(h, t, ReceiptSource, invoiceWith(flowLedger, invoiceLine("2", "10", "105")))
	if err == nil || eventing.IsPermanent(err) || !strings.Contains(err.Error(), CodePostingOriginalMissing) {
		t.Errorf("line not booked yet: %v, want a retryable %s", err, CodePostingOriginalMissing)
	}
	if err := handle(h, t, ReceiptSource, invoiceWith(flowLedger)); err != nil {
		t.Errorf("no lines: %v, want acked", err)
	}
	if err := handle(h, t, ReceiptSource, invoiceWith(flowLedger, invoiceLine("1", "4", "42"))); err != nil || len(b.steps) != 0 || b.status(t, flowReceipt, 1).ReceiptStatus != "RECEIVED" {
		t.Errorf("partial invoice: %v with %d steps, want acked and the line left at RECEIVED", err, len(b.steps))
	}

	b.stepErr = fmt.Errorf("connection reset")
	if err := handle(h, t, ReceiptSource, invoiceWith(flowLedger, invoiceLine("1", "10", "105"))); err == nil || eventing.IsPermanent(err) {
		t.Errorf("store failure: %v, want a retryable error", err)
	}
	b.stepErr = httpx.NewProblem(422, domain.CodeStatusStep, "no")
	if err := handle(h, t, ReceiptSource, invoiceWith(flowLedger, invoiceLine("1", "10", "105"))); !eventing.IsPermanent(err) {
		t.Errorf("rule of inventory: %v, want permanent", err)
	}
	b.stepErr = nil
	b.rows[0].ReceiptStatus = "ARRIVED"
	if err := handle(h, t, ReceiptSource, invoiceWith(flowLedger, invoiceLine("1", "10", "105"))); !eventing.IsPermanent(err) || !strings.Contains(err.Error(), CodePostingNotReceived) || len(b.steps) != 0 {
		t.Errorf("booking not at RECEIVED: %v, want permanent %s", err, CodePostingNotReceived)
	}
}
