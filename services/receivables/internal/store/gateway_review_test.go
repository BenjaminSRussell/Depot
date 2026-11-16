package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/gateway"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/gateway/stripe"
)

// Review M1: a refund delivered while the success of the same attempt is being handled is deferred (ErrGatewayBusy, the
// provider redelivers) and, redelivered, reverses the payment the success recorded.
func TestGatewayEvent_RefundDuringSuccessIsDeferredThenReverses(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-R001", "100.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodCard, "race-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("100.00")}), "pi_race")
	refund := event("evt_race_ref", gateway.KindRefunded, "pi_race", "100.00", "USD")
	var refundErr error
	// The refund webhook arrives while the success holds the attempt's lease, after its payment is recorded.
	s.afterGatewayPayment = func() {
		_, refundErr = s.HandleGatewayEvent(portalCtx(1), g.ID, refund, GatewayEventOptions{BankAccountRef: "STRIPE"})
	}
	if _, err := s.HandleGatewayEvent(portalCtx(1), g.ID, event("evt_race_ok", gateway.KindSucceeded, "pi_race", "100.00", "USD"), GatewayEventOptions{BankAccountRef: "STRIPE"}); err != nil {
		t.Fatal(err)
	}
	s.afterGatewayPayment = nil
	if !errors.Is(refundErr, ErrGatewayBusy) {
		t.Fatalf("refund during the success = %v, want ErrGatewayBusy so it is redelivered", refundErr)
	}
	if got := gw(t, s, g.ID); got.Status != GatewaySucceeded || len(got.Events) != 1 {
		t.Fatalf("after the success = %s with %d events", got.Status, len(got.Events))
	}
	if out := handle(t, s, g, refund, nil); out != "refunded: payment reversed" {
		t.Fatalf("redelivered refund = %q", out)
	}
	got := gw(t, s, g.ID)
	pay, _ := s.GetPayment(tenantCtx(1), deref(got.PaymentID))
	if got.Status != GatewayRefunded || pay.Status != "REVERSED" || itemByID(t, s, tenantCtx(1), a.ID).Status != "OPEN" {
		t.Fatalf("after the refund: attempt %s, payment %s", got.Status, pay.Status)
	}
}

