package domain

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/shopspring/decimal"
)

// The per-counterparty elimination worked examples (IFRS 10 B86(c), and an upstream sale with NCI) are in
// testdata/ic-partner-examples.json, the file the accountant reviews in the sign-off packet (tools/signoff-packets, #512):
// the partner balances each unit's ledger holds, the rates and the expected pair figures and E1 to E3 entries.

type partnerExample struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Standard      string `json:"standard"`
	Narrative     string `json:"narrative"`
	GroupCurrency string `json:"groupCurrency"`
	Period        string `json:"period"`
	Units         []struct {
		ID           string `json:"id"`
		Currency     string `json:"currency"`
		OwnershipPct string `json:"ownershipPct,omitempty"`
	} `json:"units"`
	Rates map[string]struct {
		Average decimal.Decimal `json:"average"`
		Closing decimal.Decimal `json:"closing"`
	} `json:"rates"`
	Terms map[string]struct {
		InvestmentCost     decimal.Decimal `json:"investmentCost"`
		ShareCapital       decimal.Decimal `json:"shareCapital"`
		HistoricalRate     decimal.Decimal `json:"historicalRate"`
		SellerIsSubsidiary bool            `json:"sellerIsSubsidiary"`
	} `json:"terms"`
	Opening map[string]struct {
		NetAssets decimal.Decimal `json:"netAssets"`
		Rate      decimal.Decimal `json:"rate"`
	} `json:"opening"`
	PartnerRows []struct {
		Unit         string `json:"unit"`
		Account      string `json:"account"`
		Counterparty string `json:"counterparty"`
		Net          string `json:"net"`
	} `json:"partnerRows"`
	Expected struct {
		Pairs   map[string]map[string]string `json:"pairs"`
		Entries []struct {
			Unit    string          `json:"unit"`
			Rule    string          `json:"rule"`
			Account string          `json:"account"`
			Debit   decimal.Decimal `json:"debit"`
			Credit  decimal.Decimal `json:"credit"`
		} `json:"entries"`
		UnrealizedProfitStatement string `json:"unrealizedProfitStatement,omitempty"`
	} `json:"expected"`
}

type partnerExamplesFile struct {
	Accounts []struct {
		MainAccount          string `json:"mainAccount"`
		ConsolidationAccount string `json:"consolidationAccount"`
		Role                 string `json:"role"`
	} `json:"accounts"`
	Examples []partnerExample `json:"examples"`
}

