package main

import (
	"fmt"
	"strconv"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/xlsx"
)

const (
	statementsSource = "services/finreport/internal/domain/testdata/demo-fy2026.json"
	// statementsGolden is finreport's own evaluated output on the demo year: every cell of TB-STD, BS-STD, PL-STD and CF-STD
	// at every period end, written and asserted by TestDemoDataset_StatementsGolden.
	statementsGolden = "services/finreport/internal/domain/testdata/demo-fy2026-statements.csv"
)

type demoYear struct {
	FiscalYear int `json:"fiscalYear"`
	Accounts   []struct {
		Account string `json:"account"`
		Name    string `json:"name"`
		Class   string `json:"class"`
	} `json:"accounts"`
	Journals []struct {
		Period  int    `json:"period"`
		Debit   string `json:"debit"`
		Credit  string `json:"credit"`
		Amount  string `json:"amount"`
		Closing bool   `json:"closing"`
	} `json:"journals"`
}

// statementSheets name the sheet of each standard statement, in the order the workbook shows them.
var statementSheets = []struct{ code, sheet string }{
	{"TB-STD", "Trial balance"},
	{"BS-STD", "Balance sheet"},
	{"PL-STD", "Income statement"},
	{"CF-STD", "Cash flow"},
}

// engineOutput is the golden file read back: values by statement, period, row and column.
type engineOutput struct {
	name    map[string]string
	columns map[string][][2]string // statement -> (code, label) in order
	rows    map[string][][3]string // statement -> (period, row code, row label) in order
	value   map[[4]string]string
}

func (o engineOutput) get(statement string, period int, row, col string) (decimal.Decimal, error) {
	v, ok := o.value[[4]string{statement, strconv.Itoa(period), row, col}]
	if !ok {
		return decimal.Zero, fmt.Errorf("%s has no %s %s/%s for period %d", statementsGolden, statement, row, col, period)
	}
	return dec(v)
}

func readEngineOutput(root string) (engineOutput, error) {
	header, records, err := readCSV(root, statementsGolden)
	if err != nil {
		return engineOutput{}, err
	}
	c, err := columns(statementsGolden, header, "period", "statement", "statement_name", "row", "row_label", "column", "column_label", "value")
	if err != nil {
		return engineOutput{}, err
	}
	out := engineOutput{name: map[string]string{}, columns: map[string][][2]string{}, rows: map[string][][3]string{}, value: map[[4]string]string{}}
	seenCol, seenRow := map[[2]string]bool{}, map[[3]string]bool{}
	for _, r := range records {
		st := r[c["statement"]]
		out.name[st] = r[c["statement_name"]]
		if k := [2]string{st, r[c["column"]]}; !seenCol[k] {
			seenCol[k] = true
			out.columns[st] = append(out.columns[st], [2]string{r[c["column"]], r[c["column_label"]]})
		}
		if k := [3]string{st, r[c["period"]], r[c["row"]]}; !seenRow[k] {
			seenRow[k] = true
			out.rows[st] = append(out.rows[st], [3]string{r[c["period"]], r[c["row"]], r[c["row_label"]]})
		}
		out.value[[4]string{st, r[c["period"]], r[c["row"]], r[c["column"]]}] = r[c["value"]]
	}
	return out, nil
}

