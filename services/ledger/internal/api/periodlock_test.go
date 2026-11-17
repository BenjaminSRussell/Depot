package api

import (
	"crypto/ed25519"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const (
	testPeriod    = "0190a000-0000-7000-8000-0000000000f1"
	periodLockURL = "/v1/ledger/ledgers/" + testLedger + "/period-locks/" + testPeriod
	lockBody      = `{"startDate":"2026-03-01","endDate":"2026-03-31","version":7,"sources":[
		{"sourceSystem":"ap","module":"ap","status":"closed"},
		{"sourceSystem":"*","module":"all","status":"open","openUntil":"2026-04-03T00:00:00Z"}]}`
)

func TestPutPeriodLock_PassesTheLockDown(t *testing.T) {
	fs := &fakeStore{}
	rec := call(server(fs), http.MethodPut, periodLockURL, lockBody, dutyPeriodLock, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(fs.locks) != 1 {
		t.Fatalf("locks %+v", fs.locks)
	}
	got := fs.locks[0]
	if got.LedgerID != testLedger || got.FiscalPeriodID != testPeriod || got.Version != 7 || got.Actor != "clerk-1" ||
		got.Start.Format("2006-01-02") != "2026-03-01" || got.End.Format("2006-01-02") != "2026-03-31" || len(got.Sources) != 2 ||
		got.Sources[1].OpenUntil == nil || got.Sources[0].Status != "closed" {
		t.Fatalf("lock passed down %+v", got)
	}
}

func TestPutPeriodLock_RefusesAnInvalidLock(t *testing.T) {
	cases := map[string]string{
		"no catch-all source": `{"startDate":"2026-03-01","endDate":"2026-03-31","version":1,"sources":[{"sourceSystem":"ap","module":"ap","status":"closed"}]}`,
		"unknown status":      `{"startDate":"2026-03-01","endDate":"2026-03-31","version":1,"sources":[{"sourceSystem":"*","module":"all","status":"shut"}]}`,
		"duplicate source":    `{"startDate":"2026-03-01","endDate":"2026-03-31","version":1,"sources":[{"sourceSystem":"*","module":"all","status":"open"},{"sourceSystem":"*","module":"all","status":"closed"}]}`,
		"no sources":          `{"startDate":"2026-03-01","endDate":"2026-03-31","version":1,"sources":[]}`,
		"end before start":    `{"startDate":"2026-03-31","endDate":"2026-03-01","version":1,"sources":[{"sourceSystem":"*","module":"all","status":"open"}]}`,
		"no version":          `{"startDate":"2026-03-01","endDate":"2026-03-31","sources":[{"sourceSystem":"*","module":"all","status":"open"}]}`,
		"bad date":            `{"startDate":"March","endDate":"2026-03-31","version":1,"sources":[{"sourceSystem":"*","module":"all","status":"open"}]}`,
		"unknown field":       `{"startDate":"2026-03-01","endDate":"2026-03-31","version":1,"status":"closed","sources":[{"sourceSystem":"*","module":"all","status":"open"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := call(server(fs), http.MethodPut, periodLockURL, body, dutyPeriodLock, nil)
			if rec.Code != http.StatusBadRequest || len(fs.locks) != 0 {
				t.Fatalf("want 400 and nothing stored, got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
	fs := &fakeStore{}
	if rec := call(server(fs), http.MethodPut, "/v1/ledger/ledgers/"+testLedger+"/period-locks/not-a-uuid", lockBody, dutyPeriodLock, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad period id: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPutPeriodLock_NeedsTheLockDuty(t *testing.T) {
	fs := &fakeStore{}
	if rec := call(server(fs), http.MethodPut, periodLockURL, lockBody, allDuties, nil); rec.Code != http.StatusForbidden || len(fs.locks) != 0 {
		t.Fatalf("a user holding every ledger duty must not set a period lock: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPutPeriodLock_StoreRefusalIsPassedOn(t *testing.T) {
	fs := &fakeStore{err: httpx.NewProblem(http.StatusConflict, "LEDGER_PERIOD_LOCK_STALE", "stale")}
	rec := call(server(fs), http.MethodPut, periodLockURL, lockBody, dutyPeriodLock, nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "LEDGER_PERIOD_LOCK_STALE") {
		t.Fatalf("want 409 stale, got %d %s", rec.Code, rec.Body.String())
	}
}

// Only close's service token carries ledger.period.lock: receiving's grant on the ledger does not, and
// close's posting token does not open the route.
func TestPutPeriodLock_OnlyTheCloseServiceTokenOpensIt(t *testing.T) {
	cpub, cpriv, _ := ed25519.GenerateKey(nil)
	rpub, rpriv, _ := ed25519.GenerateKey(nil)
	closeSigner, _ := tenant.NewServiceSigner("close", cpriv)
	receiving, _ := tenant.NewServiceSigner("receiving", rpriv)
	tenant.SetServiceVerifier(tenant.NewServiceVerifier("ledger", map[string]ed25519.PublicKey{"close": cpub, "receiving": rpub}))
	t.Cleanup(func() { tenant.SetServiceVerifier(nil) })
	fs := &fakeStore{}
	h := server(fs)
	controller := tenant.Principal{TenantID: testTenant, LegalEntityID: testLegalEntity, UserID: "controller"}
	if _, err := receiving.MintDuties("ledger", controller, "", []string{dutyPeriodLock}); err == nil {
		t.Fatal("receiving must not be able to sign ledger.period.lock")
	}
	token, err := closeSigner.MintDuties("ledger", controller, "", []string{dutyPeriodLock})
	if err != nil {
		t.Fatal(err)
	}
	if rec := serviceCall(h, http.MethodPut, periodLockURL, lockBody, token, nil); rec.Code != http.StatusOK || len(fs.locks) != 1 || fs.locks[0].Actor != "controller" {
		t.Fatalf("close lock: %d %s %+v", rec.Code, rec.Body.String(), fs.locks)
	}
	post, _ := closeSigner.MintDuties("ledger", controller, "", []string{"ledger.entry.post"})
	if rec := serviceCall(h, http.MethodPut, periodLockURL, lockBody, post, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("close's posting token must not set a lock: %d %s", rec.Code, rec.Body.String())
	}
}
