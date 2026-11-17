package store

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/gateway"
)

const gwCustomer = "00000000-0000-0000-0000-0000000000c1"

// portalCtx is the receivables side of a customer portal call: the tenant, one legal entity and the portal's customer.
func portalCtx(n int) context.Context {
	return GatewayContext(context.Background(), GatewayRoute{TenantID: testkit.TenantID(n), LegalEntityID: legalEntity})
}

func seedGatewayItem(t *testing.T, s *Postgres, number, amount, currency string) OpenItem {
	t.Helper()
	item, err := s.CreateOpenItem(tenantCtx(1), OpenItem{LegalEntityID: legalEntity, CustomerID: gwCustomer, SourceType: "INVOICE", DocumentNumber: number,
		DocumentDate: "2027-03-01", DueDate: "2027-03-31", Currency: currency, Amount: amt(amount), OpenAmount: amt(amount)})
	if err != nil {
		t.Fatalf("create open item: %v", err)
	}
	return item
}

func attempt(t *testing.T, s *Postgres, method gateway.Method, key string, allocs ...GatewayAllocation) GatewayPayment {
	t.Helper()
	g, _, err := s.CreateGatewayAttempt(portalCtx(1), GatewayAttemptInput{LegalEntityID: legalEntity, CustomerID: gwCustomer, Provider: "STRIPE", Method: method, Allocations: allocs}, key)
	if err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	return g
}

func withIntent(t *testing.T, s *Postgres, g GatewayPayment, intent string) GatewayPayment {
	t.Helper()
	if err := s.SetGatewayIntent(portalCtx(1), g.ID, intent); err != nil {
		t.Fatalf("set intent: %v", err)
	}
	r, err := s.ResolveGatewayIntent(context.Background(), "STRIPE", intent)
	if err != nil || r.GatewayPaymentID != g.ID || r.TenantID != testkit.TenantID(1) || r.LegalEntityID != legalEntity {
		t.Fatalf("route = %+v (%v)", r, err)
	}
	return g
}

var eventTime = time.Date(2027, 3, 20, 15, 0, 0, 0, time.UTC)

func event(id string, kind gateway.Kind, intent, amount, currency string) gateway.Event {
	return gateway.Event{ID: id, Type: "test." + string(kind), Kind: kind, Created: eventTime, IntentID: intent, Amount: amt(amount), Total: amt(amount), Currency: currency}
}

func handle(t *testing.T, s *Postgres, g GatewayPayment, ev gateway.Event, details *gateway.Details) string {
	t.Helper()
	opts := GatewayEventOptions{BankAccountRef: "STRIPE"}
	if details != nil {
		opts.Details = func(context.Context) (gateway.Details, error) { return *details, nil }
	}
	out, err := s.HandleGatewayEvent(portalCtx(1), g.ID, ev, opts)
	if err != nil {
		t.Fatalf("handle %s: %v", ev.ID, err)
	}
	return out
}

