package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

const vendorID = "0b1c2d3e-0000-4000-8000-0000000000cc"

// vendor810 is an X12 810 the distributor (a vendor of ours here) sends us for PO-1001: 10 EA of prod-a at 8, a 6.80 tax and a total of 86.80.
func vendor810(t *testing.T, control, number string, withCurrency bool) string {
	t.Helper()
	body := []domain.Segment{{ID: "BIG", Elements: []string{"20261111", number, "20261015", "PO-1001"}}}
	if withCurrency {
		body = append(body, domain.Segment{ID: "CUR", Elements: []string{"BY", "USD"}})
	}
	body = append(body,
		domain.Segment{ID: "IT1", Elements: []string{"10", "10", "EA", "8", "", "BP", "prod-a"}},
		domain.Segment{ID: "TDS", Elements: []string{"8680"}},
		domain.Segment{ID: "CTT", Elements: []string{"1"}},
	)
	ic := domain.Interchange{
		Delims:   domain.Delims{Element: '*', Component: '>', Segment: '~'},
		Sender:   domain.Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"},
		Receiver: domain.Party{Qualifier: "ZZ", ID: "MANUFACTURER"},
		Date:     "261111", Time: "0900", Version: "00401", Control: control, Usage: "T",
		Groups: []domain.Group{{FunctionalID: "IN", Sender: "DISTRIBUTOR", Receiver: "MANUFACTURER", Date: "20261111", Time: "0900", Control: control[len(control)-2:], Version: "004010",
			Transactions: []domain.Transaction{{SetID: "810", Control: "0081", Body: body}}}},
	}
	raw, err := domain.Serialize(ic)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// fakePayables is payables' vendor-invoice intake: one row per key.
type fakePayables struct {
	mu      sync.Mutex
	got     []sources.VendorInvoiceRequest
	actors  []string
	refuse  *sources.DownstreamRefusedError
	rows    map[string]sources.VendorInvoiceRef
	failing error
}

func (p *fakePayables) ReceiveVendorInvoice(ctx context.Context, legalEntityID, key string, body sources.VendorInvoiceRequest) (sources.VendorInvoiceRef, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pr, ok := tenant.FromContext(ctx); ok {
		p.actors = append(p.actors, pr.UserID+"@"+pr.LegalEntityID)
	}
	if p.failing != nil {
		return sources.VendorInvoiceRef{}, p.failing
	}
	if p.refuse != nil {
		return sources.VendorInvoiceRef{}, p.refuse
	}
	p.got = append(p.got, body)
	if p.rows == nil {
		p.rows = map[string]sources.VendorInvoiceRef{}
	}
	if r, ok := p.rows[key]; ok {
		return r, nil
	}
	r := sources.VendorInvoiceRef{ID: "5a1b2c3d-0000-4000-8000-00000000000" + string(rune('1'+len(p.rows))), Status: "RECEIVED"}
	p.rows[key] = r
	return r, nil
}

func putVendorProfileFor(t *testing.T, h http.Handler, partner string) {
	t.Helper()
	body := `{"legalEntityId":"` + srcLE + `","vendorId":"` + vendorID + `"}`
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/vendor-profile", body, "edi.admin", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("vendor profile: %d %s", rec.Code, rec.Body.String())
	}
}

func receiveVendor(t *testing.T, h http.Handler, route, raw string) (int, receiveResponse) {
	t.Helper()
	rec := do(t, h, http.MethodPost, route+"?environment=TEST", raw, "edi.operate", srcScope)
	var out receiveResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code >= 400 {
		out.AckStatus = rec.Body.String()
	}
	return rec.Code, out
}

func messageStatus(t *testing.T, h http.Handler, id string) string {
	t.Helper()
	rec := do(t, h, http.MethodGet, "/edi/v1/messages/"+id, "", "edi.operate", srcScope)
	var m store.MessageRecord
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m.Status
}

