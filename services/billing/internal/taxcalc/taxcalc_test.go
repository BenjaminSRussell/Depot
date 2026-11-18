package taxcalc_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/billing/internal/api"
	"github.com/BenjaminSRussell/depot/services/billing/internal/domain"
	"github.com/BenjaminSRussell/depot/services/billing/internal/taxcalc"
)

const leID = "0190f0a0-0000-7000-8000-000000000001"

type engine struct {
	verifier *tenant.ServiceVerifier
	mu       sync.Mutex
	status   int
	answer   string
	claims   tenant.ServiceClaims
	path     string
	body     map[string]any
}

func (e *engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	claims, err := e.verifier.Verify(r.Header.Get(tenant.HeaderServiceToken))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	e.mu.Lock()
	e.claims, e.path = claims, r.URL.Path
	e.body = map[string]any{}
	_ = json.Unmarshal(raw, &e.body)
	status, answer := e.status, e.answer
	e.mu.Unlock()
	if status == http.StatusFound {
		http.Redirect(w, r, "/elsewhere", status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(answer))
}

func newClient(t *testing.T, status int, answer string, timeout time.Duration) (*taxcalc.Client, *engine) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("billing", priv)
	if err != nil {
		t.Fatal(err)
	}
	e := &engine{verifier: tenant.NewServiceVerifier("tax", map[string]ed25519.PublicKey{"billing": pub}), status: status, answer: answer}
	srv := httptest.NewServer(e)
	t.Cleanup(srv.Close)
	c, err := taxcalc.New(srv.URL, signer, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return c, e
}

func callerCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "clerk", Duties: map[string]struct{}{"billing.invoice.maintain": {}}})
}

func lines(n int) []domain.Line {
	out := make([]domain.Line, n)
	for i := range out {
		out[i] = domain.Line{No: i + 1, Description: "Widget", Qty: decimal.NewFromInt(2), UnitPrice: decimal.NewFromInt(50), DiscountPct: decimal.Zero,
			RevenueAccount: "4000", TaxCode: "DE19", ItemTaxGroup: "GOODS"}
	}
	return out
}

func input(n int) api.QuoteInput {
	return api.QuoteInput{LegalEntityID: leID, Currency: "EUR", Date: time.Date(2027, 3, 10, 0, 0, 0, 0, time.UTC), Lines: lines(n),
		ShipTo: &domain.ShipTo{Country: "DE", TaxGroup: "B2B", TaxID: "DE123456789", Certificate: "CERT-1"}}
}

func TestQuote_SendsTheShipToAndReadsTaxAndCodePerLine(t *testing.T) {
	c, e := newClient(t, http.StatusOK, `{"lines":[
		{"line_ref":"1","base":"100","taxes":[{"code":"DE-REDUCED","tax_amount":"7.00"}]},
		{"line_ref":"0","base":"100","taxes":[{"code":"DE-STD","tax_amount":"19.00"}]}]}`, time.Second)
	got, err := c.Quote(callerCtx(), input(2))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Taxes[0].Equal(decimal.RequireFromString("19")) || !got.Taxes[1].Equal(decimal.RequireFromString("7")) || got.Codes[0] != "DE-STD" || got.Codes[1] != "DE-REDUCED" {
		t.Errorf("answer %+v, want the engine's tax and code matched by line_ref", got)
	}
	if e.path != "/v1/tax/calculate" || e.claims.Iss != "billing" || e.claims.Aud != "tax" || e.claims.Act != "service:billing-tax-calc" ||
		len(e.claims.Duties) != 1 || e.claims.Duties[0] != "tax.tax.calculate" || e.claims.Le != leID || e.claims.Tid != testkit.TenantID(1) {
		t.Errorf("call %s signed %+v", e.path, e.claims)
	}
	party, _ := e.body["party"].(map[string]any)
	first, _ := e.body["lines"].([]any)[0].(map[string]any)
	if e.body["currency"] != "EUR" || e.body["direction"] != "output" || e.body["document_date"] != "2027-03-10" || e.body["tax_date"] != "2027-03-10" ||
		e.body["stored_config"] != true || party["country"] != "DE" || party["tax_group"] != "B2B" || party["tax_id"] != "DE123456789" || party["certificate"] != "CERT-1" ||
		first["line_ref"] != "0" || first["amount"] != "100" || first["item_tax_group"] != "GOODS" {
		t.Errorf("request body %v", e.body)
	}
}

func TestQuote_CodeOfALineWithSeveralTaxesIsTheLargest(t *testing.T) {
	c, _ := newClient(t, http.StatusOK, `{"lines":[{"line_ref":"0","taxes":[{"code":"STATE","tax_amount":"4.00"},{"code":"FEDERAL","tax_amount":"9.00"},{"code":"LOCAL","tax_amount":"1.00"}]}]}`, time.Second)
	got, err := c.Quote(callerCtx(), input(1))
	if err != nil || got.Codes[0] != "FEDERAL" || !got.Taxes[0].Equal(decimal.RequireFromString("14")) {
		t.Fatalf("got %+v, %v, want FEDERAL and 14", got, err)
	}
}

