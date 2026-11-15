package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
	"github.com/BenjaminSRussell/passage/services/sales/internal/upstream"
)

const (
	prodNew      = "0195f0a0-0000-7000-8000-0000000000d3"
	prodDenied   = "0195f0a0-0000-7000-8000-0000000000d4"
	prodUnpriced = "0195f0a0-0000-7000-8000-0000000000d5"
	prodOpen     = "0195f0a0-0000-7000-8000-0000000000d6"
	groupA       = "0195f0a0-0000-7000-8000-0000000000a9"
)

// listedStore adds product listings to the portal store.
type listedStore struct {
	*portalWriteStore
	listings []store.ProductListing
}

func (s *listedStore) ListListings(_ context.Context, f store.ListingFilter) ([]store.ProductListing, error) {
	var out []store.ProductListing
	for _, l := range s.listings {
		if (f.ProductID == "" || l.ProductID == f.ProductID) && (f.ScopeKey == "" || l.ScopeKey == f.ScopeKey) {
			out = append(out, l)
		}
	}
	return out, nil
}

// negotiatedPricer answers as pricing does: the customer's negotiated net price per product, PRC-0001 for a product with none.
type negotiatedPricer struct {
	net   map[string]string
	err   error
	asked []upstream.PriceRequest
}

func (p *negotiatedPricer) ResolvePrices(_ context.Context, req upstream.PriceRequest) ([]upstream.PricedLine, error) {
	p.asked = append(p.asked, req)
	if p.err != nil {
		return nil, p.err
	}
	out := make([]upstream.PricedLine, len(req.Lines))
	for i, l := range req.Lines {
		out[i] = upstream.PricedLine{LineNo: i + 1, Unit: l.Unit, ErrorCode: "PRC-0001"}
		if n, ok := p.net[l.ProductID]; ok {
			out[i] = upstream.PricedLine{LineNo: i + 1, Unit: l.Unit, UnitPrice: decimal.RequireFromString("100"), NetUnitPrice: decimal.RequireFromString(n)}
		}
	}
	return out, nil
}

func listing(scope, key, product, typ string) store.ProductListing {
	return store.ProductListing{ScopeType: scope, ScopeKey: key, ProductID: product, ListingType: typ, LegalEntityID: portLE}
}

func newFirstOrderHarness(t *testing.T, s *listedStore, pricer PortalPricer) *writeHarness {
	t.Helper()
	c := newCustomerHarness(t, s)
	r := chi.NewRouter()
	r.Use(tenant.Middleware)
	api := New(s).WithCustomerGroups(fixedGroups{}).WithPortalPricer(pricer)
	api.Register(r)
	return &writeHarness{h: r, signer: c.signer, s: s.portalWriteStore}
}

func firstOrderStore() *listedStore {
	return &listedStore{portalWriteStore: newPortalWriteStore(), listings: []store.ProductListing{
		listing("GROUP", groupA, prodNew, "ALLOW"),
		listing("CUSTOMER", custA, prodDenied, "ALLOW"),
		listing("GROUP", groupA, prodDenied, "ALLOW"),
		listing("CUSTOMER", custA, prodDenied, "DENY"),
		listing("GROUP", groupA, prodUnpriced, "ALLOW"),
		// prodOpen has no ALLOW anywhere: unrestricted for staff, but the portal offers only what is listed.
		listing("CUSTOMER", custB, prodOnlyB, "ALLOW"),
	}}
}

func firstOrderBody(lines ...string) string {
	return `{"requestedShipDate":"` + futureD + `","lines":[` + strings.Join(lines, ",") + `]}`
}

func line(product, unit, qty string) string {
	u := ""
	if unit != "" {
		u = `"unit":"` + unit + `",`
	}
	return `{"productId":"` + product + `",` + u + `"quantity":"` + qty + `"}`
}

