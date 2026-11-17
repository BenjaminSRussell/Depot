// Package stripe is the Stripe adapter of the portal payment gateway (#30). It talks to Stripe's REST API over plain HTTPS with
// form-encoded bodies (no SDK): it creates PaymentIntents (card, or us_bank_account with Financial Connections verification),
// reads a PaymentIntent's payment method, and verifies and reads webhook events (Stripe-Signature, HMAC-SHA256, 5 minute
// tolerance).
//
// The secret key and the webhook signing secrets are held by the Gateway and sent only in the Authorization header or used for
// the HMAC; they are never logged and never part of an error.
package stripe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/money"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/gateway"
)

const (
	// DefaultBaseURL is Stripe's API.
	DefaultBaseURL = "https://api.stripe.com"
	// APIVersion pins the Stripe API version the requests and the webhook payloads are read with. The webhook endpoint
	// registered in the Stripe dashboard must use the same version (#213).
	APIVersion = "2024-06-20"
	// Tolerance is how old (or how far in the future) a webhook's signed timestamp may be.
	Tolerance = 5 * time.Minute
	// maxResponse caps a Stripe API response read.
	maxResponse = 1 << 20
)

// Config holds the adapter's settings. SecretKey and WebhookSecrets are secrets.
type Config struct {
	SecretKey      string
	PublishableKey string
	// WebhookSecrets are the signing secrets (whsec_...) of the registered endpoints: the platform endpoint and, with Connect,
	// the Connect endpoint; two of one endpoint while a secret is rolled.
	WebhookSecrets []string
	// BaseURL overrides DefaultBaseURL (tests point it at a fake Stripe).
	BaseURL string
	Client  *http.Client
}

// Gateway is the Stripe adapter.
type Gateway struct {
	secretKey      string
	publishableKey string
	webhookSecrets [][]byte
	base           string
	client         *http.Client
}

// New checks the configuration and returns the adapter.
func New(c Config) (*Gateway, error) {
	if !strings.HasPrefix(c.SecretKey, "sk_") && !strings.HasPrefix(c.SecretKey, "rk_") {
		return nil, errors.New("stripe: the secret key must be a Stripe secret or restricted key (sk_ or rk_)")
	}
	if !strings.HasPrefix(c.PublishableKey, "pk_") {
		// Without it the portal could create PaymentIntents but never show the payment form.
		return nil, errors.New("stripe: the publishable key (pk_...) is required")
	}
	g := &Gateway{secretKey: c.SecretKey, publishableKey: c.PublishableKey, base: strings.TrimRight(c.BaseURL, "/"), client: c.Client}
	for _, s := range c.WebhookSecrets {
		if s = strings.TrimSpace(s); s != "" {
			g.webhookSecrets = append(g.webhookSecrets, []byte(s))
		}
	}
	if len(g.webhookSecrets) == 0 {
		return nil, errors.New("stripe: at least one webhook signing secret is required")
	}
	if g.base == "" {
		g.base = DefaultBaseURL
	}
	if g.client == nil {
		g.client = &http.Client{Timeout: 15 * time.Second}
	}
	return g, nil
}

// Provider is STRIPE.
func (g *Gateway) Provider() string { return "STRIPE" }

// PublishableKey is the key Stripe.js is initialised with.
func (g *Gateway) PublishableKey() string { return g.publishableKey }

// zeroDecimal are the currencies Stripe takes in whole units.
var zeroDecimal = map[string]bool{
	"BIF": true, "CLP": true, "DJF": true, "GNF": true, "JPY": true, "KMF": true, "KRW": true, "MGA": true, "PYG": true,
	"RWF": true, "VND": true, "VUV": true, "XAF": true, "XOF": true, "XPF": true,
}

// twoDecimalWhole are ISO zero-decimal currencies Stripe still takes with two decimals (amounts in whole units, times 100).
var twoDecimalWhole = map[string]bool{"ISK": true, "UGX": true}

// exponent is the number of decimals in Stripe's integer amount for a currency.
func exponent(currency string) int32 {
	switch {
	case zeroDecimal[currency]:
		return 0
	case twoDecimalWhole[currency]:
		return 2
	}
	return money.MinorUnits(currency)
}

