package customsdata_test

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/customsdata"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func signer(t *testing.T) *tenant.ServiceSigner {
	t.Helper()
	_, key, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := tenant.NewServiceSigner("fulfillment", key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: "0195f0a0-0000-7000-8000-000000000001", LegalEntityID: "0195f0a0-0000-7000-8000-0000000000e1", UserID: "u", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

func TestLookup_ReadsPartnerAndProductsAndDegrades(t *testing.T) {
	var partyAsked, productAsked string
	party := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		partyAsked = r.URL.Query().Get("ids")
		if r.Header.Get(tenant.HeaderServiceToken) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"items":[{"partnerId":"c1","kind":"CUSTOMER","country":"DE","vatId":"DE123"}]}`))
	}))
	defer party.Close()
	product := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		productAsked = r.URL.Query().Get("ids")
		_, _ = w.Write([]byte(`{"items":[{"id":"P1","commodityCode":"84137021","originCountry":"IT","netWeightKg":"12.5"}]}`))
	}))
	defer product.Close()
	c, err := customsdata.New(party.URL+"/", product.URL, signer(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Lookup(ctx(), "0195f0a0-0000-7000-8000-0000000000e1", "c1", []string{"P1", "p1", "p2"})
	if err != nil || got.Partner == nil || got.Partner.Country != "DE" || got.Partner.VATID != "DE123" {
		t.Fatalf("partner: %+v %v", got, err)
	}
	if pr := got.Products["p1"]; pr.CommodityCode != "84137021" || pr.OriginCountry != "IT" || pr.NetWeightKg != "12.5" {
		t.Errorf("product: %+v", got.Products)
	}
	if partyAsked != "c1" || productAsked != "p1,p2" {
		t.Errorf("asked party %q product %q, want c1 and the distinct lower-case product ids", partyAsked, productAsked)
	}
	// A failing party read leaves the products in, and says so.
	party.Close()
	got, err = c.Lookup(ctx(), "0195f0a0-0000-7000-8000-0000000000e1", "c1", []string{"p1"})
	if err == nil || !strings.Contains(err.Error(), "partner tax registration") || got.Partner != nil || got.Products["p1"].CommodityCode != "84137021" {
		t.Errorf("degraded: %+v %v", got, err)
	}
	if _, err := customsdata.New("", product.URL, signer(t)); err == nil {
		t.Error("a missing party URL must be refused")
	}
	if c, err := customsdata.FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Errorf("FromEnv without configuration: %v %v", c, err)
	}
}
