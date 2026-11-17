package main

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/xlsx"
)

const consolidationSource = "services/consolidation/internal/domain/testdata/consol-group-3-interim.json"

type consolGroup struct {
	Name     string `json:"name"`
	Note     string `json:"note"`
	Currency string `json:"currency"`
	Pairs    []struct {
		Unit               string            `json:"unit"`
		SellerIsSubsidiary bool              `json:"sellerIsSubsidiary"`
		OwnershipPct       string            `json:"ownershipPct"`
		Rates              map[string]string `json:"rates"`
		ICSales            string            `json:"icSales"`
		ICCost             string            `json:"icCost"`
		UnsoldShare        string            `json:"unsoldShare"`
		ICReceivable       string            `json:"icReceivable"`
		PayableGroup       string            `json:"payableGroup"`
		ShareCapital       string            `json:"shareCapital"`
		PreAcqRE           string            `json:"preAcqRE"`
		InvestmentCost     string            `json:"investmentCost"`
		NetIncome          string            `json:"netIncome"`
	} `json:"pairs"`
	Workings map[string]string `json:"workings"`
	Expected []struct {
		Rule    string `json:"rule"`
		Account string `json:"account"`
		Debit   string `json:"debit"`
		Credit  string `json:"credit"`
	} `json:"expected"`
}

func buildConsolidation(root string) ([]*Table, error) {
	var g consolGroup
	if err := readJSON(root, consolidationSource, &g); err != nil {
		return nil, err
	}
	header := []string{"Input"}
	for _, p := range g.Pairs {
		header = append(header, p.Unit)
	}
	inputs := newTable("Inputs", "Inputs: per subsidiary, ownership, rates and the intercompany and acquisition amounts (unit currency; payable and investment in "+g.Currency+")", header...)
	field := func(label string, get func(i int) xlsx.Cell) {
		row := []xlsx.Cell{str(label)}
		for i := range g.Pairs {
			row = append(row, get(i))
		}
		inputs.add(row...)
	}
	field("Sale direction", func(i int) xlsx.Cell {
		if g.Pairs[i].SellerIsSubsidiary {
			return str("upstream (subsidiary sells to the parent)")
		}
		return str("downstream (parent sells to the subsidiary)")
	})
	field("Ownership %", func(i int) xlsx.Cell { return num(g.Pairs[i].OwnershipPct) })
	for _, r := range []string{"closing", "average", "opening", "historical"} {
		field("Rate, "+r, func(i int) xlsx.Cell { return num(g.Pairs[i].Rates[r]) })
	}
	field("Intercompany sales", func(i int) xlsx.Cell { return num(g.Pairs[i].ICSales) })
	field("Intercompany cost", func(i int) xlsx.Cell { return num(g.Pairs[i].ICCost) })
	field("Share still held by the buyer", func(i int) xlsx.Cell { return num(g.Pairs[i].UnsoldShare) })
	field("Intercompany receivable", func(i int) xlsx.Cell { return num(g.Pairs[i].ICReceivable) })
	field("Payable in the group (group currency)", func(i int) xlsx.Cell { return num(g.Pairs[i].PayableGroup) })
	field("Share capital at acquisition", func(i int) xlsx.Cell { return num(g.Pairs[i].ShareCapital) })
	field("Pre-acquisition retained earnings", func(i int) xlsx.Cell { return num(g.Pairs[i].PreAcqRE) })
	field("Investment cost (group currency)", func(i int) xlsx.Cell { return num(g.Pairs[i].InvestmentCost) })
	field("Net income", func(i int) xlsx.Cell { return num(g.Pairs[i].NetIncome) })

	workings := newTable("Workings", "Inputs: the hand computation behind each elimination rule", "Rule", "Working")
	rules := make([]string, 0, len(g.Workings))
	for r := range g.Workings {
		rules = append(rules, r)
	}
	sort.Strings(rules)
	for _, r := range rules {
		workings.add(str(r), str(g.Workings[r]))
	}
	workings.add(str("Note"), str(g.Note))

	expected := newTable("Expected", "Expected: the elimination journal of the group, rule by rule ("+g.Currency+")", "Rule", "Account", "Debit", "Credit")
	type tot struct{ dr, cr decimal.Decimal }
	byRule := map[string]*tot{}
	var order []string
	all := tot{}
	for _, e := range g.Expected {
		dr, err1 := dec(e.Debit)
		cr, err2 := dec(e.Credit)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%s %s: bad amount", e.Rule, e.Account)
		}
		expected.add(str(e.Rule), str(e.Account), money(dr), money(cr))
		if byRule[e.Rule] == nil {
			byRule[e.Rule] = &tot{}
			order = append(order, e.Rule)
		}
		byRule[e.Rule].dr, byRule[e.Rule].cr = byRule[e.Rule].dr.Add(dr), byRule[e.Rule].cr.Add(cr)
		all.dr, all.cr = all.dr.Add(dr), all.cr.Add(cr)
	}
	recon := newTable("Reconciliation", "Tie-out: each rule's entries balance, and so does the whole journal", "Rule", "Debits", "Credits", "Difference")
	for _, r := range order {
		recon.add(str(r), money(byRule[r].dr), money(byRule[r].cr), money(byRule[r].dr.Sub(byRule[r].cr)))
	}
	recon.add(str("Total"), money(all.dr), money(all.cr), money(all.dr.Sub(all.cr)))
	return []*Table{inputs, workings, expected, recon}, nil
}