// Worked example: customer A has bought only the pump (USD, 120 less 10 percent). Its group lists a new product; pricing resolves
// the customer's negotiated net price 87.5000 for 4 EA, so the line is 350.00, taking the pump line's site and warehouse. A reorder in
// the same order keeps its history price (324 for 3).
func TestCustomerPortal_FirstOrderOfAListedProductUsesPricingsNegotiatedPrice(t *testing.T) {
	s := firstOrderStore()
	p := &negotiatedPricer{net: map[string]string{prodNew: "87.5000"}}
	w := newFirstOrderHarness(t, s, p)
	rec := w.post(t, custA, customerPortal+"/orders", "submit-0001", firstOrderBody(line(prodNew, "EA", "4"), line(prodPump, "", "3")))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first order: %d %s", rec.Code, rec.Body)
	}
	if len(p.asked) != 1 {
		t.Fatalf("pricing asked %d times, want once", len(p.asked))
	}
	ask := p.asked[0]
	if ask.CustomerID != custA || ask.CustomerGroupID != groupA || ask.LegalEntityID != portLE || ask.Currency != "USD" || ask.SalesChannel != "PORTAL" ||
		ask.PricingDate != futureD || len(ask.Lines) != 1 || ask.Lines[0].ProductID != prodNew || ask.Lines[0].Unit != "EA" || ask.Lines[0].Quantity != "4" {
		t.Fatalf("pricing was asked %+v", ask)
	}
	in := s.createdOrder[0]
	if len(in.Lines) != 2 || in.Lines[0].ProductID != prodNew || in.Lines[1].ProductID != prodPump {
		t.Fatalf("lines %+v keep the request order", in.Lines)
	}
	n := in.Lines[0]
	if !n.UnitPrice.Equal(decimal.RequireFromString("87.5")) || !n.DiscPct.IsZero() || n.SiteID != siteA || n.WarehouseID != whA || !n.LineNet.Equal(decimal.RequireFromString("350")) {
		t.Errorf("new line %+v, want net 87.5 x 4 = 350 at the pump line's site and warehouse", n)
	}
	if r := in.Lines[1]; !r.LineNet.Equal(decimal.RequireFromString("324")) {
		t.Errorf("reorder %+v keeps its history price", r)
	}
	if in.CanOverrideListing {
		t.Error("a portal order never overrides a listing")
	}
}

// A first-ever product for a customer with an earlier order of something else takes that order's terms.
func TestCustomerPortal_FirstOrderTakesTheTermsOfTheMostRecentOrder(t *testing.T) {
	s := firstOrderStore()
	p := &negotiatedPricer{net: map[string]string{prodNew: "10"}}
	w := newFirstOrderHarness(t, s, p)
	if rec := w.post(t, custA, customerPortal+"/orders", "submit-0002", firstOrderBody(line(prodNew, "EA", "2"))); rec.Code != http.StatusCreated {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	in := s.createdOrder[0]
	if in.Currency != "USD" || in.InvoiceAccountID != acctA || in.Lines[0].SiteID != siteA {
		t.Fatalf("terms %+v", in)
	}
}

func TestCustomerPortal_FirstOrderRefusals(t *testing.T) {
	cases := []struct {
		name, body string
		pricer     *negotiatedPricer
		want       int
		field      string
	}{
		{"a product no listing names", firstOrderBody(line(prodOpen, "EA", "1")), &negotiatedPricer{net: map[string]string{prodOpen: "5"}}, 422, "lines[0].productId"},
		{"a product only another customer is listed for", firstOrderBody(line(prodOnlyB, "EA", "1")), &negotiatedPricer{net: map[string]string{prodOnlyB: "5"}}, 422, "lines[0].productId"},
		{"a customer DENY beats a group ALLOW", firstOrderBody(line(prodDenied, "EA", "1")), &negotiatedPricer{net: map[string]string{prodDenied: "5"}}, 422, "lines[0].productId"},
		{"a listed product pricing has no price for", firstOrderBody(line(prodUnpriced, "EA", "1")), &negotiatedPricer{}, 422, "lines[0].productId"},
		{"a listed product without a unit", firstOrderBody(line(prodNew, "", "1")), &negotiatedPricer{net: map[string]string{prodNew: "5"}}, 422, ""},
		{"pricing down", firstOrderBody(line(prodNew, "EA", "1")), &negotiatedPricer{err: upstream.ErrNotConfigured}, 503, ""},
	}
	for _, tc := range cases {
		s := firstOrderStore()
		w := newFirstOrderHarness(t, s, tc.pricer)
		rec := w.post(t, custA, customerPortal+"/orders", "submit-0003", tc.body)
		if rec.Code != tc.want && !(tc.name == "a listed product without a unit" && rec.Code == 400) {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body, tc.want)
		}
		if tc.field != "" && !strings.Contains(rec.Body.String(), tc.field) {
			t.Errorf("%s: %s does not name %s", tc.name, rec.Body, tc.field)
		}
		if len(s.createdOrder) != 0 {
			t.Errorf("%s: an order was stored", tc.name)
		}
	}
}

