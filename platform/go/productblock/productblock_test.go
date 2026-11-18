package productblock_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/productblock"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	tenantID  = "11111111-1111-4111-8111-111111111111"
	entityID  = "22222222-2222-4222-8222-222222222222"
	productID = "33333333-3333-4333-8333-333333333333"

	blockedBody = `{"type":"https://depot.dev/problems/PRD-LC-BLOCKED","title":"Unprocessable Entity","status":422,"code":"PRD-LC-BLOCKED","detail":"This product is blocked for purchasing. Ask a product data steward to lift the block."}`
)

func ctx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, LegalEntityID: entityID, UserID: "buyer"})
}

type fixture struct {
	client *productblock.Client
	calls  *atomic.Int32
}

func newFixture(t *testing.T, handler http.HandlerFunc, httpClient *http.Client) fixture {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("purchasing", priv)
	if err != nil {
		t.Fatal(err)
	}
	c, err := productblock.New(srv.URL, signer, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{client: c, calls: &calls}
}

func status(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func asProblem(t *testing.T, err error, wantStatus int, wantCode string) *httpx.Problem {
	t.Helper()
	var p *httpx.Problem
	if !errors.As(err, &p) {
		t.Fatalf("want a problem, got %v", err)
	}
	if p.Status != wantStatus || p.Code != wantCode {
		t.Fatalf("problem %d %s, want %d %s", p.Status, p.Code, wantStatus, wantCode)
	}
	return p
}

func TestBlockedProductIsRefusedWithProductsPlainLanguageDetail(t *testing.T) {
	f := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/product/products/"+productID+"/process-policy" || r.URL.Query().Get("legalEntityId") != entityID || r.URL.Query().Get("process") != productblock.PurchaseOrder {
			t.Errorf("request %s", r.URL.String())
		}
		if r.Header.Get(tenant.HeaderServiceToken) == "" {
			t.Error("the call must carry a service token")
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(blockedBody))
	}, nil)
	p := asProblem(t, f.client.Check(ctx(), entityID, productID, productblock.PurchaseOrder), http.StatusUnprocessableEntity, productblock.CodeBlocked)
	if p.Title != "Unprocessable Entity" || strings.Contains(p.Title, "PRD-") {
		t.Fatalf("title %q must be plain", p.Title)
	}
	if !strings.Contains(p.Detail, "blocked for purchasing") || !strings.Contains(p.Detail, "Ask a product data steward") {
		t.Fatalf("detail %q must name the block and who to ask", p.Detail)
	}
}

func TestAllowedAndUnreleasedProductsPass(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"allow":    status(http.StatusOK, `{"policy":"allow"}`),
		"warn":     status(http.StatusOK, `{"policy":"warn","code":"PRD-LC-WARN"}`),
		"released": status(http.StatusNotFound, `{"code":"COM-0001"}`),
	} {
		t.Run(name, func(t *testing.T) {
			if err := newFixture(t, h, nil).client.Check(ctx(), entityID, productID, productblock.SalesOrder); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPolicyUnavailableFailsClosedAndIsNotCached(t *testing.T) {
	slow := &http.Client{Timeout: 20 * time.Millisecond}
	cases := map[string]struct {
		handler http.HandlerFunc
		client  *http.Client
	}{
		"server error":       {status(http.StatusInternalServerError, ``), nil},
		"timeout":            {func(http.ResponseWriter, *http.Request) { time.Sleep(200 * time.Millisecond) }, slow},
		"not json":           {status(http.StatusOK, `<html>`), nil},
		"wrong shape":        {status(http.StatusOK, `"allow"`), nil},
		"other refusal":      {status(http.StatusUnprocessableEntity, `{"code":"PRD-OTHER","detail":"x"}`), nil},
		"unreadable refusal": {status(http.StatusUnprocessableEntity, `nope`), nil},
		"forbidden":          {status(http.StatusForbidden, `{}`), nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, c.handler, c.client)
			_ = asProblem(t, f.client.Check(ctx(), entityID, productID, productblock.PurchaseOrder), http.StatusServiceUnavailable, productblock.CodeUnavailable)
			_ = asProblem(t, f.client.Check(ctx(), entityID, productID, productblock.PurchaseOrder), http.StatusServiceUnavailable, productblock.CodeUnavailable)
			if f.calls.Load() != 2 {
				t.Fatalf("an error must not be cached, product was asked %d times", f.calls.Load())
			}
		})
	}
}

func TestBlockChangeTakesEffectWithinTheCacheWindow(t *testing.T) {
	var blocked atomic.Bool
	blocked.Store(true)
	f := newFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		if blocked.Load() {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(blockedBody))
			return
		}
		_, _ = w.Write([]byte(`{"policy":"allow"}`))
	}, nil)
	clock := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	productblock.SetClock(f.client, func() time.Time { return clock })

	_ = asProblem(t, f.client.Check(ctx(), entityID, productID, productblock.PurchaseOrder), 422, productblock.CodeBlocked)
	blocked.Store(false)

	clock = clock.Add(productblock.DefaultTTL - time.Second)
	asProblem(t, f.client.Check(ctx(), entityID, productID, productblock.PurchaseOrder), 422, productblock.CodeBlocked)
	if f.calls.Load() != 1 {
		t.Fatalf("inside the window the answer is cached, product was asked %d times", f.calls.Load())
	}

	clock = clock.Add(2 * time.Second)
	if err := f.client.Check(ctx(), entityID, productID, productblock.PurchaseOrder); err != nil {
		t.Fatalf("after the window the lifted block must pass: %v", err)
	}
	if f.calls.Load() != 2 {
		t.Fatalf("after the window product is asked again, got %d calls", f.calls.Load())
	}
}

