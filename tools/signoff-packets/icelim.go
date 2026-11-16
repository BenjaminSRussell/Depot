package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

const icElimSource = "services/consolidation/internal/domain/testdata/ic-partner-examples.json"

type icExamples struct {
	Accounts []struct {
		MainAccount          string `json:"mainAccount"`
		ConsolidationAccount string `json:"consolidationAccount"`
		Role                 string `json:"role"`
	} `json:"accounts"`
	Examples []struct {
		ID            string `json:"id"`
		Title         string `json:"title"`
		Standard      string `json:"standard"`
		Narrative     string `json:"narrative"`
		GroupCurrency string `json:"groupCurrency"`
		Period        string `json:"period"`
		Units         []struct {
			ID           string `json:"id"`
			Currency     string `json:"currency"`
			OwnershipPct string `json:"ownershipPct"`
		} `json:"units"`
		Rates map[string]struct {
			Average string `json:"average"`
			Closing string `json:"closing"`
		} `json:"rates"`
		PartnerRows []struct {
			Unit         string `json:"unit"`
			Account      string `json:"account"`
			Counterparty string `json:"counterparty"`
			Net          string `json:"net"`
		} `json:"partnerRows"`
		Expected struct {
			Pairs   map[string]map[string]string `json:"pairs"`
			Entries []struct {
				Unit    string `json:"unit"`
				Rule    string `json:"rule"`
				Account string `json:"account"`
				Debit   string `json:"debit"`
				Credit  string `json:"credit"`
			} `json:"entries"`
			UnrealizedProfitStatement string `json:"unrealizedProfitStatement"`
		} `json:"expected"`
	} `json:"examples"`
}

var pairFigureText = map[string]string{
	"icSalesGroup":          "Seller's intercompany sales (group currency)",
	"icCostGroup":           "Seller's cost of those sales (group currency)",
	"receivableGroup":       "Seller's receivable (group currency)",
	"payableUnit":           "Buyer's payable (buyer's currency)",
	"unrealizedProfitGroup": "Unrealised profit in the buyer's inventory (group currency)",
	"icSales":               "Subsidiary's intercompany sales (its currency)",
	"icCost":                "Subsidiary's cost of those sales (its currency)",
	"icReceivable":          "Subsidiary's receivable (its currency)",
	"payableGroup":          "Parent's payable (group currency)",
}

var ruleText = map[string]string{"E1": "E1 sales and cost", "E2": "E2 receivable and payable", "E3": "E3 unrealised profit"}

func buildICElimination(root string) ([]*Table, error) {
	var f icExamples
	if err := readJSON(root, icElimSource, &f); err != nil {
		return nil, err
	}
	accounts := newTable("Accounts", "Inputs: the ledger accounts of the examples and the consolidation account and role each maps to",
		"Ledger account", "Consolidation account", "Role")
	for _, a := range f.Accounts {
		accounts.add(str(a.MainAccount), str(a.ConsolidationAccount), str(a.Role))
	}
	consAccount := map[string]string{}
	for _, a := range f.Accounts {
		consAccount[a.MainAccount] = a.ConsolidationAccount
	}
	examples := newTable("Examples", "Inputs: each worked example, its group, rates and the reasoning to confirm",
		"Example", "Title", "Standard", "Group currency", "Period", "Units", "Rates", "Reasoning")
	balances := newTable("Partner balances", "Inputs: what each unit's ledger holds per counterparty (net, debit positive, unit currency)",
		"Example", "Unit", "Unit currency", "Ledger account", "Consolidation account", "Counterparty", "Net")
	pairs := newTable("Expected pairs", "Expected: the intercompany figures of each holder-subsidiary pair", "Example", "Subsidiary", "Figure", "Description", "Amount")
	entries := newTable("Expected entries", "Expected: the E1 to E3 elimination entries per subsidiary", "Example", "Subsidiary", "Rule", "Account", "Debit", "Credit")
	recon := newTable("Reconciliation", "Tie-out per example and rule: debits equal credits, and the E3 profit eliminated is the sum over the subsidiaries",
		"Example", "Rule", "Debits", "Credits", "Difference", "Unrealised profit eliminated", "Statement line (expected)")
	for _, e := range f.Examples {
		currency := map[string]string{}
		var units []string
		for _, u := range e.Units {
			currency[u.ID] = u.Currency
			if u.OwnershipPct == "" {
				units = append(units, fmt.Sprintf("%s (%s, parent)", u.ID, u.Currency))
			} else {
				units = append(units, fmt.Sprintf("%s (%s, %s%%)", u.ID, u.Currency, u.OwnershipPct))
			}
		}
		var rates []string
		codes := make([]string, 0, len(e.Rates))
		for c := range e.Rates {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		for _, c := range codes {
			rates = append(rates, fmt.Sprintf("%s average %s, closing %s", c, e.Rates[c].Average, e.Rates[c].Closing))
		}
		examples.add(str(e.ID), str(e.Title), str(e.Standard), str(e.GroupCurrency), str(e.Period), str(strings.Join(units, "; ")), str(strings.Join(rates, "; ")),
			str(e.Narrative))
		for _, r := range e.PartnerRows {
			balances.add(str(e.ID), str(r.Unit), str(currency[r.Unit]), str(r.Account), str(consAccount[r.Account]), str(r.Counterparty), num(r.Net))
		}
		subs := make([]string, 0, len(e.Expected.Pairs))
		for s := range e.Expected.Pairs {
			subs = append(subs, s)
		}
		sort.Strings(subs)
		for _, s := range subs {
			figs := e.Expected.Pairs[s]
			names := make([]string, 0, len(figs))
			for n := range figs {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				pairs.add(str(e.ID), str(s), str(n), str(pairFigureText[n]), num(figs[n]))
			}
		}
		type tot struct{ dr, cr, profit decimal.Decimal }
		byRule := map[string]*tot{}
		var order []string
		for _, en := range e.Expected.Entries {
			entries.add(str(e.ID), str(en.Unit), str(en.Rule), str(en.Account), num(en.Debit), num(en.Credit))
			dr, err1 := dec(en.Debit)
			cr, err2 := dec(en.Credit)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("%s %s %s: bad amount", e.ID, en.Unit, en.Account)
			}
			if byRule[en.Rule] == nil {
				byRule[en.Rule] = &tot{}
				order = append(order, en.Rule)
			}
			t := byRule[en.Rule]
			t.dr, t.cr = t.dr.Add(dr), t.cr.Add(cr)
			if en.Rule == "E3" && en.Account == "COGS" {
				t.profit = t.profit.Add(dr)
			}
		}
		sort.Strings(order)
		for _, r := range order {
			t := byRule[r]
			profit, statement := str(""), str("")
			if r == "E3" {
				profit = money(t.profit)
				statement = num(e.Expected.UnrealizedProfitStatement)
			}
			recon.add(str(e.ID), str(ruleText[r]), money(t.dr), money(t.cr), money(t.dr.Sub(t.cr)), profit, statement)
		}
	}
	return []*Table{accounts, examples, balances, pairs, entries, recon}, nil
}