func TestQuote_ExemptLineHasNoCodeAndNoTax(t *testing.T) {
	c, _ := newClient(t, http.StatusOK, `{"lines":[{"line_ref":"0","base":"100","taxes":[]}]}`, time.Second)
	in := input(1)
	in.ShipTo = nil
	got, err := c.Quote(callerCtx(), in)
	if err != nil || !got.Taxes[0].IsZero() || got.Codes[0] != "" {
		t.Fatalf("got %+v, %v, want no tax and no code", got, err)
	}
}

func TestQuote_WithoutShipToSendsAnEmptyParty(t *testing.T) {
	c, e := newClient(t, http.StatusOK, `{"lines":[{"line_ref":"0","taxes":[]}]}`, time.Second)
	in := input(1)
	in.ShipTo = nil
	if _, err := c.Quote(callerCtx(), in); err != nil {
		t.Fatal(err)
	}
	if party, _ := e.body["party"].(map[string]any); len(party) != 0 {
		t.Errorf("party %v, want empty", party)
	}
}

func TestQuote_RefusalsAndBadAnswers(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		answer  string
		unknown bool
	}{
		{name: "server error", status: http.StatusInternalServerError, answer: `{"code":"COM-0500"}`},
		{name: "bad request", status: http.StatusBadRequest, answer: `{"code":"COM-0001"}`},
		{name: "forbidden", status: http.StatusForbidden, answer: `{"code":"AUTH-0003"}`},
		{name: "unprocessable is a refused tax code", status: http.StatusUnprocessableEntity, answer: `{"code":"group_unresolved"}`, unknown: true},
		{name: "redirect", status: http.StatusFound},
		{name: "not json", status: http.StatusOK, answer: `<html>`},
		{name: "fewer lines than asked", status: http.StatusOK, answer: `{"lines":[]}`},
		{name: "unknown line ref", status: http.StatusOK, answer: `{"lines":[{"line_ref":"9","taxes":[]}]}`},
		{name: "negative tax", status: http.StatusOK, answer: `{"lines":[{"line_ref":"0","taxes":[{"code":"X","tax_amount":"-1.00"}]}]}`},
		{name: "tax that is not a number", status: http.StatusOK, answer: `{"lines":[{"line_ref":"0","taxes":[{"code":"X","tax_amount":"NaN"}]}]}`},
		{name: "empty tax", status: http.StatusOK, answer: `{"lines":[{"line_ref":"0","taxes":[{"code":"X","tax_amount":""}]}]}`},
		{name: "oversize body", status: http.StatusOK, answer: `{"pad":"` + strings.Repeat("x", 1<<20) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newClient(t, tc.status, tc.answer, time.Second)
			_, err := c.Quote(callerCtx(), input(1))
			var unknown *api.UnknownTaxCodeError
			switch {
			case tc.unknown && !errors.As(err, &unknown):
				t.Fatalf("err %v, want UnknownTaxCodeError", err)
			case !tc.unknown && !errors.Is(err, api.ErrTaxUnavailable):
				t.Fatalf("err %v, want ErrTaxUnavailable", err)
			}
		})
	}
}

func TestQuote_TimeoutIsUnavailable(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { time.Sleep(300 * time.Millisecond) }))
	t.Cleanup(slow.Close)
	_, priv, _ := ed25519.GenerateKey(nil)
	signer, err := tenant.NewServiceSigner("billing", priv)
	if err != nil {
		t.Fatal(err)
	}
	c, err := taxcalc.New(slow.URL, signer, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Quote(callerCtx(), input(1)); !errors.Is(err, api.ErrTaxUnavailable) {
		t.Fatalf("err %v, want ErrTaxUnavailable", err)
	}
}

func TestQuote_NeedsACallerPrincipal(t *testing.T) {
	c, _ := newClient(t, http.StatusOK, `{"lines":[]}`, time.Second)
	if _, err := c.Quote(context.Background(), input(1)); err == nil || errors.Is(err, api.ErrTaxUnavailable) {
		t.Fatalf("err %v, want a plain error: a call without a tenant is a bug, not an outage", err)
	}
}

func TestFromEnv(t *testing.T) {
	if c, err := taxcalc.FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Errorf("TAX_URL unset: %v %v, want nil, nil", c, err)
	}
	withURL := func(k string) string {
		if k == "TAX_URL" {
			return "http://tax"
		}
		return ""
	}
	if c, err := taxcalc.FromEnv(withURL); c != nil || err == nil {
		t.Errorf("TAX_URL without a signing key: %v %v, want an error", c, err)
	}
}
