package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target/targettest"
)

const ledgerA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

type stagedEnv struct {
	loadEnv
	assets     *targettest.FakeStaged
	production *targettest.FakeStaged
	payroll    *targettest.FakeStaged
	xref       *fakeXrefIDs
}

func newStagedEnv(t *testing.T) stagedEnv {
	t.Helper()
	e := newLoadEnv(t, 3, nil)
	assets, prod, pay := targettest.NewAssetsOpening(t), targettest.NewProductionOpening(t), targettest.NewPayrollOpening(t)
	cfg := func(url string) target.Config {
		return target.Config{BaseURL: url, Signer: targettest.HeaderSigner{Name: "migration-factory"}, Attempts: 3, Backoff: time.Millisecond,
			Sleep: func(context.Context, time.Duration) error { return nil }}
	}
	x := &fakeXrefIDs{found: map[string]map[string]string{
		"item":   {"USMF|FG-1": prodBlt, "FG-2": prodNut},
		"worker": {"USMF|E1": custA, "E2": custB},
	}}
	e.h = New(e.st).WithTarget(e.tgt).WithAssets(target.NewAssets(cfg(assets.URL))).WithProduction(target.NewProduction(cfg(prod.URL))).
		WithPayroll(target.NewPayroll(cfg(pay.URL))).WithXref(x).Routes()
	return stagedEnv{loadEnv: e, assets: assets, production: prod, payroll: pay, xref: x}
}

func (e stagedEnv) startStaged(t *testing.T, pack, master string, records ...string) runDetailDTO {
	t.Helper()
	rec := e.do(t, "POST", "/api/v1/migration/packs/"+pack+"/runs", stagedBody("USMF", master, records...))
	if rec.Code != http.StatusCreated {
		t.Fatalf("start %s run: %d %s", pack, rec.Code, rec.Body)
	}
	return decode[runDetailDTO](t, rec)
}

func (e stagedEnv) stagedLoad(t *testing.T, runID, body string, want int) stagedLoadDTO {
	t.Helper()
	rec := e.do(t, "POST", "/api/v1/migration/runs/"+runID+"/staged-load", body)
	if rec.Code != want {
		t.Fatalf("staged-load: %d %s", rec.Code, rec.Body)
	}
	if want != http.StatusOK {
		return stagedLoadDTO{}
	}
	return decode[stagedLoadDTO](t, rec)
}

func (e stagedEnv) detail(t *testing.T, runID string) stagedLoadDetailDTO {
	t.Helper()
	return decode[stagedLoadDetailDTO](t, e.do(t, "GET", "/api/v1/migration/runs/"+runID+"/staged-load", ""))
}

func assetsBody(extra string) string {
	return fmt.Sprintf(`{"legalEntityId":%q,"journal":"post","ledgerId":%q,"openingDate":"2027-01-01","depreciatedThrough":"2026-12","assetClass":"MACHINERY"%s}`, leA, ledgerA, extra)
}

// asset builds a fixed_assets record the factory validates (BR-49.6): next depreciation equals the straight-line projection.
func asset(id, number, book, method string, useful, remaining int, cost, accumulated, next, extra string) string {
	return fmt.Sprintf(`{"legacyId":%q,"asset":%q,"book":%q,"currency":"USD","method":%q,"usefulLifeMonths":%d,"remainingLifeMonths":%d,"acquisitionCost":%q,"accumulatedDepreciation":%q,"nextDepreciation":%q%s}`,
		id, number, book, method, useful, remaining, cost, accumulated, next, extra)
}

var cleanAssets = []string{
	asset("FA-1", "PRESS-1", "GAAP", "SL", 120, 60, "120000", "60000", "1000", `,"assetName":"Press 1","acquisitionDate":"2022-01-15","assetClass":"MACHINERY"`),
	asset("FA-2", "PRESS-1", "TAX", "SL", 60, 30, "50000", "25000", "833.33", `,"assetName":"Press 1","acquisitionDate":"2022-01-15","assetClass":"MACHINERY"`),
	asset("FA-3", "VAN-9", "GAAP", "SL", 60, 10, "6000", "5000", "100", `,"acquisitionDate":"2021-07-01"`),
}