func gw(t *testing.T, s *Postgres, id string) GatewayPayment {
	t.Helper()
	g, err := s.GetGatewayPayment(portalCtx(1), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	return g
}

func TestGatewayAttempt_ValidatesItemsAndAmounts(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-9001", "1000.00", "USD")
	b := seedGatewayItem(t, s, "INV-9002", "250.00", "USD")
	eur := seedGatewayItem(t, s, "INV-9003", "100.00", "EUR")
	other, err := s.CreateOpenItem(tenantCtx(1), OpenItem{LegalEntityID: legalEntity, CustomerID: "00000000-0000-0000-0000-0000000000c2", SourceType: "INVOICE",
		DocumentNumber: "INV-9004", DocumentDate: "2027-03-01", DueDate: "2027-03-31", Currency: "USD", Amount: amt("50.00"), OpenAmount: amt("50.00")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := portalCtx(1)
	in := func(method gateway.Method, allocs ...GatewayAllocation) GatewayAttemptInput {
		return GatewayAttemptInput{LegalEntityID: legalEntity, CustomerID: gwCustomer, Provider: "STRIPE", Method: method, Allocations: allocs}
	}
	cases := map[string]struct {
		in   GatewayAttemptInput
		code string
	}{
		"overpayment":      {in(gateway.MethodCard, GatewayAllocation{OpenItemID: a.ID, Amount: amt("1000.01")}), CodeGatewayOverpay},
		"another customer": {in(gateway.MethodCard, GatewayAllocation{OpenItemID: other.ID, Amount: amt("10")}), CodeOpenItemNotFound},
		"two currencies":   {in(gateway.MethodCard, GatewayAllocation{OpenItemID: a.ID, Amount: amt("10")}, GatewayAllocation{OpenItemID: eur.ID, Amount: amt("10")}), CodeGatewayItem},
		"ACH in euros":     {in(gateway.MethodACH, GatewayAllocation{OpenItemID: eur.ID, Amount: amt("10")}), CodeGatewayMethod},
		"fraction of cent": {in(gateway.MethodCard, GatewayAllocation{OpenItemID: a.ID, Amount: amt("10.005")}), "COM-0001"},
		"zero":             {in(gateway.MethodCard, GatewayAllocation{OpenItemID: a.ID, Amount: decimal.Zero}), "COM-0001"},
		"same item twice":  {in(gateway.MethodCard, GatewayAllocation{OpenItemID: a.ID, Amount: amt("1")}, GatewayAllocation{OpenItemID: a.ID, Amount: amt("1")}), "COM-0001"},
		"nothing to pay":   {in(gateway.MethodCard), "COM-0001"},
		"other legal entity": {GatewayAttemptInput{LegalEntityID: "00000000-0000-0000-0000-0000000000b2", CustomerID: gwCustomer, Provider: "STRIPE", Method: gateway.MethodCard,
			Allocations: []GatewayAllocation{{OpenItemID: a.ID, Amount: amt("1")}}}, CodeOpenItemNotFound},
	}
	for name, c := range cases {
		if _, _, err := s.CreateGatewayAttempt(ctx, c.in, "key-"+name); problemCode(err) != c.code {
			t.Errorf("%s: got %v, want %s", name, err, c.code)
		}
	}

	// A partial payment of two items, replayed by its key, and the key refused for another payment.
	ok := in(gateway.MethodCard, GatewayAllocation{OpenItemID: a.ID, Amount: amt("400.00")}, GatewayAllocation{OpenItemID: b.ID, Amount: amt("250.00")})
	first, replay, err := s.CreateGatewayAttempt(ctx, ok, "pay-key-1")
	if err != nil || replay || first.Status != GatewayRequiresPayment || !first.Amount.Equal(amt("650.00")) || first.Currency != "USD" || len(first.Allocations) != 2 {
		t.Fatalf("attempt = %+v (%v)", first, err)
	}
	again, replay, err := s.CreateGatewayAttempt(ctx, ok, "pay-key-1")
	if err != nil || !replay || again.ID != first.ID {
		t.Fatalf("replay = %+v %v (%v)", again.ID, replay, err)
	}
	changed := in(gateway.MethodCard, GatewayAllocation{OpenItemID: a.ID, Amount: amt("401.00")})
	if _, _, err := s.CreateGatewayAttempt(ctx, changed, "pay-key-1"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
}

func TestGatewayAttempt_ProcessingACHClaimsTheItem(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-9101", "300.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodACH, "ach-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("200.00")}), "pi_ach_claim")
	handle(t, s, g, event("evt_p", gateway.KindProcessing, "pi_ach_claim", "200.00", "USD"), nil)
	if _, _, err := s.CreateGatewayAttempt(portalCtx(1), GatewayAttemptInput{LegalEntityID: legalEntity, CustomerID: gwCustomer, Provider: "STRIPE",
		Method: gateway.MethodCard, Allocations: []GatewayAllocation{{OpenItemID: a.ID, Amount: amt("100.01")}}}, "card-2"); problemCode(err) != CodeGatewayOverpay {
		t.Fatalf("paying more than the processing ACH payment leaves: got %v, want %s", err, CodeGatewayOverpay)
	}
	attempt(t, s, gateway.MethodCard, "card-3", GatewayAllocation{OpenItemID: a.ID, Amount: amt("100.00")})
}

func TestGatewayEvent_CardSuccessRecordsAndAppliesThePayment(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-9201", "1000.00", "USD")
	b := seedGatewayItem(t, s, "INV-9202", "250.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodCard, "card-ok", GatewayAllocation{OpenItemID: a.ID, Amount: amt("1000.00")}, GatewayAllocation{OpenItemID: b.ID, Amount: amt("100.00")}), "pi_card_ok")

	details := gateway.Details{Method: gateway.MethodCard, Brand: "visa", Last4: "4242", ExpMonth: 12, ExpYear: 2030}
	if out := handle(t, s, g, event("evt_ok", gateway.KindSucceeded, "pi_card_ok", "1100.00", "USD"), &details); out != "succeeded: payment recorded and applied" {
		t.Fatalf("outcome = %q", out)
	}
	got := gw(t, s, g.ID)
	if got.Status != GatewaySucceeded || got.PaymentID == nil || deref(got.CardBrand) != "visa" || deref(got.Last4) != "4242" || got.CardExpYear == nil || *got.CardExpYear != 2030 || got.NeedsReview {
		t.Fatalf("attempt = %+v", got)
	}
	pay, err := s.GetPayment(tenantCtx(1), *got.PaymentID)
	if err != nil || pay.Status != "APPLIED" || pay.Source != "PORTAL" || pay.PaymentMode != "CARD" || deref(pay.SourceRef) != "pi_card_ok" ||
		deref(pay.BankAccountRef) != "STRIPE" || !pay.Amount.Equal(amt("1100.00")) || pay.ReceivedDate != "2027-03-20" {
		t.Fatalf("payment = %+v (%v)", pay, err)
	}
	if it := itemByID(t, s, tenantCtx(1), a.ID); it.Status != "SETTLED" {
		t.Fatalf("INV-9201 = %s", it.Status)
	}
	if it := itemByID(t, s, tenantCtx(1), b.ID); it.Status != "PARTIALLY_SETTLED" || !it.OpenAmount.Equal(amt("150.00")) {
		t.Fatalf("INV-9202 = %s %s, want partly settled with 150.00 open", it.Status, it.OpenAmount)
	}

	// The same event again (a redelivery or a replayed request) changes nothing.
	if out := handle(t, s, g, event("evt_ok", gateway.KindSucceeded, "pi_card_ok", "1100.00", "USD"), nil); out != "duplicate" {
		t.Fatalf("replayed event = %q", out)
	}
	// A late processing event after success is recorded and ignored.
	if out := handle(t, s, g, event("evt_late", gateway.KindProcessing, "pi_card_ok", "1100.00", "USD"), nil); out != "ignored: the payment is already succeeded" {
		t.Fatalf("late processing = %q", out)
	}
	if n := len(gw(t, s, g.ID).Events); n != 2 {
		t.Fatalf("events = %d, want 2", n)
	}
	pays, err := s.ListPayments(tenantCtx(1), PaymentFilter{}, 10)
	if err != nil || len(pays) != 1 {
		t.Fatalf("payments = %d (%v), want one", len(pays), err)
	}
}

func TestGatewayEvent_ACHProcessingThenSucceededThenReturned(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-9301", "500.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodACH, "ach-ok", GatewayAllocation{OpenItemID: a.ID, Amount: amt("500.00")}), "pi_ach_ok")

	bank := gateway.Details{Method: gateway.MethodACH, BankName: "STRIPE TEST BANK", Last4: "6789"}
	ev := event("evt_proc", gateway.KindProcessing, "pi_ach_ok", "500.00", "USD")
	ev.Details = &bank
	handle(t, s, g, ev, nil)
	if got := gw(t, s, g.ID); got.Status != GatewayProcessing || got.PaymentID != nil || deref(got.BankName) != "STRIPE TEST BANK" {
		t.Fatalf("processing = %+v", got)
	}
	if it := itemByID(t, s, tenantCtx(1), a.ID); it.Status != "OPEN" {
		t.Fatalf("a processing ACH payment settled the item: %s", it.Status)
	}
	handle(t, s, g, event("evt_succ", gateway.KindSucceeded, "pi_ach_ok", "500.00", "USD"), &bank)
	got := gw(t, s, g.ID)
	pay, _ := s.GetPayment(tenantCtx(1), *got.PaymentID)
	if got.Status != GatewaySucceeded || pay.PaymentMode != "ACH" || pay.Status != "APPLIED" {
		t.Fatalf("succeeded = %+v, payment %+v", got, pay)
	}

	// The bank returns the debit later: Stripe reports it as a dispute, and the payment is reversed.
	ret := event("evt_return", gateway.KindDisputed, "pi_ach_ok", "500.00", "USD")
	ret.Reason = "debit_not_authorized"
	if out := handle(t, s, g, ret, nil); out != "disputed (debit_not_authorized): payment reversed" {
		t.Fatalf("return = %q", out)
	}
	if got := gw(t, s, g.ID); got.Status != GatewayDisputed || got.NeedsReview {
		t.Fatalf("after return = %+v", got)
	}
	pay, _ = s.GetPayment(tenantCtx(1), *got.PaymentID)
	if pay.Status != "REVERSED" {
		t.Fatalf("payment after return = %s", pay.Status)
	}
	if it := itemByID(t, s, tenantCtx(1), a.ID); it.Status != "OPEN" || !it.OpenAmount.Equal(amt("500.00")) {
		t.Fatalf("item after return = %s %s, want open again", it.Status, it.OpenAmount)
	}
}

func TestGatewayEvent_FullRefundReversesAndPartialIsFlagged(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-9401", "80.00", "USD")
	b := seedGatewayItem(t, s, "INV-9402", "90.00", "USD")
	full := withIntent(t, s, attempt(t, s, gateway.MethodCard, "ref-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("80.00")}), "pi_ref_full")
	part := withIntent(t, s, attempt(t, s, gateway.MethodCard, "ref-2", GatewayAllocation{OpenItemID: b.ID, Amount: amt("90.00")}), "pi_ref_part")
	handle(t, s, full, event("evt_f1", gateway.KindSucceeded, "pi_ref_full", "80.00", "USD"), nil)
	handle(t, s, part, event("evt_p1", gateway.KindSucceeded, "pi_ref_part", "90.00", "USD"), nil)

	if out := handle(t, s, full, event("evt_f2", gateway.KindRefunded, "pi_ref_full", "80.00", "USD"), nil); out != "refunded: payment reversed" {
		t.Fatalf("full refund = %q", out)
	}
	if it := itemByID(t, s, tenantCtx(1), a.ID); it.Status != "OPEN" {
		t.Fatalf("item after refund = %s", it.Status)
	}
	refund := event("evt_p2", gateway.KindRefunded, "pi_ref_part", "30.00", "USD")
	refund.Total = amt("90.00")
	handle(t, s, part, refund, nil)
	got := gw(t, s, part.ID)
	if got.Status != GatewaySucceeded || !got.NeedsReview || !got.RefundedAmount.Equal(amt("30.00")) {
		t.Fatalf("partial refund = %+v", got)
	}
	if it := itemByID(t, s, tenantCtx(1), b.ID); it.Status != "SETTLED" {
		t.Fatalf("a partial refund changed the item: %s", it.Status)
	}
}

func TestGatewayEvent_FailureAndRetry(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-9501", "40.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodCard, "fail-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("40.00")}), "pi_fail")
	ev := event("evt_fail", gateway.KindFailed, "pi_fail", "40.00", "USD")
	ev.FailureCode, ev.FailureMessage = "insufficient_funds", "Your card has insufficient funds."
	handle(t, s, g, ev, nil)
	got := gw(t, s, g.ID)
	if got.Status != GatewayFailed || deref(got.FailureCode) != "insufficient_funds" || deref(got.FailureMessage) != gateway.PlainFailure("insufficient_funds") || got.PaymentID != nil {
		t.Fatalf("failed = %+v", got)
	}
	// The customer tries again with another card on the same PaymentIntent.
	handle(t, s, g, event("evt_retry", gateway.KindSucceeded, "pi_fail", "40.00", "USD"), nil)
	if got := gw(t, s, g.ID); got.Status != GatewaySucceeded || got.FailureCode != nil {
		t.Fatalf("after retry = %+v", got)
	}
}

func TestGatewayEvent_ItemPaidMeanwhileGoesOnAccount(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-9601", "100.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodCard, "late-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("100.00")}), "pi_late")
	cust := gwCustomer
	other := newPayment(t, s, tenantCtx(1), &cust, "60.00", "USD", "check-1")
	if _, err := s.ApplyPayment(tenantCtx(1), other.ID, other.Version, ApplyRequest{Allocations: []domain.Allocation{{OpenItemID: a.ID, Amount: amt("60.00")}}}, ""); err != nil {
		t.Fatal(err)
	}
	handle(t, s, g, event("evt_late_ok", gateway.KindSucceeded, "pi_late", "100.00", "USD"), nil)
	got := gw(t, s, g.ID)
	pay, _ := s.GetPayment(tenantCtx(1), *got.PaymentID)
	if !got.NeedsReview || pay.Status != "ON_ACCOUNT" || !pay.UnappliedAmount.Equal(amt("60.00")) {
		t.Fatalf("attempt %+v, payment %+v: want 40.00 applied and 60.00 on account, flagged", got, pay)
	}
}

func TestGatewayEvent_OtherAccountIsIgnored(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-9701", "10.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodCard, "acct-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("10.00")}), "pi_acct")
	ev := event("evt_acct", gateway.KindSucceeded, "pi_acct", "10.00", "USD")
	ev.Account = "acct_someone_else"
	handle(t, s, g, ev, nil)
	if got := gw(t, s, g.ID); got.Status != GatewayRequiresPayment {
		t.Fatalf("an event of another account moved the payment: %+v", got)
	}
}

func TestGatewayRLS_OtherTenantAndEntitySeeNothing(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-9801", "10.00", "USD")
	g := attempt(t, s, gateway.MethodCard, "rls-g1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("10.00")})
	if _, err := s.GetGatewayPayment(portalCtx(2), g.ID); problemCode(err) != CodeGatewayNotFound {
		t.Fatalf("other tenant reads it: %v", err)
	}
	if rows, err := s.ListGatewayPayments(tenantCtx(2), GatewayFilter{}, 10); err != nil || len(rows) != 0 {
		t.Fatalf("other tenant lists %d (%v)", len(rows), err)
	}
	otherEntity := GatewayContext(context.Background(), GatewayRoute{TenantID: testkit.TenantID(1), LegalEntityID: "00000000-0000-0000-0000-0000000000b2"})
	if rows, err := s.ListGatewayPayments(otherEntity, GatewayFilter{}, 10); err != nil || len(rows) != 0 {
		t.Fatalf("other legal entity lists %d (%v)", len(rows), err)
	}
	if _, _, err := s.CreateGatewayAttempt(portalCtx(2), GatewayAttemptInput{LegalEntityID: legalEntity, CustomerID: gwCustomer, Provider: "STRIPE", Method: gateway.MethodCard,
		Allocations: []GatewayAllocation{{OpenItemID: a.ID, Amount: amt("1")}}}, "rls-g2"); problemCode(err) != CodeOpenItemNotFound {
		t.Fatalf("other tenant pays the item: %v", err)
	}
	if _, err := s.ResolveGatewayIntent(context.Background(), "STRIPE", "pi_nobody"); err != ErrGatewayRouteNotFound {
		t.Fatalf("unknown intent: %v", err)
	}
}
