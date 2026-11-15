package iccharge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/iccharge"
	"github.com/BenjaminSRussell/passage/services/payables/internal/ledger"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
	"github.com/BenjaminSRussell/passage/services/payables/migrations"
)

// publishedSource is what intercompany's enqueue passes to events.New (services/intercompany/internal/events.Source).
const publishedSource = "/passage/intercompany"

// event builds the event the way intercompany's store enqueues it: events.New with the given source.
func event(t *testing.T, data map[string]any, source string) events.Event {
	t.Helper()
	evt, err := events.New(iccharge.ChargeInvoiceRequestedType, source, ids.New(), testkit.TenantID(1), data)
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func consumerCtx(t *testing.T) context.Context {
	t.Helper()
	sp, err := tenant.ServicePrincipal(iccharge.ConsumerName, testkit.TenantID(1), "")
	if err != nil {
		t.Fatal(err)
	}
	return tenant.With(context.Background(), sp)
}

// #40: an IC charge in Invoice mode becomes one intake row in the borrower's legal entity for the pairing's vendor, once.
func TestHandle_TakesTheBorrowersVendorInvoiceInOnce(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	le, vendor, charge := ids.New(), ids.New(), ids.New()
	data := map[string]any{"id": charge, "sourceId": "TS-2", "version": 1, "lender": "A", "borrower": "B", "category": "Hour", "transactionDate": "2026-11-05",
		"currency": "USD", "price": "550.00", "tax": "104.50", "borrowerLegalEntityId": le, "vendorAccount": vendor}
	first, err := iccharge.Handle(consumerCtx(t), s, nil, event(t, data, publishedSource))
	if err != nil {
		t.Fatal(err)
	}
	if first.LegalEntityID != le || first.VendorID != vendor || first.Source != "INTERCOMPANY" || first.InvoiceNumber != "IC-TS-2-v1" ||
		first.Net.String() != "550" || first.Gross.String() != "654.5" || first.ReceivedBy != "service:payables-iccharge" {
		t.Fatalf("intake %+v", first)
	}
	again, err := iccharge.Handle(consumerCtx(t), s, nil, event(t, data, publishedSource))
	if err != nil || again.ID != first.ID {
		t.Fatalf("redelivery = %s, %v; want %s", again.ID, err, first.ID)
	}
	data["price"] = "600.00"
	if _, err := iccharge.Handle(consumerCtx(t), s, nil, event(t, data, publishedSource)); err == nil {
		t.Fatal("the same charge version with another price is refused")
	}
}

func TestHandle_RefusesWhatCannotBeTakenIn(t *testing.T) {
	good := map[string]any{"id": ids.New(), "version": 1, "lender": "A", "borrower": "B", "transactionDate": "2026-11-05", "currency": "USD", "price": "10",
		"borrowerLegalEntityId": ids.New(), "vendorAccount": ids.New()}
	for name, mut := range map[string]func(map[string]any){
		"no legal entity": func(d map[string]any) { delete(d, "borrowerLegalEntityId") },
		"no vendor":       func(d map[string]any) { d["vendorAccount"] = "V-1" },
		"bad tax":         func(d map[string]any) { d["tax"] = "-1" },
	} {
		d := map[string]any{}
		for k, v := range good {
			d[k] = v
		}
		mut(d)
		if _, err := iccharge.Handle(consumerCtx(t), nil, nil, event(t, d, publishedSource)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := iccharge.Handle(consumerCtx(t), nil, nil, event(t, good, "/passage/billing")); err == nil {
		t.Error("another source is refused")
	}
}

// fakeLedger keeps the entries it is asked to post, one per idempotency key, and refuses an unbalanced one the way the ledger does.
type fakeLedger struct {
	entries map[string]ledger.Entry
	ids     map[string]string
	calls   int
	fail    error
}

func newFakeLedger() *fakeLedger {
	return &fakeLedger{entries: map[string]ledger.Entry{}, ids: map[string]string{}}
}

func (f *fakeLedger) Post(_ context.Context, e ledger.Entry) (string, error) {
	f.calls++
	if f.fail != nil {
		return "", f.fail
	}
	debit, credit := decimal.Zero, decimal.Zero
	for _, l := range e.Lines {
		if l.Credit {
			credit = credit.Add(l.Amount)
		} else {
			debit = debit.Add(l.Amount)
		}
	}
	if !debit.Equal(credit) {
		return "", &ledger.StatusError{Status: 422, Code: "LED-0001"}
	}
	if id, ok := f.ids[e.IdempotencyKey]; ok {
		return id, nil
	}
	id := ids.New()
	f.ids[e.IdempotencyKey], f.entries[e.IdempotencyKey] = id, e
	return id, nil
}

func autoCharge(le, vendor string) map[string]any {
	return map[string]any{"id": ids.New(), "sourceId": "TS-9", "version": 1, "lender": "A", "borrower": "B", "category": "Hour", "transactionDate": "2026-11-05",
		"currency": "USD", "price": "550.00", "tax": "104.50", "borrowerLegalEntityId": le, "vendorAccount": vendor, "autoPostInvoice": true}
}

// #40: a pairing that opted in has its vendor invoice posted: worked example 550.00 net + 104.50 input tax = 654.50 payable. The
// entry balances (Dr 6200 550.00, Dr 1400 104.50, Cr 2100 654.50), the AP item is for 654.50, the intake row is POSTED, and a
// redelivery posts and opens nothing new.
func TestHandle_AutoPostsTheVendorInvoiceOnceWhenThePairingOptedIn(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	led := newFakeLedger()
	auto := &iccharge.AutoPost{Ledger: led, LedgerID: "L-B", ExpenseAccount: "6200", InputTaxAccount: "1400", ControlAccount: "2100", PaymentTermDays: 30}
	le, vendor := ids.New(), ids.New()
	data := autoCharge(le, vendor)

	first, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, data, publishedSource))
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "POSTED" || first.LedgerEntryID == "" || first.Gross.String() != "654.5" || first.Net.String() != "550" {
		t.Fatalf("intake %+v", first)
	}
	if led.calls != 1 || len(led.entries) != 1 {
		t.Fatalf("ledger calls = %d entries = %d, want one", led.calls, len(led.entries))
	}
	e := led.entries["payables-iccharge:"+data["id"].(string)+":1"]
	got := map[string]string{}
	for _, l := range e.Lines {
		side := "D"
		if l.Credit {
			side = "C"
		}
		got[side+" "+l.Account] = l.Amount.String()
	}
	want := map[string]string{"D 6200": "550", "D 1400": "104.5", "C 2100": "654.5"}
	if len(got) != 3 || got["D 6200"] != want["D 6200"] || got["D 1400"] != want["D 1400"] || got["C 2100"] != want["C 2100"] ||
		e.LegalEntityID != le || e.LedgerID != "L-B" || e.Currency != "USD" || e.PostingDate != "2026-11-05" || e.DocumentID != first.ID {
		t.Fatalf("entry %+v lines %v", e, got)
	}
	var items int
	var amount string
	if err := admin.QueryRow(context.Background(), `select count(*), coalesce(max(open_amount::text), '') from ap_open_item where source_invoice_id = $1`, first.ID).Scan(&items, &amount); err != nil {
		t.Fatal(err)
	}
	if items != 1 || amount != "654.500000" {
		t.Fatalf("open items = %d amount %s", items, amount)
	}

	again, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, data, publishedSource))
	if err != nil || again.ID != first.ID || again.Status != "POSTED" || again.LedgerEntryID != first.LedgerEntryID {
		t.Fatalf("redelivery = %+v, %v", again, err)
	}
	if led.calls != 1 {
		t.Fatalf("a redelivery posted again: %d ledger calls", led.calls)
	}
}

