package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target/targettest"
)

const (
	siteA   = "55555555-5555-4555-8555-555555555555"
	whA     = "66666666-6666-4666-8666-666666666666"
	vendA2  = "77777777-7777-4777-8777-777777777777"
	prodNut = "88888888-8888-4888-8888-888888888888"
	prodBlt = "99999999-9999-4999-8999-999999999999"
)

type orderEnv struct {
	loadEnv
	sales *targettest.FakeOrders
	purch *targettest.FakeOrders
	xref  *fakeXrefIDs
}

// fakeXrefIDs answers the cross-reference with ids per entity and records what it was asked.
type fakeXrefIDs struct {
	found map[string]map[string]string // entity -> key -> id
	asked []string
}

func (f *fakeXrefIDs) Configured() bool { return true }
func (f *fakeXrefIDs) Resolve(_ context.Context, _, _, _, entity string, keys []string) (map[string]string, error) {
	out := map[string]string{}
	for _, k := range keys {
		f.asked = append(f.asked, entity+":"+k)
		if id := f.found[entity][k]; id != "" {
			out[k] = id
		}
	}
	return out, nil
}

func newOrderEnv(t *testing.T) orderEnv {
	t.Helper()
	e := newLoadEnv(t, 3, nil)
	sales, purch := targettest.NewSalesOrders(t), targettest.NewPurchasingOrders(t)
	cfg := func(url string) target.Config {
		return target.Config{BaseURL: url, Signer: targettest.HeaderSigner{Name: "migration-factory"}, Attempts: 3, Backoff: time.Millisecond,
			Sleep: func(context.Context, time.Duration) error { return nil }}
	}
	x := &fakeXrefIDs{found: map[string]map[string]string{
		"customer": {"USMF|C1": custA, "C2": custB},
		"vendor":   {"USMF|V1": vendA2},
		"item":     {"USMF|BOLT": prodBlt, "NUT": prodNut},
	}}
	e.h = New(e.st).WithTarget(e.tgt).WithSales(target.NewSales(cfg(sales.URL))).WithPurchasing(target.NewPurchasing(cfg(purch.URL))).WithXref(x).Routes()
	return orderEnv{loadEnv: e, sales: sales, purch: purch, xref: x}
}

func orderLoadBody(extra string) string {
	return fmt.Sprintf(`{"legalEntityId":%q,"siteId":%q,"warehouseId":%q%s}`, leA, siteA, whA, extra)
}

func (e orderEnv) ordersLoad(t *testing.T, runID, body string, want int) orderLoadDTO {
	t.Helper()
	rec := e.do(t, "POST", "/api/v1/migration/runs/"+runID+"/orders-load", body)
	if rec.Code != want {
		t.Fatalf("orders-load: %d %s", rec.Code, rec.Body)
	}
	if want != http.StatusOK {
		return orderLoadDTO{}
	}
	return decode[orderLoadDTO](t, rec)
}

func (e orderEnv) startOrders(t *testing.T, records ...string) runDetailDTO {
	t.Helper()
	rec := e.do(t, "POST", ordersPath, ordersBody("", records...))
	if rec.Code != http.StatusCreated {
		t.Fatalf("start run: %d %s", rec.Code, rec.Body)
	}
	return decode[runDetailDTO](t, rec)
}

