package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/services/billing/internal/domain"
	"github.com/BenjaminSRussell/depot/services/billing/internal/store"
)

func (f *fakeStore) PostSelfBilled(_ context.Context, inv store.Invoice, number, documentNumber string) (store.Invoice, bool, error) {
	for _, e := range f.invoices {
		if e.IdempotencyKey == inv.IdempotencyKey {
			if e.RequestHash != inv.RequestHash {
				return store.Invoice{}, false, store.ErrSelfBilledKeyReuse
			}
			return e, false, nil
		}
	}
	inv.Number, inv.Status, inv.Version, inv.CustomerDocumentNumber = number, domain.StatusPosted, 2, documentNumber
	f.invoices[inv.ID] = inv
	f.selfPosts++
	return inv, true, nil
}

const selfBilledDuty = "billing.self-billed.post"

// fakePeriods answers which dates have an open receivables period.
type fakePeriods struct {
	closedBefore time.Time // dates before this are in a closed period
	err          error
	asked        []string
}

func (f *fakePeriods) Open(_ context.Context, _, _ string, d time.Time) (bool, error) {
	f.asked = append(f.asked, d.Format("2006-01-02"))
	return f.err == nil && !d.Before(f.closedBefore), f.err
}

// selfBilledAPI is setup with the legal entity's ledger profile and a period checker; the clock is 2027-03-25.
func selfBilledAPI(t *testing.T, s *fakeStore, p *fakePeriods) http.Handler {
	t.Helper()
	a, h := setupAPI(t, s, &fakeTax{perLine: "99.00"})
	a.profiles = map[string]store.GLProfile{leID: {LegalEntityID: leID, LedgerID: "0190a5f2-7b1c-7d3e-8a4f-0000000000f9"}}
	a.now = func() time.Time { return time.Date(2027, 3, 25, 10, 0, 0, 0, time.UTC) }
	if p != nil {
		a.WithPeriods(p)
	}
	return h
}

func selfBilledBody(number, tax string) string {
	return `{"legalEntityId":"` + leID + `","customerId":"` + custID + `","sourceRef":"msg-1","documentNumber":"` + number + `","documentDate":"2027-03-20",` +
		`"agreementRef":"SBA-2027-01","currency":"USD","paymentTermDays":30,"lines":[` +
		`{"description":"Pump housing","qty":"10","unitPrice":"8.00","tax":"` + tax + `","revenueAccount":"4000","taxCode":"VAT19"}]}`
}

func selfBilledAgreement(t *testing.T, h http.Handler) {
	t.Helper()
	rule := `{"legalEntityId":"` + leID + `","rule":"SELF_BILLED","agreementRef":"SBA-2027-01","validFrom":"2027-03-01"}`
	if rec := do(h, http.MethodPut, "/v1/billing/customers/"+custID+"/billing-rule", "", rule); rec.Code != http.StatusOK {
		t.Fatalf("rule: %d %s", rec.Code, rec.Body)
	}
}

// #35 (35-5): a matched self-billed document is posted as the legal customer invoice under the document's own number, with the tax it
// states, once.
func TestSelfBilled_PostsTheCustomersDocumentAsTheLegalInvoice(t *testing.T) {
	s := newFakeStore()
	h := selfBilledAPI(t, s, &fakePeriods{}) // the tax engine is not asked: the issuer is the customer
	selfBilledAgreement(t, h)
	rec := doAs(h, http.MethodPost, pathSelfBilled, "", selfBilledBody("SB-2027-0001", "15.20"), selfBilledDuty, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	var out invoiceDTO
	decode(t, rec, &out)
	if out.Number != selfBilledNumber(custID, "SB-2027-0001") || out.CustomerDocumentNumber != "SB-2027-0001" || out.Status != domain.StatusPosted || out.Type != domain.TypeShipment || out.CustomerID != custID ||
		out.Totals.Net != "80.00" || out.Totals.Tax != "15.20" || out.Totals.Gross != "95.20" || out.InvoiceDate != "2027-03-20" || out.PostingDate != "2027-03-20" || out.DueDate != "2027-04-19" {
		t.Fatalf("invoice: %+v", out)
	}
	if s.selfPosts != 1 || s.creates != 0 {
		t.Fatalf("posts %d proposals %d", s.selfPosts, s.creates)
	}
	// The same document again returns the posted invoice and posts nothing.
	again := doAs(h, http.MethodPost, pathSelfBilled, "", selfBilledBody("SB-2027-0001", "15.20"), selfBilledDuty, nil)
	var replay invoiceDTO
	decode(t, again, &replay)
	if again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" || replay.ID != out.ID || s.selfPosts != 1 {
		t.Fatalf("replay: %d %v %+v posts %d", again.Code, again.Header(), replay, s.selfPosts)
	}
	// The same document with other content is refused.
	changed := doAs(h, http.MethodPost, pathSelfBilled, "", selfBilledBody("SB-2027-0001", "16.00"), selfBilledDuty, nil)
	if changed.Code != http.StatusConflict || !strings.Contains(changed.Body.String(), CodeSelfBilledReplay) {
		t.Fatalf("changed content: %d %s", changed.Code, changed.Body)
	}
}

