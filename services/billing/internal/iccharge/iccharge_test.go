package iccharge_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/billing/internal/iccharge"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
	"github.com/BenjaminSRussell/passage/services/billing/migrations"
)

// publishedSource is what intercompany's enqueue passes to events.New (services/intercompany/internal/events.Source).
const publishedSource = "/passage/intercompany"

// published builds the event the way intercompany's store enqueues it: events.New with the publisher's source.
func published(t *testing.T, data map[string]any) events.Event {
	t.Helper()
	evt, err := events.New(iccharge.ChargeInvoiceRequestedType, publishedSource, ids.New(), testkit.TenantID(1), data)
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

func charge(le, customer string) map[string]any {
	return map[string]any{"id": ids.New(), "sourceId": "TS-2", "version": 1, "lender": "A", "borrower": "B", "category": "Hour", "transactionDate": "2026-11-05",
		"currency": "USD", "price": "550.00", "tax": "104.50", "taxCode": "IC19", "lenderLegalEntityId": le, "customerAccount": customer}
}

// #40: an IC charge in Invoice mode, as intercompany publishes it, becomes one FREE_TEXT proposal in the lender's legal entity for
// the pairing's customer, carrying the charge's own tax, once.
func TestHandle_RaisesTheLendersProposalOnceWithTheChargesTax(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	cfg := iccharge.Config{RevenueAccount: "4900", TaxCode: "STD", PaymentTermDays: 30}
	le, customer := ids.New(), ids.New()
	data := charge(le, customer)
	first, err := iccharge.Handle(consumerCtx(t), s, cfg, published(t, data))
	if err != nil {
		t.Fatal(err)
	}
	if first.LegalEntityID != le || first.CustomerID != customer || first.Type != "FREE_TEXT" || first.Status != "PROPOSAL" ||
		first.Totals.Tax.String() != "104.5" || first.Totals.Gross.String() != "654.5" || len(first.Lines) != 1 ||
		first.Lines[0].RevenueAccount != "4900" || first.Lines[0].TaxCode != "IC19" || first.DueDate.Format("2006-01-02") != "2026-12-05" {
		t.Fatalf("proposal %+v", first)
	}
	again, err := iccharge.Handle(consumerCtx(t), s, cfg, published(t, data))
	if err != nil || again.ID != first.ID {
		t.Fatalf("redelivery = %s, %v; want %s", again.ID, err, first.ID)
	}
}

// #40: the proposal names the borrower's legal entity as the trading partner, and none when it is the lender itself.
func TestProposal_NamesTheBorrowerAsTradingPartner(t *testing.T) {
	cfg := iccharge.Config{RevenueAccount: "4900", TaxCode: "STD", PaymentTermDays: 30}
	le, customer, borrower := ids.New(), ids.New(), ids.New()
	data := charge(le, customer)
	data["borrowerLegalEntityId"] = borrower
	raw, _ := json.Marshal(data)
	var d iccharge.Data
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	inv, _, _, err := iccharge.Proposal(d, cfg)
	if err != nil || inv.TradingPartnerID != borrower {
		t.Fatalf("partner %q, err %v; want %s", inv.TradingPartnerID, err, borrower)
	}
	d.BorrowerLegalEntityID = le
	if inv, _, _, err = iccharge.Proposal(d, cfg); err != nil || inv.TradingPartnerID != "" {
		t.Fatalf("a borrower that is the lender's own legal entity is no partner: %q %v", inv.TradingPartnerID, err)
	}
}

func TestHandle_RefusesWhatCannotBeInvoiced(t *testing.T) {
	cfg := iccharge.Config{RevenueAccount: "4900", TaxCode: "IC19", PaymentTermDays: 30}
	good := charge(ids.New(), ids.New())
	for name, mut := range map[string]func(map[string]any){
		"no legal entity": func(d map[string]any) { delete(d, "lenderLegalEntityId") },
		"no customer":     func(d map[string]any) { d["customerAccount"] = "C-100" },
		"no date":         func(d map[string]any) { d["transactionDate"] = "" },
		"bad tax":         func(d map[string]any) { d["tax"] = "-1" },
	} {
		d := map[string]any{}
		for k, v := range good {
			d[k] = v
		}
		mut(d)
		if _, err := iccharge.Handle(consumerCtx(t), nil, cfg, published(t, d)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	other, err := events.New(iccharge.ChargeInvoiceRequestedType, "/passage/sales", ids.New(), testkit.TenantID(1), good)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := iccharge.Handle(consumerCtx(t), nil, cfg, other); err == nil {
		t.Error("another source is refused")
	}
	if _, err := iccharge.Handle(context.Background(), nil, cfg, published(t, good)); err == nil {
		t.Error("no principal is refused")
	}
}

// closedThrough is a fake close service: every date up to and including it is in a closed period.
type closedThrough struct{ through time.Time }

func (c closedThrough) Open(_ context.Context, _, _ string, date time.Time) (bool, error) {
	return date.After(c.through), nil
}

func autoCfg(le string, closed string) iccharge.Config {
	through, _ := time.Parse(time.DateOnly, closed)
	return iccharge.Config{RevenueAccount: "4900", TaxCode: "STD", PaymentTermDays: 30, Periods: closedThrough{through}, Ledgers: map[string]string{le: ids.New()},
		Now: func() time.Time { return time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC) }}
}

// #40: a pairing that opted in has the proposal posted by the consumer: worked example, 550.00 + 104.50 tax = 654.50 gross in
// USD, one invoice number drawn once, and the same posted invoice on a redelivery.
func TestHandle_AutoPostsOnlyWhenThePairingOptedIn(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	le, customer := ids.New(), ids.New()
	cfg := autoCfg(le, "2026-09-30")

	review := charge(le, customer)
	kept, err := iccharge.Handle(consumerCtx(t), s, cfg, published(t, review))
	if err != nil || kept.Status != "PROPOSAL" {
		t.Fatalf("manual review is the default: %s, %v", kept.Status, err)
	}

	auto := charge(le, customer)
	auto["sourceId"] = "TS-3"
	auto["autoPostInvoice"] = true
	first, err := iccharge.Handle(consumerCtx(t), s, cfg, published(t, auto))
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "POSTED" || first.Number == "" || first.Totals.Net.String() != "550" || first.Totals.Tax.String() != "104.5" || first.Totals.Gross.String() != "654.5" ||
		first.Currency != "USD" || first.InvoiceDate.Format("2006-01-02") != "2026-11-05" || first.PostingDate.Format("2006-01-02") != "2026-11-05" {
		t.Fatalf("posted invoice %+v", first)
	}
	again, err := iccharge.Handle(consumerCtx(t), s, cfg, published(t, auto))
	if err != nil || again.ID != first.ID || again.Number != first.Number || again.Status != "POSTED" {
		t.Fatalf("redelivery = %+v, %v; want the same posted invoice %s", again, err, first.ID)
	}

	// A review proposal that is redelivered with the opt-in now on is posted (the pairing changed its mind), still once.
	review["autoPostInvoice"] = true
	late, err := iccharge.Handle(consumerCtx(t), s, cfg, published(t, review))
	if err != nil || late.ID != kept.ID || late.Status != "POSTED" {
		t.Fatalf("late opt-in = %+v, %v", late, err)
	}
}

// M1: a charge dated in a closed period is posted on the first open date (today), the charge date staying the invoice date; with
// both periods closed, or close not configured, the proposal stays for review.
func TestHandle_AutoPostMovesPastAClosedPeriodOrWaitsForReview(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	le, customer := ids.New(), ids.New()

	late := charge(le, customer)
	late["sourceId"], late["autoPostInvoice"], late["transactionDate"] = "TS-LATE", true, "2026-10-30"
	posted, err := iccharge.Handle(consumerCtx(t), s, autoCfg(le, "2026-10-31"), published(t, late))
	if err != nil || posted.Status != "POSTED" || posted.InvoiceDate.Format("2006-01-02") != "2026-10-30" || posted.PostingDate.Format("2006-01-02") != "2026-11-15" {
		t.Fatalf("closed October = %+v, %v; want posted on 2026-11-15 with invoice date 2026-10-30", posted, err)
	}

	allClosed := charge(le, customer)
	allClosed["sourceId"], allClosed["autoPostInvoice"] = "TS-CLOSED", true
	kept, err := iccharge.Handle(consumerCtx(t), s, autoCfg(le, "2026-12-31"), published(t, allClosed))
	if err != nil || kept.Status != "PROPOSAL" {
		t.Fatalf("every period closed = %+v, %v; want a proposal for review", kept, err)
	}

	noClose := charge(le, customer)
	noClose["sourceId"], noClose["autoPostInvoice"] = "TS-NOCLOSE", true
	kept, err = iccharge.Handle(consumerCtx(t), s, iccharge.Config{RevenueAccount: "4900", TaxCode: "STD", PaymentTermDays: 30}, published(t, noClose))
	if err != nil || kept.Status != "PROPOSAL" {
		t.Fatalf("no close service = %+v, %v; want a proposal for review", kept, err)
	}
}

// H1: a second version of a charge whose first version is posted is never auto-posted, so the borrower is not billed twice; it waits
// for review with the reason on its line. A first version that is only a proposal does not block the next.
func TestHandle_AVersionAfterAPostedOneGoesToReview(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	le, customer := ids.New(), ids.New()
	cfg := autoCfg(le, "2026-09-30")

	v1 := charge(le, customer)
	v1["autoPostInvoice"] = true
	first, err := iccharge.Handle(consumerCtx(t), s, cfg, published(t, v1))
	if err != nil || first.Status != "POSTED" {
		t.Fatalf("version 1 = %+v, %v", first, err)
	}
	v2 := map[string]any{}
	for k, v := range v1 {
		v2[k] = v
	}
	v2["version"], v2["price"], v2["tax"] = 2, "500.00", "95.00"
	second, err := iccharge.Handle(consumerCtx(t), s, cfg, published(t, v2))
	if err != nil || second.ID == first.ID || second.Status != "PROPOSAL" || second.Totals.Gross.String() != "595" ||
		!strings.Contains(second.Lines[0].Description, "an earlier version of this charge is already posted") {
		t.Fatalf("version 2 = %+v, %v; want a proposal for review naming why", second, err)
	}
	again, err := iccharge.Handle(consumerCtx(t), s, cfg, published(t, v2))
	if err != nil || again.ID != second.ID || again.Status != "PROPOSAL" {
		t.Fatalf("redelivery of version 2 = %+v, %v", again, err)
	}
}
