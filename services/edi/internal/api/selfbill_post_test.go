package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/edi/internal/domain"
	"github.com/BenjaminSRussell/passage/services/edi/internal/sources"
	"github.com/BenjaminSRussell/passage/services/edi/internal/store"
)

// fakeBilling is billing's POST /v1/billing/self-billed-invoices: one invoice per key, the same key answered with the same invoice.
type fakeBilling struct {
	mu      sync.Mutex
	posts   []domain.SelfBilledInvoiceRequest
	keys    []string
	byKey   map[string]domain.SelfBilledInvoiceRef
	fail    error
	actors  []string
	duties  []string
	refused *sources.DownstreamRefusedError
}

func (b *fakeBilling) PostSelfBilled(ctx context.Context, legalEntityID, key string, body domain.SelfBilledInvoiceRequest) (domain.SelfBilledInvoiceRef, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if p, ok := tenant.FromContext(ctx); ok {
		b.actors = append(b.actors, p.UserID)
		for d := range p.Duties {
			b.duties = append(b.duties, d)
		}
	}
	if b.fail != nil {
		return domain.SelfBilledInvoiceRef{}, b.fail
	}
	if b.refused != nil {
		return domain.SelfBilledInvoiceRef{}, b.refused
	}
	b.posts = append(b.posts, body)
	b.keys = append(b.keys, key)
	if b.byKey == nil {
		b.byKey = map[string]domain.SelfBilledInvoiceRef{}
	}
	if ref, ok := b.byKey[key]; ok {
		return ref, nil
	}
	ref := domain.SelfBilledInvoiceRef{ID: "7d0a8e2c-0000-4000-8000-0000000000b" + string(rune('0'+len(b.byKey))), Number: body.DocumentNumber, Status: "POSTED"}
	b.byKey[key] = ref
	return ref, nil
}

const selfBillSettings = `{"taxRatePct":"8.5","waitDays":30,"revenueAccount":"4000","taxCode":"VAT19","paymentTermDays":45}`

// skipBackoff lets the sweep retry now.
func skipBackoff(t *testing.T) {
	t.Helper()
	if _, err := lastPool.Exec(context.Background(), `update self_billing_document set next_post_at = null where post_parked_at is null`); err != nil {
		t.Fatal(err)
	}
}

func postedDocument(t *testing.T, h http.Handler) store.SelfBillDocument {
	t.Helper()
	docs := documents(t, h)
	if len(docs) != 1 {
		t.Fatalf("documents: %+v", docs)
	}
	return docs[0]
}

