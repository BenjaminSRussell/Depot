package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/services/consolidation/internal/api"
)

const leFR = "00000000-0000-0000-0000-0000000000a3"

const partnerMappingJSON = `{"accounts":[
  {"mainAccount":"1000","consolidationAccount":"CASH","accountType":"balance_sheet","role":"asset"},
  {"mainAccount":"1400","consolidationAccount":"INVENTORY","accountType":"balance_sheet","role":"asset"},
  {"mainAccount":"1500","consolidationAccount":"IC_REC","accountType":"balance_sheet","role":"ic_receivable"},
  {"mainAccount":"2500","consolidationAccount":"IC_PAY","accountType":"balance_sheet","role":"ic_payable"},
  {"mainAccount":"3000","consolidationAccount":"EQUITY","accountType":"balance_sheet","role":"equity"},
  {"mainAccount":"4100","consolidationAccount":"IC_REVENUE","accountType":"income","role":"ic_sales"},
  {"mainAccount":"5100","consolidationAccount":"IC_COGS","accountType":"income","role":"ic_cost"},
  {"mainAccount":"5200","consolidationAccount":"IC_PURCHASES","accountType":"income","role":"ic_purchases"},
  {"mainAccount":"5300","consolidationAccount":"IC_COGS_BUYER","accountType":"income","role":"ic_cogs"}]}`

func partnerLine(account string, credit bool, amount, partner string) ledgerclient.Line {
	l := line(account, credit, amount)
	l.TradingPartnerID = partner
	return l
}

type partnerRunOut struct {
	ID       string
	Version  int
	Warnings []string
}

type runEntries struct {
	Eliminations []struct{ Unit, Rule, Account, Debit, Credit string }
}