// Review M1, crash variant: the success recorded and applied the payment and crashed before recording itself. A refund then
// finds the payment by its key and reverses it, and the success's retry changes nothing.
func TestGatewayEvent_SuccessCrashedBeforeRecordingHeals(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-R002", "100.00", "USD")
	b := seedGatewayItem(t, s, "INV-R003", "60.00", "USD")
	ok := event("evt_crash_ok", gateway.KindSucceeded, "pi_crash", "100.00", "USD")

	g := withIntent(t, s, attempt(t, s, gateway.MethodCard, "crash-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("100.00")}), "pi_crash")
	if _, _, err := s.settleGateway(portalCtx(1), gw(t, s, g.ID), ok, nil, "STRIPE"); err != nil {
		t.Fatal(err)
	}
	if got := gw(t, s, g.ID); got.PaymentID == nil || got.Status != GatewayRequiresPayment {
		t.Fatalf("the payment id must be stored before the apply: %+v", got)
	}
	// Even if it were lost, the refund finds the payment by its gateway:<attempt> key.
	execAdmin(t, s, `update gateway_payment set payment_id = null where id = $1`, g.ID)
	if out := handle(t, s, g, event("evt_crash_ref", gateway.KindRefunded, "pi_crash", "100.00", "USD"), nil); out != "refunded: payment reversed" {
		t.Fatalf("refund after the crash = %q", out)
	}
	if out := handle(t, s, g, ok, nil); out != "ignored: the payment is already refunded" {
		t.Fatalf("the success's retry = %q", out)
	}
	got := gw(t, s, g.ID)
	pay, _ := s.GetPayment(tenantCtx(1), deref(got.PaymentID))
	if got.Status != GatewayRefunded || pay.Status != "REVERSED" || itemByID(t, s, tenantCtx(1), a.ID).Status != "OPEN" {
		t.Fatalf("healed state: attempt %s, payment %s", got.Status, pay.Status)
	}

	// Without a refund, the success's retry completes the record of the payment it already applied.
	h := withIntent(t, s, attempt(t, s, gateway.MethodCard, "crash-2", GatewayAllocation{OpenItemID: b.ID, Amount: amt("60.00")}), "pi_crash2")
	ok2 := event("evt_crash2_ok", gateway.KindSucceeded, "pi_crash2", "60.00", "USD")
	if _, _, err := s.settleGateway(portalCtx(1), gw(t, s, h.ID), ok2, nil, "STRIPE"); err != nil {
		t.Fatal(err)
	}
	if out := handle(t, s, h, ok2, nil); out != "succeeded: payment recorded and applied" {
		t.Fatalf("retry = %q", out)
	}
	pays, _ := s.ListPayments(tenantCtx(1), PaymentFilter{}, 10)
	if len(pays) != 2 || gw(t, s, h.ID).Status != GatewaySucceeded || itemByID(t, s, tenantCtx(1), b.ID).Status != "SETTLED" {
		t.Fatalf("payments %d, attempt %s", len(pays), gw(t, s, h.ID).Status)
	}
}

// A refund handled before the success took the lease (while the success read the card details from Stripe) wins: the success
// then finds the attempt refunded and records nothing, so no payment exists for money that went back.
func TestGatewayEvent_RefundBeforeTheSuccessTakesTheLease(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-R012", "40.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodCard, "race-2", GatewayAllocation{OpenItemID: a.ID, Amount: amt("40.00")}), "pi_race2")
	opts := GatewayEventOptions{BankAccountRef: "STRIPE", Details: func(context.Context) (gateway.Details, error) {
		if _, err := s.HandleGatewayEvent(portalCtx(1), g.ID, event("evt_race2_ref", gateway.KindRefunded, "pi_race2", "40.00", "USD"), GatewayEventOptions{}); err != nil {
			t.Errorf("refund: %v", err)
		}
		return gateway.Details{Method: gateway.MethodCard}, nil
	}}
	out, err := s.HandleGatewayEvent(portalCtx(1), g.ID, event("evt_race2_ok", gateway.KindSucceeded, "pi_race2", "40.00", "USD"), opts)
	if err != nil || out != "ignored: the payment is already refunded" {
		t.Fatalf("success after the refund = %q %v", out, err)
	}
	pays, _ := s.ListPayments(tenantCtx(1), PaymentFilter{}, 10)
	if len(pays) != 0 || itemByID(t, s, tenantCtx(1), a.ID).Status != "OPEN" || gw(t, s, g.ID).Status != GatewayRefunded {
		t.Fatalf("payments %d, attempt %s", len(pays), gw(t, s, g.ID).Status)
	}
}

// The lease is a row lease, not a session lock: one held by another handling defers the event, an expired one (a crashed
// handler) is taken over, and a handling releases its own lease only.
func TestGatewayLease_HeldDefersExpiredIsTakenOver(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-R013", "30.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodCard, "lease-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("30.00")}), "pi_lease")
	ok := event("evt_lease_ok", gateway.KindSucceeded, "pi_lease", "30.00", "USD")
	// A handler that crashed a moment ago holds the lease.
	execAdmin(t, s, `update gateway_payment set handling_until = now() + interval '2 minutes', handling_token = gen_random_uuid() where id = $1`, g.ID)
	if _, err := s.HandleGatewayEvent(portalCtx(1), g.ID, ok, GatewayEventOptions{}); !errors.Is(err, ErrGatewayBusy) {
		t.Fatalf("event while the lease is held = %v", err)
	}
	// Its lease expires and the redelivery takes it over.
	execAdmin(t, s, `update gateway_payment set handling_until = now() - interval '1 second' where id = $1`, g.ID)
	if out := handle(t, s, g, ok, nil); out != "succeeded: payment recorded and applied" {
		t.Fatalf("after expiry = %q", out)
	}
	var held int
	admin, _ := adminPools.Load(s.pool)
	if err := admin.(interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}).QueryRow(context.Background(), `select count(*) from gateway_payment where id = $1 and (handling_until is not null or handling_token is not null)`, g.ID).Scan(&held); err != nil || held != 0 {
		t.Fatalf("lease left after the handling: %d %v", held, err)
	}
	// A handling taken over releases nothing of the new holder's lease.
	tok, err := s.acquireGatewayLease(portalCtx(1), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.releaseGatewayLease(portalCtx(1), g.ID, "00000000-0000-4000-8000-000000000999")
	if _, err := s.acquireGatewayLease(portalCtx(1), g.ID); !errors.Is(err, ErrGatewayBusy) {
		t.Fatalf("another token released the lease: %v", err)
	}
	s.releaseGatewayLease(portalCtx(1), g.ID, tok)
	if _, err := s.acquireGatewayLease(portalCtx(1), g.ID); err != nil {
		t.Fatalf("own release: %v", err)
	}
}