func TestCacheKeepsProcessesAndTenantsApart(t *testing.T) {
	f := newFixture(t, status(http.StatusOK, `{"policy":"allow"}`), nil)
	_ = f.client.Check(ctx(), entityID, productID, productblock.PurchaseOrder)
	_ = f.client.Check(ctx(), entityID, productID, productblock.PurchaseRequisition)
	other := tenant.With(context.Background(), tenant.Principal{TenantID: "99999999-9999-4999-8999-999999999999", LegalEntityID: entityID, UserID: "buyer"})
	_ = f.client.Check(other, entityID, productID, productblock.PurchaseOrder)
	if f.calls.Load() != 3 {
		t.Fatalf("each process and tenant is its own answer, got %d calls", f.calls.Load())
	}
}

func TestInputsThatAreNotCheckable(t *testing.T) {
	f := newFixture(t, status(http.StatusInternalServerError, ``), nil)
	for name, args := range map[string][2]string{"free text line": {entityID, ""}, "product not a UUID": {entityID, "nope"}, "legal entity not a UUID": {"nope", productID}} {
		if err := f.client.Check(ctx(), args[0], args[1], productblock.PurchaseOrder); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if f.calls.Load() != 0 {
		t.Fatalf("nothing to check must not call product, got %d calls", f.calls.Load())
	}
	if err := f.client.Check(ctx(), entityID, productID, ""); err == nil {
		t.Fatal("an empty process is a programming error")
	} else if errors.As(err, new(*httpx.Problem)) {
		t.Fatalf("an empty process must not look like a refusal: %v", err)
	}
	asProblem(t, f.client.Check(context.Background(), entityID, productID, productblock.PurchaseOrder), 503, productblock.CodeUnavailable)
}

func TestOutcomesAreCounted(t *testing.T) {
	blocked := newFixture(t, status(http.StatusUnprocessableEntity, blockedBody), nil)
	_ = blocked.client.Check(ctx(), entityID, productID, "counted_block")
	_ = blocked.client.Check(ctx(), entityID, productID, "counted_block")
	down := newFixture(t, status(http.StatusInternalServerError, ``), nil)
	_ = down.client.Check(ctx(), entityID, productID, "counted_down")
	var out bytes.Buffer
	httpx.WriteMetrics(&out)
	for _, want := range []string{
		`depot_product_block_checks_total{process="counted_block",outcome="blocked"} 2`,
		`depot_product_block_checks_total{process="counted_down",outcome="unavailable"} 1`,
		`depot_product_block_cache_total{result="hit"}`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("metrics lack %s:\n%s", want, out.String())
		}
	}
}

func TestFromEnv(t *testing.T) {
	if c, err := productblock.FromEnv(func(string) string { return "" }); c != nil || err != nil {
		t.Fatalf("PRODUCT_URL unset is off: %v %v", c, err)
	}
	if _, err := productblock.FromEnv(func(k string) string {
		if k == "PRODUCT_URL" {
			return "http://product"
		}
		return ""
	}); err == nil {
		t.Fatal("PRODUCT_URL without a signing key must stop the start")
	}
}

func TestFakeRefusesBlockedProducts(t *testing.T) {
	f := &productblock.Fake{BlockedDetail: map[string]string{productID: "blocked"}}
	asProblem(t, f.Check(ctx(), entityID, productID, productblock.SalesOrder), 422, productblock.CodeBlocked)
	if err := f.Check(ctx(), entityID, "other", productblock.SalesOrder); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAllStopsAtFirstBlockedProductAndAllowsNilChecker(t *testing.T) {
	fake := &productblock.Fake{BlockedDetail: map[string]string{"b": "blocked"}}
	err := productblock.CheckAll(ctx(), fake, entityID, productblock.SalesOrder, []string{"a", "b", "c"})
	if err == nil || len(fake.Calls) != 2 {
		t.Fatalf("err=%v calls=%v, want a refusal after two checks", err, fake.Calls)
	}
	if err := productblock.CheckAll(ctx(), nil, entityID, productblock.SalesOrder, []string{"a"}); err != nil {
		t.Fatalf("nil checker must allow, got %v", err)
	}
}
