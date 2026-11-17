// Package gateway is the provider-neutral interface of the customer portal's card and ACH payments (#30, FR-30.14). Callers (the
// portal route and the webhook listener) see only these types; a provider adapter (gateway/stripe now, an Adyen adapter later)
// turns them into its own API calls and turns its signed webhooks into Events.
//
// PCI scope stays SAQ A: card and bank details are entered on the provider's hosted element in the customer's browser and go
// straight to the provider. An adapter creates the provider's payment server-side and hands back only the client secret the
// element needs; nothing in this package carries a card number, a CVC or a bank account number. Brand, last four digits and
// expiry month and year are what an Event may report.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

// Method is how the customer pays.
type Method string

const (
	// MethodCard is a card payment (Stripe: payment method type card).
	MethodCard Method = "CARD"
	// MethodACH is a US bank account debit (Stripe: us_bank_account, verified instantly through Financial Connections, with
	// microdeposits as the provider's own fallback). It settles in days: the payment is PROCESSING until it succeeds.
	MethodACH Method = "ACH"
)

// IntentRequest asks the provider for a payment the customer then completes on the hosted element.
type IntentRequest struct {
	// AttemptID is the Depot payment attempt; the provider idempotency key is derived from it, so a retried create returns
	// the same provider payment instead of a second one.
	AttemptID string
	Amount    decimal.Decimal
	Currency  string
	Method    Method
	// Account is the provider account the payment is made on (a Stripe Connect account), empty for the platform account.
	Account     string
	Description string
	// Metadata travels with the provider payment and comes back on its events (tenant, legal entity, attempt, invoices).
	Metadata map[string]string
}

// Intent is the provider payment as the browser needs it.
type Intent struct {
	ID           string
	ClientSecret string
	Status       string
}

// Kind is what a webhook event means for a payment attempt.
type Kind string

const (
	KindSucceeded  Kind = "SUCCEEDED"
	KindProcessing Kind = "PROCESSING"
	KindFailed     Kind = "FAILED"
	KindRefunded   Kind = "REFUNDED"
	// KindDisputed is a dispute whose funds were withdrawn: a chargeback, or an ACH return after success.
	KindDisputed Kind = "DISPUTED"
	// KindInquiry is a dispute that withdrew no money (Stripe's warning_* statuses: an inquiry or retrieval, under review, or
	// closed without becoming a chargeback). It is flagged for a clerk and never reverses anything.
	KindInquiry Kind = "INQUIRY"
	// KindReinstated is a dispute whose withdrawn funds came back (the dispute was won). Flagged for a clerk to record again.
	KindReinstated Kind = "REINSTATED"
	// KindCanceled is a provider payment cancelled before it was paid (by Depot or at the provider).
	KindCanceled Kind = "CANCELED"
	// KindIgnored is an event type this integration does not act on; it is acknowledged so the provider stops sending it.
	KindIgnored Kind = "IGNORED"
)

// Details are the payment method facts the provider reports. No card number, CVC or bank account number is ever among them.
type Details struct {
	Method   Method
	Brand    string
	Last4    string
	ExpMonth int
	ExpYear  int
	BankName string
}

// Event is one verified provider webhook event.
type Event struct {
	ID       string
	Type     string
	Kind     Kind
	Created  time.Time
	Livemode bool
	// Account is the connected account the event is about, empty for the platform account.
	Account string
	// IntentID is the provider payment the event is about.
	IntentID string
	// Amount and Currency: the amount received (SUCCEEDED), the payment's amount and the amount refunded so far (REFUNDED), or
	// the disputed amount (DISPUTED). Converted from the provider's minor units with the currency's exponent.
	Amount   decimal.Decimal
	Currency string
	// Total is the provider payment's whole amount (REFUNDED), to tell a full refund from a partial one.
	Total decimal.Decimal
	// Details is set when the event carries the payment method (a failure does; a success is completed with PaymentDetails).
	Details     *Details
	FailureCode string
	// FailureMessage is the provider's own message; callers show PlainFailure(FailureCode) to customers.
	FailureMessage string
	// Reason is the dispute reason (an ACH return names why the bank sent the money back).
	Reason string
	// DisputeStatus is the provider's dispute status (warning_needs_response, needs_response, won, lost, ...).
	DisputeStatus string
	Metadata      map[string]string
}

