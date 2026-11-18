package openinputs

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	tenantID = "11111111-1111-4111-8111-111111111111"
	entityID = "22222222-2222-4222-8222-222222222222"
	siteID   = "33333333-3333-4333-8333-333333333333"
	itemA    = "44444444-4444-4444-8444-444444444444"
	itemB    = "55555555-5555-4555-8555-555555555555"
)

type peers struct {
	client *Client
	pub    ed25519.PublicKey
	tokens map[string]string
}

func newPeers(t *testing.T, sales, inventory http.HandlerFunc, timeout time.Duration) *peers {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("planning", priv)
	if err != nil {
		t.Fatal(err)
	}
	p := &peers{pub: pub, tokens: map[string]string{}}
	record := func(audience string, h http.HandlerFunc) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p.tokens[audience] = r.Header.Get(tenant.HeaderServiceToken)
			h(w, r)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	salesSrv, inventorySrv := record("sales", sales), record("inventory", inventory)
	if p.client, err = New(salesSrv.URL+"/", inventorySrv.URL, signer, timeout); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *peers) claims(t *testing.T, audience string) tenant.ServiceClaims {
	t.Helper()
	c, err := tenant.NewServiceVerifier(audience, map[string]ed25519.PublicKey{"planning": p.pub}).Verify(p.tokens[audience])
	if err != nil {
		t.Fatalf("token for %s: %v", audience, err)
	}
	return c
}

func serve(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

func line(product, qty string) string {
	return `{"orderNumber":"SO-7","lineNo":2,"productId":"` + product + `","openQty":` + qty + `,"dueDate":"2026-11-30"}`
}

func TestOpenSalesLines_FollowsCursorsAndSignsWithTheOneDuty(t *testing.T) {
	var queries []string
	sales := func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.URL.RawQuery)
		if r.URL.Path != "/v1/sales/open-order-lines" || r.Method != http.MethodGet {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("cursor") == "" {
			serve(`{"items":[`+line(itemA, `"120"`)+`],"next":"2026-11-30|x"}`)(w, r)
			return
		}
		serve(`{"items":[`+line(strings.ToUpper(itemB), `"3.5"`)+`],"next":""}`)(w, r)
	}
	p := newPeers(t, sales, serve(`{}`), time.Second)
	lines, err := p.client.OpenSalesLines(context.Background(), tenantID, entityID, siteID, "2027-11-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].ProductID != itemA || !lines[0].Qty.Equal(decimal.NewFromInt(120)) || lines[0].Ref != "SO-7/2" || lines[0].Date != "2026-11-30" ||
		lines[1].ProductID != itemB || !lines[1].Qty.Equal(decimal.RequireFromString("3.5")) {
		t.Fatalf("lines = %+v", lines)
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "legalEntityId="+entityID) || !strings.Contains(queries[0], "siteId="+siteID) ||
		!strings.Contains(queries[0], "dueTo=2027-11-02") || !strings.Contains(queries[1], "cursor=2026-11-30%7Cx") {
		t.Fatalf("queries = %v", queries)
	}
	c := p.claims(t, "sales")
	if c.Iss != "planning" || c.Act != "service:planning-open-inputs" || c.Tid != tenantID || c.Le != entityID || strings.Join(c.Duties, ",") != "sales.order.open-lines" {
		t.Fatalf("claims = %+v", c)
	}
}

func TestOnHand_ReadsTheSiteByProductAndSignsWithTheReadDuty(t *testing.T) {
	var query string
	inventory := func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		serve(`{"items":[{"group":{"product":"`+itemA+`"},"physicalInvent":"20"},{"group":{"product":"`+itemB+`"},"physicalInvent":"-2"}],"truncated":false}`)(w, r)
	}
	p := newPeers(t, serve(`{}`), inventory, time.Second)
	onHand, err := p.client.OnHand(context.Background(), tenantID, entityID, siteID)
	if err != nil {
		t.Fatal(err)
	}
	if len(onHand) != 2 || !onHand[itemA].Equal(decimal.NewFromInt(20)) || !onHand[itemB].Equal(decimal.NewFromInt(-2)) {
		t.Fatalf("on-hand = %v", onHand)
	}
	if !strings.Contains(query, "site="+siteID) || !strings.Contains(query, "groupBy=product") {
		t.Fatalf("query = %q", query)
	}
	if c := p.claims(t, "inventory"); c.Aud != "inventory" || strings.Join(c.Duties, ",") != "inventory.read" || c.Le != entityID {
		t.Fatalf("claims = %+v", c)
	}
}

