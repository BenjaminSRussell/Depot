package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/gateway/stripe"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/gateway/stripe/stripetest"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
	"github.com/BenjaminSRussell/passage/services/receivables/migrations"
)

const (
	gwWhsec   = "whsec_test_gateway"
	gwTenant  = "00000000-0000-4000-8000-0000000000f1"
	gwLE      = "00000000-0000-4000-8000-0000000000f2"
	gwCustA   = "00000000-0000-4000-8000-0000000000fa"
	gwCustB   = "00000000-0000-4000-8000-0000000000fb"
	gwSecretK = "sk_test_gateway"
)

// gatewayHarness is receivables on a real database with the Stripe adapter pointed at a fake Stripe, called as the customer
// portal (signed service tokens), as staff (headers) and as Stripe (signed webhooks on the webhook listener).
type gatewayHarness struct {
	api     http.Handler
	webhook http.Handler
	signer  *tenant.ServiceSigner
	fake    *stripetest.Server
	store   *store.Postgres
	now     time.Time
}

func newGatewayHarness(t *testing.T, configure bool) *gatewayHarness {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(app)
	fake := stripetest.New()
	t.Cleanup(fake.Close)
	h := &gatewayHarness{signer: nil, fake: fake, store: st, now: time.Now().UTC().Truncate(time.Second)}
	a := New(st)
	if configure {
		gw, err := stripe.New(stripe.Config{SecretKey: gwSecretK, PublishableKey: "pk_test_gateway", WebhookSecrets: []string{gwWhsec}, BaseURL: fake.URL})
		if err != nil {
			t.Fatal(err)
		}
		a.WithGateway(GatewayConfig{Gateway: gw, BankAccountRef: "STRIPE", Now: func() time.Time { return h.now }})
	}
	r := chi.NewRouter()
	r.Use(tenant.Middleware)
	a.Register(r)
	h.api, h.webhook = r, a.WebhookRoutes()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if h.signer, err = tenant.NewServiceSigner(tenant.CustomerPortalIssuer, priv); err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("receivables", map[string]ed25519.PublicKey{tenant.CustomerPortalIssuer: pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	return h
}

func (h *gatewayHarness) item(t *testing.T, customer, number, amount string) store.OpenItem {
	t.Helper()
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: gwTenant, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	it, err := h.store.CreateOpenItem(ctx, store.OpenItem{LegalEntityID: gwLE, CustomerID: customer, SourceType: "INVOICE", DocumentNumber: number,
		DocumentDate: "2027-03-01", DueDate: "2027-03-31", Currency: "USD", Amount: decimal.RequireFromString(amount), OpenAmount: decimal.RequireFromString(amount)})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func (h *gatewayHarness) portal(t *testing.T, customer, method, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	token, err := h.signer.Mint("receivables", tenant.Principal{TenantID: gwTenant, LegalEntityID: gwLE, UserID: "contact-1", CustomerID: customer, Duties: map[string]struct{}{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Service-Token", token)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	h.api.ServeHTTP(rec, req)
	return rec
}

func (h *gatewayHarness) staff(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Passage-Tenant", gwTenant)
	req.Header.Set("X-Passage-Duties", "receivables.view")
	req.Header.Set("X-Passage-Legal-Entity", gwLE)
	rec := httptest.NewRecorder()
	h.api.ServeHTTP(rec, req)
	return rec
}

func (h *gatewayHarness) deliver(payload []byte, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/v1/stripe", bytes.NewReader(payload))
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.webhook.ServeHTTP(rec, req)
	return rec
}

func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return out
}

func codeOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	return decodeBody[struct {
		Code string `json:"code"`
	}](t, rec).Code
}

type intentAnswer struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Amount         string `json:"amount"`
	ClientSecret   string `json:"clientSecret"`
	PublishableKey string `json:"publishableKey"`
	CardBrand      string `json:"cardBrand"`
	Last4          string `json:"last4"`
	PaymentID      string `json:"paymentId"`
	NeedsReview    bool   `json:"needsReview"`
}

func TestGatewayAPI_CardPaymentFromPortalToApplication(t *testing.T) {
	h := newGatewayHarness(t, true)
	a := h.item(t, gwCustA, "INV-7001", "1000.00")
	b := h.item(t, gwCustA, "INV-7002", "500.00")
	other := h.item(t, gwCustB, "INV-7003", "80.00")
	const path = "/v1/receivables/customer-portal/payment-intents"
	body := `{"method":"CARD","allocations":[{"openItemId":"` + a.ID + `","amount":"1000.00"},{"openItemId":"` + b.ID + `","amount":"250.50"}]}`

	// Overpayment, another customer's invoice and a missing key are refused before Stripe is called.
	over := `{"method":"CARD","allocations":[{"openItemId":"` + b.ID + `","amount":"500.01"}]}`
	if rec := h.portal(t, gwCustA, http.MethodPost, path, over, "key-over-1"); rec.Code != http.StatusUnprocessableEntity || codeOf(t, rec) != "AR-0019" {
		t.Fatalf("overpayment: %d %s", rec.Code, rec.Body)
	}
	theirs := `{"method":"CARD","allocations":[{"openItemId":"` + other.ID + `","amount":"10.00"}]}`
	if rec := h.portal(t, gwCustA, http.MethodPost, path, theirs, "key-theirs-1"); rec.Code != http.StatusNotFound {
		t.Fatalf("another customer's item: %d %s", rec.Code, rec.Body)
	}
	if rec := h.portal(t, gwCustA, http.MethodPost, path, body, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", rec.Code)
	}
	if n := len(h.fake.Requests()); n != 0 {
		t.Fatalf("Stripe was called %d times for refused payments", n)
	}

	rec := h.portal(t, gwCustA, http.MethodPost, path, body, "key-pay-0001")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decodeBody[intentAnswer](t, rec)
	if created.Status != "REQUIRES_PAYMENT" || created.ClientSecret == "" || created.PublishableKey != "pk_test_gateway" || created.Amount != "1250.5" {
		t.Fatalf("created = %+v", created)
	}
	stripeReq := h.fake.Requests()[0]
	if stripeReq.Header.Get("Idempotency-Key") != stripe.IdempotencyKey(created.ID) || stripeReq.Form.Get("amount") != "125050" ||
		stripeReq.Form.Get("currency") != "usd" || stripeReq.Form.Get("metadata[passage_attempt_id]") != created.ID ||
		stripeReq.Form.Get("metadata[passage_documents]") != "INV-7001,INV-7002" {
		t.Fatalf("Stripe got %v %v", stripeReq.Header, stripeReq.Form)
	}
	replay := decodeBody[intentAnswer](t, h.portal(t, gwCustA, http.MethodPost, path, body, "key-pay-0001"))
	if replay.ID != created.ID || replay.ClientSecret != created.ClientSecret {
		t.Fatalf("replay = %+v, want the same payment and client secret", replay)
	}
	intentID := strings.SplitN(created.ClientSecret, "_secret_", 2)[0]

	// Another customer cannot read it; the contact can.
	if rec := h.portal(t, gwCustB, http.MethodGet, path+"/"+created.ID, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("other customer reads the payment: %d", rec.Code)
	}
	if rec := h.portal(t, gwCustA, http.MethodGet, path+"/"+created.ID, "", ""); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "clientSecret") {
		t.Fatalf("own read: %d %s", rec.Code, rec.Body)
	}

	// Webhooks: a bad signature and a stale one are refused; the browser's word counts for nothing.
	payload := stripetest.Event("evt_api_ok", "payment_intent.succeeded", h.now, stripetest.Intent(intentID, 125050, "usd", map[string]string{"passage_attempt_id": created.ID}))
	if rec := h.deliver(payload, stripetest.Signed(payload, "whsec_wrong", h.now)); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad signature: %d", rec.Code)
	}
	if rec := h.deliver(payload, stripetest.Signed(payload, gwWhsec, h.now.Add(-6*time.Minute))); rec.Code != http.StatusBadRequest {
		t.Fatalf("stale: %d", rec.Code)
	}
	if got := decodeBody[intentAnswer](t, h.staff("/v1/receivables/gateway-payments/"+created.ID)); got.Status != "REQUIRES_PAYMENT" {
		t.Fatalf("a refused webhook moved the payment: %+v", got)
	}
	rec = h.deliver(payload, stripetest.Signed(payload, gwWhsec, h.now))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "succeeded") {
		t.Fatalf("succeeded: %d %s", rec.Code, rec.Body)
	}
	// The same event replayed with a fresh signature is a no-op.
	if rec := h.deliver(payload, stripetest.Signed(payload, gwWhsec, h.now)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "duplicate") {
		t.Fatalf("replayed: %d %s", rec.Code, rec.Body)
	}
	got := decodeBody[intentAnswer](t, h.staff("/v1/receivables/gateway-payments/"+created.ID))
	if got.Status != "SUCCEEDED" || got.CardBrand != "visa" || got.Last4 != "4242" || got.PaymentID == "" || got.NeedsReview {
		t.Fatalf("after webhook = %+v", got)
	}
	pay := decodeBody[map[string]any](t, h.staff("/v1/receivables/payments/"+got.PaymentID))
	if pay["status"] != "APPLIED" || pay["source"] != "PORTAL" || pay["paymentMode"] != "CARD" {
		t.Fatalf("payment = %v", pay)
	}
	list := decodeBody[struct{ Items []intentAnswer }](t, h.staff("/v1/receivables/gateway-payments?payment="+got.PaymentID))
	if len(list.Items) != 1 || list.Items[0].ID != created.ID {
		t.Fatalf("list by payment = %+v", list)
	}
	item := decodeBody[map[string]any](t, h.portal(t, gwCustA, http.MethodGet, "/v1/receivables/customer-portal/open-items/"+b.ID, "", ""))
	if item["openAmount"] != "249.5" {
		t.Fatalf("INV-7002 after a partial payment = %v", item)
	}

	// An event for a PaymentIntent Passage did not create is acknowledged and ignored.
	stranger := stripetest.Event("evt_stranger", "payment_intent.succeeded", h.now, stripetest.Intent("pi_not_ours", 100, "usd", nil))
	if rec := h.deliver(stranger, stripetest.Signed(stranger, gwWhsec, h.now)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ignored") {
		t.Fatalf("stranger: %d %s", rec.Code, rec.Body)
	}
}