// A failure between the steps is finished by the redelivery: the ledger was down, the row waits as RECEIVED, and the next delivery
// posts it once.
func TestHandle_AutoPostResumesAfterALedgerFailure(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	led := newFakeLedger()
	auto := &iccharge.AutoPost{Ledger: led, LedgerID: "L-B", ExpenseAccount: "6200", InputTaxAccount: "1400", ControlAccount: "2100"}
	data := autoCharge(ids.New(), ids.New())

	led.fail = errors.New("ledger: connection refused")
	in, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, data, publishedSource))
	if err == nil || eventing.IsPermanent(err) || in.Status != "RECEIVED" {
		t.Fatalf("ledger down = %+v, %v: want a retryable error and the row kept as RECEIVED", in, err)
	}
	led.fail = nil
	done, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, data, publishedSource))
	if err != nil || done.ID != in.ID || done.Status != "POSTED" {
		t.Fatalf("retry = %+v, %v", done, err)
	}
	if len(led.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(led.entries))
	}

	// A ledger that refuses the entry for good is a permanent failure, and the row stays for review.
	other := autoCharge(ids.New(), ids.New())
	other["sourceId"] = "TS-10"
	led.fail = &ledger.StatusError{Status: 422, Code: "LED-0004"}
	kept, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, other, publishedSource))
	if err == nil || !eventing.IsPermanent(err) || kept.Status != "RECEIVED" {
		t.Fatalf("refused entry = %+v, %v", kept, err)
	}
}

// Manual review stays the default, and an opted-in charge on a deployment without the auto-post settings is taken in and refused,
// not silently left for review.
func TestHandle_ReviewIsTheDefaultAndAutoPostNeedsItsSettings(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	led := newFakeLedger()
	auto := &iccharge.AutoPost{Ledger: led, LedgerID: "L-B", ExpenseAccount: "6200", InputTaxAccount: "1400", ControlAccount: "2100"}

	review := autoCharge(ids.New(), ids.New())
	delete(review, "autoPostInvoice")
	in, err := iccharge.Handle(consumerCtx(t), s, auto, event(t, review, publishedSource))
	if err != nil || in.Status != "RECEIVED" || led.calls != 0 {
		t.Fatalf("default = %+v, %v, ledger calls %d", in, err, led.calls)
	}

	unconfigured := autoCharge(ids.New(), ids.New())
	unconfigured["sourceId"] = "TS-11"
	kept, err := iccharge.Handle(consumerCtx(t), s, nil, event(t, unconfigured, publishedSource))
	if err == nil || !eventing.IsPermanent(err) || kept.Status != "RECEIVED" || kept.ID == "" {
		t.Fatalf("no settings = %+v, %v", kept, err)
	}

	noTaxAccount := &iccharge.AutoPost{Ledger: led, LedgerID: "L-B", ExpenseAccount: "6200", ControlAccount: "2100"}
	taxed := autoCharge(ids.New(), ids.New())
	taxed["sourceId"] = "TS-12"
	if _, err := iccharge.Handle(consumerCtx(t), s, noTaxAccount, event(t, taxed, publishedSource)); err == nil || !eventing.IsPermanent(err) {
		t.Fatalf("tax without an input tax account: %v", err)
	}
	if led.calls != 0 {
		t.Fatalf("nothing should reach the ledger: %d calls", led.calls)
	}
}
