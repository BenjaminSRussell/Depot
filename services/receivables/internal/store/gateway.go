package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/money"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/gateway"
)

// Problem codes of the portal's card and ACH payments (#30, FR-30.14).
const (
	// CodeGatewayOverpay is the PRD's AR-0019: the amount on an item is more than what is still open on it.
	CodeGatewayOverpay     = "AR-0019"
	CodeGatewayItem        = "AR-GATEWAY-ITEM"
	CodeGatewayMethod      = "AR-GATEWAY-METHOD"
	CodeGatewayNotFound    = "AR-GATEWAY-NOT-FOUND"
	CodeGatewayOff         = "AR-GATEWAY-OFF"
	CodeGatewayUnavailable = "AR-GATEWAY-UNAVAILABLE"
	CodeGatewayRefused     = "AR-GATEWAY-REFUSED"
	// CodeGatewayAmount: the provider cannot charge the amount in this currency (for example KWD by card in steps of 0.010).
	CodeGatewayAmount = "AR-GATEWAY-AMOUNT"
	// CodeGatewayState: the payment can no longer be cancelled; it is under way or done.
	CodeGatewayState = "AR-GATEWAY-STATE"
	// MaxGatewayItems bounds the open items of one payment.
	MaxGatewayItems = 50
)

// Gateway payment statuses.
const (
	GatewayRequiresPayment = "REQUIRES_PAYMENT"
	GatewayProcessing      = "PROCESSING"
	GatewaySucceeded       = "SUCCEEDED"
	GatewayFailed          = "FAILED"
	GatewayRefunded        = "REFUNDED"
	GatewayDisputed        = "DISPUTED"
)

// GatewayAllocation is one open item of a gateway payment and the amount the customer pays on it.
type GatewayAllocation struct {
	OpenItemID     string          `json:"openItemId"`
	DocumentNumber string          `json:"documentNumber"`
	Amount         decimal.Decimal `json:"amount"`
}

// GatewayEventRecord is one provider webhook event handled for a gateway payment.
type GatewayEventRecord struct {
	EventID     string    `json:"eventId"`
	EventType   string    `json:"eventType"`
	Outcome     string    `json:"outcome"`
	OccurredAt  time.Time `json:"occurredAt"`
	ProcessedAt time.Time `json:"processedAt"`
}

// GatewayPayment is a customer's card or ACH payment attempt from the portal. No card number, CVC or bank account number is
// stored: only the brand, the last four digits, the expiry and the bank's name.
type GatewayPayment struct {
	ID               string               `json:"id"`
	LegalEntityID    string               `json:"legalEntityId"`
	CustomerID       string               `json:"customerId"`
	Provider         string               `json:"provider"`
	ProviderAccount  *string              `json:"providerAccount,omitempty"`
	ProviderIntentID *string              `json:"providerIntentId,omitempty"`
	Method           string               `json:"method"`
	Amount           decimal.Decimal      `json:"amount"`
	Currency         string               `json:"currency"`
	Status           string               `json:"status"`
	CardBrand        *string              `json:"cardBrand,omitempty"`
	Last4            *string              `json:"last4,omitempty"`
	CardExpMonth     *int                 `json:"cardExpMonth,omitempty"`
	CardExpYear      *int                 `json:"cardExpYear,omitempty"`
	BankName         *string              `json:"bankName,omitempty"`
	FailureCode      *string              `json:"failureCode,omitempty"`
	FailureMessage   *string              `json:"failureMessage,omitempty"`
	PaymentID        *string              `json:"paymentId,omitempty"`
	RefundedAmount   decimal.Decimal      `json:"refundedAmount"`
	NeedsReview      bool                 `json:"needsReview"`
	ReviewReason     *string              `json:"reviewReason,omitempty"`
	ContactID        *string              `json:"-"`
	Version          int                  `json:"version"`
	CreatedAt        time.Time            `json:"createdAt"`
	UpdatedAt        time.Time            `json:"updatedAt"`
	LastEventAt      *time.Time           `json:"lastEventAt,omitempty"`
	Allocations      []GatewayAllocation  `json:"allocations"`
	Events           []GatewayEventRecord `json:"events,omitempty"`
}

// GatewayAttemptInput starts a gateway payment for the portal's customer.
type GatewayAttemptInput struct {
	LegalEntityID   string
	CustomerID      string
	Provider        string
	ProviderAccount string
	Method          gateway.Method
	ContactID       string
	Allocations     []GatewayAllocation
	// CheckAmount is the provider's granularity check (Stripe takes 3-decimal currencies in steps of 0.010); nil checks nothing.
	CheckAmount func(amount decimal.Decimal, currency string) error
}

// GatewayReservationWindow is how long an attempt the provider created a payment for, still waiting for the customer or for
// the provider's webhook, keeps what it pays from being paid again. Cancelling it, its failure or this window releases it.
const GatewayReservationWindow = 30 * time.Minute

// GatewayFilter narrows the gateway payment list.
type GatewayFilter struct {
	Status     string
	CustomerID string
	PaymentID  string
}

// GatewayRoute is where a provider payment belongs: read across tenants for the webhook listener.
type GatewayRoute struct {
	TenantID         string
	LegalEntityID    string
	GatewayPaymentID string
}

// ErrGatewayRouteNotFound: no attempt holds the provider payment (another system's payment on the same provider account).
var ErrGatewayRouteNotFound = errors.New("store: no gateway payment for the provider payment")

