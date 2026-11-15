package api_test

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/print/internal/api"
	"github.com/BenjaminSRussell/passage/services/print/internal/delivery"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
	"github.com/BenjaminSRussell/passage/services/print/migrations"
)

const (
	leA   = "0190a5f2-7b1c-7d3e-8a4f-123456789a01"
	custA = "0190a5f2-7b1c-7d3e-8a4f-123456789b01"
	custB = "0190a5f2-7b1c-7d3e-8a4f-123456789b02"
)

type outbox struct {
	mu   sync.Mutex
	sent []delivery.Message
}

func (o *outbox) Send(_ context.Context, m delivery.Message) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent = append(o.sent, m)
	return nil
}

// inEntity is call with the caller's legal-entity claim set to leA.
func inEntity(t *testing.T, h http.Handler, method, path, body, duties, key string) *httptest.ResponseRecorder {
	t.Helper()
	return callAs(t, h, method, path, body, duties, key, leA)
}

func dbRouter(t *testing.T, sender api.Sender, opts ...api.Option) http.Handler {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return router(store.New(app), sender, opts...)
}

const invoiceDoc = `{"documentType":"CUSTOMER_INVOICE","number":"INV-9","legalEntity":{"name":"Cedar Ridge Pumps"},"columns":[{"header":"Item"}],"lines":[["Casting"]]`

