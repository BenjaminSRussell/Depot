package stripe_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/gateway"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/gateway/stripe"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/gateway/stripe/stripetest"
)

const (
	secretKey = "sk_test_fake"
	whsec     = "whsec_test_one"
)

func newGateway(t *testing.T, base string, secrets ...string) *stripe.Gateway {
	t.Helper()
	if len(secrets) == 0 {
		secrets = []string{whsec}
	}
	g, err := stripe.New(stripe.Config{SecretKey: secretKey, PublishableKey: "pk_test_fake", WebhookSecrets: secrets, BaseURL: base})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestNew_RefusesBadKeys(t *testing.T) {
	for name, c := range map[string]stripe.Config{
		"publishable as secret": {SecretKey: "pk_test_x", WebhookSecrets: []string{whsec}},
		"no webhook secret":     {SecretKey: secretKey},
		"bad publishable":       {SecretKey: secretKey, PublishableKey: "sk_test_x", WebhookSecrets: []string{whsec}},
	} {
		if _, err := stripe.New(c); err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), "sk_test") {
			t.Errorf("%s: the error echoes a key: %v", name, err)
		}
	}
}

func TestCreateIntent_SendsAmountCurrencyMetadataAndIdempotencyKey(t *testing.T) {
	fake := stripetest.New()
	defer fake.Close()
	g := newGateway(t, fake.URL)
	req := gateway.IntentRequest{AttemptID: "a1b2", Amount: decimal.RequireFromString("1250.50"), Currency: "USD", Method: gateway.MethodCard,
		Account: "acct_123", Description: "Payment of INV-1", Metadata: map[string]string{"passage_attempt_id": "a1b2", "passage_documents": "INV-1"}}
	intent, err := g.CreateIntent(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if intent.ID == "" || !strings.HasPrefix(intent.ClientSecret, intent.ID+"_secret_") {
		t.Fatalf("intent = %+v", intent)
	}
	again, err := g.CreateIntent(context.Background(), req)
	if err != nil || again.ID != intent.ID {
		t.Fatalf("a retried create made %s (%v), want the same PaymentIntent %s", again.ID, err, intent.ID)
	}
	reqs := fake.Requests()
	r := reqs[0]
	if r.Header.Get("Idempotency-Key") != "passage-pi-a1b2" || r.Header.Get("Stripe-Account") != "acct_123" ||
		r.Header.Get("Stripe-Version") != stripe.APIVersion || r.Header.Get("Authorization") != "Bearer "+secretKey {
		t.Fatalf("headers = %v", r.Header)
	}
	if r.Form.Get("amount") != "125050" || r.Form.Get("currency") != "usd" || r.Form.Get("payment_method_types[]") != "card" ||
		r.Form.Get("metadata[passage_attempt_id]") != "a1b2" || r.Form.Get("metadata[passage_documents]") != "INV-1" {
		t.Fatalf("form = %v", r.Form)
	}
	if reqs[1].Header.Get("Idempotency-Key") != "passage-pi-a1b2" {
		t.Fatal("the retry did not reuse the idempotency key")
	}
}

func TestCreateIntent_ACHUsesFinancialConnections(t *testing.T) {
	fake := stripetest.New()
	defer fake.Close()
	g := newGateway(t, fake.URL)
	if _, err := g.CreateIntent(context.Background(), gateway.IntentRequest{AttemptID: "x", Amount: decimal.RequireFromString("10"), Currency: "USD", Method: gateway.MethodACH}); err != nil {
		t.Fatal(err)
	}
	f := fake.Requests()[0].Form
	if f.Get("payment_method_types[]") != "us_bank_account" || f.Get("payment_method_options[us_bank_account][verification_method]") != "automatic" ||
		f.Get("payment_method_options[us_bank_account][financial_connections][permissions][]") != "payment_method" {
		t.Fatalf("form = %v", f)
	}
	if fake.Requests()[0].Header.Get("Stripe-Account") != "" {
		t.Fatal("a platform payment sent a Stripe-Account header")
	}
}

func TestCreateIntent_RefusalAndOutage(t *testing.T) {
	fake := stripetest.New()
	defer fake.Close()
	g := newGateway(t, fake.URL)
	fake.FailStatus, fake.FailCode = http.StatusBadRequest, "amount_too_small"
	_, err := g.CreateIntent(context.Background(), gateway.IntentRequest{AttemptID: "x", Amount: decimal.RequireFromString("0.10"), Currency: "USD", Method: gateway.MethodCard})
	var ge *gateway.Error
	if !errors.As(err, &ge) || !ge.Refused() || ge.Code != "amount_too_small" {
		t.Fatalf("refusal = %v", err)
	}
	fake.FailStatus, fake.FailCode = http.StatusInternalServerError, "api_error"
	_, err = g.CreateIntent(context.Background(), gateway.IntentRequest{AttemptID: "y", Amount: decimal.RequireFromString("10"), Currency: "USD", Method: gateway.MethodCard})
	if !errors.As(err, &ge) || ge.Refused() {
		t.Fatalf("a 500 must be retryable: %v", err)
	}
	down := newGateway(t, "http://127.0.0.1:1")
	_, err = down.CreateIntent(context.Background(), gateway.IntentRequest{AttemptID: "z", Amount: decimal.RequireFromString("10"), Currency: "USD", Method: gateway.MethodCard})
	if !errors.As(err, &ge) || ge.Refused() || strings.Contains(err.Error(), secretKey) {
		t.Fatalf("unreachable = %v", err)
	}
}

func TestMinorUnits(t *testing.T) {
	cases := []struct {
		amount, currency string
		minor            int64
		ok               bool
	}{
		{"1250.50", "USD", 125050, true},
		{"1250.505", "USD", 0, false},
		{"5000", "JPY", 5000, true},
		{"5000.5", "JPY", 0, false},
		{"1.230", "KWD", 1230, true},
		{"1.234", "KWD", 0, false},
		{"150", "ISK", 15000, true},
		{"0", "USD", 0, false},
		{"-1", "USD", 0, false},
	}
	for _, c := range cases {
		got, err := stripe.ToMinor(decimal.RequireFromString(c.amount), c.currency)
		if (err == nil) != c.ok || got != c.minor {
			t.Errorf("ToMinor(%s %s) = %d, %v; want %d ok=%v", c.amount, c.currency, got, err, c.minor, c.ok)
		}
		if c.ok && !stripe.FromMinor(got, c.currency).Equal(decimal.RequireFromString(c.amount)) {
			t.Errorf("FromMinor(%d %s) does not round-trip", got, c.currency)
		}
	}
}

func TestParseWebhook_VerifiesSignatureAndAge(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	g := newGateway(t, "http://unused", "whsec_old", whsec)
	payload := stripetest.Event("evt_1", "payment_intent.succeeded", now, stripetest.Intent("pi_1", 125050, "usd", map[string]string{"passage_attempt_id": "a"}))

	ev, err := g.ParseWebhook(payload, stripetest.Signed(payload, whsec, now), now.Add(30*time.Second))
	if err != nil {
		t.Fatalf("valid: %v", err)
	}
	if ev.ID != "evt_1" || ev.Kind != gateway.KindSucceeded || ev.IntentID != "pi_1" || !ev.Amount.Equal(decimal.RequireFromString("1250.50")) || ev.Currency != "USD" {
		t.Fatalf("event = %+v", ev)
	}
	if _, err := g.ParseWebhook(payload, stripetest.Signed(payload, "whsec_other", now), now); !errors.Is(err, gateway.ErrSignature) {
		t.Fatalf("bad secret: %v", err)
	}
	tampered := []byte(strings.Replace(string(payload), "125050", "999999", 1))
	if _, err := g.ParseWebhook(tampered, stripetest.Signed(payload, whsec, now), now); !errors.Is(err, gateway.ErrSignature) {
		t.Fatalf("tampered body: %v", err)
	}
	if _, err := g.ParseWebhook(payload, stripetest.Signed(payload, whsec, now), now.Add(6*time.Minute)); !errors.Is(err, gateway.ErrStale) {
		t.Fatalf("stale: %v", err)
	}
	if _, err := g.ParseWebhook(payload, stripetest.Signed(payload, whsec, now.Add(6*time.Minute)), now); !errors.Is(err, gateway.ErrStale) {
		t.Fatalf("future: %v", err)
	}
	if _, err := g.ParseWebhook(payload, http.Header{}, now); !errors.Is(err, gateway.ErrSignature) {
		t.Fatalf("no header: %v", err)
	}
	if _, err := g.ParseWebhook(payload, stripetest.Signed(payload, "whsec_old", now), now); err != nil {
		t.Fatalf("rolled secret: %v", err)
	}
}

func TestParseEvent_Kinds(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	failed := stripetest.Intent("pi_2", 1000, "usd", nil)
	failed["last_payment_error"] = map[string]any{"code": "card_declined", "decline_code": "insufficient_funds", "message": "Your card has insufficient funds.",
		"payment_method": stripetest.PaymentMethod("card")}
	ev, err := stripe.ParseEvent(stripetest.Event("evt_2", "payment_intent.payment_failed", now, failed))
	if err != nil || ev.Kind != gateway.KindFailed || ev.FailureCode != "insufficient_funds" || ev.Details == nil || ev.Details.Last4 != "4242" || ev.Details.Brand != "visa" {
		t.Fatalf("failed = %+v (%v)", ev, err)
	}
	ev, err = stripe.ParseEvent(stripetest.Event("evt_3", "payment_intent.processing", now, stripetest.Intent("pi_3", 1000, "usd", nil)))
	if err != nil || ev.Kind != gateway.KindProcessing {
		t.Fatalf("processing = %+v (%v)", ev, err)
	}
	ev, err = stripe.ParseEvent(stripetest.Event("evt_4", "charge.refunded", now, map[string]any{"id": "ch_1", "object": "charge", "amount": 1000, "amount_refunded": 400, "currency": "usd", "payment_intent": "pi_3"}))
	if err != nil || ev.Kind != gateway.KindRefunded || ev.IntentID != "pi_3" || !ev.Amount.Equal(decimal.RequireFromString("4")) || !ev.Total.Equal(decimal.RequireFromString("10")) {
		t.Fatalf("refunded = %+v (%v)", ev, err)
	}
	ev, err = stripe.ParseEvent(stripetest.Event("evt_5", "charge.dispute.created", now, map[string]any{"id": "dp_1", "object": "dispute", "amount": 1000, "currency": "usd", "payment_intent": "pi_3", "reason": "debit_not_authorized"}))
	if err != nil || ev.Kind != gateway.KindDisputed || ev.Reason != "debit_not_authorized" {
		t.Fatalf("dispute = %+v (%v)", ev, err)
	}
	ev, err = stripe.ParseEvent(stripetest.Event("evt_6", "customer.created", now, map[string]any{"id": "cus_1"}))
	if err != nil || ev.Kind != gateway.KindIgnored {
		t.Fatalf("other = %+v (%v)", ev, err)
	}
	if _, err := stripe.ParseEvent([]byte(`{"id":"x"}`)); !errors.Is(err, gateway.ErrMalformed) {
		t.Fatalf("malformed: %v", err)
	}
}

func TestPaymentDetails_KeepsOnlyBrandLast4ExpiryAndBank(t *testing.T) {
	fake := stripetest.New()
	defer fake.Close()
	g := newGateway(t, fake.URL)
	d, err := g.PaymentDetails(context.Background(), "", "pi_9")
	if err != nil || d.Method != gateway.MethodCard || d.Brand != "visa" || d.Last4 != "4242" || d.ExpMonth != 12 || d.ExpYear != 2030 {
		t.Fatalf("card = %+v (%v)", d, err)
	}
	if fake.Requests()[0].Form.Get("expand[]") != "payment_method" {
		t.Fatalf("query = %v", fake.Requests()[0].Form)
	}
	fake.Method = "us_bank_account"
	d, err = g.PaymentDetails(context.Background(), "acct_1", "pi_9")
	if err != nil || d.Method != gateway.MethodACH || d.BankName != "STRIPE TEST BANK" || d.Last4 != "6789" {
		t.Fatalf("bank = %+v (%v)", d, err)
	}
}

func TestPlainFailure(t *testing.T) {
	if gateway.PlainFailure("card_declined") == "" || gateway.PlainFailure("something_new") == "" || gateway.PlainFailure("") != "" {
		t.Fatal("plain failure messages")
	}
}