// #35 (35-5): a vendor's X12 810 has its own route and goes into payables' intake as source X12_810, never matched as a self-billed invoice.
func TestVendor810_RouteSendsTheInvoiceToPayablesAndNotToSelfBilling(t *testing.T) {
	a, h, partner := vendorSetup(t)
	payables := &fakePayables{}
	a.VendorInvoices = payables
	// The route needs the partner to be one of our vendors.
	code, res := receiveVendor(t, h, "/edi/v1/inbound/x12/vendor", vendor810(t, "000000091", "V-7001", true))
	if code != http.StatusConflict || !strings.Contains(res.AckStatus, CodeNoVendorProfile) {
		t.Fatalf("without a vendor profile: %d %s", code, res.AckStatus)
	}
	if n := countInterchanges(t, h, "IN"); n != 0 {
		t.Fatalf("a refused interchange was recorded: %d", n)
	}
	putVendorProfileFor(t, h, partner)
	if rec := do(t, h, http.MethodGet, "/edi/v1/partners/"+partner+"/vendor-profile", "", "edi.admin", srcScope); !strings.Contains(rec.Body.String(), vendorID) {
		t.Fatalf("get profile: %s", rec.Body.String())
	}
	code, res = receiveVendor(t, h, "/edi/v1/inbound/x12/vendor", vendor810(t, "000000092", "V-7002", true))
	if code != http.StatusCreated || len(res.Messages) != 1 {
		t.Fatalf("receive: %d %+v", code, res)
	}
	if st := messageStatus(t, h, res.Messages[0].MessageID); st != "POSTED" {
		t.Fatalf("the vendor invoice message is %s", st)
	}
	if len(payables.got) != 1 {
		t.Fatalf("payables got %d invoices", len(payables.got))
	}
	g := payables.got[0]
	if g.Source != "X12_810" || g.SourceRef != res.Messages[0].MessageID || g.VendorID != vendorID || g.LegalEntityID != srcLE || g.InvoiceNumber != "V-7002" ||
		g.InvoiceDate != "2026-11-11" || g.Currency != "USD" || g.Gross != "86.8" || g.PurchaseOrderNumber != "PO-1001" || g.Purpose != "standard" ||
		len(g.Lines) != 1 || g.Lines[0].Quantity != "10" || g.Lines[0].UnitPrice != "8" || g.Lines[0].Item != "prod-a" || g.Lines[0].Unit != "EA" {
		t.Fatalf("intake request: %+v", g)
	}
	if len(payables.actors) != 1 || payables.actors[0] != "service:edi-cxml@"+srcLE {
		t.Fatalf("payables was called as %v", payables.actors)
	}
	if docs := documents(t, h); len(docs) != 0 {
		t.Fatalf("a vendor invoice was matched as self-billed: %+v", docs)
	}
	if rec := do(t, h, http.MethodGet, "/edi/v1/messages/"+res.Messages[0].MessageID, "", "edi.operate", srcScope); !strings.Contains(rec.Body.String(), `"vendor":true`) || strings.Contains(rec.Body.String(), `"selfBilled":true`) {
		t.Fatalf("canonical: %s", rec.Body.String())
	}
	// A partner with a vendor profile has its 810s read this way on the generic route too.
	code, res = receiveVendor(t, h, "/edi/v1/inbound/x12", vendor810(t, "000000093", "V-7003", true))
	if code != http.StatusCreated || len(payables.got) != 2 || payables.got[1].InvoiceNumber != "V-7003" {
		t.Fatalf("generic route: %d %+v, payables %d", code, res, len(payables.got))
	}
}

