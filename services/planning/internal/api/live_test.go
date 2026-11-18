package api

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/planning/internal/openinputs"
	"github.com/BenjaminSRussell/depot/services/planning/internal/store"
)

const liveEntity = "0190f0a0-0000-7000-8000-00000000000a"

const liveBody = `{"asOf":"2026-11-02","siteId":"` + siteID + `","legalEntityId":"` + liveEntity + `"}`

func salesBody(qty string) string {
	return `{"items":[{"orderNumber":"SO-1","lineNo":1,"productId":"` + fgID + `","openQty":"` + qty + `","dueDate":"2026-11-30"}],"next":""}`
}

func onHandBody(qty string) string {
	return `{"items":[{"group":{"product":"` + fgID + `"},"physicalInvent":"` + qty + `"}],"truncated":false}`
}

type livePeers struct {
	sales, inventory   string
	salesStatus        int
	calls              atomic.Int64
	salesQuery, onHand string
}

func (p *livePeers) api(t *testing.T, st store.Store) *API {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("planning", priv)
	if err != nil {
		t.Fatal(err)
	}
	salesSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.calls.Add(1)
		p.salesQuery = r.URL.RawQuery
		if p.salesStatus != 0 {
			w.WriteHeader(p.salesStatus)
			return
		}
		_, _ = w.Write([]byte(p.sales))
	}))
	inventorySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.calls.Add(1)
		p.onHand = r.URL.RawQuery
		_, _ = w.Write([]byte(p.inventory))
	}))
	t.Cleanup(salesSrv.Close)
	t.Cleanup(inventorySrv.Close)
	client, err := openinputs.New(salesSrv.URL, inventorySrv.URL, signer, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return New(st).WithOpenInputs(client)
}

func TestRun_LiveInputs_PlannedQtyIsDemandMinusOnHand(t *testing.T) {
	peers := &livePeers{sales: salesBody("120"), inventory: onHandBody("20")}
	st := &fakeStore{runCovs: map[string]store.RunCoverage{fgID: requirementCoverage()}}
	a := peers.api(t, st)
	rec := runWith(t, a, st, liveBody, "k-live")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	o := onlyOrder(t, st.last)
	if !o.Qty.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("planned order %+v; want qty 120 demand - 20 on hand = 100", o)
	}
	if st.last.ItemsPlanned != 1 {
		t.Fatalf("items planned %d, want the one product with open demand", st.last.ItemsPlanned)
	}
	for _, want := range []string{"legalEntityId=" + liveEntity, "siteId=" + siteID} {
		if !strings.Contains(peers.salesQuery, want) {
			t.Errorf("sales query %q lacks %s", peers.salesQuery, want)
		}
	}
	if !strings.Contains(peers.onHand, "site="+siteID) {
		t.Errorf("inventory query %q lacks the site", peers.onHand)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `planning_live_inputs_total{outcome="loaded"} 1`) {
		t.Fatalf("metrics %s", metrics.Body.String())
	}
}

func TestRun_LiveInputs_NegativeOnHandCountsAsNone(t *testing.T) {
	peers := &livePeers{sales: salesBody("120"), inventory: onHandBody("-5")}
	st := &fakeStore{runCovs: map[string]store.RunCoverage{fgID: requirementCoverage()}}
	if rec := runWith(t, peers.api(t, st), st, liveBody, "k-neg"); rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if o := onlyOrder(t, st.last); !o.Qty.Equal(decimal.NewFromInt(120)) {
		t.Fatalf("planned order %+v; want the whole demand of 120", o)
	}
}

func TestRun_LiveInputs_ProductWithoutStoredCoverageIsRefusedNotSkipped(t *testing.T) {
	peers := &livePeers{sales: salesBody("120"), inventory: onHandBody("20")}
	st := &fakeStore{}
	rec := runWith(t, peers.api(t, st), st, liveBody, "k-nocov")
	if rec.Code != http.StatusBadRequest || problemOf(t, rec).Errors["items[0].orderType"] == "" || st.runs != nil {
		t.Fatalf("status %d body %s runs %v; want 400 naming the item and no version", rec.Code, rec.Body.String(), st.runs)
	}
}

