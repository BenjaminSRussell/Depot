package main

import (
	"fmt"
	"sort"

	"github.com/shopspring/decimal"
)

const modificationsSource = "services/revrec/internal/domain/testdata/modification-examples.json"

type modificationFile struct {
	Examples []struct {
		Number    int    `json:"number"`
		Title     string `json:"title"`
		Standard  string `json:"standard"`
		Narrative string `json:"narrative"`
		Input     struct {
			Type             string            `json:"type"`
			EffectiveDate    string            `json:"effectiveDate"`
			Currency         string            `json:"currency"`
			TransactionPrice string            `json:"transactionPrice"`
			PriceChange      string            `json:"priceChange"`
			Scope            map[string]string `json:"scope"`
			Obligations      []struct {
				ID        string `json:"id"`
				Method    string `json:"method"`
				SSP       string `json:"ssp"`
				SSPSource string `json:"sspSource"`
				Allocated string `json:"allocated"`
				Lines     []struct {
					ID     string `json:"id"`
					Date   string `json:"date"`
					Amount string `json:"amount"`
					Status string `json:"status"`
				} `json:"lines"`
			} `json:"obligations"`
		} `json:"input"`
		Expected struct {
			TransactionPriceAfter string `json:"transactionPriceAfter"`
			CatchUp               string `json:"catchUp"`
			Reversal              string `json:"reversal"`
			Obligations           []struct {
				ID              string `json:"id"`
				AllocatedBefore string `json:"allocatedBefore"`
				AllocatedAfter  string `json:"allocatedAfter"`
				SSPBefore       string `json:"sspBefore"`
				SSPAfter        string `json:"sspAfter"`
				Progress        string `json:"progress"`
				CatchUp         string `json:"catchUp"`
				Reversal        string `json:"reversal"`
				Removed         bool   `json:"removed"`
				Lines           []struct {
					ID     string `json:"id"`
					Amount string `json:"amount"`
				} `json:"lines"`
			} `json:"obligations"`
		} `json:"expected"`
	} `json:"examples"`
}

var modTypeText = map[string]string{"CATCH_UP": "cumulative catch-up", "PROSPECTIVE": "prospective"}

func buildModifications(root string) ([]*Table, error) {
	var f modificationFile
	if err := readJSON(root, modificationsSource, &f); err != nil {
		return nil, err
	}
	examples := newTable("Examples", "Inputs: each worked example, the modification and the reasoning to confirm",
		"Example", "Title", "Standard", "Accounting", "Effective date", "Currency", "Price before", "Price change", "Price after", "Scope kept", "Reasoning")
	before := newTable("Contract before", "Inputs: the obligations and their schedule lines before the modification",
		"Example", "Obligation", "Method", "SSP", "SSP source", "Allocated", "Line", "Date", "Amount", "Status")
	changes := newTable("Expected changes", "Expected: the re-allocation per obligation (allocation, SSP, progress, catch-up, revenue reversed)",
		"Example", "Obligation", "Allocated before", "Allocated after", "SSP before", "SSP after", "Progress", "Catch-up", "Revenue reversed", "Removed")
	remaining := newTable("Remaining schedule", "Expected: the new amount of every line not yet recognized (0.00 when cancelled)",
		"Example", "Obligation", "Line", "Date", "Amount before", "Amount after")
	recon := newTable("Reconciliation", "Tie-out per example: revenue recognized before, less revenue reversed, plus the remaining schedule, is the new price",
		"Example", "Recognized before", "Revenue reversed", "Remaining schedule after", "Total", "Price after", "Difference", "Allocated after", "Allocation difference")
	for _, e := range f.Examples {
		in, exp := e.Input, e.Expected
		scope := ""
		keys := make([]string, 0, len(in.Scope))
		for k := range in.Scope {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				scope += ", "
			}
			scope += fmt.Sprintf("%s: %s", k, in.Scope[k])
		}
		label := fmt.Sprintf("%d", e.Number)
		examples.add(integer(e.Number), str(e.Title), str(e.Standard), str(modTypeText[in.Type]), str(in.EffectiveDate), str(in.Currency),
			num(in.TransactionPrice), num(in.PriceChange), num(exp.TransactionPriceAfter), str(scope), str(e.Narrative))

		recognized := decimal.Zero
		lineDate, lineAmount := map[string]string{}, map[string]string{}
		for _, o := range in.Obligations {
			for _, l := range o.Lines {
				before.add(integer(e.Number), str(o.ID), str(o.Method), num(o.SSP), str(o.SSPSource), num(o.Allocated), str(l.ID), str(l.Date), num(l.Amount), str(l.Status))
				lineDate[l.ID], lineAmount[l.ID] = l.Date, l.Amount
				if l.Status == "RECOGNIZED" {
					v, err := dec(l.Amount)
					if err != nil {
						return nil, fmt.Errorf("example %s line %s: %w", label, l.ID, err)
					}
					recognized = recognized.Add(v)
				}
			}
		}
		left, allocated := decimal.Zero, decimal.Zero
		for _, c := range exp.Obligations {
			changes.add(integer(e.Number), str(c.ID), num(c.AllocatedBefore), num(c.AllocatedAfter), num(c.SSPBefore), num(c.SSPAfter), num(c.Progress),
				num(c.CatchUp), num(c.Reversal), yesNo(c.Removed))
			a, err := dec(c.AllocatedAfter)
			if err != nil {
				return nil, fmt.Errorf("example %s %s: %w", label, c.ID, err)
			}
			allocated = allocated.Add(a)
			for _, l := range c.Lines {
				remaining.add(integer(e.Number), str(c.ID), str(l.ID), str(lineDate[l.ID]), num(lineAmount[l.ID]), num(l.Amount))
				v, err := dec(l.Amount)
				if err != nil {
					return nil, fmt.Errorf("example %s line %s: %w", label, l.ID, err)
				}
				left = left.Add(v)
			}
		}
		reversal, err1 := dec(exp.Reversal)
		after, err2 := dec(exp.TransactionPriceAfter)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("example %s: bad reversal or price", label)
		}
		total := recognized.Sub(reversal).Add(left)
		recon.add(integer(e.Number), money(recognized), money(reversal), money(left), money(total), money(after), money(total.Sub(after)), money(allocated),
			money(allocated.Sub(after)))
	}
	return []*Table{examples, before, changes, remaining, recon}, nil
}