// Without a pricing source the portal keeps its old rule: a product never bought is refused, listed or not.
func TestCustomerPortal_WithoutPricingAFirstOrderIsStillRefused(t *testing.T) {
	s := firstOrderStore()
	c := newCustomerHarness(t, s)
	r := chi.NewRouter()
	r.Use(tenant.Middleware)
	New(s).WithCustomerGroups(fixedGroups{}).Register(r)
	w := &writeHarness{h: r, signer: c.signer, s: s.portalWriteStore}
	if rec := w.post(t, custA, customerPortal+"/orders", "submit-0004", firstOrderBody(line(prodNew, "EA", "1"))); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// The product list offers the listed products the customer has not bought, never one a DENY covers or another customer's listing.
func TestCustomerPortal_ProductListOffersListedProducts(t *testing.T) {
	s := firstOrderStore()
	w := newFirstOrderHarness(t, s, &negotiatedPricer{})
	rec := w.get(t, custA, customerPortal+"/products")
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var out struct {
		Items  []portalProduct       `json:"items"`
		Listed []portalListedProduct `json:"listed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, l := range out.Listed {
		got[l.ProductID] = true
	}
	if len(out.Items) != 1 || !got[prodNew] || !got[prodUnpriced] || got[prodDenied] || got[prodOnlyB] || got[prodPump] || len(got) != 2 {
		t.Fatalf("items %+v listed %+v", out.Items, out.Listed)
	}
}

type failingGroups struct{}

func (failingGroups) CustomerGroup(context.Context, string, string) (string, error) {
	return "", errors.New("party is down")
}

// If party is down the product list still answers with the reorder history; only the listed products are left out.
func TestCustomerPortal_ProductListSurvivesAPartyOutage(t *testing.T) {
	s := firstOrderStore()
	c := newCustomerHarness(t, s)
	r := chi.NewRouter()
	r.Use(tenant.Middleware)
	New(s).WithCustomerGroups(failingGroups{}).WithPortalPricer(&negotiatedPricer{}).Register(r)
	w := &writeHarness{h: r, signer: c.signer, s: s.portalWriteStore}
	rec := w.get(t, custA, customerPortal+"/products")
	var out struct {
		Items  []portalProduct       `json:"items"`
		Listed []portalListedProduct `json:"listed"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Items) != 1 || len(out.Listed) != 0 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// Pricing refusing sales' token is not retryable: 502 SAL-CUS-005, not the retry advice of 503.
func TestCustomerPortal_PricingAuthRefusalIsNotRetryable(t *testing.T) {
	s := firstOrderStore()
	w := newFirstOrderHarness(t, s, &negotiatedPricer{err: &upstream.StatusError{Audience: "pricing", Status: 401}})
	rec := w.post(t, custA, customerPortal+"/orders", "submit-0009", firstOrderBody(line(prodNew, "EA", "1")))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "SAL-CUS-005") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