func buildStatements(root string) ([]*Table, error) {
	var y demoYear
	if err := readJSON(root, statementsSource, &y); err != nil {
		return nil, err
	}
	engine, err := readEngineOutput(root)
	if err != nil {
		return nil, err
	}
	amounts := make([]decimal.Decimal, len(y.Journals))
	journals := newTable("Journals", "Inputs: every journal of the year, one debit and one credit of the same amount",
		"Period", "Debit account", "Credit account", "Amount", "Closing entry")
	for i, j := range y.Journals {
		a, err := dec(j.Amount)
		if err != nil {
			return nil, fmt.Errorf("journal %d: %w", i+1, err)
		}
		amounts[i] = a
		journals.add(integer(j.Period), str(j.Debit), str(j.Credit), money(a), yesNo(j.Closing))
	}
	class := map[string]string{}
	accounts := newTable("Accounts", "Inputs: the accounts and the class the fixture gives each (used only for the tie-out)", "Account", "Name", "Class")
	for _, a := range y.Accounts {
		if a.Class == "" {
			return nil, fmt.Errorf("account %s has no class in %s", a.Account, statementsSource)
		}
		class[a.Account] = a.Class
		accounts.add(str(a.Account), str(a.Name), str(a.Class))
	}

	tables := []*Table{journals, accounts}
	for _, s := range statementSheets {
		cols := engine.columns[s.code]
		if len(cols) == 0 {
			return nil, fmt.Errorf("%s has no %s", statementsGolden, s.code)
		}
		header := []string{"Period", "Row", "Line"}
		for _, col := range cols {
			header = append(header, col[1])
		}
		t := newTable(s.sheet, "Expected: finreport's "+engine.name[s.code]+" ("+s.code+") as the engine evaluates it at every period end", header...)
		for _, r := range engine.rows[s.code] {
			row := []xlsx.Cell{num(r[0]), str(r[1]), str(r[2])}
			for _, col := range cols {
				row = append(row, num(engine.value[[4]string{s.code, r[0], r[1], col[0]}]))
			}
			t.add(row...)
		}
		tables = append(tables, t)
	}

	// The tie-out compares the engine with figures taken straight from the journals by the fixture's account classes.
	movement := func(classes map[string]bool, at int, withClosing bool) decimal.Decimal {
		sum := decimal.Zero
		for i, j := range y.Journals {
			if j.Period < 1 || j.Period > at || (j.Closing && !withClosing) {
				continue
			}
			if classes[class[j.Debit]] {
				sum = sum.Add(amounts[i])
			}
			if classes[class[j.Credit]] {
				sum = sum.Sub(amounts[i])
			}
		}
		return sum
	}
	balance := func(classes map[string]bool, at int) decimal.Decimal {
		sum := decimal.Zero
		for i, j := range y.Journals {
			if j.Period > at {
				continue
			}
			if classes[class[j.Debit]] {
				sum = sum.Add(amounts[i])
			}
			if classes[class[j.Credit]] {
				sum = sum.Sub(amounts[i])
			}
		}
		return sum
	}
	pl := map[string]bool{"revenue": true, "expense": true}
	cash := map[string]bool{"cash": true}
	assets := map[string]bool{"cash": true, "asset": true}
	recon := newTable("Reconciliation", "Tie-out of the engine against the journals: net income, change in cash and total assets; then the engine's own check rows",
		"Period", "Net income (engine)", "Net income (journals)", "Net income difference",
		"Change in cash (engine)", "Change in cash (journals)", "Cash difference",
		"Total assets (engine)", "Total assets (journals)", "Assets difference",
		"Engine check: trial balance total", "Engine check: balance sheet", "Engine check: cash flow")
	for at := 1; at <= 12; at++ {
		var v [6]decimal.Decimal
		for i, q := range [][3]string{{"PL-STD", "RES", "YTD"}, {"CF-STD", "CF_NET", "YTD"}, {"BS-STD", "ASSETS", "CUR"}, {"TB-STD", "TOTAL", "CLOSE"},
			{"BS-STD", "CHK", "CUR"}, {"CF-STD", "CF_CHK", "YTD"}} {
			if v[i], err = engine.get(q[0], at, q[1], q[2]); err != nil {
				return nil, err
			}
		}
		ni := movement(pl, at, false).Neg()
		cashMove := movement(cash, at, true)
		totalAssets := balance(assets, at)
		recon.add(integer(at), money(v[0]), money(ni), money(v[0].Sub(ni)), money(v[1]), money(cashMove), money(v[1].Sub(cashMove)),
			money(v[2]), money(totalAssets), money(v[2].Sub(totalAssets)), money(v[3]), money(v[4]), money(v[5]))
	}
	return append(tables, recon), nil
}