func loadPartnerExamples(t *testing.T) partnerExamplesFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/ic-partner-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var f partnerExamplesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func partnerExampleByID(t *testing.T, id string) partnerExample {
	t.Helper()
	for _, e := range loadPartnerExamples(t).Examples {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("no example %s in testdata/ic-partner-examples.json", id)
	return partnerExample{}
}

// derive collects the example's partner rows through CollectPartners and derives the run; extra are the non-intercompany
// balances (equity, revenue) the run needs besides them.
func (e partnerExample) derive(t *testing.T, extra []CollectLine) (DeriveInput, Derived) {
	t.Helper()
	var units []Unit
	currency := map[string]string{}
	for _, u := range e.Units {
		currency[u.ID] = u.Currency
		if u.OwnershipPct == "" {
			units = append(units, Unit{ID: u.ID, LegalEntityID: "le-" + u.ID, LedgerID: "l-" + u.ID, Currency: u.Currency})
			continue
		}
		units = append(units, heldByParent(subsidiary(u.ID, u.Currency, u.OwnershipPct)))
	}
	rates := map[string]CollectRate{}
	for c, r := range e.Rates {
		rates[c] = CollectRate{Average: r.Average, Closing: r.Closing}
	}
	terms := map[string]UnitTerms{}
	for id, tm := range e.Terms {
		terms[id] = UnitTerms{InvestmentCost: tm.InvestmentCost, ShareCapital: tm.ShareCapital, HistoricalRate: tm.HistoricalRate, SellerIsSubsidiary: tm.SellerIsSubsidiary}
	}
	opening := map[string]OpeningBasis{}
	for id, o := range e.Opening {
		opening[id] = OpeningBasis{NetAssets: o.NetAssets, Rate: o.Rate}
	}
	var ledgers []UnitPartnerLedger
	index := map[string]int{}
	for _, r := range e.PartnerRows {
		i, ok := index[r.Unit]
		if !ok {
			i = len(ledgers)
			index[r.Unit] = i
			ledgers = append(ledgers, UnitPartnerLedger{UnitID: r.Unit, Currency: currency[r.Unit]})
		}
		ledgers[i].Rows = append(ledgers[i].Rows, prow(r.Account, r.Counterparty, r.Net))
	}
	partners, err := CollectPartners(e.GroupCurrency, partnerMapping, ledgers, rates)
	if err != nil {
		t.Fatal(err)
	}
	in := DeriveInput{GroupCurrency: e.GroupCurrency, Period: e.Period, Units: units, Lines: append(extra, roleLines(partners)...), Rates: rates,
		Terms: terms, PartnerLines: partners, Opening: opening}
	d, err := DeriveRun(in)
	if err != nil {
		t.Fatal(err)
	}
	return in, d
}

// pairFigures are the intercompany amounts of a derived pair the example states: the group-currency figures a holder's ledger
// gives and the unit-currency ones a subsidiary seller's gives.
func pairFigures(p Pair) map[string]string {
	out := map[string]string{}
	put := func(name string, v *decimal.Decimal) {
		if v != nil {
			out[name] = v.StringFixed(2)
		}
	}
	nonZero := func(name string, v decimal.Decimal) {
		if !v.IsZero() {
			out[name] = v.StringFixed(2)
		}
	}
	put("icSalesGroup", p.ICSalesGroup)
	put("icCostGroup", p.ICCostGroup)
	put("receivableGroup", p.ReceivableGroup)
	put("payableUnit", p.PayableUnit)
	put("unrealizedProfitGroup", p.UnrealizedProfitGroup)
	if p.ICSalesGroup == nil {
		nonZero("icSales", p.ICSales)
		nonZero("icCost", p.ICCost)
		nonZero("icReceivable", p.ICReceivable)
		nonZero("payableGroup", p.PayableGroup)
	}
	return out
}

// checkPartnerExample compares the derived pairs and their E1 to E3 entries with the file.
func checkPartnerExample(t *testing.T, e partnerExample, d Derived) {
	t.Helper()
	if len(d.Units) != len(e.Expected.Pairs) {
		t.Fatalf("%s: %d pairs, want %d", e.ID, len(d.Units), len(e.Expected.Pairs))
	}
	type key struct{ unit, rule, account string }
	got := map[key][2]decimal.Decimal{}
	for _, u := range d.Units {
		want, ok := e.Expected.Pairs[u.ID]
		if !ok {
			t.Fatalf("%s: unexpected pair %s", e.ID, u.ID)
		}
		have := pairFigures(u.Pair)
		for name, v := range want {
			if have[name] != v {
				t.Errorf("%s %s: %s = %q, want %s (all %v)", e.ID, u.ID, name, have[name], v, have)
			}
		}
		entries, err := Eliminate(u.Pair)
		if err != nil {
			t.Fatal(err)
		}
		for _, en := range entries {
			if en.RuleCode == RuleE1 || en.RuleCode == RuleE2 || en.RuleCode == RuleE3 {
				k := key{u.ID, en.RuleCode, en.Account}
				got[k] = [2]decimal.Decimal{got[k][0].Add(en.Debit), got[k][1].Add(en.Credit)}
			}
		}
	}
	if len(got) != len(e.Expected.Entries) {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k.unit+" "+k.rule+" "+k.account)
		}
		sort.Strings(keys)
		t.Errorf("%s: %d E1 to E3 entries, want %d: %v", e.ID, len(got), len(e.Expected.Entries), keys)
	}
	for _, w := range e.Expected.Entries {
		g, ok := got[key{w.Unit, w.Rule, w.Account}]
		if !ok || !g[0].Equal(w.Debit) || !g[1].Equal(w.Credit) {
			t.Errorf("%s: %s %s %s = Dr %s Cr %s, want Dr %s Cr %s", e.ID, w.Unit, w.Rule, w.Account, g[0], g[1], w.Debit, w.Credit)
		}
	}
}

func TestPartnerExamples_AccountsMatchTheMapping(t *testing.T) {
	f := loadPartnerExamples(t)
	if len(f.Accounts) != len(partnerMapping) || len(f.Examples) != 2 {
		t.Fatalf("%d accounts and %d examples, want %d and 2", len(f.Accounts), len(f.Examples), len(partnerMapping))
	}
	for i, a := range f.Accounts {
		m := partnerMapping[i]
		if a.MainAccount != m.MainAccount || a.ConsolidationAccount != m.ConsolidationAccount || a.Role != m.Role {
			t.Errorf("account %d is %+v, the mapping says %+v", i, a, m)
		}
	}
}
