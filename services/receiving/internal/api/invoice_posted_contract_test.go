package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/api"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/domain"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/purchasing"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/store/memstore"
)

const (
	stockFlowDir      = "../../../inventory/testdata/receipt-flow/"
	invoicePostedType = "dev.depot.payables.invoice.posted.v1"
)

func flowFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(stockFlowDir + name)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func eventsOfType(t *testing.T, st *memstore.Store, typ string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, e := range st.Events() {
		if e.Type != typ {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(e.Data, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

func sameMembers(t *testing.T, name string, got, want map[string]any) {
	t.Helper()
	gotLine, wantLine := got["lines"].([]any)[0].(map[string]any), want["lines"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(sortedKeys(got), sortedKeys(want)) || !reflect.DeepEqual(sortedKeys(gotLine), sortedKeys(wantLine)) {
		t.Fatalf("%s: members differ from the fixture:\n got %v / %v\nwant %v / %v", name, sortedKeys(got), sortedKeys(gotLine), sortedKeys(want), sortedKeys(wantLine))
	}
}

func TestPlainReceipt_PostedCorrectedAndInvoicedEventsMatchTheInventoryContract(t *testing.T) {
	receiptWant, correctedWant, invoiceWant := flowFixture(t, "receipt-posted.json"), flowFixture(t, "receipt-corrected.json"), flowFixture(t, "invoice-posted.json")
	line := receiptWant["lines"].([]any)[0].(map[string]any)
	product, site, warehouse, location := line["product_id"].(string), line["site_id"].(string), line["warehouse_id"].(string), line["location_id"].(string)

	po := fmt.Sprintf(`{"id":%q,"legalEntityId":%q,"vendorId":%q,"currency":"USD","approvalStatus":"APPROVED","documentStatus":"NONE","lines":[{"lineNo":1,"productId":%q,"unit":"EA","qty":"100","unitPrice":"10.00","priceUnit":"1","discountPct":"0","receivedQty":"0","invoicedQty":"0","status":"OPEN","siteId":%q,"warehouseId":%q}]}`,
		poID, legalEntityID, vendorID, product, site, warehouse)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(po)) }))
	t.Cleanup(srv.Close)
	st := memstore.New()
	a := api.New(st, api.Ports{
		Periods: domain.FakePeriods{Status: domain.PeriodOpen}, Ledger: &domain.FakeLedger{}, PurchaseOrders: purchasing.New(srv.URL, nil),
		Profile:       domain.PostingProfile{LedgerID: ledgerID, Inventory: "1400", GRIR: "2100", PPV: "5200", VendorControl: "2000", Expense: "6000", InputTax: "1570"},
		ResolveMinAge: time.Nanosecond, Tax: testTax, SelfBillParties: testParties,
	})
	h := httpx.NewRouter()
	a.Register(h)

	body := fmt.Sprintf(`{"vendorId":%q,"purchaseOrderId":%q,"legalEntityId":%q,"packingSlip":"PS-PLAIN","receiptDate":%q,"currency":"USD","lines":[{"poLineNo":1,"productId":%q,"overDeliveryPct":5,"unit":"EA","qty":10,"locationId":%q}]}`,
		vendorID, poID, legalEntityID, receiptWant["posting_date"], product, location)
	var r receiptJSON
	_ = json.Unmarshal(doWith(h, http.MethodPost, receiptsPath, body, maintainer("pl-reg")).Body.Bytes(), &r)
	post := doWith(h, http.MethodPost, receiptsPath+"/"+r.ID+":post", "", duty("receiving.receipt.post", "pl-post", "If-Match", fmt.Sprintf(`"%d"`, r.Version)))
	if post.Code != http.StatusOK {
		t.Fatalf("post: %d %s", post.Code, post.Body)
	}
	var posted receiptJSON
	_ = json.Unmarshal(post.Body.Bytes(), &posted)
	corr := doWith(h, http.MethodPost, receiptsPath+"/"+r.ID+":correct", `{"postingDate":"2026-10-27","lines":[{"lineNo":1,"qtyDelta":-2}]}`,
		duty("receiving.receipt.post", "pl-correct", "If-Match", fmt.Sprintf(`"%d"`, posted.Version)))
	if corr.Code != http.StatusOK {
		t.Fatalf("correct: %d %s", corr.Code, corr.Body)
	}
	invoice := fmt.Sprintf(`{"vendorId":%q,"legalEntityId":%q,"invoiceNumber":"INV-PLAIN","invoiceDate":"2026-10-26","postingDate":"2026-10-26","currency":"USD","ledgerId":%q,"fiscalPeriodId":%q,"lines":[{"receiptId":%q,"receiptLineNo":1,"qty":8,"unitPrice":10.5}]}`,
		vendorID, legalEntityID, ledgerID, periodID, r.ID)
	if inv := doWith(h, http.MethodPost, invoicesPath, invoice, duty("receiving.vendor-invoice.post", "pl-invoice")); inv.Code != http.StatusCreated {
		t.Fatalf("invoice: %d %s", inv.Code, inv.Body)
	}

	for name, tc := range map[string]struct {
		typ  string
		want map[string]any
		keys []string
	}{
		"receipt.posted":    {api.ReceiptPostedType, receiptWant, []string{"posting_date", "currency"}},
		"receipt.corrected": {"dev.depot.receiving.receipt.corrected.v1", correctedWant, []string{"posting_date", "kind", "correction_seq", "currency"}},
		"invoice.posted":    {invoicePostedType, invoiceWant, []string{"posting_date", "invoice_date", "currency", "source"}},
	} {
		got := eventsOfType(t, st, tc.typ)
		if len(got) != 1 {
			t.Fatalf("%s: %d events, want 1", name, len(got))
		}
		sameMembers(t, name, got[0], tc.want)
		for _, k := range tc.keys {
			if !reflect.DeepEqual(got[0][k], tc.want[k]) {
				t.Errorf("%s: %s = %v, fixture %v", name, k, got[0][k], tc.want[k])
			}
		}
		if got[0]["legal_entity_id"] != legalEntityID {
			t.Errorf("%s: legal_entity_id %v, want %s", name, got[0]["legal_entity_id"], legalEntityID)
		}
	}
	invoiced := eventsOfType(t, st, invoicePostedType)[0]
	invoiceLine := invoiced["lines"].([]any)[0].(map[string]any)
	if invoiced["ledger_entry_id"] == "" || invoiceLine["receipt_id"] != r.ID || invoiceLine["receipt_line_no"] != float64(1) || invoiceLine["qty"] != "8" {
		t.Errorf("invoice.posted %v, want the ledger entry and the receipt line it clears", invoiced)
	}
}