// Open order lines go to sales and purchasing as opening batches, the legacy customers, vendors and items are found through the
// cross-reference, both targets' totals are read back and reconcile, and a second call sends nothing (#49).
func TestOrderLoad_SalesAndPurchaseLinesGoToTheirServicesAndReconcile(t *testing.T) {
	e := newOrderEnv(t)
	run := e.startOrders(t, cleanOrders...)
	got := e.ordersLoad(t, run.RunID, orderLoadBody(""), http.StatusOK)
	if got.Status != "RECONCILED" || !got.Clean || got.Loaded != 3 || got.Pending != 0 || got.Rejected != 0 || got.Staged != 3 {
		t.Fatalf("load %+v", got)
	}
	if k := e.sales.Batches(); len(k) != 1 || !strings.HasPrefix(k[0], "mf-orders-sales-AX2012-") {
		t.Fatalf("sales batches %v", k)
	}
	if k := e.purch.Batches(); len(k) != 1 || !strings.HasPrefix(k[0], "mf-orders-purchasing-AX2012-") {
		t.Fatalf("purchasing batches %v", k)
	}
	for _, f := range []*targettest.FakeOrders{e.sales, e.purch} {
		for _, c := range f.Calls() {
			if len(c.Duties) != 1 || !strings.Contains(c.Duties[0], "opening-order.import") {
				t.Fatalf("each token carries one duty of the grant: %+v", c)
			}
		}
	}
	detail := decode[orderLoadDetailDTO](t, e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/orders-load", ""))
	names := map[string]int{}
	for _, c := range detail.Reconciliation {
		names[c.Name]++
		if c.Status != "MATCH" {
			t.Fatalf("reconciliation %+v", c)
		}
	}
	// per order type: line count and open quantity, and the open value of its currency
	if names["target.open_orders.line_count"] != 2 || names["target.open_orders.open_quantity"] != 2 || names["target.open_orders.open_value"] != 2 {
		t.Fatalf("reconciliation %+v", detail.Reconciliation)
	}
	posts := len(e.sales.Calls()) + len(e.purch.Calls())
	again := e.ordersLoad(t, run.RunID, orderLoadBody(""), http.StatusOK)
	if again.Status != "RECONCILED" || again.Attempts != 2 {
		t.Fatalf("second load %+v", again)
	}
	for _, c := range append(e.sales.Calls(), e.purch.Calls()...) {
		if c.Method == http.MethodPost && c.Status == http.StatusCreated {
			posts--
		}
	}
	if n := e.scalar(t, `select count(*) from order_load_row where run_id = $1 and status = 'LOADED'`, run.RunID); n != 3 {
		t.Fatalf("loaded rows %d", n)
	}
	if got := len(e.sales.Batches()) + len(e.purch.Batches()); got != 2 {
		t.Fatalf("batches after the second call: %d", got)
	}
}

// A line the factory cannot map holds back its whole order (sales stores an order under its number once, so a partial order could
// never be completed), the other order still loads, and the held order loads whole once the mapping exists. A refusal of one line
// by the target holds back its order the same way. The run is not reconciled while lines are missing.
func TestOrderLoad_AnOrderIsSentWholeOrNotAtAll(t *testing.T) {
	e := newOrderEnv(t)
	e.xref.found["item"] = map[string]string{"USMF|BOLT": prodBlt} // NUT has no product id
	run := e.startOrders(t, cleanOrders...)
	got := e.ordersLoad(t, run.RunID, orderLoadBody(""), http.StatusOK)
	// SO-1 has SL-1 (BOLT) and SL-2 (NUT, unmapped): neither goes out. PL-1 loads.
	if got.Loaded != 1 || got.Rejected != 2 || got.Clean || got.Status != "MISMATCH" {
		t.Fatalf("load %+v", got)
	}
	if k := e.sales.Batches(); len(k) != 0 {
		t.Fatalf("a part of the order SO-1 was sent: %v", k)
	}
	detail := decode[orderLoadDetailDTO](t, e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/orders-load", ""))
	codes := map[string]string{}
	for _, r := range detail.Rejects {
		codes[r.LegacyID] = r.Code
	}
	if codes["SL-1"] != CodeOrderHeld || codes["SL-2"] != "MIG-REF-UNRESOLVED" || len(codes) != 2 {
		t.Fatalf("rejects %+v", detail.Rejects)
	}
	// The sales target refuses one line of the order: the whole order stays out, and nothing is stored there.
	e.xref.found["item"]["NUT"] = prodNut
	e.sales.Refuse["SL-2"] = true
	got = e.ordersLoad(t, run.RunID, orderLoadBody(""), http.StatusOK)
	if got.Rejected != 2 || got.Status != "MISMATCH" || len(e.sales.Batches()) != 0 {
		t.Fatalf("refused line %+v batches %v", got, e.sales.Batches())
	}
	detail = decode[orderLoadDetailDTO](t, e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/orders-load", ""))
	codes = map[string]string{}
	for _, r := range detail.Rejects {
		codes[r.LegacyID] = r.Code
	}
	if codes["SL-2"] != "SLS-OPB-001" || codes["SL-1"] != CodeOrderHeld {
		t.Fatalf("rejects %+v", detail.Rejects)
	}
	// The target stops refusing: the retry loads the order whole and the run reconciles.
	delete(e.sales.Refuse, "SL-2")
	done := e.ordersLoad(t, run.RunID, orderLoadBody(""), http.StatusOK)
	if done.Status != "RECONCILED" || done.Loaded != 3 || !done.Clean || len(e.sales.Batches()) != 1 {
		t.Fatalf("retry %+v", done)
	}
}

// Batches are cut between orders, never inside one, and an order larger than a batch is refused with its reason.
func TestOrderLoad_BatchesAreCutAtOrderBoundaries(t *testing.T) {
	old := orderBatchLimit
	orderBatchLimit = 2
	defer func() { orderBatchLimit = old }()
	e := newOrderEnv(t)
	run := e.startOrders(t,
		orderRec("SL-1", "SALES", "SO-1", "1", "BOLT", "C1", "10", "4", "2.5"), orderRec("SL-2", "SALES", "SO-1", "2", "NUT", "C1", "4", "0", "1.5"),
		orderRec("SL-3", "SALES", "SO-2", "1", "BOLT", "C1", "5", "0", "2"), orderRec("SL-4", "SALES", "SO-3", "1", "BOLT", "C1", "1", "0", "2"),
		orderRec("SL-5", "SALES", "SO-3", "2", "BOLT", "C1", "1", "0", "2"), orderRec("SL-6", "SALES", "SO-3", "3", "BOLT", "C1", "1", "0", "2"))
	got := e.ordersLoad(t, run.RunID, orderLoadBody(""), http.StatusOK)
	// SO-1 (2 lines) and SO-2 (1) cannot share a batch of 2 lines; SO-3 has 3 lines and is refused.
	if got.Loaded != 3 || got.Rejected != 3 || got.Status != "MISMATCH" || len(e.sales.Batches()) != 2 {
		t.Fatalf("load %+v batches %v", got, e.sales.Batches())
	}
	detail := decode[orderLoadDetailDTO](t, e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/orders-load", ""))
	for _, r := range detail.Rejects {
		if r.Code != CodeOrderTooLarge {
			t.Fatalf("reject %+v", r)
		}
	}
}

// The target stored a batch and its answer never arrived. The next pass has other lines pending (a mapping was added), so it forms
// another batch whose rows repeat the first one's: the target names the batch that holds each reference, with the order-number error
// next to it, and the factory counts those orders as loaded instead of rejected.
func TestOrderLoad_ALostAnswerIsRecognisedWhateverTheErrorsSayAndTheKeyChanged(t *testing.T) {
	e := newOrderEnv(t)
	e.xref.found["item"] = map[string]string{"USMF|BOLT": prodBlt}
	run := e.startOrders(t, append([]string{orderRec("SL-9", "SALES", "SO-9", "1", "BOLT", "C1", "3", "0", "4")}, cleanOrders...)...)
	e.sales.DropAnswers = 10
	first := e.ordersLoad(t, run.RunID, orderLoadBody(""), http.StatusOK)
	if first.Status != "PARTIAL" || len(e.sales.Batches()) != 1 {
		t.Fatalf("lost answer %+v batches %v", first, e.sales.Batches())
	}
	// NUT gets its product: SO-1 becomes sendable, so the pending set (and the key) changes; SO-9 is already stored at the target.
	e.sales.DropAnswers = 0
	e.xref.found["item"]["NUT"] = prodNut
	done := e.ordersLoad(t, run.RunID, orderLoadBody(""), http.StatusOK)
	if done.Status != "RECONCILED" || done.Staged != 4 || done.Loaded != 4 || done.Rejected != 0 || !done.Clean || len(e.sales.Batches()) != 2 {
		t.Fatalf("second pass %+v batches %v", done, e.sales.Batches())
	}
}

// A target that holds another open value than was loaded shows as a MISMATCH, not hidden.
func TestOrderLoad_ReadBackMismatchIsNotHidden(t *testing.T) {
	e := newOrderEnv(t)
	run := e.startOrders(t, cleanOrders...)
	e.sales.ValueDrift = decimal.NewFromInt(5)
	got := e.ordersLoad(t, run.RunID, orderLoadBody(""), http.StatusOK)
	if got.Status != "MISMATCH" || got.Clean {
		t.Fatalf("load %+v", got)
	}
}

func TestOrderLoad_RefusesWhatIsNotLoadableOrNotConfigured(t *testing.T) {
	e := newOrderEnv(t)
	run := e.startOrders(t, cleanOrders...)
	for name, body := range map[string]string{
		"no site":        fmt.Sprintf(`{"legalEntityId":%q,"warehouseId":%q}`, leA, whA),
		"bad legal":      fmt.Sprintf(`{"legalEntityId":"x","siteId":%q,"warehouseId":%q}`, siteA, whA),
		"bad mapped id":  orderLoadBody(`,"products":{"BOLT":"nope"}`),
		"another entity": fmt.Sprintf(`{"legalEntityId":%q,"siteId":%q,"warehouseId":%q}`, leB, siteA, whA),
	} {
		if rec := e.do(t, "POST", "/api/v1/migration/runs/"+run.RunID+"/orders-load", body); rec.Code < 400 || rec.Code >= 500 {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	// An onhand run is not an open_orders run.
	oh := e.startPack2(t, onhandPath, onhandBody("USMF", "", cleanOnhand...))
	if rec := e.do(t, "POST", "/api/v1/migration/runs/"+oh+"/orders-load", orderLoadBody("")); rec.Code != http.StatusConflict {
		t.Fatalf("onhand run: %d %s", rec.Code, rec.Body)
	}
	// No target configured: 503, and nothing stays LOADING.
	bare := New(e.st).WithTarget(e.tgt).Routes()
	rec := do(t, bare, request{method: "POST", path: "/api/v1/migration/runs/" + run.RunID + "/orders-load", tenant: tenantA, duties: allDuties, le: leA, body: orderLoadBody("")})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), CodeOrdersNotConfigured) {
		t.Fatalf("no target: %d %s", rec.Code, rec.Body)
	}
	if got := e.ordersLoad(t, run.RunID, orderLoadBody(""), http.StatusOK); got.Status != "RECONCILED" {
		t.Fatalf("a failed claim must not block the next: %+v", got)
	}
}

func (e orderEnv) startPack2(t *testing.T, path, body string) string {
	t.Helper()
	rec := e.do(t, "POST", path, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start run: %d %s", rec.Code, rec.Body)
	}
	return decode[runDetailDTO](t, rec).RunID
}