// Assets go to the opening register as batches, the load reconciles cost, accumulated depreciation and NBV per currency with what the register
// holds, the opening journal is booked per book and currency, and a second call sends nothing (#49).
func TestStagedLoad_FixedAssetsGoToTheRegisterAndTheJournalIsBooked(t *testing.T) {
	e := newStagedEnv(t)
	run := e.startStaged(t, "fixed_assets", `"GAAP","TAX"`, append(append([]string{}, cleanAssets...), padAssets(0)...)...)
	if run.Status != "RECONCILED" {
		t.Fatalf("run %+v %+v", run.runDTO, run.Quarantined)
	}
	got := e.stagedLoad(t, run.RunID, assetsBody(""), http.StatusOK)
	if got.Status != "RECONCILED" || !got.Clean || got.Loaded != 3 || got.Pending != 0 || got.Rejected != 0 || got.Pack != "fixed_assets" {
		t.Fatalf("load %+v", got)
	}
	keys := e.assets.Batches()
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "mf-fixedassets-AX2012-") {
		t.Fatalf("assets batches %v", keys)
	}
	if x := e.assets.Extra(keys[0]); x["openingDate"] != "2027-01-01" || x["journal"] != "post" {
		t.Fatalf("batch request fields %v", x)
	}
	// One token per call, each with one duty of the grant; the journal calls carry assets.journal.post.
	posts := 0
	for _, c := range e.assets.Calls() {
		if len(c.Duties) != 1 {
			t.Fatalf("each token carries one duty: %+v", c)
		}
		if strings.HasSuffix(c.Path, "gl-postings") {
			posts++
			if c.Duties[0] != "assets.journal.post" {
				t.Fatalf("journal call %+v", c)
			}
		}
	}
	if posts != 2 {
		t.Fatalf("one journal call per book (GAAP, TAX): %d", posts)
	}
	for _, req := range e.assets.JournalRequests() {
		if req["ledgerId"] != ledgerA || req["currency"] != "USD" || (req["bookCode"] != "GAAP" && req["bookCode"] != "TAX") {
			t.Fatalf("journal request %v", req)
		}
	}
	d := e.detail(t, run.RunID)
	names := map[string]int{}
	for _, c := range d.Reconciliation {
		names[c.Name]++
		if c.Status != "MATCH" {
			t.Fatalf("reconciliation %+v", c)
		}
	}
	if names["target.fixed_assets.line_count"] != 1 || names["target.fixed_assets.cost"] != 1 || names["target.fixed_assets.accumulatedDepreciation"] != 1 || names["target.fixed_assets.nbv"] != 1 {
		t.Fatalf("reconciliation %+v", d.Reconciliation)
	}
	before := len(e.assets.Calls())
	again := e.stagedLoad(t, run.RunID, assetsBody(""), http.StatusOK)
	if again.Status != "RECONCILED" || again.Attempts != 2 || len(e.assets.Batches()) != 1 {
		t.Fatalf("second load %+v", again)
	}
	for _, c := range e.assets.Calls()[before:] {
		if c.Method == http.MethodPost && !strings.HasSuffix(c.Path, "gl-postings") {
			t.Fatalf("the second pass sent a batch: %+v", c)
		}
	}
	if n := e.scalar(t, `select count(*) from staged_load_row where run_id = $1 and status = 'LOADED'`, run.RunID); n != 3 {
		t.Fatalf("loaded rows %d", n)
	}
}