// Gateway is one payment provider.
type Gateway interface {
	// Provider is the provider's name as stored: STRIPE or ADYEN.
	Provider() string
	// PublishableKey is the browser key of the hosted element; it is not a secret.
	PublishableKey() string
	// CreateIntent creates the provider payment (idempotent per AttemptID).
	CreateIntent(ctx context.Context, req IntentRequest) (Intent, error)
	// PaymentDetails reads the payment method of a provider payment (brand, last four, expiry, bank name).
	PaymentDetails(ctx context.Context, account, intentID string) (Details, error)
	// CheckAmount tells whether the provider can charge the amount in the currency (its granularity), with a plain reason.
	CheckAmount(amount decimal.Decimal, currency string) error
	// CancelIntent cancels a provider payment the customer abandoned (idempotent per attempt). A payment already under way
	// is refused with an *Error whose Code is "payment_intent_unexpected_state".
	CancelIntent(ctx context.Context, account, intentID, attemptID string) error
	// ParseWebhook verifies the signature of a webhook (and its age) before parsing anything, then reads the event.
	ParseWebhook(payload []byte, header http.Header, now time.Time) (Event, error)
}

var (
	// ErrSignature: the webhook's signature does not verify with any configured secret.
	ErrSignature = errors.New("gateway: webhook signature does not verify")
	// ErrStale: the signed timestamp is outside the tolerance (a replay of an old delivery).
	ErrStale = errors.New("gateway: webhook timestamp outside the tolerance")
	// ErrMalformed: the payload verified but is not an event this integration can read.
	ErrMalformed = errors.New("gateway: webhook payload malformed")
)

// Error is a refusal or failure of the provider API.
type Error struct {
	// Status is the provider's HTTP status (0 when the provider could not be reached).
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("gateway: provider answered %d %s: %s", e.Status, e.Code, e.Message)
}

// Refused reports a refusal the same request will get again (a 4xx other than 409 and 429): the attempt cannot go on.
func (e *Error) Refused() bool {
	return e.Status >= 400 && e.Status < 500 && e.Status != http.StatusConflict && e.Status != http.StatusTooManyRequests
}

// PlainFailure turns a provider failure code into a sentence a customer understands. Unknown codes get a general sentence.
func PlainFailure(code string) string {
	switch code {
	case "":
		return ""
	case "card_declined", "generic_decline", "do_not_honor", "call_issuer", "pickup_card", "restricted_card", "transaction_not_allowed":
		return "Your card was declined. Try another card or contact your bank."
	case "insufficient_funds":
		return "There was not enough money in the account. Try another card or bank account."
	case "expired_card":
		return "The card has expired. Use another card."
	case "incorrect_cvc", "invalid_cvc":
		return "The card's security code is wrong. Check it and try again."
	case "incorrect_number", "invalid_number":
		return "The card number is wrong. Check it and try again."
	case "authentication_required", "payment_intent_authentication_failure":
		return "Your bank asked for a confirmation that was not completed. Try again and finish the confirmation."
	case "processing_error":
		return "The payment could not be processed. Try again in a few minutes."
	case "account_closed", "bank_account_restricted", "no_account", "account_frozen", "bank_account_unusable":
		return "The bank account cannot be debited. Use another bank account or pay by card."
	case "debit_not_authorized":
		return "Your bank says the debit was not authorized. Contact your bank, or pay by card."
	case "payment_method_microdeposit_verification_attempts_exceeded", "payment_method_microdeposit_verification_amounts_mismatch",
		"payment_method_microdeposit_verification_timeout":
		return "The bank account could not be verified. Connect it again or pay by card."
	case "amount_too_small":
		return "The amount is below the smallest payment we can take. Pay a larger amount."
	case "amount_too_large":
		return "The amount is above the largest payment we can take. Pay in several parts."
	case "amount_invalid":
		return "The amount cannot be charged in this currency. Change the amount and try again."
	case "canceled":
		return "The payment was cancelled before it was made. Nothing was charged."
	}
	return "The payment did not go through. Try again or use another payment method."
}