// ToMinor converts an amount to Stripe's integer amount, refusing more decimals than the currency has (no rounding: a
// fraction of a cent is never charged or dropped). Stripe's three-decimal currencies take only multiples of ten.
func ToMinor(amount decimal.Decimal, currency string) (int64, error) {
	iso := money.MinorUnits(currency)
	if zeroDecimal[currency] {
		iso = 0
	}
	if !amount.IsPositive() {
		return 0, errors.New("the amount must be above zero")
	}
	if !amount.Equal(amount.Truncate(iso)) {
		if iso == 0 {
			return 0, fmt.Errorf("%s payments are in whole units, without decimals", currency)
		}
		return 0, fmt.Errorf("%s payments have at most %d decimals", currency, iso)
	}
	minor := amount.Shift(exponent(currency))
	if !minor.Equal(minor.Truncate(0)) {
		return 0, fmt.Errorf("%s payments have at most %d decimals", currency, iso)
	}
	if exponent(currency) == 3 && minor.Mod(decimal.NewFromInt(10)).Sign() != 0 {
		return 0, fmt.Errorf("%s card and bank payments must be in steps of 0.010", currency)
	}
	return minor.IntPart(), nil
}

// FromMinor converts Stripe's integer amount back to a decimal amount.
func FromMinor(minor int64, currency string) decimal.Decimal {
	return decimal.NewFromInt(minor).Shift(-exponent(currency))
}

type stripeError struct {
	Error struct {
		Type        string `json:"type"`
		Code        string `json:"code"`
		DeclineCode string `json:"decline_code"`
		Message     string `json:"message"`
	} `json:"error"`
}

// call sends one request to Stripe. Every create carries the Idempotency-Key; the Stripe-Account header names the connected
// account when there is one.
func (g *Gateway) call(ctx context.Context, method, path string, form url.Values, account, idempotencyKey string, out any) error {
	var body io.Reader
	target := g.base + path
	if method == http.MethodGet && len(form) > 0 {
		target += "?" + form.Encode()
	} else if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.secretKey)
	req.Header.Set("Stripe-Version", APIVersion)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if account != "" {
		req.Header.Set("Stripe-Account", account)
	}
	res, err := g.client.Do(req)
	if err != nil {
		// The error names the URL and the transport failure only; the key is in a header and never in it.
		return &gateway.Error{Status: 0, Code: "unreachable", Message: "Stripe could not be reached"}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return &gateway.Error{Status: 0, Code: "unreachable", Message: "Stripe's answer could not be read"}
	}
	if res.StatusCode >= 300 {
		var se stripeError
		_ = json.Unmarshal(raw, &se)
		code := se.Error.Code
		if se.Error.DeclineCode != "" {
			code = se.Error.DeclineCode
		}
		if code == "" {
			code = se.Error.Type
		}
		return &gateway.Error{Status: res.StatusCode, Code: code, Message: truncate(se.Error.Message, 300)}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &gateway.Error{Status: res.StatusCode, Code: "malformed", Message: "Stripe's answer was not the expected JSON"}
	}
	return nil
}

// CheckAmount tells whether Stripe can charge the amount in the currency, with a plain reason when it cannot.
func (g *Gateway) CheckAmount(amount decimal.Decimal, currency string) error {
	_, err := ToMinor(amount, strings.ToUpper(currency))
	return err
}

// CancelIntent cancels an abandoned PaymentIntent (POST /v1/payment_intents/{id}/cancel, Idempotency-Key per attempt). When Stripe
// refuses, the PaymentIntent is read: it counts as cancelled only when its status is canceled. Any other status (succeeded,
// processing, requires_capture: the customer may already have paid) is refused with Code payment_intent_unexpected_state, so
// the attempt is left for its webhook. Stripe's refusal text is never relied on.
func (g *Gateway) CancelIntent(ctx context.Context, account, intentID, attemptID string) error {
	var out intentJSON
	form := url.Values{"cancellation_reason": {"abandoned"}}
	err := g.call(ctx, http.MethodPost, "/v1/payment_intents/"+url.PathEscape(intentID)+"/cancel", form, account, "depot-pi-cancel-"+attemptID, &out)
	if err == nil {
		return nil
	}
	var ge *gateway.Error
	if !errors.As(err, &ge) || !ge.Refused() {
		return err
	}
	var current intentJSON
	if rerr := g.call(ctx, http.MethodGet, "/v1/payment_intents/"+url.PathEscape(intentID), nil, account, "", &current); rerr != nil {
		return rerr
	}
	if current.Status == "canceled" {
		return nil
	}
	return &gateway.Error{Status: http.StatusConflict, Code: "payment_intent_unexpected_state", Message: "the PaymentIntent is " + truncate(current.Status, 40)}
}

