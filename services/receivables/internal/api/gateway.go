package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/gateway"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/store"
)

// Card and ACH payments from the customer portal (#30, FR-30.14). The portal asks for a provider payment for open items of its
// own customer and gets back only the client secret of the provider's hosted element; card and bank details never reach
// Depot. The payment's status comes only from the provider's signature-verified webhooks, on their own listener.
const (
	dutyCustomerPortalPay = "receivables.customer-portal.pay"
	maxWebhookBody        = 256 << 10
)

// GatewayStore is the store side of the gateway payments; store.Postgres implements it.
type GatewayStore interface {
	CreateGatewayAttempt(ctx context.Context, in store.GatewayAttemptInput, key string) (store.GatewayPayment, bool, error)
	SetGatewayIntent(ctx context.Context, id, intentID string) error
	FailGatewayAttempt(ctx context.Context, id, code, message string) error
	ListGatewayPayments(ctx context.Context, f store.GatewayFilter, limit int) ([]store.GatewayPayment, error)
	GetGatewayPayment(ctx context.Context, id string) (store.GatewayPayment, error)
	ResolveGatewayIntent(ctx context.Context, provider, intentID string) (store.GatewayRoute, error)
	HandleGatewayEvent(ctx context.Context, attemptID string, ev gateway.Event, opts store.GatewayEventOptions) (string, error)
	CancelGatewayAttempt(ctx context.Context, id, customerID string, cancel func(g store.GatewayPayment) error) (store.GatewayPayment, error)
}

// GatewayConfig is the payment provider of this deployment.
type GatewayConfig struct {
	Gateway gateway.Gateway
	// Accounts maps a legal entity to its provider account (a Stripe Connect account id). Empty: every legal entity takes
	// payments on the platform account. Not empty: only the listed legal entities take payments, each on its own account.
	Accounts map[string]string
	// BankAccountRef is the ar_bank_account account_ref of the provider's clearing account (default STRIPE).
	BankAccountRef string
	Now            func() time.Time
}

// WithGateway turns on card and ACH payments with the provider in c.
func (a *API) WithGateway(c GatewayConfig) *API {
	if c.Now == nil {
		c.Now = time.Now
	}
	a.gw = &c
	return a
}

func (a *API) gatewayStore(w http.ResponseWriter, r *http.Request) (GatewayStore, bool) {
	gs, ok := a.store.(GatewayStore)
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, store.CodeGatewayOff, "card and bank payments are not available"))
	}
	return gs, ok
}

func (a *API) gatewayRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyView)).Get("/gateway-payments", a.listGatewayPayments)
	r.With(authz.RequireDuty(dutyView)).Get("/gateway-payments/{ref}", a.getGatewayPayment)
}