func TestGatewayAPI_RefusalsAndCallers(t *testing.T) {
	h := newGatewayHarness(t, true)
	a := h.item(t, gwCustA, "INV-7101", "0.40")
	const path = "/v1/receivables/customer-portal/payment-intents"
	body := `{"method":"CARD","allocations":[{"openItemId":"` + a.ID + `","amount":"0.40"}]}`

	h.fake.FailStatus, h.fake.FailCode = http.StatusBadRequest, "amount_too_small"
	rec := h.portal(t, gwCustA, http.MethodPost, path, body, "key-small-01")
	if rec.Code != http.StatusUnprocessableEntity || codeOf(t, rec) != "AR-GATEWAY-REFUSED" {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	list := decodeBody[struct{ Items []intentAnswer }](t, h.portal(t, gwCustA, http.MethodGet, path, "", ""))
	if len(list.Items) != 1 || list.Items[0].Status != "FAILED" {
		t.Fatalf("after refusal = %+v", list)
	}
	if rec := h.portal(t, gwCustA, http.MethodPost, path, `{"method":"CASH","allocations":[]}`, "key-cash-01"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad method: %d", rec.Code)
	}

	// Staff holding the duty still cannot use the portal route: it is the portal's, for a customer.
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", gwTenant)
	req.Header.Set("X-Passage-Duties", "receivables.customer-portal.pay")
	req.Header.Set("X-Passage-Legal-Entity", gwLE)
	req.Header.Set("Idempotency-Key", "key-staff-01")
	staff := httptest.NewRecorder()
	h.api.ServeHTTP(staff, req)
	if staff.Code != http.StatusForbidden {
		t.Fatalf("staff on the portal route: %d %s", staff.Code, staff.Body)
	}
}

func TestGatewayAPI_OffWithoutAProvider(t *testing.T) {
	h := newGatewayHarness(t, false)
	a := h.item(t, gwCustA, "INV-7201", "10.00")
	rec := h.portal(t, gwCustA, http.MethodPost, "/v1/receivables/customer-portal/payment-intents", `{"method":"CARD","allocations":[{"openItemId":"`+a.ID+`","amount":"10.00"}]}`, "key-off-001")
	if rec.Code != http.StatusServiceUnavailable || codeOf(t, rec) != "AR-GATEWAY-OFF" {
		t.Fatalf("no provider: %d %s", rec.Code, rec.Body)
	}
	payload := stripetest.Event("evt_off", "payment_intent.succeeded", h.now, stripetest.Intent("pi_x", 100, "usd", nil))
	if rec := h.deliver(payload, stripetest.Signed(payload, gwWhsec, h.now)); rec.Code != http.StatusNotFound {
		t.Fatalf("webhook without a provider: %d", rec.Code)
	}
}

func TestGatewayAPI_CancelGranularityAndForeignCharges(t *testing.T) {
	h := newGatewayHarness(t, true)
	a := h.item(t, gwCustA, "INV-7301", "100.00")
	const path = "/v1/receivables/customer-portal/payment-intents"
	body := `{"method":"CARD","allocations":[{"openItemId":"` + a.ID + `","amount":"100.00"}]}`
	created := decodeBody[intentAnswer](t, h.portal(t, gwCustA, http.MethodPost, path, body, "key-cancel-01"))
	// While it waits for the customer, the invoice cannot be paid again.
	if rec := h.portal(t, gwCustA, http.MethodPost, path, body, "key-cancel-02"); rec.Code != http.StatusUnprocessableEntity || codeOf(t, rec) != "AR-0019" {
		t.Fatalf("second payment while the first waits: %d %s", rec.Code, rec.Body)
	}
	if rec := h.portal(t, gwCustB, http.MethodPost, path+"/"+created.ID+":cancel", "{}", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("another customer cancels: %d %s", rec.Code, rec.Body)
	}
	rec := h.portal(t, gwCustA, http.MethodPost, path+"/"+created.ID+":cancel", "{}", "")
	if rec.Code != http.StatusOK || decodeBody[intentAnswer](t, rec).Status != "FAILED" {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	last := h.fake.Requests()[len(h.fake.Requests())-1]
	if !strings.HasSuffix(last.Path, "/cancel") || last.Header.Get("Idempotency-Key") != "passage-pi-cancel-"+created.ID {
		t.Fatalf("Stripe cancel = %s %v", last.Path, last.Header)
	}
	if rec := h.portal(t, gwCustA, http.MethodPost, path, body, "key-cancel-03"); rec.Code != http.StatusCreated {
		t.Fatalf("after the cancel the invoice can be paid: %d %s", rec.Code, rec.Body)
	}

	// An amount Stripe cannot take is refused before Stripe is called, with a plain reason.
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: gwTenant, Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	kwd, err := h.store.CreateOpenItem(ctx, store.OpenItem{LegalEntityID: gwLE, CustomerID: gwCustA, SourceType: "INVOICE", DocumentNumber: "INV-7302",
		DocumentDate: "2027-03-01", DueDate: "2027-03-31", Currency: "KWD", Amount: decimal.RequireFromString("12.345"), OpenAmount: decimal.RequireFromString("12.345")})
	if err != nil {
		t.Fatal(err)
	}
	calls := len(h.fake.Requests())
	rec = h.portal(t, gwCustA, http.MethodPost, path, `{"method":"CARD","allocations":[{"openItemId":"`+kwd.ID+`","amount":"12.345"}]}`, "key-kwd-0001")
	if rec.Code != http.StatusUnprocessableEntity || codeOf(t, rec) != "AR-GATEWAY-AMOUNT" || !strings.Contains(rec.Body.String(), "steps of 0.010") || len(h.fake.Requests()) != calls {
		t.Fatalf("KWD 12.345: %d %s", rec.Code, rec.Body)
	}

	// A verified refund of a charge without a PaymentIntent is acknowledged, not refused (Stripe would retry and disable the endpoint).
	foreign := []byte(`{"id":"evt_foreign","object":"event","type":"charge.refunded","created":` + strconv.FormatInt(h.now.Unix(), 10) + `,"data":{"object":{"id":"ch_1","amount":500,"amount_refunded":500,"currency":"usd","payment_intent":null}}}`)
	if rec := h.deliver(foreign, stripetest.Signed(foreign, gwWhsec, h.now)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ignored") {
		t.Fatalf("charge without a PaymentIntent: %d %s", rec.Code, rec.Body)
	}
}
