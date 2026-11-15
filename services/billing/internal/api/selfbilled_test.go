package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/billing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

func (f *fakeStore) PostSelfBilled(_ context.Context, inv store.Invoice, number string) (store.Invoice, bool, error) {
	for _, e := range f.invoices {
		if e.IdempotencyKey == inv.IdempotencyKey {
			if e.RequestHash != inv.RequestHash {
				return store.Invoice{}, false, store.ErrSelfBilledKeyReuse
			}
			return e, false, nil
		}
	}
	inv.Number, inv.Status, inv.Version = number, domain.StatusPosted, 2
	f.invoices[inv.ID] = inv
	f.selfPosts++
	return inv, true, nil
}

const selfBilledDuty = "billing.self-billed.post"

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
	h := setup(t, s, &fakeTax{perLine: "99.00"}) // the tax engine is not asked: the issuer is the customer
	selfBilledAgreement(t, h)
	rec := doAs(h, http.MethodPost, pathSelfBilled, "", selfBilledBody("SB-2027-0001", "15.20"), selfBilledDuty, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("post: %d %s", rec.Code, rec.Body)
	}
	var out invoiceDTO
	decode(t, rec, &out)
	if out.Number != "SB-2027-0001" || out.Status != domain.StatusPosted || out.Type != domain.TypeShipment || out.CustomerID != custID ||
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
	h := setup(t, s, &fakeTax{perLine: "0"})
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
	h := setup(t, s, &fakeTax{perLine: "0"})
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