func (a *API) listGatewayPayments(w http.ResponseWriter, r *http.Request) {
	gs, ok := a.gatewayStore(w, r)
	if !ok || !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.GatewayFilter{Status: q.Get("status"), CustomerID: q.Get("customer"), PaymentID: q.Get("payment")}
	if f.Status != "" && !gatewayStatuses[f.Status] {
		badQuery(w, r, "status", "must be REQUIRES_PAYMENT, PROCESSING, SUCCEEDED, FAILED, REFUNDED or DISPUTED")
		return
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		badQuery(w, r, "customer", "must be a UUID")
		return
	}
	if f.PaymentID != "" && !ids.Valid(f.PaymentID) {
		badQuery(w, r, "payment", "must be a UUID")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	rows, err := gs.ListGatewayPayments(r.Context(), f, limit)
	if err != nil {
		a.fail("list gateway payments", w, r, err)
		return
	}
	for i := range rows {
		rows[i].FailureMessage = plainFailure(rows[i])
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (a *API) getGatewayPayment(w http.ResponseWriter, r *http.Request) {
	gs, ok := a.gatewayStore(w, r)
	if !ok || !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "ref")
	if !ids.Valid(id) {
		badQuery(w, r, "ref", "must be a UUID")
		return
	}
	g, err := gs.GetGatewayPayment(r.Context(), id)
	if err != nil {
		a.fail("get gateway payment", w, r, err)
		return
	}
	g.FailureMessage = plainFailure(g)
	httpx.JSON(w, http.StatusOK, g)
}

var gatewayStatuses = map[string]bool{
	store.GatewayRequiresPayment: true, store.GatewayProcessing: true, store.GatewaySucceeded: true,
	store.GatewayFailed: true, store.GatewayRefunded: true, store.GatewayDisputed: true,
}

func plainFailure(g store.GatewayPayment) *string {
	if g.FailureCode == nil {
		return g.FailureMessage
	}
	msg := gateway.PlainFailure(*g.FailureCode)
	return &msg
}

// portalIntentRequest is the portal's payment: how the customer pays and how much on which of its open items.
type portalIntentRequest struct {
	Method      string `json:"method"`
	Allocations []struct {
		OpenItemID string          `json:"openItemId"`
		Amount     decimal.Decimal `json:"amount"`
	} `json:"allocations"`
}

// portalIntent is a payment as the customer sees it. ClientSecret is set only on the answer that created the provider payment
// (and its replays); it is never stored.
type portalIntent struct {
	ID             string                   `json:"id"`
	Provider       string                   `json:"provider"`
	Method         string                   `json:"method"`
	Status         string                   `json:"status"`
	Amount         decimal.Decimal          `json:"amount"`
	Currency       string                   `json:"currency"`
	CardBrand      *string                  `json:"cardBrand,omitempty"`
	Last4          *string                  `json:"last4,omitempty"`
	BankName       *string                  `json:"bankName,omitempty"`
	FailureMessage *string                  `json:"failureMessage,omitempty"`
	CreatedAt      time.Time                `json:"createdAt"`
	Allocations    []portalIntentAllocation `json:"allocations"`
	ClientSecret   string                   `json:"clientSecret,omitempty"`
	PublishableKey string                   `json:"publishableKey,omitempty"`
	StripeAccount  string                   `json:"stripeAccount,omitempty"`
}

type portalIntentAllocation struct {
	DocumentNumber string          `json:"documentNumber"`
	Amount         decimal.Decimal `json:"amount"`
}

func toPortalIntent(g store.GatewayPayment) portalIntent {
	out := portalIntent{ID: g.ID, Provider: g.Provider, Method: g.Method, Status: g.Status, Amount: g.Amount, Currency: g.Currency,
		CardBrand: g.CardBrand, Last4: g.Last4, BankName: g.BankName, FailureMessage: plainFailure(g), CreatedAt: g.CreatedAt,
		Allocations: make([]portalIntentAllocation, 0, len(g.Allocations))}
	for _, a := range g.Allocations {
		out.Allocations = append(out.Allocations, portalIntentAllocation{DocumentNumber: a.DocumentNumber, Amount: a.Amount})
	}
	return out
}

func (a *API) portalCreateIntent(w http.ResponseWriter, r *http.Request, customer string) {
	gs, ok := a.gatewayStore(w, r)
	if !ok {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	le := p.LegalEntityID
	if !ids.Valid(le) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AR-CUS-001", "this account is not a customer account"))
		return
	}
	if a.gw == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, store.CodeGatewayOff, "card and bank payments are not set up; pay by bank transfer"))
		return
	}
	account := ""
	if len(a.gw.Accounts) > 0 {
		acct, ok := a.gw.Accounts[strings.ToLower(le)]
		if !ok {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, store.CodeGatewayOff, "card and bank payments are not set up for this company; pay by bank transfer"))
			return
		}
		account = acct
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) < 8 || len(key) > maxKeyLength {
		badQuery(w, r, idempotencyKey, "send an Idempotency-Key of 8 to 200 characters, one per payment")
		return
	}
	var body portalIntentRequest
	if err := httpx.DecodeLimit(r, &body, 64<<10); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "the body must be a JSON payment with method and allocations"))
		return
	}
	in := store.GatewayAttemptInput{LegalEntityID: strings.ToLower(le), CustomerID: customer, Provider: a.gw.Gateway.Provider(), ProviderAccount: account,
		Method: gateway.Method(body.Method), ContactID: p.UserID, CheckAmount: a.gw.Gateway.CheckAmount}
	if in.Method != gateway.MethodCard && in.Method != gateway.MethodACH {
		badQuery(w, r, "method", "must be CARD or ACH")
		return
	}
	for i, al := range body.Allocations {
		if !ids.Valid(al.OpenItemID) {
			badQuery(w, r, "allocations", "item "+strconv.Itoa(i+1)+" needs an openItemId UUID")
			return
		}
		in.Allocations = append(in.Allocations, store.GatewayAllocation{OpenItemID: al.OpenItemID, Amount: al.Amount})
	}
	g, replay, err := gs.CreateGatewayAttempt(r.Context(), in, key)
	if err != nil {
		a.fail("create portal payment", w, r, err)
		return
	}
	if replay && g.Status != store.GatewayRequiresPayment {
		// The payment went on already (or failed): the customer sees its status, not a second payment form.
		httpx.JSON(w, http.StatusOK, toPortalIntent(g))
		return
	}
	docs := make([]string, 0, len(g.Allocations))
	for _, al := range g.Allocations {
		docs = append(docs, al.DocumentNumber)
	}
	intent, err := a.gw.Gateway.CreateIntent(r.Context(), gateway.IntentRequest{
		AttemptID: g.ID, Amount: g.Amount, Currency: g.Currency, Method: gateway.Method(g.Method), Account: account,
		Description: "Payment of " + strings.Join(docs, ", "),
		Metadata: map[string]string{
			"depot_tenant_id": p.TenantID, "depot_legal_entity_id": g.LegalEntityID, "depot_attempt_id": g.ID,
			"depot_customer_id": g.CustomerID, "depot_documents": strings.Join(docs, ","),
		},
	})
	if err != nil {
		var ge *gateway.Error
		if errors.As(err, &ge) && ge.Refused() {
			_ = gs.FailGatewayAttempt(r.Context(), g.ID, ge.Code, gateway.PlainFailure(ge.Code))
			slog.Warn("payment provider refused a payment", "attempt", g.ID, "code", ge.Code, "status", ge.Status, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeGatewayRefused, gateway.PlainFailure(ge.Code)))
			return
		}
		a.failed.Add(1)
		slog.Error("payment provider unavailable", "attempt", g.ID, "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, store.CodeGatewayUnavailable, "the payment service could not be reached; try again in a minute"))
		return
	}
	if err := gs.SetGatewayIntent(r.Context(), g.ID, intent.ID); err != nil {
		a.fail("store provider payment", w, r, err)
		return
	}
	out := toPortalIntent(g)
	out.ClientSecret, out.PublishableKey, out.StripeAccount = intent.ClientSecret, a.gw.Gateway.PublishableKey(), account
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	httpx.JSON(w, status, out)
}