// Worked example IFRS 10 B86(c) end to end: the parent sells to two subsidiaries at different margins through ledger lines that name
// the trading partner; the collection keeps the intercompany part per counterparty, the run eliminates each pair on its own, the
// unrealised profit of each is estimated from the partner balances, and a rerun gives the same journal.
func TestRun_ParentSellingToTwoSubsidiaries_PartnerDimension(t *testing.T) {
	a, fake := groupsAPI(t)
	const base = "/v1/consol/groups"
	g := created[groupOut](t, call(a, http.MethodPost, base, tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`))
	us := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-US","currency":"USD"}`, leUS)))
	de := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-DE","currency":"EUR"}`, leDE)))
	fr := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-FR","currency":"USD"}`, leFR)))
	for _, sub := range []unitOut{de, fr} {
		created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units/"+sub.ID+"/ownership", tenantID, api.DutyConfigure, "",
			fmt.Sprintf(`{"holderUnitId":%q,"effectiveFrom":"2024-01-01","percent":"100"}`, us.ID)))
	}
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/account-map", tenantID, api.DutyConfigure, "", partnerMappingJSON); rec.Code != http.StatusOK {
		t.Fatalf("mapping = %d %s", rec.Code, rec.Body.String())
	}
	for id, terms := range map[string]string{
		de.ID: `{"investmentCost":"1000000","shareCapital":"800000","historicalRate":"1.25"}`,
		fr.ID: `{"investmentCost":"1000000","shareCapital":"1000000","historicalRate":"1"}`,
	} {
		if rec := call(a, http.MethodPut, base+"/"+g.ID+"/units/"+id+"/terms", tenantID, api.DutyConfigure, "", terms); rec.Code != http.StatusOK {
			t.Fatalf("terms = %d %s", rec.Code, rec.Body.String())
		}
	}

	// The parent: sales to FR 1,000 (cost 600, 400 unpaid) and to DE 2,200 (cost 1,650, 600 unpaid); each line names the buyer.
	postLedger(t, fake, leUS, "L-US", "USD", "2026-02-10", "us-fr", partnerLine("1500", false, "400", leFR), line("1000", false, "600"), partnerLine("4100", true, "1000", leFR))
	postLedger(t, fake, leUS, "L-US", "USD", "2026-02-11", "us-fr-c", partnerLine("5100", false, "600", leFR), line("1400", true, "600"))
	postLedger(t, fake, leUS, "L-US", "USD", "2026-02-12", "us-de", partnerLine("1500", false, "600", leDE), line("1000", false, "1600"), partnerLine("4100", true, "2200", leDE))
	postLedger(t, fake, leUS, "L-US", "USD", "2026-02-13", "us-de-c", partnerLine("5100", false, "1650", leDE), line("1400", true, "1650"))
	postLedger(t, fake, leUS, "L-US", "USD", "2025-01-05", "us-eq", line("1000", false, "9000000"), line("3000", true, "9000000"))
	// FR (USD) bought 1,000, owes 400 and sold on goods it bought for 700; DE (EUR) bought 2,000 EUR, owes 498 EUR and sold on 1,200 EUR.
	postLedger(t, fake, leFR, "L-FR", "USD", "2025-01-05", "fr-eq", line("1000", false, "1000000"), line("3000", true, "1000000"))
	postLedger(t, fake, leFR, "L-FR", "USD", "2026-02-10", "fr-1", partnerLine("5200", false, "1000", leUS), partnerLine("2500", true, "400", leUS), line("1000", true, "600"))
	postLedger(t, fake, leFR, "L-FR", "USD", "2026-02-11", "fr-2", partnerLine("5300", false, "700", leUS), line("1000", true, "700"))
	postLedger(t, fake, leDE, "L-DE", "EUR", "2025-01-05", "de-eq", line("1000", false, "800000"), line("3000", true, "800000"))
	postLedger(t, fake, leDE, "L-DE", "EUR", "2026-02-12", "de-1", partnerLine("5200", false, "2000", leUS), partnerLine("2500", true, "498", leUS), line("1000", true, "1502"))
	postLedger(t, fake, leDE, "L-DE", "EUR", "2026-02-13", "de-2", partnerLine("5300", false, "1200", leUS), line("1000", true, "1200"))

	collect := base + "/" + g.ID + "/collections"
	body := `{"period":"2026-03","rates":[{"currency":"EUR","average":"1.10","closing":"1.20"}]}`
	col := created[struct{ ID string }](t, call(a, http.MethodPost, collect, tenantID, api.DutyRun, "c1", body))
	if replay := call(a, http.MethodPost, collect, tenantID, api.DutyRun, "c1", body); replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("collection replay = %d %s", replay.Code, replay.Body.String())
	}

	detail := decode[struct {
		Lines        []struct{ Unit, MainAccount, GroupAmount string }
		PartnerLines []struct{ Unit, Counterparty, MainAccount, Role, UnitAmount, GroupAmount string }
	}](t, call(a, http.MethodGet, "/v1/consol/collections/"+col.ID, tenantID, api.DutyView, "", ""))
	got := map[string]string{}
	for _, p := range detail.PartnerLines {
		got[p.Unit+">"+p.Counterparty+" "+p.MainAccount] = p.GroupAmount
	}
	for k, v := range map[string]string{
		us.ID + ">" + de.ID + " 4100": "-2200.00", us.ID + ">" + fr.ID + " 4100": "-1000.00",
		us.ID + ">" + de.ID + " 1500": "600.00", us.ID + ">" + fr.ID + " 5100": "600.00",
		de.ID + ">" + us.ID + " 5200": "2200.00", de.ID + ">" + us.ID + " 2500": "-597.60", de.ID + ">" + us.ID + " 5300": "1320.00",
		fr.ID + ">" + us.ID + " 5200": "1000.00", fr.ID + ">" + us.ID + " 5300": "700.00",
	} {
		if got[k] != v {
			t.Errorf("partner line %s = %q, want %s (all %v)", k, got[k], v, got)
		}
	}
	// The partner lines are a memo: the collected balances still hold the whole account (the parent's 4100 is 3,200 in credit).
	var sales4100 decimal.Decimal
	for _, l := range detail.Lines {
		if l.Unit == us.ID && l.MainAccount == "4100" {
			sales4100 = decimal.RequireFromString(l.GroupAmount)
		}
	}
	if !sales4100.Equal(decimal.RequireFromString("-3200")) {
		t.Errorf("the parent's 4100 in the collection = %s, want -3200", sales4100)
	}
	tb := decode[struct {
		Rows []struct {
			Account  string
			Units    map[string]string
			Partners map[string]map[string]string
		}
	}](t, call(a, http.MethodGet, "/v1/consol/collections/"+col.ID+"/trial-balance", tenantID, api.DutyView, "", ""))
	foundTB := false
	for _, r := range tb.Rows {
		if r.Account == "IC_REVENUE" {
			foundTB = r.Partners[us.ID][de.ID] == "-2200.00" && r.Partners[us.ID][fr.ID] == "-1000.00" && r.Units[us.ID] == "-3200.00"
		}
	}
	if !foundTB {
		t.Errorf("the trial balance does not show the parent's IC_REVENUE split by counterparty: %+v", tb.Rows)
	}

	runBody := fmt.Sprintf(`{"collectionId":%q,"openingRates":[{"currency":"EUR","rate":"1.15"}]}`, col.ID)
	run := created[partnerRunOut](t, call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r1", runBody))
	if run.Version != 1 {
		t.Fatalf("run = %+v", run)
	}
	estimates := 0
	for _, w := range run.Warnings {
		if strings.Contains(w, "CNS-0933") {
			estimates++
		}
	}
	if estimates != 2 {
		t.Errorf("warnings = %v, want one CNS-0933 estimate per subsidiary", run.Warnings)
	}
	entries := func(id string) map[string]string {
		t.Helper()
		out := map[string]string{}
		debit, credit := decimal.Zero, decimal.Zero
		for _, e := range decode[runEntries](t, call(a, http.MethodGet, "/v1/consol/runs/"+id, tenantID, api.DutyView, "", "")).Eliminations {
			d, c := decimal.RequireFromString(e.Debit), decimal.RequireFromString(e.Credit)
			debit, credit = debit.Add(d), credit.Add(c)
			if e.Rule == "E1" || e.Rule == "E2" || e.Rule == "E3" {
				out[e.Unit+" "+e.Rule+" "+e.Account] = d.Sub(c).String()
			}
		}
		if !debit.Equal(credit) {
			t.Errorf("the elimination journal of run %s is unbalanced: debit %s credit %s", id, debit, credit)
		}
		return out
	}
	first := entries(run.ID)
	for k, v := range map[string]string{
		fr.ID + " E1 IC_REVENUE": "1000", de.ID + " E1 IC_REVENUE": "2200",
		fr.ID + " E2 IC_PAYABLE": "400", fr.ID + " E2 IC_RECEIVABLE": "-400",
		de.ID + " E2 IC_PAYABLE": "597.6", de.ID + " E2 IC_RECEIVABLE": "-600", de.ID + " E2 FX_GAIN_LOSS": "2.4",
		fr.ID + " E3 COGS": "120", fr.ID + " E3 INVENTORY": "-120",
		de.ID + " E3 COGS": "220", de.ID + " E3 INVENTORY": "-220",
	} {
		if first[k] != v {
			t.Errorf("%s = %q, want %s", k, first[k], v)
		}
	}
	// The same key replays the run; a new key over the same collection is the next version with the identical journal.
	if replay := call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r1", runBody); replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("run replay = %d %s", replay.Code, replay.Body.String())
	}
	again := created[partnerRunOut](t, call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r2", runBody))
	if again.Version != 2 {
		t.Fatalf("rerun version = %d", again.Version)
	}
	second := entries(again.ID)
	if len(second) != len(first) {
		t.Fatalf("rerun entries %v vs %v", second, first)
	}
	for k, v := range first {
		if second[k] != v {
			t.Errorf("rerun %s = %s, first run %s", k, second[k], v)
		}
	}
	// A request amount may correct a pair's figure; it changes the elimination and so the inputs hash.
	over := created[struct {
		ID         string
		InputsHash string
	}](t, call(a, http.MethodPost, "/v1/consol/runs", tenantID, api.DutyRun, "r3", fmt.Sprintf(`{"collectionId":%q,"openingRates":[{"currency":"EUR","rate":"1.15"}],"ic":[{"unit":%q,"icSales":"5"}]}`, col.ID, de.ID)))
	plain := decode[struct{ InputsHash string }](t, call(a, http.MethodGet, "/v1/consol/runs/"+run.ID, tenantID, api.DutyView, "", ""))
	if over.InputsHash == "" || over.InputsHash == plain.InputsHash {
		t.Fatalf("the override must change the inputs hash: %s vs %s", over.InputsHash, plain.InputsHash)
	}
}