const gatewayColumns = `id, legal_entity_id, customer_id, provider, provider_account, provider_intent_id, method, amount::text, currency,
	status, card_brand, last4, card_exp_month, card_exp_year, bank_name, failure_code, failure_message, payment_id::text,
	refunded_amount::text, needs_review, review_reason, contact_id, version, created_at, updated_at, last_event_at`

func scanGateway(row rowScanner) (GatewayPayment, error) {
	var g GatewayPayment
	var amount, refunded string
	var expMonth, expYear *int16
	if err := row.Scan(&g.ID, &g.LegalEntityID, &g.CustomerID, &g.Provider, &g.ProviderAccount, &g.ProviderIntentID, &g.Method, &amount,
		&g.Currency, &g.Status, &g.CardBrand, &g.Last4, &expMonth, &expYear, &g.BankName, &g.FailureCode, &g.FailureMessage, &g.PaymentID,
		&refunded, &g.NeedsReview, &g.ReviewReason, &g.ContactID, &g.Version, &g.CreatedAt, &g.UpdatedAt, &g.LastEventAt); err != nil {
		return GatewayPayment{}, err
	}
	g.Amount, g.RefundedAmount = decimal.RequireFromString(amount), decimal.RequireFromString(refunded)
	if expMonth != nil {
		m := int(*expMonth)
		g.CardExpMonth = &m
	}
	if expYear != nil {
		y := int(*expYear)
		g.CardExpYear = &y
	}
	g.Allocations = []GatewayAllocation{}
	return g, nil
}

func gatewayRequestHash(in GatewayAttemptInput) string {
	parts := []string{"gateway", in.LegalEntityID, in.CustomerID, in.Provider, in.ProviderAccount, string(in.Method)}
	for _, a := range in.Allocations {
		parts = append(parts, strings.ToLower(a.OpenItemID), a.Amount.String())
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

// CreateGatewayAttempt records a payment attempt for open items of the customer: every item is the customer's own, in the
// legal entity, an open debit, and all in one currency; the amount on each is above zero, in the currency's minor unit, and at
// most what is still open on it less what an ACH payment still processing already claims (AR-0019, no overpayment; less is a
// partial payment). An item of another customer reads as not found. A repeated Idempotency-Key answers the first attempt
// (replay true) when the request is the same.
func (s *Postgres) CreateGatewayAttempt(ctx context.Context, in GatewayAttemptInput, key string) (GatewayPayment, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out GatewayPayment
	replay := false
	if len(in.Allocations) == 0 || len(in.Allocations) > MaxGatewayItems {
		return out, false, httpx.NewProblem(http.StatusBadRequest, "COM-0001", fmt.Sprintf("choose between 1 and %d open items", MaxGatewayItems))
	}
	hash := gatewayRequestHash(in)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		prior, err := scanGateway(tx.QueryRow(ctx, `select `+gatewayColumns+` from gateway_payment where tenant_id = $1 and idempotency_key = $2`, p.TenantID, key))
		if err == nil {
			var priorHash string
			if err := tx.QueryRow(ctx, `select request_hash from gateway_payment where tenant_id = $1 and id = $2`, p.TenantID, prior.ID).Scan(&priorHash); err != nil {
				return err
			}
			if priorHash != hash || prior.CustomerID != in.CustomerID {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different payment")
			}
			out, replay = prior, true
			return loadGatewayAllocations(ctx, tx, p.TenantID, []*GatewayPayment{&out})
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		currency := ""
		total := decimal.Zero
		seen := map[string]bool{}
		allocs := make([]GatewayAllocation, 0, len(in.Allocations))
		for _, a := range in.Allocations {
			id := strings.ToLower(a.OpenItemID)
			if seen[id] {
				return httpx.NewProblem(http.StatusBadRequest, "COM-0001", "an open item is named twice")
			}
			seen[id] = true
			item, err := scanOpenItem(tx.QueryRow(ctx, `select `+openItemColumns+` from open_item where id = $1 for share`, id))
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && (item.CustomerID != in.CustomerID || item.LegalEntityID != in.LegalEntityID)) {
				return httpx.NewProblem(http.StatusNotFound, CodeOpenItemNotFound, "open item not found")
			}
			if err != nil {
				return err
			}
			if item.Amount.IsNegative() {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeGatewayItem, item.DocumentNumber+" is a credit; only invoices and other debits can be paid")
			}
			if item.Status != "OPEN" && item.Status != "PARTIALLY_SETTLED" {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeGatewayItem, item.DocumentNumber+" is already paid")
			}
			if currency == "" {
				currency = item.Currency
			} else if item.Currency != currency {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeGatewayItem, "pay items of one currency at a time")
			}
			if !a.Amount.IsPositive() || !a.Amount.Equal(a.Amount.Truncate(money.MinorUnits(currency))) {
				return httpx.NewProblem(http.StatusBadRequest, "COM-0001", fmt.Sprintf("the amount on %s must be above zero with at most %d decimals", item.DocumentNumber, money.MinorUnits(currency)))
			}
			if in.CheckAmount != nil {
				if err := in.CheckAmount(a.Amount, currency); err != nil {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeGatewayAmount, fmt.Sprintf("the amount on %s cannot be charged: %s", item.DocumentNumber, err.Error()))
				}
			}
			// What other payments already claim: ACH payments processing, and payments the provider was asked for in the last
			// GatewayReservationWindow that still wait for the customer or for the webhook (a card paid seconds ago).
			var pendingText string
			if err := tx.QueryRow(ctx, `select coalesce(sum(a.amount), 0)::text from gateway_payment_allocation a
				join gateway_payment g on g.tenant_id = a.tenant_id and g.id = a.gateway_payment_id
				where a.tenant_id = $1 and a.open_item_id = $2
				  and (g.status = 'PROCESSING' or (g.status = 'REQUIRES_PAYMENT' and g.provider_intent_id is not null
				       and g.created_at > now() - make_interval(secs => $3)))`,
				p.TenantID, id, GatewayReservationWindow.Seconds()).Scan(&pendingText); err != nil {
				return err
			}
			pending := decimal.RequireFromString(pendingText)
			payable := item.OpenAmount.Sub(pending)
			if a.Amount.GreaterThan(payable) {
				detail := fmt.Sprintf("%s has %s %s left to pay; pay that much or less", item.DocumentNumber, payable.StringFixed(money.MinorUnits(currency)), currency)
				if pending.IsPositive() {
					detail = fmt.Sprintf("%s has %s %s left to pay: %s %s is already being paid by another card or bank payment; pay that much or less, or wait for it to finish",
						item.DocumentNumber, payable.StringFixed(money.MinorUnits(currency)), currency, pending.StringFixed(money.MinorUnits(currency)), currency)
				}
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeGatewayOverpay, detail)
			}
			total = total.Add(a.Amount)
			allocs = append(allocs, GatewayAllocation{OpenItemID: id, DocumentNumber: item.DocumentNumber, Amount: a.Amount})
		}
		if in.Method == gateway.MethodACH && currency != "USD" {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeGatewayMethod, "bank account (ACH) payments are in US dollars only; pay by card")
		}
		var account, contact *string
		if in.ProviderAccount != "" {
			account = &in.ProviderAccount
		}
		if in.ContactID != "" {
			contact = &in.ContactID
		}
		out, err = scanGateway(tx.QueryRow(ctx, `insert into gateway_payment (tenant_id, id, legal_entity_id, customer_id, provider, provider_account,
				method, amount, currency, contact_id, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) returning `+gatewayColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.CustomerID, in.Provider, account, string(in.Method), total.String(), currency, contact, key, hash))
		if err != nil {
			return err
		}
		for i, a := range allocs {
			if _, err := tx.Exec(ctx, `insert into gateway_payment_allocation (tenant_id, legal_entity_id, gateway_payment_id, line_no, open_item_id, document_number, amount)
				values ($1, $2, $3, $4, $5, $6, $7)`, p.TenantID, in.LegalEntityID, out.ID, i+1, a.OpenItemID, a.DocumentNumber, a.Amount.String()); err != nil {
				return err
			}
		}
		out.Allocations = allocs
		return nil
	})
	return out, replay, mapWriteErr(err)
}