// Without payables configured the invoice is recorded and stays MAPPED; POST /messages/{id}:post puts it into the intake once it is, and a
// second post returns the intake row already created.
func TestVendor810_StaysMappedWithoutPayablesAndPostsLater(t *testing.T) {
	a, h, partner := vendorSetup(t)
	putVendorProfileFor(t, h, partner)
	_, res := receiveVendor(t, h, "/edi/v1/inbound/x12/vendor", vendor810(t, "000000094", "V-7004", true))
	id := res.Messages[0].MessageID
	if st := messageStatus(t, h, id); st != "MAPPED" {
		t.Fatalf("message is %s", st)
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/messages/"+id+":post", "", "edi.operate", srcScope); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("post without payables: %d %s", rec.Code, rec.Body.String())
	}
	payables := &fakePayables{}
	a.VendorInvoices = payables
	rec := do(t, h, http.MethodPost, "/edi/v1/messages/"+id+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"downstreamKind":"VENDOR_INVOICE"`) || messageStatus(t, h, id) != "POSTED" {
		t.Fatalf("post: %d %s", rec.Code, rec.Body.String())
	}
	if again := do(t, h, http.MethodPost, "/edi/v1/messages/"+id+":post", "", "edi.operate", srcScope); again.Code != http.StatusOK || len(payables.got) != 1 {
		t.Fatalf("second post: %d, payables %d", again.Code, len(payables.got))
	}
}

// A document payables or the mapping refuses is parked with its code, and posts once the cause is fixed.
func TestVendor810_ParksWhatCannotBeTakenAndPostsAgainWhenFixed(t *testing.T) {
	a, h, partner := vendorSetup(t)
	payables := &fakePayables{refuse: &sources.DownstreamRefusedError{Service: "payables", Status: 409, Code: "AP-VII-002", Detail: "invoice V-7005 of this vendor was already received"}}
	a.VendorInvoices = payables
	putVendorProfileFor(t, h, partner)
	_, res := receiveVendor(t, h, "/edi/v1/inbound/x12/vendor", vendor810(t, "000000095", "V-7005", true))
	id := res.Messages[0].MessageID
	if st := messageStatus(t, h, id); st != "FAILED" {
		t.Fatalf("a refused vendor invoice is %s", st)
	}
	rec := do(t, h, http.MethodPost, "/edi/v1/messages/"+id+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeCXMLDownstream) || !strings.Contains(rec.Body.String(), "AP-VII-002") {
		t.Fatalf("parked post: %d %s", rec.Code, rec.Body.String())
	}
	payables.refuse = nil
	if rec := do(t, h, http.MethodPost, "/edi/v1/messages/"+id+":post", "", "edi.operate", srcScope); rec.Code != http.StatusOK || messageStatus(t, h, id) != "POSTED" {
		t.Fatalf("post after the fix: %d %s", rec.Code, rec.Body.String())
	}
	// An invoice with no currency cannot go into the intake: parked, EDI-0038.
	_, res = receiveVendor(t, h, "/edi/v1/inbound/x12/vendor", vendor810(t, "000000096", "V-7006", false))
	rec = do(t, h, http.MethodPost, "/edi/v1/messages/"+res.Messages[0].MessageID+":post", "", "edi.operate", srcScope)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), domain.CodeCXMLInvoiceTarget) || !strings.Contains(rec.Body.String(), "currency") {
		t.Fatalf("no currency: %d %s", rec.Code, rec.Body.String())
	}
	// A transient failure is not a park: the message stays as it was and the post can be repeated.
	payables.failing = context.DeadlineExceeded
	_, res = receiveVendor(t, h, "/edi/v1/inbound/x12/vendor", vendor810(t, "000000097", "V-7007", true))
	if rec := do(t, h, http.MethodPost, "/edi/v1/messages/"+res.Messages[0].MessageID+":post", "", "edi.operate", srcScope); rec.Code != http.StatusBadGateway {
		t.Fatalf("outage: %d %s", rec.Code, rec.Body.String())
	}
	if st := messageStatus(t, h, res.Messages[0].MessageID); st != "MAPPED" {
		t.Fatalf("after an outage the message is %s", st)
	}
}

func TestVendor810_ProfileIsChecked(t *testing.T) {
	_, h, partner := vendorSetup(t)
	for name, body := range map[string]string{
		"legalEntityId": `{"legalEntityId":"nope","vendorId":"` + vendorID + `"}`,
		"vendorId":      `{"legalEntityId":"` + srcLE + `","vendorId":"x"}`,
	} {
		rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/vendor-profile", body, "edi.admin", srcScope)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), name) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := do(t, h, http.MethodGet, "/edi/v1/partners/"+partner+"/vendor-profile", "", "edi.admin", srcScope); rec.Code != http.StatusNotFound {
		t.Fatalf("no profile yet: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/0b1c2d3e-0000-4000-8000-0000000000ee/vendor-profile", `{"legalEntityId":"`+srcLE+`","vendorId":"`+vendorID+`"}`, "edi.admin", srcScope); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown partner: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/vendor-profile", `{"legalEntityId":"`+srcLE+`","vendorId":"`+vendorID+`"}`, "edi.operate", srcScope); rec.Code != http.StatusForbidden {
		t.Fatalf("without edi.admin: %d", rec.Code)
	}
}

// vendorSetup is a partner that sends 810s and is not a self-billing customer.
func vendorSetup(t *testing.T) (*API, http.Handler, string) {
	t.Helper()
	a, h, _, partner, _ := autoSetupWith(t, newFake(), edifactDistributor)
	return a, h, partner
}

// A partner is a self-billing customer or a vendor for its 810s, never both: each setting refuses the other, whatever legal entity the
// operator is scoped to.
func TestVendor810_ExcludesSelfBillingOnTheSamePartner(t *testing.T) {
	_, _, h, partner := selfBillSetup(t) // self-billing settings exist
	body := `{"legalEntityId":"` + srcLE + `","vendorId":"` + vendorID + `"}`
	if rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/vendor-profile", body, "edi.admin", srcScope); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeVendorOrSelfBilling) {
		t.Fatalf("vendor profile on a self-billing partner: %d %s", rec.Code, rec.Body.String())
	}
	_, h2, partner2 := vendorSetup(t)
	putVendorProfileFor(t, h2, partner2)
	if rec := do(t, h2, http.MethodPut, "/edi/v1/partners/"+partner2+"/self-billing", `{"taxRatePct":"8.5","waitDays":30}`, "edi.admin", srcScope); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeVendorOrSelfBilling) {
		t.Fatalf("self-billing on a vendor partner: %d %s", rec.Code, rec.Body.String())
	}
}

// An operator scoped to another legal entity still gets the vendor routing: the profile is read across entities.
func TestVendor810_RoutingDoesNotDependOnTheOperatorsLegalEntity(t *testing.T) {
	a, h, partner := vendorSetup(t)
	payables := &fakePayables{}
	a.VendorInvoices = payables
	putVendorProfileFor(t, h, partner)
	elsewhere := map[string]string{tenant.HeaderLegalEntity: "0190a5f2-7b1c-7d3e-8a4f-0000000000f1"}
	rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", vendor810(t, "000000098", "V-7008", true), "edi.operate", elsewhere)
	if rec.Code != http.StatusCreated || len(payables.got) != 1 || payables.got[0].InvoiceNumber != "V-7008" {
		t.Fatalf("receive from another scope: %d %s, payables %d", rec.Code, rec.Body.String(), len(payables.got))
	}
	if docs := documents(t, h); len(docs) != 0 {
		t.Fatalf("matched as self-billed: %+v", docs)
	}
}