// #35 (35-5): a self-billed document that matched in full is posted by billing as our legal invoice under the customer's own number,
// with the tax the document states, as the service principal edi-selfbill with only the self-billed duty, once.
func TestSelfBilling_MatchedDocumentIsPostedAsTheLegalInvoice(t *testing.T) {
	f, a, h, partner := selfBillSetup(t)
	billing := &fakeBilling{}
	a.SelfBilledPoster = billing
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", selfBillSettings, "edi.admin", srcScope)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	rec := do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", selfBilled810(t, "SB-8101", "8", "6.80", "8680"), "edi.operate", srcScope)
	if rec.Code != http.StatusCreated {
		t.Fatalf("receive: %d %s", rec.Code, rec.Body.String())
	}
	if len(billing.posts) != 1 {
		t.Fatalf("billing was called %d times", len(billing.posts))
	}
	p := billing.posts[0]
	if p.DocumentNumber != "SB-8101" || p.LegalEntityID != srcLE || p.CustomerID != srcCustomer || p.Currency != "USD" || p.PaymentTermDays != 45 ||
		p.DocumentDate != "2026-11-11" || len(p.Lines) != 1 || p.Lines[0].Qty != "10" || p.Lines[0].UnitPrice != "8" || p.Lines[0].Tax != "6.8" ||
		p.Lines[0].RevenueAccount != "4000" || p.Lines[0].TaxCode != "VAT19" || !strings.Contains(p.Lines[0].Description, "prod-a") || !strings.Contains(p.Lines[0].Description, "SHP-0001") {
		t.Fatalf("request: %+v", p)
	}
	// The line names the sales order and order line its delivery note and PO line matched, and the product, so billing's invoice.posted
	// tells sales what was invoiced.
	if p.Lines[0].OrderID != srcOrder || p.Lines[0].OrderLineID != "ol-1" || p.Lines[0].ProductID != "" {
		t.Fatalf("order references: %+v", p.Lines[0])
	}
	doc := postedDocument(t, h)
	if doc.BillingInvoiceID == "" || doc.BillingNumber != "SB-8101" || doc.PostError != "" || p.SourceRef != doc.ID || billing.keys[0] != "self-billed:"+doc.ID {
		t.Fatalf("document: %+v key %v ref %s", doc, billing.keys, p.SourceRef)
	}
	if len(billing.actors) != 1 || billing.actors[0] != "service:edi-selfbill" || len(billing.duties) != 2 {
		t.Fatalf("billing was called as %v with %v", billing.actors, billing.duties)
	}
	// The sweep posts nothing a second time.
	if rec := do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"posted":0`) {
		t.Fatalf("sweep: %d %s", rec.Code, rec.Body.String())
	}
	if len(billing.posts) != 1 {
		t.Fatalf("billing was called again by the sweep: %d", len(billing.posts))
	}
}

// A document that is not matched in full is never posted: its differences are disputes for a person to settle first.
func TestSelfBilling_DisputedDocumentIsNotPosted(t *testing.T) {
	f, a, h, partner := selfBillSetup(t)
	billing := &fakeBilling{}
	a.SelfBilledPoster = billing
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", selfBillSettings, "edi.admin", srcScope)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	// The customer billed 9 per unit: a price difference.
	do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", selfBilled810(t, "SB-8102", "9", "7.65", "9765"), "edi.operate", srcScope)
	if doc := postedDocument(t, h); doc.Status != "DISPUTED" || doc.BillingInvoiceID != "" {
		t.Fatalf("document: %+v", doc)
	}
	if len(billing.posts) != 0 {
		t.Fatalf("a disputed document was posted: %+v", billing.posts)
	}
}

// A document held by a dispute posts once its disputes are decided: the sweep picks the RESOLVED document up and billing gets the lines at the
// customer's figures the dispute accepted, each naming its sales order line (#35).
func TestSelfBilling_ResolvedDocumentIsPostedBySweepAtTheAcceptedFigures(t *testing.T) {
	f, a, h, partner := selfBillSetup(t)
	billing := &fakeBilling{}
	a.SelfBilledPoster = billing
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", selfBillSettings, "edi.admin", srcScope)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", selfBilled810(t, "SB-8104", "9", "7.65", "9765"), "edi.operate", srcScope)
	if doc := postedDocument(t, h); doc.Status != "DISPUTED" || len(billing.posts) != 0 {
		t.Fatalf("held by the dispute: %+v", doc)
	}
	// Still open: the sweep leaves it.
	if rec := do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope); rec.Code != http.StatusOK || len(billing.posts) != 0 {
		t.Fatalf("sweep with the dispute open: %d %s", rec.Code, rec.Body.String())
	}
	open := disputes(t, h, "OPEN")
	if len(open) != 1 {
		t.Fatalf("disputes: %+v", open)
	}
	if rec := do(t, h, http.MethodPost, "/edi/v1/self-billing/disputes/"+open[0].ID+":resolve", `{"decision":"ACCEPTED","note":"price change agreed"}`, "edi.self-billing.reconcile", srcScope); rec.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", rec.Code, rec.Body.String())
	}
	if len(billing.posts) != 0 {
		t.Fatalf("posted by the resolution itself: %+v", billing.posts)
	}
	rec := do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"posted":1`) || len(billing.posts) != 1 {
		t.Fatalf("sweep after the resolution: %d %s", rec.Code, rec.Body.String())
	}
	p := billing.posts[0]
	if p.DocumentNumber != "SB-8104" || len(p.Lines) != 1 || p.Lines[0].UnitPrice != "9" || p.Lines[0].Tax != "7.65" || p.Lines[0].OrderID != srcOrder || p.Lines[0].OrderLineID != "ol-1" {
		t.Fatalf("request: %+v", p)
	}
	if doc := postedDocument(t, h); doc.Status != "RESOLVED" || doc.BillingNumber != "SB-8104" || doc.PostError != "" {
		t.Fatalf("document: %+v", doc)
	}
	// Once posted, the sweep posts nothing again.
	do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope)
	if len(billing.posts) != 1 {
		t.Fatalf("posted again: %d", len(billing.posts))
	}
}