// Review N2 (probe): handlings hold no connection while they wait for another; two concurrent deliveries on a two-connection
// pool both complete.
func TestGatewayEvent_NoConnectionHeldAcrossTheHandling(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-R014", "10.00", "USD")
	b := seedGatewayItem(t, s, "INV-R015", "10.00", "USD")
	g1 := withIntent(t, s, attempt(t, s, gateway.MethodCard, "hw-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("10.00")}), "pi_hw_1")
	g2 := withIntent(t, s, attempt(t, s, gateway.MethodCard, "hw-2", GatewayAllocation{OpenItemID: b.ID, Amount: amt("10.00")}), "pi_hw_2")
	cfg := s.pool.Config()
	cfg.MaxConns = 2
	small, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	ss := New(small)
	ctx, cancel := context.WithTimeout(portalCtx(1), 20*time.Second)
	defer cancel()
	both := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	n := 0
	gate := func(context.Context) (gateway.Details, error) {
		mu.Lock()
		n++
		if n == 2 {
			once.Do(func() { close(both) })
		}
		mu.Unlock()
		select {
		case <-both:
		case <-time.After(2 * time.Second):
		}
		return gateway.Details{}, nil
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, g := range []GatewayPayment{g1, g2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = ss.HandleGatewayEvent(ctx, g.ID, event("evt_hw_"+g.ID, gateway.KindSucceeded, []string{"pi_hw_1", "pi_hw_2"}[i], "10.00", "USD"), GatewayEventOptions{BankAccountRef: "STRIPE", Details: gate})
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("handlings on a 2-connection pool: %v", errs)
	}
	if gw(t, s, g1.ID).Status != GatewaySucceeded || gw(t, s, g2.ID).Status != GatewaySucceeded {
		t.Fatal("both attempts must have succeeded")
	}
}

// Concurrent deliveries of one success: exactly one payment, the item settled once; the others are deferred or duplicates.
func TestGatewayEvent_ConcurrentDuplicateSuccessIsExactlyOnce(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-R004", "100.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodCard, "dup-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("100.00")}), "pi_dup")
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.HandleGatewayEvent(portalCtx(1), g.ID, event("evt_dup", gateway.KindSucceeded, "pi_dup", "100.00", "USD"), GatewayEventOptions{BankAccountRef: "STRIPE"})
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil && !errors.Is(err, ErrGatewayBusy) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	pays, _ := s.ListPayments(tenantCtx(1), PaymentFilter{}, 10)
	if len(pays) > 1 || !itemByID(t, s, tenantCtx(1), a.ID).OpenAmount.Equal(amt("0")) && len(pays) == 1 {
		t.Fatalf("payments %d, item open %s", len(pays), itemByID(t, s, tenantCtx(1), a.ID).OpenAmount)
	}
	handle(t, s, g, event("evt_dup", gateway.KindSucceeded, "pi_dup", "100.00", "USD"), nil)
	pays, _ = s.ListPayments(tenantCtx(1), PaymentFilter{}, 10)
	if len(pays) != 1 || !itemByID(t, s, tenantCtx(1), a.ID).OpenAmount.IsZero() || gw(t, s, g.ID).Status != GatewaySucceeded {
		t.Fatalf("after the redelivery: payments %d", len(pays))
	}
}

// The final write is a compare-and-set: an attempt that moved since it was read is not overwritten.
func TestGatewayEvent_FinalWriteIsCompareAndSet(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-R005", "10.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodCard, "cas-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("10.00")}), "pi_cas")
	stale := gw(t, s, g.ID)
	execAdmin(t, s, `update gateway_payment set version = version + 1 where id = $1`, g.ID)
	err := s.recordGatewayEvent(portalCtx(1), stale, event("evt_cas", gateway.KindFailed, "pi_cas", "10.00", "USD"), "failed", func(u *gatewayUpdate) { u.status = GatewayFailed })
	if !errors.Is(err, ErrGatewayBusy) {
		t.Fatalf("stale write = %v", err)
	}
	if got := gw(t, s, g.ID); got.Status != GatewayRequiresPayment || len(got.Events) != 0 {
		t.Fatalf("the stale write changed the attempt: %s, %d events", got.Status, len(got.Events))
	}
}

// Review M2: an inquiry withdraws nothing and reverses nothing; reinstated funds are flagged; withdrawn funds reverse.
func TestGatewayEvent_InquiryIsFlaggedAndOnlyWithdrawnFundsReverse(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-R006", "50.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodCard, "inq-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("50.00")}), "pi_inq")
	handle(t, s, g, event("evt_inq_ok", gateway.KindSucceeded, "pi_inq", "50.00", "USD"), nil)
	inq := event("evt_inq", gateway.KindInquiry, "pi_inq", "50.00", "USD")
	inq.DisputeStatus = "warning_needs_response"
	handle(t, s, g, inq, nil)
	got := gw(t, s, g.ID)
	pay, _ := s.GetPayment(tenantCtx(1), deref(got.PaymentID))
	if got.Status != GatewaySucceeded || !got.NeedsReview || pay.Status != "APPLIED" || itemByID(t, s, tenantCtx(1), a.ID).Status != "SETTLED" {
		t.Fatalf("after an inquiry: attempt %s review %v, payment %s", got.Status, got.NeedsReview, pay.Status)
	}
	withdrawn := event("evt_inq_wd", gateway.KindDisputed, "pi_inq", "50.00", "USD")
	handle(t, s, g, withdrawn, nil)
	pay, _ = s.GetPayment(tenantCtx(1), deref(got.PaymentID))
	if gw(t, s, g.ID).Status != GatewayDisputed || pay.Status != "REVERSED" {
		t.Fatalf("after funds withdrawn: %s, %s", gw(t, s, g.ID).Status, pay.Status)
	}
	back := event("evt_inq_back", gateway.KindReinstated, "pi_inq", "50.00", "USD")
	handle(t, s, g, back, nil)
	if got := gw(t, s, g.ID); got.Status != GatewayDisputed || !got.NeedsReview || deref(got.ReviewReason) == "" {
		t.Fatalf("after reinstatement: %+v", got)
	}
}

// Review L1: a failure older than the processing event that moved the attempt is ignored, and the ACH payment keeps its claim.
func TestGatewayEvent_StaleFailureAfterProcessingIsIgnored(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-R007", "100.00", "USD")
	g := withIntent(t, s, attempt(t, s, gateway.MethodACH, "order-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("100.00")}), "pi_order")
	handle(t, s, g, event("evt_order_proc", gateway.KindProcessing, "pi_order", "100.00", "USD"), nil)
	failed := event("evt_order_fail", gateway.KindFailed, "pi_order", "100.00", "USD")
	failed.Created = eventTime.Add(-time.Hour)
	failed.FailureCode = "card_declined"
	if out := handle(t, s, g, failed, nil); out != "ignored: older than the last event applied" {
		t.Fatalf("stale failure = %q", out)
	}
	if got := gw(t, s, g.ID); got.Status != GatewayProcessing {
		t.Fatalf("status = %s", got.Status)
	}
	if _, _, err := s.CreateGatewayAttempt(portalCtx(1), GatewayAttemptInput{LegalEntityID: legalEntity, CustomerID: gwCustomer, Provider: "STRIPE", Method: gateway.MethodCard,
		Allocations: []GatewayAllocation{{OpenItemID: a.ID, Amount: amt("100.00")}}}, "order-2"); problemCode(err) != CodeGatewayOverpay {
		t.Fatalf("a second full payment while ACH processes: %v", err)
	}
	// A newer failure still applies.
	newer := event("evt_order_fail2", gateway.KindFailed, "pi_order", "100.00", "USD")
	newer.Created = eventTime.Add(time.Hour)
	newer.FailureCode = "insufficient_funds"
	handle(t, s, g, newer, nil)
	if got := gw(t, s, g.ID); got.Status != GatewayFailed {
		t.Fatalf("newer failure: %s", got.Status)
	}
}

// Review L2: an attempt the provider was asked for reserves what it pays until it fails, is cancelled or its window passes.
func TestGatewayAttempt_LiveCardAttemptReservesUntilCancelledOrExpired(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-R008", "100.00", "USD")
	in := func(amount string) GatewayAttemptInput {
		return GatewayAttemptInput{LegalEntityID: legalEntity, CustomerID: gwCustomer, Provider: "STRIPE", Method: gateway.MethodCard,
			Allocations: []GatewayAllocation{{OpenItemID: a.ID, Amount: amt(amount)}}}
	}
	first := withIntent(t, s, attempt(t, s, gateway.MethodCard, "live-1", GatewayAllocation{OpenItemID: a.ID, Amount: amt("100.00")}), "pi_live")
	if _, _, err := s.CreateGatewayAttempt(portalCtx(1), in("1.00"), "live-2"); problemCode(err) != CodeGatewayOverpay {
		t.Fatalf("second payment while the first waits: %v", err)
	}
	if _, err := s.CancelGatewayAttempt(portalCtx(1), first.ID, "00000000-0000-0000-0000-0000000000c2", nil); problemCode(err) != CodeGatewayNotFound {
		t.Fatalf("another customer cancels: %v", err)
	}
	cancelled := ""
	got, err := s.CancelGatewayAttempt(portalCtx(1), first.ID, gwCustomer, func(g GatewayPayment) error { cancelled = deref(g.ProviderIntentID); return nil })
	if err != nil || cancelled != "pi_live" || got.Status != GatewayFailed || deref(got.FailureCode) != "canceled" {
		t.Fatalf("cancel = %+v %v (provider asked for %q)", got, err, cancelled)
	}
	second := withIntent(t, s, attempt(t, s, gateway.MethodCard, "live-3", GatewayAllocation{OpenItemID: a.ID, Amount: amt("100.00")}), "pi_live2")
	if _, _, err := s.CreateGatewayAttempt(portalCtx(1), in("1.00"), "live-4"); problemCode(err) != CodeGatewayOverpay {
		t.Fatalf("while the second waits: %v", err)
	}
	execAdmin(t, s, `update gateway_payment set created_at = now() - interval '31 minutes' where id = $1`, second.ID)
	if _, _, err := s.CreateGatewayAttempt(portalCtx(1), in("100.00"), "live-5"); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	// A processing payment cannot be cancelled.
	b := seedGatewayItem(t, s, "INV-R009", "20.00", "USD")
	ach := withIntent(t, s, attempt(t, s, gateway.MethodACH, "live-6", GatewayAllocation{OpenItemID: b.ID, Amount: amt("20.00")}), "pi_live3")
	handle(t, s, ach, event("evt_live_proc", gateway.KindProcessing, "pi_live3", "20.00", "USD"), nil)
	if _, err := s.CancelGatewayAttempt(portalCtx(1), ach.ID, gwCustomer, nil); problemCode(err) != CodeGatewayState {
		t.Fatalf("cancel while processing: %v", err)
	}
	// Stripe's own cancellation event fails a waiting attempt.
	c := seedGatewayItem(t, s, "INV-R010", "20.00", "USD")
	w := withIntent(t, s, attempt(t, s, gateway.MethodCard, "live-7", GatewayAllocation{OpenItemID: c.ID, Amount: amt("20.00")}), "pi_live4")
	cev := event("evt_live_cancel", gateway.KindCanceled, "pi_live4", "20.00", "USD")
	cev.FailureCode = "canceled"
	if out := handle(t, s, w, cev, nil); out != "cancelled" || gw(t, s, w.ID).Status != GatewayFailed {
		t.Fatalf("cancel event = %q, %s", out, gw(t, s, w.ID).Status)
	}
}

// Review L3: an amount the provider cannot charge (KWD by card in steps of 0.010) is refused when the attempt is created.
func TestGatewayAttempt_ProviderGranularity(t *testing.T) {
	s := setup(t)
	a := seedGatewayItem(t, s, "INV-R011", "12.345", "KWD")
	check := func(amount decimal.Decimal, currency string) error {
		_, err := stripe.ToMinor(amount, currency)
		return err
	}
	in := func(amount string) GatewayAttemptInput {
		return GatewayAttemptInput{LegalEntityID: legalEntity, CustomerID: gwCustomer, Provider: "STRIPE", Method: gateway.MethodCard,
			Allocations: []GatewayAllocation{{OpenItemID: a.ID, Amount: amt(amount)}}, CheckAmount: check}
	}
	if _, _, err := s.CreateGatewayAttempt(portalCtx(1), in("12.345"), "kwd-1"); problemCode(err) != CodeGatewayAmount {
		t.Fatalf("12.345 KWD: %v", err)
	}
	if _, _, err := s.CreateGatewayAttempt(portalCtx(1), in("12.340"), "kwd-2"); err != nil {
		t.Fatalf("12.340 KWD: %v", err)
	}
}