// A line without the optional fields the register needs, a book code the register cannot hold and a line the register refuses are rejected with
// their reasons, the rest loads, and the run is not reconciled while lines are missing.
func TestStagedLoad_RejectsWhatTheRegisterCannotTake(t *testing.T) {
	e := newStagedEnv(t)
	noDate := asset("FA-4", "VAN-10", "GAAP", "SL", 60, 10, "6000", "5000", "100", `,"assetClass":"MACHINERY"`)
	badBook := asset("FA-5", "VAN-11", "gaap book", "SL", 60, 10, "6000", "5000", "100", `,"acquisitionDate":"2021-07-01","assetClass":"VEH"`)
	refused := asset("FA-6", "VAN-12", "GAAP", "SL", 60, 10, "6000", "5000", "100", `,"acquisitionDate":"2021-07-01","assetClass":"VEH"`)
	run := e.startStaged(t, "fixed_assets", `"GAAP","TAX","gaap book"`, append(append([]string{}, cleanAssets...), noDate, badBook, refused, asset("FA-7", "VAN-13", "GAAP", "SL", 60, 10, "6000", "5000", "100", `,"acquisitionDate":"2021-07-01","assetClass":"VEH"`))...)
	e.assets.Refuse["FA-6"] = true
	got := e.stagedLoad(t, run.RunID, assetsBody(`,"assetClass":""`), http.StatusOK)
	// FA-3 has no class on the line and the load names none; FA-4 has no date; FA-5 has a bad book; FA-6 is refused by the register.
	if got.Status != "MISMATCH" || got.Clean || got.Loaded != 3 || got.Rejected != 4 {
		t.Fatalf("load %+v", got)
	}
	codes := map[string]string{}
	for _, r := range e.detail(t, run.RunID).Rejects {
		codes[r.LegacyID] = r.Code
	}
	want := map[string]string{"FA-3": domain.CodeFieldRequired, "FA-4": domain.CodeFieldRequired, "FA-5": domain.CodeStagedField, "FA-6": "AST-OPB-001", "FA-7": ""}
	for id, code := range want {
		if codes[id] != code {
			t.Fatalf("rejects %+v", codes)
		}
	}
	// With a default class and the register no longer refusing, a retry loads the rest; the missing date and the bad book stay rejected.
	delete(e.assets.Refuse, "FA-6")
	got = e.stagedLoad(t, run.RunID, assetsBody(""), http.StatusOK)
	if got.Loaded != 5 || got.Rejected != 2 || got.Status != "MISMATCH" {
		t.Fatalf("retry %+v", got)
	}
}