// A dispute rejected keeps our figures and leaves the correction to the customer: a document left with no accepted or matched line has nothing to
// invoice, and is parked with that reason instead of being retried for ever.
func TestSelfBilling_ADocumentWithEveryLineRejectedIsNotPosted(t *testing.T) {
	f, a, h, partner := selfBillSetup(t)
	billing := &fakeBilling{}
	a.SelfBilledPoster = billing
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", selfBillSettings, "edi.admin", srcScope)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", selfBilled810(t, "SB-8105", "9", "7.65", "9765"), "edi.operate", srcScope)
	open := disputes(t, h, "OPEN")
	if len(open) != 1 {
		t.Fatalf("disputes: %+v", open)
	}
	do(t, h, http.MethodPost, "/edi/v1/self-billing/disputes/"+open[0].ID+":resolve", `{"decision":"REJECTED","note":"our price stands"}`, "edi.self-billing.reconcile", srcScope)
	do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope)
	doc := postedDocument(t, h)
	if len(billing.posts) != 0 || doc.Status != "RESOLVED" || doc.BillingInvoiceID != "" || !strings.Contains(doc.PostError, "nothing to invoice") || !strings.Contains(doc.PostError, "Parked") {
		t.Fatalf("document %+v billing %+v", doc, billing.posts)
	}
}

// A post that fails (billing down, a setting missing) leaves the match as it is, says why, and the sweep posts it once it can.
func TestSelfBilling_FailedPostIsRetriedBySweep(t *testing.T) {
	f, a, h, partner := selfBillSetup(t)
	billing := &fakeBilling{fail: errors.New("billing: connection refused")}
	a.SelfBilledPoster = billing
	// No revenue account yet: the document waits with the reason.
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", selfBilled810(t, "SB-8103", "8", "6.80", "8680"), "edi.operate", srcScope)
	doc := postedDocument(t, h)
	if doc.Status != "MATCHED" || doc.BillingInvoiceID != "" || !strings.Contains(doc.PostError, "revenue account") || len(billing.posts) != 0 {
		t.Fatalf("without settings: %+v", doc)
	}
	// With the settings but billing down.
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", selfBillSettings, "edi.admin", srcScope)
	if rec := do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"posted":0`) {
		t.Fatalf("sweep while billing is down: %d %s", rec.Code, rec.Body.String())
	}
	if doc = postedDocument(t, h); doc.BillingInvoiceID != "" || !strings.Contains(doc.PostError, "connection refused") {
		t.Fatalf("while billing is down: %+v", doc)
	}
	// Billing refuses the document for a reason in the customer's rule: recorded, the document keeps waiting.
	billing.fail = nil
	billing.refused = &sources.DownstreamRefusedError{Service: "billing", Status: 409, Code: "BIL-0152", Detail: "the customer has no SELF_BILLED rule in force"}
	if rec := do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope); !strings.Contains(rec.Body.String(), `"posted":0`) {
		t.Fatalf("sweep inside the backoff: %s", rec.Body.String())
	}
	if doc = postedDocument(t, h); !strings.Contains(doc.PostError, "connection refused") {
		t.Fatalf("a document inside its backoff was tried again: %+v", doc)
	}
	skipBackoff(t)
	do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope)
	if doc = postedDocument(t, h); doc.BillingInvoiceID != "" || !strings.Contains(doc.PostError, "BIL-0152") {
		t.Fatalf("refused: %+v", doc)
	}
	// Billing recovers: the sweep posts it.
	billing.refused = nil
	skipBackoff(t)
	rec := do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"posted":1`) {
		t.Fatalf("sweep after recovery: %d %s", rec.Code, rec.Body.String())
	}
	if doc = postedDocument(t, h); doc.BillingInvoiceID == "" || doc.BillingNumber != "SB-8103" || doc.PostError != "" {
		t.Fatalf("after recovery: %+v", doc)
	}
}

