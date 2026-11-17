package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/billing/internal/domain"
	"github.com/BenjaminSRussell/depot/services/billing/migrations"
)

func TestBillingRule_PutVersionsAndSelfBilledWindow(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	ctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	le, customer := ids.New(), ids.New()
	if r, found, err := s.GetBillingRule(ctx, le, customer); err != nil || found || r.Rule != RuleStandard {
		t.Fatalf("no rule: %+v %v %v", r, found, err)
	}
	from, to := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 12, 31, 0, 0, 0, 0, time.UTC)
	got, err := s.PutBillingRule(ctx, BillingRule{LegalEntityID: le, CustomerID: customer, Rule: RuleSelfBilled, AgreementRef: "SBA-1", ValidFrom: &from, ValidTo: &to, UpdatedBy: "clerk"}, 0)
	if err != nil || got.Version != 1 {
		t.Fatalf("put: %+v %v", got, err)
	}
	if _, err := s.PutBillingRule(ctx, BillingRule{LegalEntityID: le, CustomerID: customer, Rule: RuleStandard, UpdatedBy: "clerk"}, 0); !errors.Is(err, ErrRuleVersion) {
		t.Fatalf("a second create is a version conflict: %v", err)
	}
	r, found, err := s.GetBillingRule(ctx, le, customer)
	if err != nil || !found || !r.SelfBilledOn(time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)) || r.SelfBilledOn(time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		r.SelfBilledOn(time.Date(2027, 2, 28, 0, 0, 0, 0, time.UTC)) || !r.SelfBilledOn(to) {
		t.Fatalf("window: %+v %v %v", r, found, err)
	}
	if got, err := s.PutBillingRule(ctx, BillingRule{LegalEntityID: le, CustomerID: customer, Rule: RuleStandard, UpdatedBy: "clerk"}, 1); err != nil || got.Version != 2 || got.Rule != RuleStandard {
		t.Fatalf("update: %+v %v", got, err)
	}
	// SELF_BILLED without the agreement is refused by the table too.
	if _, err := s.PutBillingRule(ctx, BillingRule{LegalEntityID: le, CustomerID: ids.New(), Rule: RuleSelfBilled, UpdatedBy: "clerk"}, 0); err == nil {
		t.Fatal("SELF_BILLED without agreement stored")
	}
}

func selfBilledInvoice(le, customer, key, hash string, qty string) Invoice {
	day := time.Date(2027, 3, 20, 0, 0, 0, 0, time.UTC)
	net, tax := decimal.RequireFromString("80.00"), decimal.RequireFromString("15.20")
	return Invoice{ID: ids.New(), LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, Type: domain.TypeShipment, Status: domain.StatusProposal,
		Currency: "USD", InvoiceDate: day, PostingDate: day, DueDate: day.AddDate(0, 0, 30), PaymentTermDays: 30,
		Totals: domain.Totals{Net: net, Charges: decimal.Zero, Tax: tax, Gross: net.Add(tax)}, IdempotencyKey: key, RequestHash: hash,
		Lines: []Line{{Line: domain.Line{No: 1, Description: "Pump housing", Qty: decimal.RequireFromString(qty), UnitPrice: decimal.NewFromInt(8), DiscountPct: decimal.Zero, RevenueAccount: "4000", TaxCode: "VAT19"},
			Net: net, Tax: tax}}}
}

// #35 (35-5): the matched self-billed document is posted as the legal invoice under the document's own number, once.
func TestPostSelfBilled_PostsUnderTheDocumentNumberOnceAndWritesTheEvent(t *testing.T) {
	admin, pool := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	ctx := ctxAllEntities(testkit.TenantID(1))
	s := New(pool)
	le, customer := ids.New(), ids.New()
	first, created, err := s.PostSelfBilled(ctx, selfBilledInvoice(le, customer, SelfBilledKeyPrefix+"msg-1", "h1", "10"), "SB-aaaa-SB-2027-0001", "SB-2027-0001")
	if err != nil || !created || first.Number != "SB-aaaa-SB-2027-0001" || first.Status != domain.StatusPosted || first.Version != 2 {
		t.Fatalf("post: %+v %v %v", first, created, err)
	}
	got, err := s.GetInvoice(ctx, first.ID)
	if err != nil || got.Number != "SB-aaaa-SB-2027-0001" || got.Status != domain.StatusPosted || got.FiscalYear != 2027 || got.Totals.Gross.String() != "95.2" || len(got.Lines) != 1 {
		t.Fatalf("stored invoice: %+v %v", got, err)
	}
	// The same document again returns the posted invoice, creates nothing and draws no number.
	again, created, err := s.PostSelfBilled(ctx, selfBilledInvoice(le, customer, SelfBilledKeyPrefix+"msg-1", "h1", "10"), "SB-aaaa-SB-2027-0001", "SB-2027-0001")
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay: %+v %v %v", again, created, err)
	}
	if _, _, err := s.PostSelfBilled(ctx, selfBilledInvoice(le, customer, SelfBilledKeyPrefix+"msg-1", "other", "10"), "SB-aaaa-SB-2027-0001", "SB-2027-0001"); !errors.Is(err, ErrSelfBilledKeyReuse) {
		t.Fatalf("other content under the same document: %v", err)
	}
	var events int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where aggregate_id = $1`, first.ID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("invoice.posted events: %d %v", events, err)
	}
	// A second document with the same number in the legal entity is refused by the number's uniqueness.
	if _, _, err := s.PostSelfBilled(ctx, selfBilledInvoice(le, customer, SelfBilledKeyPrefix+"msg-2", "h2", "10"), "SB-aaaa-SB-2027-0001", "SB-2027-0001"); !errors.Is(err, ErrSelfBilledNumberTaken) {
		t.Fatalf("a second document of the customer with the same number: %v", err)
	}
	// Another customer's document may carry the same number: its invoice number is in its own namespace.
	other := ids.New()
	if got, created, err := s.PostSelfBilled(ctx, selfBilledInvoice(le, other, SelfBilledKeyPrefix+"msg-3", "h3", "10"), "SB-bbbb-SB-2027-0001", "SB-2027-0001"); err != nil || !created || got.CustomerDocumentNumber != "SB-2027-0001" {
		t.Fatalf("another customer, the same number: %+v %v %v", got, created, err)
	}
	// Billing's own numbering is untouched: a proposal posted afterwards still draws INV-<year>-0000001.
	var counters int
	if err := admin.QueryRow(context.Background(), `select count(*) from number_sequence_counter`).Scan(&counters); err != nil || counters != 0 {
		t.Fatalf("sequence counters drawn: %d %v", counters, err)
	}
}