func loadGatewayAllocations(ctx context.Context, tx pgx.Tx, tenantID string, rows []*GatewayPayment) error {
	if len(rows) == 0 {
		return nil
	}
	byID := map[string]*GatewayPayment{}
	idList := make([]string, 0, len(rows))
	for _, g := range rows {
		byID[g.ID] = g
		idList = append(idList, g.ID)
	}
	res, err := tx.Query(ctx, `select gateway_payment_id::text, open_item_id::text, document_number, amount::text from gateway_payment_allocation
		where tenant_id = $1 and gateway_payment_id = any($2::uuid[]) order by gateway_payment_id, line_no`, tenantID, idList)
	if err != nil {
		return err
	}
	defer res.Close()
	for res.Next() {
		var gid, amount string
		var a GatewayAllocation
		if err := res.Scan(&gid, &a.OpenItemID, &a.DocumentNumber, &amount); err != nil {
			return err
		}
		a.Amount = decimal.RequireFromString(amount)
		if g := byID[gid]; g != nil {
			g.Allocations = append(g.Allocations, a)
		}
	}
	return res.Err()
}

// SetGatewayIntent stores the provider payment of an attempt; the trigger adds the webhook route. Setting the same id again
// is a no-op.
func (s *Postgres) SetGatewayIntent(ctx context.Context, id, intentID string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update gateway_payment set provider_intent_id = $3, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and (provider_intent_id is null or provider_intent_id = $3)`, p.TenantID, id, intentID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusConflict, CodeGatewayRefused, "the payment already has another provider payment")
		}
		return nil
	})
}

// FailGatewayAttempt marks an attempt the provider refused to create (the same request would be refused again).
func (s *Postgres) FailGatewayAttempt(ctx context.Context, id, code, message string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update gateway_payment set status = 'FAILED', failure_code = left($3, 80), failure_message = left($4, 300),
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2 and status = 'REQUIRES_PAYMENT'`, p.TenantID, id, code, message)
		return err
	})
}