// IdempotencyKey is the Stripe Idempotency-Key of a payment attempt's create.
func IdempotencyKey(attemptID string) string { return "depot-pi-" + attemptID }

type intentJSON struct {
	ID           string `json:"id"`
	ClientSecret string `json:"client_secret"`
	Status       string `json:"status"`
}

// CreateIntent creates the PaymentIntent of an attempt: the amount in Stripe's minor units, the one payment method type the
// customer chose, the attempt's metadata, and the Idempotency-Key derived from the attempt id.
func (g *Gateway) CreateIntent(ctx context.Context, r gateway.IntentRequest) (gateway.Intent, error) {
	currency := strings.ToUpper(r.Currency)
	minor, err := ToMinor(r.Amount, currency)
	if err != nil {
		return gateway.Intent{}, &gateway.Error{Status: http.StatusBadRequest, Code: "amount_invalid", Message: err.Error()}
	}
	form := url.Values{}
	form.Set("amount", strconv.FormatInt(minor, 10))
	form.Set("currency", strings.ToLower(currency))
	switch r.Method {
	case gateway.MethodCard:
		form.Set("payment_method_types[]", "card")
	case gateway.MethodACH:
		form.Set("payment_method_types[]", "us_bank_account")
		// Instant verification through Financial Connections; Stripe falls back to microdeposits by itself.
		form.Set("payment_method_options[us_bank_account][verification_method]", "automatic")
		form.Set("payment_method_options[us_bank_account][financial_connections][permissions][]", "payment_method")
	default:
		return gateway.Intent{}, &gateway.Error{Status: http.StatusBadRequest, Code: "method_invalid", Message: "unknown payment method"}
	}
	if r.Description != "" {
		form.Set("description", truncate(r.Description, 1000))
	}
	keys := make([]string, 0, len(r.Metadata))
	for k := range r.Metadata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// Stripe metadata: up to 50 keys of 40 characters, values of 500.
		form.Set("metadata["+truncate(k, 40)+"]", truncate(r.Metadata[k], 500))
	}
	var out intentJSON
	if err := g.call(ctx, http.MethodPost, "/v1/payment_intents", form, r.Account, IdempotencyKey(r.AttemptID), &out); err != nil {
		return gateway.Intent{}, err
	}
	if out.ID == "" || out.ClientSecret == "" {
		return gateway.Intent{}, &gateway.Error{Status: http.StatusBadGateway, Code: "malformed", Message: "Stripe answered without a PaymentIntent"}
	}
	return gateway.Intent{ID: out.ID, ClientSecret: out.ClientSecret, Status: out.Status}, nil
}

type paymentMethodJSON struct {
	Type string `json:"type"`
	Card *struct {
		Brand    string `json:"brand"`
		Last4    string `json:"last4"`
		ExpMonth int    `json:"exp_month"`
		ExpYear  int    `json:"exp_year"`
	} `json:"card"`
	USBankAccount *struct {
		BankName string `json:"bank_name"`
		Last4    string `json:"last4"`
	} `json:"us_bank_account"`
}

// details keeps the brand, last four, expiry and bank name, and nothing else of the payment method.
func (pm *paymentMethodJSON) details() *gateway.Details {
	if pm == nil {
		return nil
	}
	d := &gateway.Details{}
	switch pm.Type {
	case "card":
		d.Method = gateway.MethodCard
		if pm.Card != nil {
			d.Brand, d.Last4, d.ExpMonth, d.ExpYear = truncate(pm.Card.Brand, 20), last4(pm.Card.Last4), pm.Card.ExpMonth, pm.Card.ExpYear
		}
	case "us_bank_account":
		d.Method = gateway.MethodACH
		if pm.USBankAccount != nil {
			d.BankName, d.Last4 = truncate(pm.USBankAccount.BankName, 100), last4(pm.USBankAccount.Last4)
		}
	default:
		return nil
	}
	return d
}

