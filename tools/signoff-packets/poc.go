package main

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/xlsx"
)

const (
	pocWorkedSource    = "services/projects/testdata/poc-worked-example.csv"
	pocEstimatesSource = "services/projects/testdata/proj-estimates-12x4.csv"
)

// pocLabels name the CSV columns for the reviewer; a column not listed keeps its name with underscores as spaces.
var pocLabels = map[string]string{
	"project": "Project", "period": "Period", "currency": "Currency", "contract_value": "Contract value", "cost_to_date": "Cost to date",
	"etc": "Estimate to complete", "prior_revenue": "Prior revenue to date", "prior_revenue_to_date": "Prior revenue to date",
	"billed_to_date": "Billed to date", "prior_loss_provision": "Prior loss provision", "expect_eac": "Estimate at completion",
	"expect_poc": "Percentage of completion", "expect_revenue_to_date": "Revenue to date", "expect_revenue_this_period": "Revenue this period",
	"expect_accrued": "Accrued revenue", "expect_accrued_revenue": "Accrued revenue", "expect_deferred": "Deferred revenue",
	"expect_deferred_revenue": "Deferred revenue", "expect_gross_profit_to_date": "Gross profit to date", "expect_loss_provision": "Loss provision",
	"expect_provision_movement": "Provision movement",
}

func pocLabel(col string) string {
	if l, ok := pocLabels[col]; ok {
		return l
	}
	s := strings.ReplaceAll(strings.TrimPrefix(col, "expect_"), "_", " ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// pocSplit writes one CSV into an inputs table and an expected table: the expect_ columns are expected results, the key
// columns (project, period) are on both.
func pocSplit(root, rel, inName, inAbout, expName, expAbout string) (*Table, *Table, []map[string]string, error) {
	header, records, err := readCSV(root, rel)
	if err != nil {
		return nil, nil, nil, err
	}
	var inCols, expCols []int
	var inHead, expHead []string
	for i, h := range header {
		key := h == "project" || h == "period"
		if strings.HasPrefix(h, "expect_") || key {
			expCols, expHead = append(expCols, i), append(expHead, pocLabel(h))
		}
		if !strings.HasPrefix(h, "expect_") {
			inCols, inHead = append(inCols, i), append(inHead, pocLabel(h))
		}
	}
	in, exp := newTable(inName, inAbout, inHead...), newTable(expName, expAbout, expHead...)
	var rows []map[string]string
	cell := func(h, v string) xlsx.Cell {
		if h == "project" || h == "currency" {
			return str(v)
		}
		return num(v)
	}
	for n, rec := range records {
		if len(rec) != len(header) {
			return nil, nil, nil, fmt.Errorf("%s row %d has %d fields, want %d", rel, n+2, len(rec), len(header))
		}
		var a, b []xlsx.Cell
		for _, i := range inCols {
			a = append(a, cell(header[i], rec[i]))
		}
		for _, i := range expCols {
			b = append(b, cell(header[i], rec[i]))
		}
		in.add(a...)
		exp.add(b...)
		m := map[string]string{}
		for i, h := range header {
			m[h] = rec[i]
		}
		rows = append(rows, m)
	}
	return in, exp, rows, nil
}

func buildPOC(root string) ([]*Table, error) {
	wIn, wExp, wRows, err := pocSplit(root, pocWorkedSource,
		"Worked example inputs", "Inputs: the worked example (contract 500,000.00) period by period",
		"Worked example expected", "Expected: revenue, accrued and deferred revenue and the loss provision of the worked example")
	if err != nil {
		return nil, err
	}
	eIn, eExp, eRows, err := pocSplit(root, pocEstimatesSource,
		"Estimates inputs", "Inputs: 12 projects over 4 periods (contract value, cost to date, estimate to complete, billing)",
		"Estimates expected", "Expected: estimate at completion, percentage of completion, revenue, accrued and deferred revenue, gross profit and the loss provision")
	if err != nil {
		return nil, err
	}
	recon := newTable("Reconciliation", "Tie-out per row: revenue to date less billing is accrued less deferred revenue, and revenue to date is the prior revenue plus this period's",
		"Source", "Project", "Period", "Revenue to date", "Billed to date", "Accrued less deferred", "Billing difference", "Prior revenue plus this period", "Revenue difference")
	add := func(source string, rows []map[string]string, prior, accrued, deferred string) error {
		for _, r := range rows {
			var v [6]decimal.Decimal
			keys := []string{"expect_revenue_to_date", "billed_to_date", accrued, deferred, prior, "expect_revenue_this_period"}
			for i, k := range keys {
				d, err := dec(r[k])
				if err != nil {
					return fmt.Errorf("%s period %s %s: %w", source, r["period"], k, err)
				}
				v[i] = d
			}
			rtd, billed, acc, def, pri, this := v[0], v[1], v[2], v[3], v[4], v[5]
			recon.add(str(source), str(r["project"]), num(r["period"]), money(rtd), money(billed), money(acc.Sub(def)), money(rtd.Sub(billed).Sub(acc.Sub(def))),
				money(pri.Add(this)), money(rtd.Sub(pri).Sub(this)))
		}
		return nil
	}
	if err := add("Worked example", wRows, "prior_revenue", "expect_accrued", "expect_deferred"); err != nil {
		return nil, err
	}
	if err := add("Estimates", eRows, "prior_revenue_to_date", "expect_accrued_revenue", "expect_deferred_revenue"); err != nil {
		return nil, err
	}
	return []*Table{wIn, wExp, eIn, eExp, recon}, nil
}