// CancelGatewayAttempt ends an attempt the customer abandoned (went back to change the amounts), releasing what it reserved.
// cancel asks the provider to cancel its payment first; a payment already under way is refused (AR-GATEWAY-STATE). Only the
// customer's own attempt still waiting for the customer can be cancelled; another customer's reads as not found.
func (s *Postgres) CancelGatewayAttempt(ctx context.Context, id, customerID string, cancel func(g GatewayPayment) error) (GatewayPayment, error) {
	g, err := s.GetGatewayPayment(ctx, id)
	if err == nil && !strings.EqualFold(g.CustomerID, customerID) {
		err = httpx.NewProblem(http.StatusNotFound, CodeGatewayNotFound, "payment not found")
	}
	if err != nil {
		return GatewayPayment{}, err
	}
	if g.Status != GatewayRequiresPayment {
		if g.Status == GatewayFailed {
			return g, nil
		}
		return g, httpx.NewProblem(http.StatusConflict, CodeGatewayState, "the payment is already under way and cannot be cancelled")
	}
	// The provider is asked first, outside the lease. A payment it says is under way or paid is refused and left as it is: its
	// webhook settles it, and it keeps reserving its invoices.
	if g.ProviderIntentID != nil && cancel != nil {
		if err := cancel(g); err != nil {
			return g, err
		}
	}
	token, err := s.acquireGatewayLease(ctx, id)
	if err != nil {
		return g, err
	}
	defer s.releaseGatewayLease(ctx, id, token)
	p, _ := tenant.FromContext(ctx)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update gateway_payment set status = 'FAILED', failure_code = 'canceled', failure_message = $3,
			version = version + 1, updated_at = now() where tenant_id = $1 and id = $2 and status = 'REQUIRES_PAYMENT' and version = $4`,
			p.TenantID, id, gateway.PlainFailure("canceled"), g.Version)
		if err == nil && tag.RowsAffected() == 0 {
			return httpx.NewProblem(http.StatusConflict, CodeGatewayState, "the payment changed while it was being cancelled; reload it")
		}
		return err
	})
	if err != nil {
		return g, err
	}
	return s.GetGatewayPayment(ctx, id)
}

// ListGatewayPayments lists gateway payments, newest first, with their items.
func (s *Postgres) ListGatewayPayments(ctx context.Context, f GatewayFilter, limit int) ([]GatewayPayment, error) {
	p, _ := tenant.FromContext(ctx)
	out := []GatewayPayment{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+gatewayColumns+` from gateway_payment
			where ($1::text = '' or status = $1::text) and ($2::text = '' or customer_id::text = $2::text)
			and ($3::text = '' or payment_id::text = $3::text)
			order by created_at desc, id limit $4`, f.Status, f.CustomerID, f.PaymentID, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			g, err := scanGateway(rows)
			if err != nil {
				rows.Close()
				return err
			}
			out = append(out, g)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		ptrs := make([]*GatewayPayment, len(out))
		for i := range out {
			ptrs[i] = &out[i]
		}
		return loadGatewayAllocations(ctx, tx, p.TenantID, ptrs)
	})
	return out, err
}

// GetGatewayPayment reads one gateway payment with its items and the webhook events handled for it.
func (s *Postgres) GetGatewayPayment(ctx context.Context, id string) (GatewayPayment, error) {
	p, _ := tenant.FromContext(ctx)
	var out GatewayPayment
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanGateway(tx.QueryRow(ctx, `select `+gatewayColumns+` from gateway_payment where id = $1`, id))
		if err != nil {
			return notFound(err, CodeGatewayNotFound, "payment not found")
		}
		if err := loadGatewayAllocations(ctx, tx, p.TenantID, []*GatewayPayment{&out}); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select event_id, event_type, outcome, occurred_at, processed_at from gateway_event
			where tenant_id = $1 and gateway_payment_id = $2 order by processed_at, event_id`, p.TenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		out.Events = []GatewayEventRecord{}
		for rows.Next() {
			var e GatewayEventRecord
			if err := rows.Scan(&e.EventID, &e.EventType, &e.Outcome, &e.OccurredAt, &e.ProcessedAt); err != nil {
				return err
			}
			out.Events = append(out.Events, e)
		}
		return rows.Err()
	})
	return out, err
}

// ResolveGatewayIntent finds the attempt of a provider payment across tenants (SECURITY DEFINER receivables_gateway_route); the
// webhook listener calls it after the signature verified, before it has a tenant.
func (s *Postgres) ResolveGatewayIntent(ctx context.Context, provider, intentID string) (GatewayRoute, error) {
	var r GatewayRoute
	err := s.pool.QueryRow(ctx, `select tenant_id::text, legal_entity_id::text, gateway_payment_id::text from receivables_gateway_route($1, $2)`,
		provider, intentID).Scan(&r.TenantID, &r.LegalEntityID, &r.GatewayPaymentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, ErrGatewayRouteNotFound
	}
	return r, err
}

// GatewayContext is the principal a webhook is handled under: the tenant and the one legal entity of the attempt.
func GatewayContext(ctx context.Context, r GatewayRoute) context.Context {
	return tenant.With(ctx, tenant.Principal{TenantID: r.TenantID, LegalEntityID: r.LegalEntityID, Service: "receivables", Duties: map[string]struct{}{}})
}

// GatewayEventOptions are what handling an event needs besides the event.
type GatewayEventOptions struct {
	// BankAccountRef is the account_ref of the provider's clearing account in ar_bank_account: the receipt journal of a
	// succeeded payment debits the GL account finance maps it to (#512 sign-off).
	BankAccountRef string
	// Details reads the payment method of the provider payment (brand, last four, expiry, bank); nil or a failure leaves them out.
	Details func(ctx context.Context) (gateway.Details, error)
}