// portalIntentAction serves POST /customer-portal/payment-intents/{id}:cancel: the customer went back to change the amounts,
// so the abandoned payment is cancelled at the provider and stops reserving what it would have paid.
func (a *API) portalIntentAction(w http.ResponseWriter, r *http.Request, customer string) {
	gs, ok := a.gatewayStore(w, r)
	if !ok {
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "ref"), ":")
	if !found || !ids.Valid(id) || action != "cancel" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeGatewayNotFound, "payment not found"))
		return
	}
	var cancel func(g store.GatewayPayment) error
	if a.gw != nil {
		gw := a.gw.Gateway
		cancel = func(g store.GatewayPayment) error {
			err := gw.CancelIntent(r.Context(), deref(g.ProviderAccount), deref(g.ProviderIntentID), g.ID)
			var ge *gateway.Error
			if errors.As(err, &ge) && ge.Code == "payment_intent_unexpected_state" {
				return httpx.NewProblem(http.StatusConflict, store.CodeGatewayState, "the payment is already under way and cannot be cancelled")
			}
			if err != nil {
				slog.Error("payment provider cancel failed", "attempt", g.ID, "err", err, "correlationId", middleware.GetReqID(r.Context()))
				return httpx.NewProblem(http.StatusBadGateway, store.CodeGatewayUnavailable, "the payment service could not be reached; try again in a minute")
			}
			return nil
		}
	}
	g, err := gs.CancelGatewayAttempt(r.Context(), id, customer, cancel)
	if errors.Is(err, store.ErrGatewayBusy) {
		err = httpx.NewProblem(http.StatusConflict, store.CodeGatewayState, "the payment is changing right now; reload it")
	}
	if err != nil {
		a.fail("cancel portal payment", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPortalIntent(g))
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (a *API) portalListIntents(w http.ResponseWriter, r *http.Request, customer string) {
	gs, ok := a.gatewayStore(w, r)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	rows, err := gs.ListGatewayPayments(r.Context(), store.GatewayFilter{CustomerID: customer}, limit)
	if err != nil {
		a.fail("list portal payments", w, r, err)
		return
	}
	out := make([]portalIntent, 0, len(rows))
	for _, g := range rows {
		if strings.EqualFold(g.CustomerID, customer) {
			out = append(out, toPortalIntent(g))
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) portalGetIntent(w http.ResponseWriter, r *http.Request, customer string) {
	gs, ok := a.gatewayStore(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "ref")
	if !ids.Valid(id) {
		badQuery(w, r, "ref", "must be a UUID")
		return
	}
	g, err := gs.GetGatewayPayment(r.Context(), id)
	if err == nil && !strings.EqualFold(g.CustomerID, customer) {
		err = httpx.NewProblem(http.StatusNotFound, store.CodeGatewayNotFound, "payment not found")
	}
	if err != nil {
		a.fail("get portal payment", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toPortalIntent(g))
}

// WebhookRoutes is the handler of the payment provider's webhook listener (RECEIVABLES_WEBHOOK_ADDR). The provider calls it
// without a Depot identity, so it has no tenant middleware and reads no X-Depot-* header: the signature (Stripe-Signature,
// HMAC-SHA256 over the timestamp and the raw body, at most 5 minutes old) is checked before anything is parsed, the provider
// payment id then selects the attempt (tenant and legal entity come from it), and the event id makes a redelivery or a
// replayed request a no-op. Mount it on its own listener and expose only that through the public route.
//
//	POST /webhooks/v1/stripe
func (a *API) WebhookRoutes() http.Handler {
	r := httpx.NewRouterWithoutTenant()
	r.Post("/webhooks/v1/stripe", a.stripeWebhook)
	return r
}

func (a *API) stripeWebhook(w http.ResponseWriter, r *http.Request) {
	if a.gw == nil || a.gw.Gateway.Provider() != "STRIPE" {
		http.NotFound(w, r)
		return
	}
	gs, ok := a.store.(GatewayStore)
	if !ok {
		http.NotFound(w, r)
		return
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	ev, err := a.gw.Gateway.ParseWebhook(payload, r.Header, a.gw.Now())
	switch {
	case errors.Is(err, gateway.ErrSignature), errors.Is(err, gateway.ErrStale):
		slog.Warn("payment webhook refused", "reason", err.Error(), "correlationId", middleware.GetReqID(r.Context()))
		http.Error(w, "signature does not verify", http.StatusBadRequest)
		return
	case err != nil:
		slog.Warn("payment webhook unreadable", "reason", err.Error(), "correlationId", middleware.GetReqID(r.Context()))
		http.Error(w, "event unreadable", http.StatusBadRequest)
		return
	}
	if ev.Kind == gateway.KindIgnored {
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	route, err := gs.ResolveGatewayIntent(r.Context(), a.gw.Gateway.Provider(), ev.IntentID)
	if errors.Is(err, store.ErrGatewayRouteNotFound) {
		// Another system's payment on the same provider account: acknowledged so the provider stops redelivering it.
		slog.Info("payment webhook for an unknown provider payment", "event", ev.ID, "type", ev.Type)
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	if err != nil {
		slog.Error("payment webhook route lookup", "event", ev.ID, "err", err)
		http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
		return
	}
	if attempt := ev.Metadata["depot_attempt_id"]; attempt != "" && !strings.EqualFold(attempt, route.GatewayPaymentID) {
		slog.Warn("payment webhook metadata names another attempt", "event", ev.ID, "attempt", route.GatewayPaymentID)
		httpx.JSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	ctx := store.GatewayContext(r.Context(), route)
	gw := a.gw.Gateway
	outcome, err := gs.HandleGatewayEvent(ctx, route.GatewayPaymentID, ev, store.GatewayEventOptions{
		BankAccountRef: a.gw.BankAccountRef,
		Details: func(ctx context.Context) (gateway.Details, error) {
			return gw.PaymentDetails(ctx, ev.Account, ev.IntentID)
		},
	})
	if errors.Is(err, store.ErrGatewayBusy) {
		// Another event of the same attempt is being handled: the provider redelivers this one after it.
		slog.Warn("payment webhook deferred: the attempt is busy", "event", ev.ID, "type", ev.Type, "attempt", route.GatewayPaymentID)
		http.Error(w, "busy; retry", http.StatusConflict)
		return
	}
	if err != nil {
		// The provider redelivers; every step is idempotent.
		a.failed.Add(1)
		slog.Error("payment webhook handling failed", "event", ev.ID, "type", ev.Type, "attempt", route.GatewayPaymentID, "err", err)
		http.Error(w, "temporarily unavailable", http.StatusInternalServerError)
		return
	}
	slog.Info("payment webhook handled", "event", ev.ID, "type", ev.Type, "attempt", route.GatewayPaymentID, "outcome", outcome)
	httpx.JSON(w, http.StatusOK, map[string]string{"status": outcome})
}