func TestRun_LiveInputs_FailClosedWithoutAVersion(t *testing.T) {
	cases := map[string]*livePeers{
		"sales 5xx":          {salesStatus: http.StatusInternalServerError, inventory: onHandBody("20")},
		"sales 403":          {salesStatus: http.StatusForbidden, inventory: onHandBody("20")},
		"qty as a number":    {sales: strings.Replace(salesBody("120"), `"120"`, `120`, 1), inventory: onHandBody("20")},
		"on-hand truncated":  {sales: salesBody("120"), inventory: `{"items":[],"truncated":true}`},
		"on-hand not a body": {sales: salesBody("120"), inventory: `[]`},
	}
	for name, peers := range cases {
		st := &fakeStore{runCovs: map[string]store.RunCoverage{fgID: requirementCoverage()}}
		rec := runWith(t, peers.api(t, st), st, liveBody, "k-fail")
		if rec.Code != http.StatusServiceUnavailable || problemOf(t, rec).Code != "PLAN-LIVE-UNAVAILABLE" || st.runs != nil {
			t.Errorf("%s: status %d body %s runs %v; want 503 PLAN-LIVE-UNAVAILABLE and no version", name, rec.Code, rec.Body.String(), st.runs)
		}
	}
}

func TestRun_LiveInputs_UnconfiguredPeersAnswer503(t *testing.T) {
	st := &fakeStore{}
	rec := do(t, st, http.MethodPost, "/v1/planning/plans/MAIN:run", liveBody, "planning.run", true, "k-unset")
	if rec.Code != http.StatusServiceUnavailable || problemOf(t, rec).Code != "PLAN-LIVE-UNAVAILABLE" || st.runs != nil {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestRun_LiveInputs_NoOpenDemandIsRefused(t *testing.T) {
	peers := &livePeers{sales: `{"items":[],"next":""}`, inventory: onHandBody("20")}
	st := &fakeStore{}
	rec := runWith(t, peers.api(t, st), st, liveBody, "k-empty")
	if rec.Code != http.StatusUnprocessableEntity || problemOf(t, rec).Code != "PLAN-LIVE-EMPTY" || st.runs != nil {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
}

func TestRun_LiveInputs_RetryReplaysWhateverThePeersSayNow(t *testing.T) {
	peers := &livePeers{sales: salesBody("120"), inventory: onHandBody("20")}
	st := &fakeStore{runCovs: map[string]store.RunCoverage{fgID: requirementCoverage()}}
	a := peers.api(t, st)
	if rec := runWith(t, a, st, liveBody, "k-retry"); rec.Code != http.StatusCreated {
		t.Fatalf("first run status %d body %s", rec.Code, rec.Body.String())
	}
	peers.sales, peers.inventory = salesBody("500"), onHandBody("0")
	replay := runWith(t, a, st, liveBody, "k-retry")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry status %d body %s, want 200 replay", replay.Code, replay.Body.String())
	}
}

func TestRun_LiveInputs_SentItemsAreNeverReadFromThePeers(t *testing.T) {
	peers := &livePeers{sales: salesBody("999"), inventory: onHandBody("999")}
	st := &fakeStore{}
	if rec := runWith(t, peers.api(t, st), st, runBody, "k-inline"); rec.Code != http.StatusCreated {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if peers.calls.Load() != 0 || !onlyOrder(t, st.last).Qty.Equal(decimal.NewFromInt(110)) {
		t.Fatalf("peer calls %d, order %+v; an inline snapshot must plan as sent", peers.calls.Load(), st.last.Result.Orders)
	}
}

func TestRun_LiveInputs_BadHeaderFieldsAreRefusedBeforeAnyPeerCall(t *testing.T) {
	peers := &livePeers{sales: salesBody("120"), inventory: onHandBody("20")}
	st := &fakeStore{}
	a := peers.api(t, st)
	for name, body := range map[string]string{
		"asOf":   `{"asOf":"2026-13-40","siteId":"` + siteID + `"}`,
		"siteId": `{"asOf":"2026-11-02","siteId":"nope"}`,
		"items":  `{"asOf":"2026-11-02","siteId":"` + siteID + `","items":"x"}`,
	} {
		rec := runWith(t, a, st, body, "k-bad-"+name)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d body %s, want 400", name, rec.Code, rec.Body.String())
		}
	}
	if peers.calls.Load() != 0 {
		t.Fatalf("%d peer calls for refused requests", peers.calls.Load())
	}
}