// ErrGatewayBusy: another delivery is handling the same attempt, or the attempt moved while this event was handled. It is
// transient: the webhook answers 5xx and the provider redelivers, and the redelivery sees the new state.
var ErrGatewayBusy = errors.New("store: the payment attempt is being changed by another event; retry")

// GatewayLease is how long one handling may hold an attempt. It is far above a handling's length (a few transactions and no
// provider call), and short enough that the attempt of a crashed handler is free again within minutes.
const GatewayLease = 2 * time.Minute

// acquireGatewayLease takes the attempt's lease with one conditional UPDATE (a free or expired lease only) and answers the
// token that releases it. A lease held by another handling is ErrGatewayBusy: the webhook answers 409 and the provider
// redelivers. It is safe behind a transaction-mode pooler and holds no connection: each transaction of the handling takes
// one from the pool and gives it back.
func (s *Postgres) acquireGatewayLease(ctx context.Context, attemptID string) (string, error) {
	p, _ := tenant.FromContext(ctx)
	token := ids.New()
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update gateway_payment set handling_until = now() + make_interval(secs => $3), handling_token = $4
			where tenant_id = $1 and id = $2 and (handling_until is null or handling_until < now())`,
			p.TenantID, attemptID, GatewayLease.Seconds(), token)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from gateway_payment where tenant_id = $1 and id = $2)`, p.TenantID, attemptID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return httpx.NewProblem(http.StatusNotFound, CodeGatewayNotFound, "payment not found")
			}
			return ErrGatewayBusy
		}
		return nil
	})
	return token, err
}