// PaymentDetails reads the PaymentIntent with its payment method expanded.
func (g *Gateway) PaymentDetails(ctx context.Context, account, intentID string) (gateway.Details, error) {
	var out struct {
		PaymentMethod *paymentMethodJSON `json:"payment_method"`
	}
	form := url.Values{"expand[]": {"payment_method"}}
	if err := g.call(ctx, http.MethodGet, "/v1/payment_intents/"+url.PathEscape(intentID), form, account, "", &out); err != nil {
		return gateway.Details{}, err
	}
	if d := out.PaymentMethod.details(); d != nil {
		return *d, nil
	}
	return gateway.Details{}, nil
}

// VerifySignature checks a Stripe-Signature header ("t=<unix>,v1=<hex>[,v1=<hex>...]") against the payload with each secret:
// HMAC-SHA256 over "<t>.<payload>", compared in constant time, and the timestamp within Tolerance of now.
func VerifySignature(payload []byte, header string, secrets [][]byte, now time.Time) error {
	var ts int64
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return gateway.ErrSignature
			}
			ts = n
		case "v1":
			if b, err := hex.DecodeString(v); err == nil {
				sigs = append(sigs, b)
			}
		}
	}
	if ts == 0 || len(sigs) == 0 {
		return gateway.ErrSignature
	}
	signed := append([]byte(strconv.FormatInt(ts, 10)+"."), payload...)
	matched := false
	for _, secret := range secrets {
		mac := hmac.New(sha256.New, secret)
		mac.Write(signed)
		want := mac.Sum(nil)
		for _, s := range sigs {
			if hmac.Equal(want, s) {
				matched = true
			}
		}
	}
	if !matched {
		return gateway.ErrSignature
	}
	if d := now.Sub(time.Unix(ts, 0)); d > Tolerance || d < -Tolerance {
		return gateway.ErrStale
	}
	return nil
}