func TestOpenSalesLines_FailClosed(t *testing.T) {
	status := func(code int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
	}
	slow := func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}
	cases := map[string]http.HandlerFunc{
		"5xx":                   status(http.StatusInternalServerError),
		"403":                   status(http.StatusForbidden),
		"redirect":              func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/elsewhere", http.StatusFound) },
		"timeout":               slow,
		"not json":              serve(`<html>`),
		"items as object":       serve(`{"items":{"a":1}}`),
		"qty as number":         serve(`{"items":[` + line(itemA, `120`) + `]}`),
		"qty not a decimal":     serve(`{"items":[` + line(itemA, `"abc"`) + `]}`),
		"qty NaN":               serve(`{"items":[` + line(itemA, `"NaN"`) + `]}`),
		"qty zero":              serve(`{"items":[` + line(itemA, `"0"`) + `]}`),
		"qty negative":          serve(`{"items":[` + line(itemA, `"-1"`) + `]}`),
		"product not a uuid":    serve(`{"items":[` + line("nope", `"1"`) + `]}`),
		"cursor never advances": serve(`{"items":[` + line(itemA, `"1"`) + `],"next":"same"}`),
	}
	for name, h := range cases {
		p := newPeers(t, h, serve(`{}`), 100*time.Millisecond)
		if _, err := p.client.OpenSalesLines(context.Background(), tenantID, entityID, siteID, ""); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: err = %v, want ErrUnavailable", name, err)
		}
	}
}

func TestOnHand_FailClosed(t *testing.T) {
	cases := map[string]string{
		"truncated":         `{"items":[],"truncated":true}`,
		"not json":          `<html>`,
		"physical a number": `{"items":[{"group":{"product":"` + itemA + `"},"physicalInvent":20}]}`,
		"not a decimal":     `{"items":[{"group":{"product":"` + itemA + `"},"physicalInvent":"abc"}]}`,
		"product not uuid":  `{"items":[{"group":{"product":"nope"},"physicalInvent":"1"}]}`,
		"no product group":  `{"items":[{"group":{},"physicalInvent":"1"}]}`,
	}
	for name, body := range cases {
		p := newPeers(t, serve(`{}`), serve(body), time.Second)
		if _, err := p.client.OnHand(context.Background(), tenantID, entityID, siteID); !errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: err = %v, want ErrUnavailable", name, err)
		}
	}
	p := newPeers(t, serve(`{}`), func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }, time.Second)
	if _, err := p.client.OnHand(context.Background(), tenantID, entityID, siteID); !errors.Is(err, ErrUnavailable) {
		t.Errorf("503: err = %v, want ErrUnavailable", err)
	}
}

func TestClient_RefusesATenantThatIsNotAUUID(t *testing.T) {
	p := newPeers(t, serve(`{}`), serve(`{}`), time.Second)
	if _, err := p.client.OpenSalesLines(context.Background(), "nope", entityID, siteID, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if p.tokens["sales"] != "" {
		t.Fatal("a call was sent without a principal")
	}
}

func TestFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if c, err := FromEnv(env(map[string]string{"SALES_URL": "http://sales"})); c != nil || err != nil {
		t.Fatalf("one URL unset: %v %v, want nil, nil", c, err)
	}
	if c, err := FromEnv(env(nil)); c != nil || err != nil {
		t.Fatalf("both URLs unset: %v %v, want nil, nil", c, err)
	}
	urls := map[string]string{"SALES_URL": "http://sales", "INVENTORY_URL": "http://inventory", tenant.EnvServiceName: "planning"}
	if c, err := FromEnv(env(urls)); c != nil || err == nil {
		t.Fatalf("URLs without a signing key: %v %v, want an error", c, err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	urls[tenant.EnvServiceSigningKey] = base64.StdEncoding.EncodeToString(priv.Seed())
	if c, err := FromEnv(env(urls)); c == nil || err != nil {
		t.Fatalf("configured: %v %v, want a client", c, err)
	}
}