// releaseGatewayLease ends the lease if this handling still holds it (a handling that outlived its lease and was taken over
// releases nothing).
func (s *Postgres) releaseGatewayLease(ctx context.Context, attemptID, token string) {
	p, _ := tenant.FromContext(ctx)
	ctx = context.WithoutCancel(ctx)
	_ = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update gateway_payment set handling_until = null, handling_token = null
			where tenant_id = $1 and id = $2 and handling_token = $3`, p.TenantID, attemptID, token)
		return err
	})
}

// loadForEvent reads the attempt with its items, and whether the event was handled already.
func (s *Postgres) loadForEvent(ctx context.Context, attemptID string, ev gateway.Event) (GatewayPayment, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var g GatewayPayment
	duplicate := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		g, err = scanGateway(tx.QueryRow(ctx, `select `+gatewayColumns+` from gateway_payment where id = $1`, attemptID))
		if err != nil {
			return notFound(err, CodeGatewayNotFound, "payment not found")
		}
		if err := tx.QueryRow(ctx, `select exists (select 1 from gateway_event where tenant_id = $1 and provider = $2 and event_id = $3)`,
			p.TenantID, g.Provider, ev.ID).Scan(&duplicate); err != nil || duplicate {
			return err
		}
		return loadGatewayAllocations(ctx, tx, p.TenantID, []*GatewayPayment{&g})
	})
	return g, duplicate, err
}

// HandleGatewayEvent applies one verified webhook event to its attempt and records the event id, so a redelivery or a replay is
// answered "duplicate" without acting twice. The attempt's lease (acquireGatewayLease) is held for the whole handling, and the
// final write is a compare-and-set on the attempt's version, so two events of one attempt (a refund racing the success) never
// interleave: the second gets ErrGatewayBusy and is redelivered. No lease is held across a provider call. Only these events move money:
//
//   - SUCCEEDED records a PORTAL payment (CreatePayment, Idempotency-Key gateway:<attempt>), stores its id on the attempt at
//     once, and applies it to the items the customer chose (ApplyPayment, gateway-apply:<attempt>), each capped at what is still
//     open; what cannot be applied stays on account and the attempt is flagged for review.
//   - REFUNDED in full and DISPUTED in full (funds withdrawn: a chargeback or an ACH return) reverse it: UnapplyPayment, then
//     ReversePayment. The payment is found by the attempt's payment id, or else by its gateway:<attempt> key, so a success that
//     crashed before recording itself is still reversed. A partial refund or dispute is recorded and flagged for a clerk.
//
// An inquiry (a dispute that withdrew nothing) and reinstated dispute funds are flagged for a clerk and move no money.
// PROCESSING, FAILED and CANCELED change the attempt only, and an older event than the last one applied never does. An event
// arriving after a later state (a processing event after success) is recorded as ignored. A transient failure is returned so
// the provider redelivers; a refusal of receivables itself (an item settled meanwhile, a payment a clerk already reversed) is
// recorded on the attempt for review.
func (s *Postgres) HandleGatewayEvent(ctx context.Context, attemptID string, ev gateway.Event, opts GatewayEventOptions) (string, error) {
	g, duplicate, err := s.loadForEvent(ctx, attemptID, ev)
	if err != nil {
		return "", err
	}
	if duplicate {
		return "duplicate", nil
	}
	// The provider is asked for the payment method before the lease is taken: no lease is held across a provider call.
	var details *gateway.Details
	if ev.Kind == gateway.KindSucceeded && opts.Details != nil && g.Status != GatewaySucceeded && g.Status != GatewayRefunded && g.Status != GatewayDisputed {
		if d, err := opts.Details(ctx); err == nil {
			details = &d
		}
	}
	token, err := s.acquireGatewayLease(ctx, attemptID)
	if err != nil {
		return "", err
	}
	defer s.releaseGatewayLease(ctx, attemptID, token)
	// Read again under the lease: another event may have moved the attempt meanwhile.
	if g, duplicate, err = s.loadForEvent(ctx, attemptID, ev); err != nil {
		return "", err
	}
	if duplicate {
		return "duplicate", nil
	}
	if ev.Account != deref(g.ProviderAccount) {
		return "ignored: the event is for another provider account", s.recordGatewayEvent(ctx, g, ev, "ignored: another provider account", nil)
	}
	stale := g.LastEventAt != nil && ev.Created.Before(*g.LastEventAt)
	switch ev.Kind {
	case gateway.KindProcessing:
		if g.Status != GatewayRequiresPayment && g.Status != GatewayFailed {
			return s.ignore(ctx, g, ev)
		}
		if stale {
			return s.ignoreStale(ctx, g, ev)
		}
		return "processing", s.recordGatewayEvent(ctx, g, ev, "processing", func(u *gatewayUpdate) {
			u.status = GatewayProcessing
			u.details = ev.Details
			u.clearFailure = true
		})
	case gateway.KindFailed, gateway.KindCanceled:
		if g.Status != GatewayRequiresPayment && g.Status != GatewayProcessing {
			return s.ignore(ctx, g, ev)
		}
		if ev.Kind == gateway.KindCanceled && g.Status == GatewayProcessing {
			return s.ignore(ctx, g, ev)
		}
		if stale {
			// A failure older than the event that moved the attempt (a newer processing on the same PaymentIntent) is history.
			return s.ignoreStale(ctx, g, ev)
		}
		code := firstNonEmpty(ev.FailureCode, "unknown")
		outcome := "failed: " + code
		if ev.Kind == gateway.KindCanceled {
			outcome = "cancelled"
		}
		return outcome, s.recordGatewayEvent(ctx, g, ev, outcome, func(u *gatewayUpdate) {
			u.status = GatewayFailed
			u.details = ev.Details
			u.failureCode, u.failureMessage = code, firstNonEmpty(gateway.PlainFailure(ev.FailureCode), ev.FailureMessage)
		})
	case gateway.KindSucceeded:
		if g.Status == GatewaySucceeded || g.Status == GatewayRefunded || g.Status == GatewayDisputed {
			return s.ignore(ctx, g, ev)
		}
		paymentID, review, err := s.settleGateway(ctx, g, ev, details, opts.BankAccountRef)
		if err != nil {
			return "", err
		}
		outcome := "succeeded: payment recorded and applied"
		if review != "" {
			outcome = "succeeded: " + review
		}
		return outcome, s.recordGatewayEvent(ctx, g, ev, outcome, func(u *gatewayUpdate) {
			u.status = GatewaySucceeded
			u.details = details
			u.paymentID = paymentID
			u.clearFailure = true
			u.review = review
		})
	case gateway.KindInquiry:
		review := fmt.Sprintf("card issuer inquiry (%s) of %s %s; no money was withdrawn. Answer it in the Stripe dashboard if asked",
			firstNonEmpty(ev.DisputeStatus, "inquiry"), ev.Amount.StringFixed(money.MinorUnits(g.Currency)), g.Currency)
		if ev.DisputeStatus == "warning_closed" {
			review = "the card issuer inquiry closed without a chargeback; nothing to do"
		}
		return "inquiry: flagged for review", s.recordGatewayEvent(ctx, g, ev, "inquiry ("+firstNonEmpty(ev.DisputeStatus, "inquiry")+"): flagged for review", func(u *gatewayUpdate) {
			u.review = review
		})
	case gateway.KindReinstated:
		review := fmt.Sprintf("the dispute's %s %s came back (dispute won); record the payment again by hand and apply it", ev.Amount.StringFixed(money.MinorUnits(g.Currency)), g.Currency)
		return "dispute funds reinstated: flagged for review", s.recordGatewayEvent(ctx, g, ev, "dispute funds reinstated: flagged for review", func(u *gatewayUpdate) {
			u.review = review
		})
	case gateway.KindRefunded, gateway.KindDisputed:
		status, verb := GatewayRefunded, "refunded"
		full := !ev.Amount.LessThan(ev.Total)
		if ev.Kind == gateway.KindDisputed {
			status, verb = GatewayDisputed, "disputed"
			full = !ev.Amount.LessThan(g.Amount)
			if ev.Reason != "" {
				verb += " (" + ev.Reason + ")"
			}
		}
		if g.Status == GatewayRefunded || g.Status == GatewayDisputed {
			return s.ignore(ctx, g, ev)
		}
		if !full {
			review := fmt.Sprintf("%s %s of %s %s; adjust the payment's application by hand", verb, ev.Amount.StringFixed(money.MinorUnits(g.Currency)), g.Amount.StringFixed(money.MinorUnits(g.Currency)), g.Currency)
			return "partly " + verb, s.recordGatewayEvent(ctx, g, ev, "partly "+verb, func(u *gatewayUpdate) {
				u.refunded = &ev.Amount
				u.review = review
			})
		}
		paymentID := deref(g.PaymentID)
		if paymentID == "" {
			// The success may have recorded the payment and crashed before it stored its id: find it by its key.
			if paymentID, err = s.gatewayPaymentByKey(ctx, g.ID); err != nil {
				return "", err
			}
		}
		review := ""
		if paymentID != "" {
			if review, err = s.reverseGatewayPayment(ctx, paymentID, "Card or bank payment "+verb+" at the provider"); err != nil {
				return "", err
			}
		}
		outcome := verb + ": payment reversed"
		if paymentID == "" {
			outcome = verb + " before any payment was recorded"
		}
		if review != "" {
			outcome = verb + ": " + review
		}
		return outcome, s.recordGatewayEvent(ctx, g, ev, outcome, func(u *gatewayUpdate) {
			u.status = status
			u.paymentID = paymentID
			u.refunded = &ev.Amount
			u.review = review
		})
	}
	return "ignored", nil
}

// gatewayPaymentByKey is the receivables payment a success of the attempt recorded, by its gateway:<attempt> key, or "".
func (s *Postgres) gatewayPaymentByKey(ctx context.Context, attemptID string) (string, error) {
	p, _ := tenant.FromContext(ctx)
	id := ""
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select id::text from payment where tenant_id = $1 and idempotency_key = $2`, p.TenantID, "gateway:"+attemptID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return id, err
}