func TestDeliverDocument_SendsByTheOutputPlanOnceAndReportsEveryDestination(t *testing.T) {
	box := &outbox{}
	h := dbRouter(t, &fakeSender{}, api.WithMailer(box))
	seedRule := func(key, body string) {
		if rec := call(t, h, http.MethodPost, "/print/v1/output-rules", body, tenantAdmin, key); rec.Code != http.StatusCreated {
			t.Fatalf("rule %s: %d %s", key, rec.Code, rec.Body)
		}
	}
	seedRule("r1", `{"documentType":"CUSTOMER_INVOICE","copyKind":"ORIGINAL","channel":"EMAIL","destination":"ap@bluewater.example","copies":1,"sequence":1}`)
	seedRule("r2", `{"documentType":"CUSTOMER_INVOICE","copyKind":"ORIGINAL","channel":"PORTAL","copies":1,"sequence":1}`)
	seedRule("r3", `{"documentType":"CUSTOMER_INVOICE","copyKind":"COPY","channel":"ARCHIVE","copies":1,"sequence":1}`)
	seedRule("r4", `{"documentType":"CUSTOMER_INVOICE","copyKind":"ORIGINAL","channel":"EINVOICE","copies":1,"sequence":2}`)
	seedRule("r5", `{"documentType":"CUSTOMER_INVOICE","copyKind":"ORIGINAL","channel":"PRINTER","destination":"OFFICE","copies":1,"sequence":3}`)
	body := invoiceDoc + `,"sourceKey":"invoice:INV-9","accountId":"` + custA + `","legalEntityId":"` + leA + `"}`
	rec := inEntity(t, h, http.MethodPost, "/print/v1/documents:deliver", body, "print.document.deliver", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deliver: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Pages   int `json:"pages"`
		Results []struct{ Channel, Status, Detail string }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Pages != 1 || len(out.Results) != 5 {
		t.Fatalf("response: %v %s", err, rec.Body)
	}
	status := map[string]string{}
	for _, r := range out.Results {
		status[r.Channel] = r.Status
	}
	// No printer is registered for the OFFICE queue, so that destination fails with its reason; the archive and the e-invoice network are skipped.
	if status["EMAIL"] != "SENT" || status["PORTAL"] != "SENT" || status["ARCHIVE"] != "SKIPPED" || status["EINVOICE"] != "SKIPPED" || status["PRINTER"] != "FAILED" {
		t.Fatalf("statuses: %v", status)
	}
	if len(box.sent) != 1 || box.sent[0].To != "ap@bluewater.example" || !strings.HasPrefix(string(box.sent[0].Attachment), "%PDF-") {
		t.Fatalf("mail: %+v", box.sent)
	}
	// Delivering the same document again sends no second mail.
	if rec := inEntity(t, h, http.MethodPost, "/print/v1/documents:deliver", body, "print.document.deliver", ""); rec.Code != http.StatusOK || len(box.sent) != 1 {
		t.Fatalf("repeat: %d mails %d", rec.Code, len(box.sent))
	}
	// The portal reads the list and the PDF for the customer of its token only.
	portal := newPortalCaller(t, h)
	list := portal.get(custA, "/print/v1/customer-portal/documents")
	var docs struct {
		Items []struct{ ID, Number, DocumentType string }
	}
	if err := json.Unmarshal(list.Body.Bytes(), &docs); err != nil || list.Code != http.StatusOK || len(docs.Items) != 1 || docs.Items[0].Number != "INV-9" {
		t.Fatalf("portal list: %d %s", list.Code, list.Body)
	}
	pdf := portal.get(custA, "/print/v1/customer-portal/documents/"+docs.Items[0].ID+"/pdf")
	if pdf.Code != http.StatusOK || pdf.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(pdf.Body.String(), "%PDF-") {
		t.Fatalf("portal pdf: %d %v", pdf.Code, pdf.Header())
	}
	if other := portal.get(custB, "/print/v1/customer-portal/documents/"+docs.Items[0].ID+"/pdf"); other.Code != http.StatusNotFound {
		t.Fatalf("another customer's pdf: %d", other.Code)
	}
	if none := portal.get(custB, "/print/v1/customer-portal/documents"); none.Code != http.StatusOK || !strings.Contains(none.Body.String(), `"items":[]`) {
		t.Fatalf("another customer's list: %d %s", none.Code, none.Body)
	}
	// A query naming another customer changes nothing: who the call is for is in the token.
	if leak := portal.get(custB, "/print/v1/customer-portal/documents?accountId="+custA); !strings.Contains(leak.Body.String(), `"items":[]`) {
		t.Fatalf("accountId in the query: %s", leak.Body)
	}
	// A user, even one holding every print duty, is not the portal and has no customer.
	if user := call(t, h, http.MethodGet, "/print/v1/customer-portal/documents", "", "print.customer-portal.documents", ""); user.Code != http.StatusForbidden {
		t.Fatalf("portal list as a user: %d", user.Code)
	}
}

// portalCaller signs the customer portal's service tokens for print.
type portalCaller struct {
	h      http.Handler
	signer *tenant.ServiceSigner
}

func newPortalCaller(t *testing.T, h http.Handler) *portalCaller {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner(tenant.CustomerPortalIssuer, priv)
	if err != nil {
		t.Fatal(err)
	}
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("print", map[string]ed25519.PublicKey{tenant.CustomerPortalIssuer: pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	return &portalCaller{h: h, signer: signer}
}

func (p *portalCaller) get(customer, path string) *httptest.ResponseRecorder {
	token, err := p.signer.MintDuties("print", tenant.Principal{TenantID: testkit.TenantID(1), LegalEntityID: leA, UserID: "contact", CustomerID: customer, Duties: map[string]struct{}{}}, "", []string{"print.customer-portal.documents"})
	if err != nil {
		panic(err)
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Passage-Service-Token", token)
	rec := httptest.NewRecorder()
	p.h.ServeHTTP(rec, req)
	return rec
}

func TestDeliverDocument_ChecksItsRequest(t *testing.T) {
	h := dbRouter(t, &fakeSender{}, api.WithMailer(&outbox{}))
	for name, body := range map[string]string{
		"no source key":    invoiceDoc + `}`,
		"bad account":      invoiceDoc + `,"sourceKey":"k","accountId":"nope"}`,
		"bad legal entity": invoiceDoc + `,"sourceKey":"k","legalEntityId":"nope"}`,
	} {
		if rec := inEntity(t, h, http.MethodPost, "/print/v1/documents:deliver", body, "print.document.deliver", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := call(t, h, http.MethodPost, "/print/v1/documents:deliver", invoiceDoc+`,"sourceKey":"k"}`, "print.document.render", ""); rec.Code != http.StatusForbidden {
		t.Errorf("render duty: %d", rec.Code)
	}
	// A legal entity outside the caller's scope is refused.
	req := httptest.NewRequest(http.MethodPost, "/print/v1/documents:deliver", strings.NewReader(invoiceDoc+`,"sourceKey":"k","legalEntityId":"`+leA+`"}`))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", "print.document.deliver")
	req.Header.Set("X-Passage-Legal-Entity", "0190a5f2-7b1c-7d3e-8a4f-123456789a77")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("legal entity outside the scope: %d %s", rec.Code, rec.Body)
	}
}

func TestDeliverDocument_Is503WithoutADeliveryStore(t *testing.T) {
	h := docRouter() // in-memory stores do not keep deliveries
	if rec := call(t, h, http.MethodPost, "/print/v1/documents:deliver", invoiceDoc+`,"sourceKey":"k"}`, "print.document.deliver", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("deliver: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, h, http.MethodGet, "/print/v1/statement-schedules", "", "print.job.submit", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("schedules: %d", rec.Code)
	}
}

func TestStatementSchedules_CreateListPauseRunAndDelete(t *testing.T) {
	h := dbRouter(t, &fakeSender{})
	body := `{"name":"Monthly statements","legalEntityId":"` + leA + `","senderName":"Cedar Ridge Pumps, Inc.","cadence":"MONTHLY","runDay":3}`
	if rec := inEntity(t, h, http.MethodPost, "/print/v1/statement-schedules", body, "print.admin", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("create without a key: %d", rec.Code)
	}
	if rec := inEntity(t, h, http.MethodPost, "/print/v1/statement-schedules", strings.Replace(body, `"runDay":3`, `"runDay":31`, 1), "print.admin", "k0"); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "runDay") {
		t.Fatalf("bad run day: %d %s", rec.Code, rec.Body)
	}
	if rec := inEntity(t, h, http.MethodPost, "/print/v1/statement-schedules", body, "print.job.submit", "k1"); rec.Code != http.StatusForbidden {
		t.Fatalf("create without print.admin: %d", rec.Code)
	}
	rec := inEntity(t, h, http.MethodPost, "/print/v1/statement-schedules", body, "print.admin", "k1")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var sc struct {
		ID, NextRunOn, Language string
		Enabled                 bool
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sc); err != nil || !sc.Enabled || sc.Language != "en" || sc.NextRunOn == "" {
		t.Fatalf("schedule: %v %s", err, rec.Body)
	}
	if again := inEntity(t, h, http.MethodPost, "/print/v1/statement-schedules", body, "print.admin", "k1"); again.Code != http.StatusOK || again.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay: %d", again.Code)
	}
	if list := call(t, h, http.MethodGet, "/print/v1/statement-schedules", "", "print.job.submit", ""); list.Code != http.StatusOK || strings.Count(list.Body.String(), `"id"`) != 1 {
		t.Fatalf("list: %d %s", list.Code, list.Body)
	}
	if rec := inEntity(t, h, http.MethodPost, "/print/v1/statement-schedules", strings.Replace(body, `"cadence":"MONTHLY"`, `"cadence":"MONTHLY","templateCode":"NOPE-1"`, 1), "print.admin", "k2"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown template: %d %s", rec.Code, rec.Body)
	}
	paused := call(t, h, http.MethodPatch, "/print/v1/statement-schedules/"+sc.ID, `{"enabled":false}`, "print.admin", "")
	if paused.Code != http.StatusOK || !strings.Contains(paused.Body.String(), `"enabled":false`) {
		t.Fatalf("pause: %d %s", paused.Code, paused.Body)
	}
	if rec := call(t, h, http.MethodPatch, "/print/v1/statement-schedules/"+sc.ID, `{}`, "print.admin", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("patch without enabled: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, "/print/v1/statement-schedules/"+sc.ID+":run", "", "print.admin", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("run without a source: %d %s", rec.Code, rec.Body)
	}
	if runs := call(t, h, http.MethodGet, "/print/v1/statement-schedules/"+sc.ID+"/runs", "", "print.job.submit", ""); runs.Code != http.StatusOK || !strings.Contains(runs.Body.String(), `"items":[]`) {
		t.Fatalf("runs: %d %s", runs.Code, runs.Body)
	}
	if rec := call(t, h, http.MethodDelete, "/print/v1/statement-schedules/"+sc.ID, "", "print.admin", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodDelete, "/print/v1/statement-schedules/"+sc.ID, "", "print.admin", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second delete: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodDelete, "/print/v1/statement-schedules/not-an-id", "", "print.admin", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete of a malformed id: %d", rec.Code)
	}
}