func TestSelfBilled_NeedsTheRuleTheAgreementAndTheServiceDuty(t *testing.T) {
	s := newFakeStore()
	h := selfBilledAPI(t, s, &fakePeriods{})
	body := selfBilledBody("SB-1", "15.20")
	if rec := doAs(h, http.MethodPost, pathSelfBilled, "", body, selfBilledDuty, nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeNotSelfBilled) {
		t.Fatalf("a customer on no rule: %d %s", rec.Code, rec.Body)
	}
	selfBilledAgreement(t, h)
	if rec := doAs(h, http.MethodPost, pathSelfBilled, "", strings.Replace(body, `"documentDate":"2027-03-20"`, `"documentDate":"2027-02-20"`, 1), selfBilledDuty, nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeNotSelfBilled) {
		t.Fatalf("a document before the agreement starts: %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(h, http.MethodPost, pathSelfBilled, "", strings.Replace(body, "SBA-2027-01", "SBA-OTHER", 1), selfBilledDuty, nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeSelfBilledAgreement) {
		t.Fatalf("another agreement: %d %s", rec.Code, rec.Body)
	}
	for _, duties := range []string{allDuties, "billing.invoice.post"} {
		if rec := doAs(h, http.MethodPost, pathSelfBilled, "", body, duties, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("a caller without %s: %d", selfBilledDuty, rec.Code)
		}
	}
	if s.selfPosts != 0 {
		t.Fatalf("refused documents posted %d invoices", s.selfPosts)
	}
}

func TestSelfBilled_ChecksTheDocument(t *testing.T) {
	s := newFakeStore()
	h := selfBilledAPI(t, s, &fakePeriods{})
	selfBilledAgreement(t, h)
	good := selfBilledBody("SB-1", "15.20")
	for field, body := range map[string]string{
		"documentNumber":  strings.Replace(good, `"documentNumber":"SB-1"`, `"documentNumber":" "`, 1),
		"sourceRef":       strings.Replace(good, `"sourceRef":"msg-1"`, `"sourceRef":""`, 1),
		"documentDate":    strings.Replace(good, `"documentDate":"2027-03-20"`, `"documentDate":"20.03.2027"`, 1),
		"currency":        strings.Replace(good, `"currency":"USD"`, `"currency":"usd"`, 1),
		"customerId":      strings.Replace(good, custID, "nope", 1),
		"lines[0].tax":    strings.Replace(good, `"tax":"15.20"`, `"tax":"-1"`, 1),
		"lines[0].qty":    strings.Replace(good, `"qty":"10"`, `"qty":"0"`, 1),
		"paymentTermDays": strings.Replace(good, `"paymentTermDays":30`, `"paymentTermDays":900`, 1),
	} {
		rec := doAs(h, http.MethodPost, pathSelfBilled, "", body, selfBilledDuty, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), field) {
			t.Errorf("%s: %d %s", field, rec.Code, rec.Body)
		}
	}
	if s.selfPosts != 0 {
		t.Fatalf("refused documents posted %d invoices", s.selfPosts)
	}
}