// journal linked sends no journal request; an outage leaves lines pending and the next call finishes; a lost answer is recognised; a refused journal
// leaves the load UNRECONCILED until it is booked; a target holding other totals shows as a MISMATCH.
func TestStagedLoad_JournalOutageAndReadBack(t *testing.T) {
	e := newStagedEnv(t)
	run := e.startStaged(t, "fixed_assets", `"GAAP","TAX"`, cleanAssets...)
	e.assets.FailNext(http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	if first := e.stagedLoad(t, run.RunID, assetsBody(""), http.StatusOK); first.Status != "PARTIAL" || first.Pending != 3 {
		t.Fatalf("outage %+v", first)
	}
	e.assets.PostFailures = 1
	refused := e.stagedLoad(t, run.RunID, assetsBody(""), http.StatusOK)
	if refused.Status != "UNRECONCILED" || !strings.Contains(refused.LastError, "LEDGER_PERIOD_CLOSED") || refused.Loaded != 3 {
		t.Fatalf("refused journal %+v", refused)
	}
	done := e.stagedLoad(t, run.RunID, assetsBody(""), http.StatusOK)
	if done.Status != "RECONCILED" || !done.Clean || len(e.assets.Batches()) != 1 {
		t.Fatalf("booked %+v", done)
	}
	// A different run of another company with the journal linked asks the ledger for nothing.
	linked := newStagedEnv(t)
	lrun := linked.startStaged(t, "fixed_assets", `"GAAP","TAX"`, cleanAssets...)
	lgot := linked.stagedLoad(t, lrun.RunID, fmt.Sprintf(`{"legalEntityId":%q,"journal":"linked","openingDate":"2027-01-01","depreciatedThrough":"2026-12","assetClass":"M"}`, leA), http.StatusOK)
	if lgot.Status != "RECONCILED" || len(linked.assets.JournalRequests()) != 0 {
		t.Fatalf("linked %+v journal calls %v", lgot, linked.assets.JournalRequests())
	}
	linked.assets.Drift = decimal.NewFromInt(7)
	if got := linked.stagedLoad(t, lrun.RunID, assetsBody(""), http.StatusOK); got.Status != "MISMATCH" || got.Clean {
		t.Fatalf("drift %+v", got)
	}
}

func TestStagedLoad_RefusesWhatIsNotLoadableOrNotConfigured(t *testing.T) {
	e := newStagedEnv(t)
	run := e.startStaged(t, "fixed_assets", `"GAAP","TAX"`, cleanAssets...)
	for name, body := range map[string]string{
		"no journal":     fmt.Sprintf(`{"legalEntityId":%q,"openingDate":"2027-01-01","depreciatedThrough":"2026-12"}`, leA),
		"bad month":      fmt.Sprintf(`{"legalEntityId":%q,"journal":"linked","openingDate":"2027-01-01","depreciatedThrough":"2026"}`, leA),
		"no ledger":      fmt.Sprintf(`{"legalEntityId":%q,"journal":"post","openingDate":"2027-01-01","depreciatedThrough":"2026-12"}`, leA),
		"bad date":       fmt.Sprintf(`{"legalEntityId":%q,"journal":"linked","openingDate":"01/01/2027","depreciatedThrough":"2026-12"}`, leA),
		"another entity": strings.Replace(assetsBody(""), leA, leB, 1),
	} {
		if rec := e.do(t, "POST", "/api/v1/migration/runs/"+run.RunID+"/staged-load", body); rec.Code < 400 || rec.Code >= 500 {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	oh := e.startPack2(t, onhandPath, onhandBody("USMF", "", cleanOnhand...))
	if rec := e.do(t, "POST", "/api/v1/migration/runs/"+oh+"/staged-load", assetsBody("")); rec.Code != http.StatusConflict {
		t.Fatalf("onhand run: %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(t, "GET", "/api/v1/migration/runs/"+run.RunID+"/staged-load", ""); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), CodeNoStagedLoad) {
		t.Fatalf("before the first load: %d %s", rec.Code, rec.Body)
	}
	bare := New(e.st).WithTarget(e.tgt).Routes()
	rec := do(t, bare, request{method: "POST", path: "/api/v1/migration/runs/" + run.RunID + "/staged-load", tenant: tenantA, duties: allDuties, le: leA, body: assetsBody("")})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), CodeStagedNotConfigured) {
		t.Fatalf("no target: %d %s", rec.Code, rec.Body)
	}
	if got := e.stagedLoad(t, run.RunID, assetsBody(""), http.StatusOK); got.Status != "RECONCILED" {
		t.Fatalf("a failed claim must not block the next: %+v", got)
	}
}

// A fully loaded staged pack counts as posted in the go/no-go once its load is RECONCILED (no waiver needed).
func TestStagedLoad_ABuiltBatchKeyIsStable(t *testing.T) {
	a := domain.StagedBatchKey(domain.PackFixedAssets, "AX2012", "USMF", []string{"h1", "h2"})
	b := domain.StagedBatchKey(domain.PackFixedAssets, "AX2012", "USMF", []string{"h1", "h2"})
	if a != b || !domain.IsStagedBatchKey(domain.PackFixedAssets, "AX2012", "USMF", a) || domain.IsStagedBatchKey(domain.PackWIPProd, "AX2012", "USMF", a) || domain.IsStagedBatchKey(domain.PackFixedAssets, "AX2012", "DEMF", a) {
		t.Fatalf("batch key %s", a)
	}
}

func (e stagedEnv) startPack2(t *testing.T, path, body string) string {
	t.Helper()
	rec := e.do(t, "POST", path, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start run: %d %s", rec.Code, rec.Body)
	}
	return decode[runDetailDTO](t, rec).RunID
}