// setGatewayPaymentID stores the payment a success recorded right after CreatePayment, before it is applied, so a crash
// between the two still leaves the attempt pointing at its payment. It is written under the attempt's lock and does not move
// the version the final compare-and-set checks.
func (s *Postgres) setGatewayPaymentID(ctx context.Context, attemptID, paymentID string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update gateway_payment set payment_id = $3, updated_at = now() where tenant_id = $1 and id = $2 and payment_id is null`,
			p.TenantID, attemptID, paymentID)
		return err
	})
}

func (s *Postgres) ignore(ctx context.Context, g GatewayPayment, ev gateway.Event) (string, error) {
	outcome := "ignored: the payment is already " + strings.ToLower(g.Status)
	return outcome, s.recordGatewayEvent(ctx, g, ev, outcome, nil)
}

func (s *Postgres) ignoreStale(ctx context.Context, g GatewayPayment, ev gateway.Event) (string, error) {
	outcome := "ignored: older than the last event applied"
	return outcome, s.recordGatewayEvent(ctx, g, ev, outcome, nil)
}

type gatewayUpdate struct {
	status         string
	details        *gateway.Details
	failureCode    string
	failureMessage string
	clearFailure   bool
	paymentID      string
	refunded       *decimal.Decimal
	review         string
}

// recordGatewayEvent applies the update to the attempt and records the event in one transaction. The update is a
// compare-and-set on the version the handling read: if the attempt moved meanwhile, nothing is written and ErrGatewayBusy
// tells the provider to redeliver. A concurrent delivery of the same event records nothing a second time.
func (s *Postgres) recordGatewayEvent(ctx context.Context, g GatewayPayment, ev gateway.Event, outcome string, change func(*gatewayUpdate)) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into gateway_event (tenant_id, legal_entity_id, provider, event_id, event_type, gateway_payment_id, outcome, occurred_at)
			values ($1, $2, $3, $4, $5, $6, left($7, 300), $8) on conflict do nothing`, p.TenantID, g.LegalEntityID, g.Provider, ev.ID, ev.Type, g.ID, outcome, ev.Created)
		if err != nil || tag.RowsAffected() == 0 || change == nil {
			return err
		}
		var u gatewayUpdate
		change(&u)
		var brand, last4, bank *string
		var expMonth, expYear *int
		if d := u.details; d != nil {
			brand, last4, bank = nilIfEmpty(d.Brand), nilIfEmpty(d.Last4), nilIfEmpty(d.BankName)
			if d.ExpMonth >= 1 && d.ExpMonth <= 12 {
				expMonth = &d.ExpMonth
			}
			if d.ExpYear >= 2000 && d.ExpYear <= 2200 {
				expYear = &d.ExpYear
			}
		}
		var refunded *string
		if u.refunded != nil {
			r := u.refunded.String()
			refunded = &r
		}
		tag, err = tx.Exec(ctx, `update gateway_payment set
				status = coalesce(nullif($3, ''), status),
				card_brand = coalesce($4, card_brand), last4 = coalesce($5, last4),
				card_exp_month = coalesce($6, card_exp_month), card_exp_year = coalesce($7, card_exp_year), bank_name = coalesce($8, bank_name),
				failure_code = case when $9 then null when $10 <> '' then left($10, 80) else failure_code end,
				failure_message = case when $9 then null when $11 <> '' then left($11, 300) else failure_message end,
				payment_id = coalesce(nullif($12, '')::uuid, payment_id),
				refunded_amount = coalesce($13::numeric, refunded_amount),
				needs_review = needs_review or $14 <> '', review_reason = case when $14 <> '' then left($14, 300) else review_reason end,
				last_event_at = greatest(coalesce(last_event_at, $16), $16),
				version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 and version = $15`, p.TenantID, g.ID, u.status, brand, last4, expMonth, expYear, bank,
			u.clearFailure, u.failureCode, u.failureMessage, u.paymentID, refunded, u.review, g.Version, ev.Created)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrGatewayBusy
		}
		return nil
	})
}

