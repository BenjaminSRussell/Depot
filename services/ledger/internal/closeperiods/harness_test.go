package closeperiods

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/ledger/internal/api"
	"github.com/BenjaminSRussell/depot/services/ledger/internal/store"
	"github.com/BenjaminSRussell/depot/services/ledger/migrations"
)

// harness is the ledger-post-into-closed-period harness (#441, B-08c): the real ledger HTTP API and store
// over a test database, its close client with a fake clock, and a fake close service that answers the
// period resolve from closeStatus. close() does what the close service's PATCH does: it sets the ledger's
// period lock with a signed close service token and only then lets the close service answer closed.
type harness struct {
	t           *testing.T
	ledger      http.Handler
	client      *Client
	now         time.Time
	closeSigner *tenant.ServiceSigner
	closeStatus atomic.Value
	tenantID    string
	le          string
	ledgerID    string
	periodID    string
	start, end  time.Time
	today       time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	h := &harness{t: t, tenantID: testkit.TenantID(1), le: ids.New(), periodID: ids.New(), now: time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)}
	h.closeStatus.Store("open")
	today := time.Now().UTC()
	h.today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	h.start = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	h.end = h.start.AddDate(0, 1, -1)

	st := store.New(app)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: h.tenantID, LegalEntityID: h.le, UserID: "setup", Duties: testkit.ScopeDuties(h.le)})
	for num, a := range map[string]store.Account{
		"6010": {Name: "Freight expense", Type: "expense", Postable: true},
		"2100": {Name: "Vendor payable", Type: "liability", Postable: true},
	} {
		if _, _, err := st.PutAccount(ctx, "STD", num, 0, a); err != nil {
			t.Fatalf("put account %s: %v", num, err)
		}
	}
	l, err := st.CreateLedger(ctx, store.Ledger{LegalEntityID: h.le, Code: "GL", Name: "General ledger", ChartCode: "STD", Currency: "USD", FuturePostingDays: 366})
	if err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	h.ledgerID = l.ID

	closeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != resolvePath || r.Header.Get(tenant.HeaderServiceToken) == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"fiscalPeriodId":"` + h.periodID + `","status":"` + h.closeStatus.Load().(string) + `"}`))
	}))
	t.Cleanup(closeSrv.Close)
	ledgerSigner, err := tenant.NewServiceSigner("ledger", ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatal(err)
	}
	h.client = New(closeSrv.URL, nil, ledgerSigner)
	h.client.now = func() time.Time { return h.now }

	pub, priv, _ := ed25519.GenerateKey(nil)
	h.closeSigner, _ = tenant.NewServiceSigner("close", priv)
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"close": pub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })

	router := httpx.NewRouter()
	api.New(st).WithPeriodGate(h.client).Register(router)
	h.ledger = router
	return h
}

// post sends a journal entry dated today as an end user and returns the status and body.
func (h *harness) post() (int, string) {
	h.t.Helper()
	body := `{"ledgerId":"` + h.ledgerID + `","documentId":"` + ids.New() + `","postingDate":"` + h.today.Format(time.DateOnly) + `",
	  "description":"Vendor invoice","source":{"system":"ap","type":"vendor_invoice","id":"inv","uri":"depot://ap/invoices/inv"},
	  "lines":[{"account":"6010","debitCredit":"D","amount":{"currency":"USD","value":"10.00"}},
	           {"account":"2100","debitCredit":"C","amount":{"currency":"USD","value":"10.00"}}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/ledger/journal-entries", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "harness-"+ids.New())
	req.Header.Set(tenant.HeaderTenant, h.tenantID)
	req.Header.Set(tenant.HeaderLegalEntity, h.le)
	req.Header.Set(tenant.HeaderUser, "clerk")
	req.Header.Set(tenant.HeaderDuties, "ledger.journal.post")
	rec := httptest.NewRecorder()
	h.ledger.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// lock sets the ledger's period lock as close's PATCH does, with all's status for every source.
func (h *harness) lock(version int64, status string) {
	h.t.Helper()
	controller := tenant.Principal{TenantID: h.tenantID, LegalEntityID: h.le, UserID: "controller"}
	token, err := h.closeSigner.MintDuties("ledger", controller, h.le, []string{"ledger.period.lock"})
	if err != nil {
		h.t.Fatal(err)
	}
	body := `{"startDate":"` + h.start.Format(time.DateOnly) + `","endDate":"` + h.end.Format(time.DateOnly) + `","version":` +
		strconv.FormatInt(version, 10) + `,"sources":[{"sourceSystem":"*","module":"all","status":"` + status + `"}]}`
	req := httptest.NewRequest(http.MethodPut, "/v1/ledger/ledgers/"+h.ledgerID+"/period-locks/"+h.periodID, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderServiceToken, token)
	rec := httptest.NewRecorder()
	h.ledger.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("period lock %s: %d %s", status, rec.Code, rec.Body.String())
	}
}

func (h *harness) wantPosted(step string) {
	h.t.Helper()
	if code, body := h.post(); code != http.StatusCreated {
		h.t.Fatalf("%s: want 201, got %d %s", step, code, body)
	}
}

func (h *harness) wantRefused(step string) {
	h.t.Helper()
	if code, body := h.post(); code != http.StatusUnprocessableEntity || !strings.Contains(body, "LEDGER_PERIOD_CLOSED") {
		h.t.Fatalf("%s: want 422 LEDGER_PERIOD_CLOSED, got %d %s", step, code, body)
	}
}

// FR-14.1, B-08c: close period P, then post into P at once: refused, with no window at all. The clock does
// not move between the close and the post, and the refusal does not wait for the close service's own
// commit or for any cache to expire.
func TestHarness_PostIntoAPeriodJustClosedIsRefusedAtOnce(t *testing.T) {
	h := newHarness(t)
	h.wantPosted("post while the period is open")

	// Close's PATCH: the ledger acknowledges the lock while close's own transaction is still open, so the
	// close service still answers open.
	h.lock(1, "closed")
	h.wantRefused("post right after the ledger acknowledged the close, close still answering open")

	// Close commits and answers closed: still refused, at the same instant and after the 30 s cache TTL.
	h.closeStatus.Store("closed")
	h.wantRefused("post right after close committed")
	h.now = h.now.Add(CacheTTL + time.Second)
	h.wantRefused("post after the cache TTL")

	// A reopen sets the lock open again; postings go through once close answers open and its closed answer
	// is out of the pre-check cache.
	h.lock(2, "open")
	h.closeStatus.Store("open")
	h.now = h.now.Add(CacheTTL + time.Second)
	h.wantPosted("post after the reopen")
}
