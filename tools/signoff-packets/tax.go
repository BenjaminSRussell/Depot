package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

const (
	taxMatrixSource   = "services/tax/testdata/matrix500.json"
	taxExpectedSource = "services/tax/testdata/matrix500-expected.csv"
)

type taxMatrix struct {
	Provenance string `json:"provenance"`
	Sets       []struct {
		Region   string `json:"region"`
		Currency string `json:"currency"`
		Stacks   [][]struct {
			Name string `json:"name"`
			Pct  string `json:"pct"`
			Seq  int    `json:"seq"`
		} `json:"stacks"`
		AmountSets [][]string `json:"amountSets"`
		Roundings  []string   `json:"roundings"`
		Modes      []string   `json:"modes"`
		WantCount  int        `json:"wantCount"`
	} `json:"sets"`
}

var taxModeText = map[string]string{
	"domestic":       "domestic sale, tax charged",
	"reverse_charge": "reverse charge: the buyer accounts for output and input tax, nothing charged",
	"rate_history":   "a rate change in 2026; the document date takes the current rate",
}

func buildTax(root string) ([]*Table, error) {
	var m taxMatrix
	if err := readJSON(root, taxMatrixSource, &m); err != nil {
		return nil, err
	}
	scenarios := newTable("Scenarios", "Inputs: the 500 scenarios, the grid of matrix500.json (region, tax stack, line amounts, price incl. tax, rounding, mode)",
		"Scenario", "Region", "Currency", "Tax stack", "Line amounts", "Price includes tax", "Rounding", "Mode")
	names := map[string]bool{}
	for _, set := range m.Sets {
		count := 0
		for si, stack := range set.Stacks {
			parts := make([]string, len(stack))
			for i, s := range stack {
				parts[i] = s.Name + " " + s.Pct + "%"
				if s.Seq > 0 {
					parts[i] += fmt.Sprintf(" (compound, step %d)", s.Seq)
				}
			}
			for ai, amounts := range set.AmountSets {
				for _, inclusive := range []bool{false, true} {
					for _, rounding := range set.Roundings {
						for _, mode := range set.Modes {
							count++
							name := fmt.Sprintf("%s/stack%d/amounts%d/incl=%v/%s/%s", set.Region, si, ai, inclusive, rounding, mode)
							names[name] = true
							scenarios.add(str(name), str(set.Region), str(set.Currency), str(strings.Join(parts, " + ")), str(strings.Join(amounts, "; ")),
								yesNo(inclusive), str(strings.ReplaceAll(rounding, "_", " ")), str(taxModeText[mode]))
						}
					}
				}
			}
		}
		if count != set.WantCount {
			return nil, fmt.Errorf("%s grid has %d scenarios, matrix500.json says %d", set.Region, count, set.WantCount)
		}
	}

	header, records, err := readCSV(root, taxExpectedSource)
	if err != nil {
		return nil, err
	}
	col, err := columns(taxExpectedSource, header, "scenario", "region", "price_includes_tax", "mode", "line", "gross", "base", "tax_code", "rate_pct", "compound_seq", "tax", "legs")
	if err != nil {
		return nil, err
	}
	type codeKey struct{ region, code string }
	type codeSum struct {
		rows int
		tax  decimal.Decimal
	}
	byRegion := map[string]*Table{}
	var order []string
	sums := map[codeKey]*codeSum{}
	type lineKey struct{ scenario, line string }
	lineTax := map[lineKey]decimal.Decimal{}
	lineInfo := map[lineKey][]string{}
	for n, r := range records {
		if !names[r[col["scenario"]]] {
			return nil, fmt.Errorf("%s row %d: scenario %s is not in the grid", taxExpectedSource, n+2, r[col["scenario"]])
		}
		region := r[col["region"]]
		t, ok := byRegion[region]
		if !ok {
			t = newTable("Expected "+region, "Expected: base and tax per line and tax code for the "+region+" scenarios (a reverse charge books the amount twice, output and input)",
				"Scenario", "Line", "Gross", "Base", "Tax code", "Rate %", "Compound step", "Tax", "Legs")
			byRegion[region] = t
			order = append(order, region)
		}
		t.add(str(r[col["scenario"]]), num(r[col["line"]]), num(r[col["gross"]]), num(r[col["base"]]), str(r[col["tax_code"]]), num(r[col["rate_pct"]]),
			num(r[col["compound_seq"]]), num(r[col["tax"]]), num(r[col["legs"]]))
		tax, err := dec(r[col["tax"]])
		if err != nil {
			return nil, fmt.Errorf("%s row %d: %w", taxExpectedSource, n+2, err)
		}
		k := codeKey{region, r[col["tax_code"]]}
		if sums[k] == nil {
			sums[k] = &codeSum{}
		}
		sums[k].rows++
		sums[k].tax = sums[k].tax.Add(tax)
		lk := lineKey{r[col["scenario"]], r[col["line"]]}
		lineTax[lk] = lineTax[lk].Add(tax)
		lineInfo[lk] = []string{region, r[col["price_includes_tax"]], r[col["mode"]], r[col["gross"]], r[col["base"]]}
	}
	if len(names) != 500 {
		return nil, fmt.Errorf("the grid has %d scenarios, want 500", len(names))
	}

	// Price-inclusive lines where tax is charged: base plus tax must give back the gross amount.
	type check struct{ lines, off int }
	inclusive := map[string]*check{}
	for lk, info := range lineInfo {
		if info[1] != "true" || info[2] == "reverse_charge" {
			continue
		}
		if inclusive[info[0]] == nil {
			inclusive[info[0]] = &check{}
		}
		gross, _ := dec(info[3])
		base, _ := dec(info[4])
		inclusive[info[0]].lines++
		if !base.Add(lineTax[lk]).Equal(gross) {
			inclusive[info[0]].off++
		}
	}

	recon := newTable("Reconciliation", "Tax per region and tax code, and an arithmetic check: price-inclusive lines whose base plus tax is not the gross (0 by construction of the split)",
		"Region", "Tax code", "Rows", "Tax total", "Price-inclusive lines charged", "Arithmetic check: base plus tax not equal to gross")
	keys := make([]codeKey, 0, len(sums))
	for k := range sums {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].region != keys[j].region {
			return regionIndex(order, keys[i].region) < regionIndex(order, keys[j].region)
		}
		return keys[i].code < keys[j].code
	})
	for _, k := range keys {
		c := inclusive[k.region]
		if c == nil {
			c = &check{}
		}
		recon.add(str(k.region), str(k.code), integer(sums[k].rows), money(sums[k].tax), integer(c.lines), integer(c.off))
	}
	out := []*Table{scenarios}
	for _, r := range order {
		out = append(out, byRegion[r])
	}
	return append(out, recon), nil
}

func regionIndex(order []string, r string) int {
	for i, o := range order {
		if o == r {
			return i
		}
	}
	return len(order)
}