func TestSelfBilling_SettingsForTheInvoiceAreChecked(t *testing.T) {
	_, _, h, partner := selfBillSetup(t)
	for field, body := range map[string]string{
		"revenueAccount":  `{"revenueAccount":"40 00"}`,
		"taxCode":         `{"taxCode":"` + strings.Repeat("V", 21) + `"}`,
		"paymentTermDays": `{"paymentTermDays":400}`,
	} {
		rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", body, "edi.admin", srcScope)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), field) {
			t.Errorf("%s: %d %s", field, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", selfBillSettings, "edi.admin", srcScope)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"revenueAccount":"4000"`) || !strings.Contains(rec.Body.String(), `"paymentTermDays":45`) {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
	}
	if get := do(t, h, http.MethodGet, "/edi/v1/partners/"+partner+"/self-billing", "", "edi.admin", srcScope); !strings.Contains(get.Body.String(), `"taxCode":"VAT19"`) {
		t.Fatalf("get: %s", get.Body.String())
	}
}

// A document billing holds with other content (BIL-0154) is parked at once with a clear reason, is not tried again by the sweep, and
// gets a fresh start when the partner's settings are saved.
func TestSelfBilling_ConflictParksTheDocumentUntilTheSettingsChange(t *testing.T) {
	f, a, h, partner := selfBillSetup(t)
	billing := &fakeBilling{refused: &sources.DownstreamRefusedError{Service: "billing", Status: 409, Code: "BIL-0154", Detail: "other content"}}
	a.SelfBilledPoster = billing
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", selfBillSettings, "edi.admin", srcScope)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", selfBilled810(t, "SB-8104", "8", "6.80", "8680"), "edi.operate", srcScope)
	doc := postedDocument(t, h)
	if !strings.Contains(doc.PostError, "Parked after 1 attempt") || !strings.Contains(doc.PostError, "BIL-0154") {
		t.Fatalf("not parked: %+v", doc)
	}
	billing.refused = nil
	skipBackoff(t)
	if rec := do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope); !strings.Contains(rec.Body.String(), `"posted":0`) {
		t.Fatalf("a parked document was tried: %s", rec.Body.String())
	}
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", selfBillSettings, "edi.admin", srcScope)
	if rec := do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope); !strings.Contains(rec.Body.String(), `"posted":1`) {
		t.Fatalf("after the settings were saved: %s", rec.Body.String())
	}
}

// Failures back off and park after the attempt cap.
func TestSelfBilling_RepeatedFailuresParkAfterTheCap(t *testing.T) {
	f, a, h, partner := selfBillSetup(t)
	a.SelfBilledPoster = &fakeBilling{fail: errors.New("billing: connection refused")}
	do(t, h, http.MethodPut, "/edi/v1/partners/"+partner+"/self-billing", selfBillSettings, "edi.admin", srcScope)
	sendShipment(t, f, h, partner, srcShipment, "SHP-0001", time.Now().AddDate(0, 0, -3))
	do(t, h, http.MethodPost, "/edi/v1/inbound/x12?environment=TEST", selfBilled810(t, "SB-8105", "8", "6.80", "8680"), "edi.operate", srcScope)
	for i := 1; i < store.MaxSelfBillPostAttempts; i++ {
		skipBackoff(t)
		do(t, h, http.MethodPost, "/edi/v1/self-billing:sweep", "", "edi.self-billing.reconcile", srcScope)
	}
	doc := postedDocument(t, h)
	if !strings.Contains(doc.PostError, "Parked after 8 attempt") {
		t.Fatalf("after the cap: %+v", doc)
	}
}