// Sign builds a Stripe-Signature header for a payload; tests and the local webhook replay use it.
func Sign(payload []byte, secret string, at time.Time) string {
	ts := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(payload)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

type eventJSON struct {
	ID       string `json:"id"`
	Object   string `json:"object"`
	Type     string `json:"type"`
	Created  int64  `json:"created"`
	Livemode bool   `json:"livemode"`
	Account  string `json:"account"`
	Data     struct {
		Object json.RawMessage `json:"object"`
	} `json:"data"`
}

type piJSON struct {
	ID               string            `json:"id"`
	Amount           int64             `json:"amount"`
	AmountReceived   int64             `json:"amount_received"`
	Currency         string            `json:"currency"`
	Metadata         map[string]string `json:"metadata"`
	LastPaymentError *struct {
		Code          string             `json:"code"`
		DeclineCode   string             `json:"decline_code"`
		Message       string             `json:"message"`
		PaymentMethod *paymentMethodJSON `json:"payment_method"`
	} `json:"last_payment_error"`
}

type chargeJSON struct {
	ID             string            `json:"id"`
	Amount         int64             `json:"amount"`
	AmountRefunded int64             `json:"amount_refunded"`
	Currency       string            `json:"currency"`
	PaymentIntent  string            `json:"payment_intent"`
	Metadata       map[string]string `json:"metadata"`
}

type disputeJSON struct {
	ID            string            `json:"id"`
	Amount        int64             `json:"amount"`
	Currency      string            `json:"currency"`
	PaymentIntent string            `json:"payment_intent"`
	Reason        string            `json:"reason"`
	Status        string            `json:"status"`
	Metadata      map[string]string `json:"metadata"`
}

// ParseWebhook verifies the Stripe-Signature header first and reads nothing of an unverified payload.
func (g *Gateway) ParseWebhook(payload []byte, header http.Header, now time.Time) (gateway.Event, error) {
	if err := VerifySignature(payload, header.Get("Stripe-Signature"), g.webhookSecrets, now); err != nil {
		return gateway.Event{}, err
	}
	return ParseEvent(payload)
}

// ParseEvent reads a verified Stripe event into a gateway.Event.
func ParseEvent(payload []byte) (gateway.Event, error) {
	var e eventJSON
	if err := json.Unmarshal(payload, &e); err != nil || e.ID == "" || e.Type == "" || e.Object != "event" {
		return gateway.Event{}, gateway.ErrMalformed
	}
	ev := gateway.Event{ID: e.ID, Type: e.Type, Kind: gateway.KindIgnored, Created: time.Unix(e.Created, 0).UTC(), Livemode: e.Livemode, Account: e.Account}
	switch e.Type {
	case "payment_intent.succeeded", "payment_intent.processing", "payment_intent.payment_failed", "payment_intent.canceled":
		var pi piJSON
		if err := json.Unmarshal(e.Data.Object, &pi); err != nil || pi.ID == "" {
			return gateway.Event{}, gateway.ErrMalformed
		}
		cur := strings.ToUpper(pi.Currency)
		ev.IntentID, ev.Currency, ev.Metadata = pi.ID, cur, pi.Metadata
		ev.Total = FromMinor(pi.Amount, cur)
		ev.Amount = ev.Total
		switch e.Type {
		case "payment_intent.succeeded":
			ev.Kind = gateway.KindSucceeded
			if pi.AmountReceived > 0 {
				ev.Amount = FromMinor(pi.AmountReceived, cur)
			}
		case "payment_intent.processing":
			ev.Kind = gateway.KindProcessing
		case "payment_intent.canceled":
			ev.Kind = gateway.KindCanceled
			ev.FailureCode = "canceled"
		default:
			ev.Kind = gateway.KindFailed
			if lpe := pi.LastPaymentError; lpe != nil {
				ev.FailureCode = lpe.Code
				if lpe.DeclineCode != "" {
					ev.FailureCode = lpe.DeclineCode
				}
				ev.FailureMessage = truncate(lpe.Message, 300)
				ev.Details = lpe.PaymentMethod.details()
			}
		}
	case "charge.refunded":
		var c chargeJSON
		if err := json.Unmarshal(e.Data.Object, &c); err != nil || c.ID == "" {
			return gateway.Event{}, gateway.ErrMalformed
		}
		if c.PaymentIntent == "" {
			// A charge made without a PaymentIntent is never Depot's: acknowledged and ignored.
			return ev, nil
		}
		cur := strings.ToUpper(c.Currency)
		ev.Kind, ev.IntentID, ev.Currency, ev.Metadata = gateway.KindRefunded, c.PaymentIntent, cur, c.Metadata
		ev.Amount, ev.Total = FromMinor(c.AmountRefunded, cur), FromMinor(c.Amount, cur)
	case "charge.dispute.created", "charge.dispute.updated", "charge.dispute.closed", "charge.dispute.funds_withdrawn", "charge.dispute.funds_reinstated":
		var d disputeJSON
		if err := json.Unmarshal(e.Data.Object, &d); err != nil || d.ID == "" {
			return gateway.Event{}, gateway.ErrMalformed
		}
		if d.PaymentIntent == "" {
			return ev, nil
		}
		cur := strings.ToUpper(d.Currency)
		ev.IntentID, ev.Currency, ev.Metadata, ev.Reason, ev.DisputeStatus = d.PaymentIntent, cur, d.Metadata, truncate(d.Reason, 80), truncate(d.Status, 40)
		ev.Amount = FromMinor(d.Amount, cur)
		inquiry := strings.HasPrefix(d.Status, "warning_")
		switch {
		case e.Type == "charge.dispute.funds_withdrawn":
			// The money has left the account: a chargeback, or an ACH return after the payment succeeded.
			ev.Kind = gateway.KindDisputed
		case e.Type == "charge.dispute.funds_reinstated":
			ev.Kind = gateway.KindReinstated
		case inquiry:
			// An inquiry or retrieval (warning_needs_response, warning_under_review, warning_closed): no money moved.
			ev.Kind = gateway.KindInquiry
		case e.Type == "charge.dispute.created":
			// A dispute that is not an inquiry withdraws the funds when it is opened (funds_withdrawn follows and is a no-op).
			ev.Kind = gateway.KindDisputed
		}
	}
	return ev, nil
}

// truncate cuts s to at most n bytes without splitting a character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

func last4(s string) string {
	if len(s) != 4 {
		return ""
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return s
}