// settleGateway records the succeeded payment and applies it through the payment path. It returns a review reason when the
// money could not all be applied as the customer chose.
func (s *Postgres) settleGateway(ctx context.Context, g GatewayPayment, ev gateway.Event, details *gateway.Details, bankRef string) (string, string, error) {
	mode := "CARD"
	if g.Method == string(gateway.MethodACH) {
		mode = "ACH"
	}
	if details != nil && details.Method == gateway.MethodACH {
		mode = "ACH"
	} else if details != nil && details.Method == gateway.MethodCard {
		mode = "CARD"
	}
	currency := ev.Currency
	if currency == "" {
		currency = g.Currency
	}
	amount := ev.Amount
	if !amount.IsPositive() {
		amount = g.Amount
	}
	remittance := make([]domain.RemittanceLine, 0, len(g.Allocations))
	for _, a := range g.Allocations {
		remittance = append(remittance, domain.RemittanceLine{Reference: a.DocumentNumber, Amount: a.Amount})
	}
	day := ev.Created.UTC().Format(dateLayout)
	in := Payment{LegalEntityID: g.LegalEntityID, CustomerID: &g.CustomerID, PaymentMode: mode, Source: "PORTAL", SourceRef: g.ProviderIntentID,
		Amount: amount, Currency: currency, ReceivedDate: day, ValueDate: day, Remittance: remittance, BankAccountRef: nilIfEmpty(bankRef)}
	pay, err := s.CreatePayment(ctx, in, "gateway:"+g.ID)
	if err != nil {
		if review, ok := reviewable(err); ok {
			// The payment could not be recorded at all (a clerk recorded the same provider payment by hand): nothing to apply.
			return "", "the payment could not be recorded: " + review, nil
		}
		return "", "", err
	}
	if err := s.setGatewayPaymentID(ctx, g.ID, pay.ID); err != nil {
		return "", "", err
	}
	if s.afterGatewayPayment != nil {
		s.afterGatewayPayment()
	}
	if pay.Status != "RECEIVED" || !pay.UnappliedAmount.Equal(pay.Amount) {
		// An earlier delivery of this event applied it already.
		return pay.ID, "", nil
	}
	if currency != g.Currency || !amount.Equal(g.Amount) {
		return pay.ID, fmt.Sprintf("%s %s was received for a payment of %s %s; it is unapplied", amount, currency, g.Amount, g.Currency), nil
	}
	req := ApplyRequest{}
	applied := decimal.Zero
	short := []string{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, a := range g.Allocations {
			item, err := scanOpenItem(tx.QueryRow(ctx, `select `+openItemColumns+` from open_item where id = $1`, a.OpenItemID))
			if err != nil {
				return err
			}
			amt := a.Amount
			if item.Status != "OPEN" && item.Status != "PARTIALLY_SETTLED" {
				amt = decimal.Zero
			} else if amt.GreaterThan(item.OpenAmount) {
				amt = item.OpenAmount
			}
			if !amt.Equal(a.Amount) {
				short = append(short, a.DocumentNumber)
			}
			if amt.IsPositive() {
				req.Allocations = append(req.Allocations, domain.Allocation{OpenItemID: a.OpenItemID, Amount: amt})
				applied = applied.Add(amt)
			}
		}
		return nil
	})
	if err != nil {
		return pay.ID, "", err
	}
	if applied.IsZero() {
		return pay.ID, "the invoices were paid meanwhile; the payment is unapplied", nil
	}
	req.Residual = amount.Sub(applied)
	if _, err := s.ApplyPayment(ctx, pay.ID, pay.Version, req, "gateway-apply:"+g.ID); err != nil {
		if review, ok := reviewable(err); ok {
			return pay.ID, "the payment could not be applied: " + review, nil
		}
		return pay.ID, "", err
	}
	if len(short) > 0 {
		return pay.ID, fmt.Sprintf("%s %s could not be applied to %s (already paid); it is on account", req.Residual, currency, strings.Join(short, ", ")), nil
	}
	return pay.ID, "", nil
}

// reverseGatewayPayment undoes a refunded or disputed payment through the unapply and receipt reversal path.
func (s *Postgres) reverseGatewayPayment(ctx context.Context, paymentID, reason string) (string, error) {
	pay, err := s.GetPayment(ctx, paymentID)
	if err != nil {
		return "", err
	}
	if pay.Status == "REVERSED" {
		return "", nil
	}
	if !pay.UnappliedAmount.Equal(pay.Amount) {
		if _, err := s.UnapplyPayment(ctx, pay.ID, pay.Version, reason); err != nil {
			if review, ok := reviewable(err); ok {
				return "the payment could not be unapplied: " + review, nil
			}
			return "", err
		}
		if pay, err = s.GetPayment(ctx, paymentID); err != nil {
			return "", err
		}
	}
	if _, err := s.ReversePayment(ctx, pay.ID, pay.Version); err != nil {
		if review, ok := reviewable(err); ok {
			return "the payment was unapplied but its receipt could not be reversed: " + review, nil
		}
		return "", err
	}
	return "", nil
}

// reviewable tells a refusal of receivables itself (left for a clerk) from a transient failure (the provider redelivers).
func reviewable(err error) (string, bool) {
	var pr *httpx.Problem
	if !errors.As(err, &pr) {
		return "", false
	}
	switch pr.Code {
	case domain.CodeItemLocked, CodeIdempotencyBusy, CodeVersionConflict:
		return "", false
	}
	if pr.Status >= 500 {
		return "", false
	}
	return pr.Detail, true
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