// A document dated in a closed period is posted on the first open date, with the customer's date kept and a note; a date in the future is
// refused; with both periods closed, or no period service, nothing is posted.
func TestSelfBilled_NeverPostsIntoAClosedPeriodOrTheFuture(t *testing.T) {
	s := newFakeStore()
	periods := &fakePeriods{closedBefore: time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)}
	h := selfBilledAPI(t, s, periods)
	selfBilledAgreement(t, h)
	closedDoc := strings.Replace(selfBilledBody("SB-9", "15.20"), `"documentDate":"2027-03-20"`, `"documentDate":"2027-02-20"`, 1)
	closedDoc = strings.Replace(closedDoc, `"validFrom":"2027-03-01"`, `"validFrom":"2027-03-01"`, 1)
	// The rule starts on 2027-03-01, so use a later rule start for this document.
	rule := `{"legalEntityId":"` + leID + `","rule":"SELF_BILLED","agreementRef":"SBA-2027-01","validFrom":"2027-01-01"}`
	doAs(h, http.MethodPut, "/v1/billing/customers/"+custID+"/billing-rule", "", rule, allDuties, map[string]string{"If-Match": `"1"`})
	rec := doAs(h, http.MethodPost, pathSelfBilled, "", closedDoc, selfBilledDuty, nil)
	var out invoiceDTO
	decode(t, rec, &out)
	if rec.Code != http.StatusCreated || out.PostingDate != "2027-03-25" || out.InvoiceDate != "2027-02-20" || !strings.Contains(out.PostingNote, "closed receivables period") {
		t.Fatalf("closed period: %d %s", rec.Code, rec.Body)
	}
	if rec := doAs(h, http.MethodPost, pathSelfBilled, "", strings.Replace(selfBilledBody("SB-10", "15.20"), "2027-03-20", "2027-03-27", 1), selfBilledDuty, nil); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), CodeSelfBilledFuture) {
		t.Fatalf("future: %d %s", rec.Code, rec.Body)
	}
	periods.closedBefore = time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	if rec := doAs(h, http.MethodPost, pathSelfBilled, "", strings.Replace(selfBilledBody("SB-11", "15.20"), "msg-1", "msg-2", 1), selfBilledDuty, nil); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeSelfBilledPeriod) {
		t.Fatalf("both closed: %d %s", rec.Code, rec.Body)
	}
	periods.err = errors.New("close is down")
	if rec := doAs(h, http.MethodPost, pathSelfBilled, "", strings.Replace(selfBilledBody("SB-12", "15.20"), "msg-1", "msg-3", 1), selfBilledDuty, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("close down: %d %s", rec.Code, rec.Body)
	}
	noPeriods := selfBilledAPI(t, newFakeStore(), nil)
	selfBilledAgreement(t, noPeriods)
	if rec := doAs(noPeriods, http.MethodPost, pathSelfBilled, "", selfBilledBody("SB-13", "15.20"), selfBilledDuty, nil); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), CodeSelfBilledPeriod) {
		t.Fatalf("no period service: %d %s", rec.Code, rec.Body)
	}
}

func TestSelfBilledNumber_IsInTheCustomersOwnNamespace(t *testing.T) {
	a, b := selfBilledNumber("0190a5f2-7b1c-7d3e-8a4f-123456789a01", "1001"), selfBilledNumber("0190a5f2-7b1c-7d3e-8a4f-123456789b02", "1001")
	if a == b || !strings.HasPrefix(a, "SB-") || strings.HasPrefix(a, "INV-") {
		t.Fatalf("%s %s", a, b)
	}
}

// #35: a line that names its sales order and order line keeps them on the invoice line, so the invoice.posted event and the report to sales
// tell sales which order line was invoiced; a malformed id is refused.
func TestSelfBilled_LinesKeepTheirSalesOrderReferences(t *testing.T) {
	s := newFakeStore()
	h := selfBilledAPI(t, s, &fakePeriods{})
	selfBilledAgreement(t, h)
	const orderID, lineID, productID = "0190a5f2-7b1c-7d3e-8a4f-0000000000a1", "0190a5f2-7b1c-7d3e-8a4f-0000000000a2", "0190a5f2-7b1c-7d3e-8a4f-0000000000a3"
	body := strings.Replace(selfBilledBody("SB-2027-0009", "15.20"), `"taxCode":"VAT19"}`, `"taxCode":"VAT19","orderId":"`+orderID+`","orderLineId":"`+lineID+`","productId":"`+productID+`"}`, 1)
	rec := doAs(h, http.MethodPost, pathSelfBilled, "", body, selfBilledDuty, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	var out invoiceDTO
	decode(t, rec, &out)
	src := s.invoices[out.ID].Lines[0].Source
	if src.OrderID != orderID || src.OrderLineID != lineID || src.ProductID != productID {
		t.Fatalf("line source %+v", src)
	}
	bad := strings.Replace(selfBilledBody("SB-2027-0010", "15.20"), `"taxCode":"VAT19"}`, `"taxCode":"VAT19","orderLineId":"ol-1"}`, 1)
	if rec := doAs(h, http.MethodPost, pathSelfBilled, "", bad, selfBilledDuty, nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "orderLineId") {
		t.Fatalf("malformed id: %d %s", rec.Code, rec.Body)
	}
}
