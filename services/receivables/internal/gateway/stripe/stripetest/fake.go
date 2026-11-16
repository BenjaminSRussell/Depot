// Package stripetest is a fake Stripe API for tests: it answers PaymentIntent creates and reads the way Stripe does (the same
// PaymentIntent for a repeated Idempotency-Key), records every request, and builds signed webhook events. No test reaches Stripe.
package stripetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/BenjaminSRussell/passage/services/receivables/internal/gateway/stripe"
)

// Request is one call the fake received.
type Request struct {
	Method string
	Path   string
	Header http.Header
	Form   url.Values
}

// Server is the fake Stripe.
type Server struct {
	*httptest.Server
	mu       sync.Mutex
	requests []Request
	byKey    map[string]map[string]any
	n        int
	// Fail, when set, is answered to the next create: the HTTP status and Stripe's error code.
	FailStatus int
	FailCode   string
	// CancelRefused, when set, makes cancels answer that the PaymentIntent is already processing.
	CancelRefused bool
	// Method is the payment method the reads report: "card" (Visa 4242, 12/2030) or "us_bank_account" (STRIPE TEST BANK 6789).
	Method string
}

// New starts the fake.
func New() *Server {
	s := &Server{byKey: map[string]map[string]any{}, Method: "card"}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Requests returns the calls received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	form := r.PostForm
	if r.Method == http.MethodGet {
		form = r.URL.Query()
	}
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Header: r.Header.Clone(), Form: form})
	w.Header().Set("Content-Type", "application/json")
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk_test_") {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"Invalid API Key provided"}}`))
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/payment_intents":
		if s.FailStatus != 0 {
			w.WriteHeader(s.FailStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"type": "invalid_request_error", "code": s.FailCode, "message": "refused by the fake"}})
			s.FailStatus, s.FailCode = 0, ""
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if pi, ok := s.byKey[key]; ok && key != "" {
			_ = json.NewEncoder(w).Encode(pi)
			return
		}
		s.n++
		id := fmt.Sprintf("pi_fake%04d", s.n)
		pi := map[string]any{"id": id, "object": "payment_intent", "client_secret": id + "_secret_fake", "status": "requires_payment_method",
			"amount": form.Get("amount"), "currency": form.Get("currency")}
		s.byKey[key] = pi
		_ = json.NewEncoder(w).Encode(pi)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/payment_intents/") && strings.HasSuffix(r.URL.Path, "/cancel"):
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/payment_intents/"), "/cancel")
		if s.CancelRefused {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"payment_intent_unexpected_state","message":"This PaymentIntent's status is processing."}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "object": "payment_intent", "status": "canceled"})
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/payment_intents/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/payment_intents/")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "object": "payment_intent", "payment_method": PaymentMethod(s.Method)})
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"Unrecognized request URL"}}`))
	}
}

// PaymentMethod is a Stripe payment method object of the given type, with test details only.
func PaymentMethod(kind string) map[string]any {
	if kind == "us_bank_account" {
		return map[string]any{"id": "pm_bank", "type": "us_bank_account", "us_bank_account": map[string]any{"bank_name": "STRIPE TEST BANK", "last4": "6789"}}
	}
	return map[string]any{"id": "pm_card", "type": "card", "card": map[string]any{"brand": "visa", "last4": "4242", "exp_month": 12, "exp_year": 2030}}
}

// Event is a Stripe event JSON for the object.
func Event(id, typ string, created time.Time, object map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"id": id, "object": "event", "type": typ, "created": created.Unix(), "livemode": false,
		"api_version": stripe.APIVersion, "data": map[string]any{"object": object}})
	return b
}

// Intent is a PaymentIntent object for an event.
func Intent(id string, amount int64, currency string, metadata map[string]string) map[string]any {
	return map[string]any{"id": id, "object": "payment_intent", "amount": amount, "amount_received": amount, "currency": currency, "metadata": metadata}
}

// Signed signs payload with secret at the given time, as Stripe does.
func Signed(payload []byte, secret string, at time.Time) http.Header {
	h := http.Header{}
	h.Set("Stripe-Signature", stripe.Sign(payload, secret, at))
	h.Set("Content-Type", "application/json")
	return h
}
