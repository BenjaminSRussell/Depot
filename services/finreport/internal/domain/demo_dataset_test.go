package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/shopspring/decimal"
)

type demoJournal struct {
	period  int
	debit   string
	credit  string
	amount  decimal.Decimal
	closing bool
}

func cents(v int64) decimal.Decimal { return decimal.New(v, -2) }

// demoFY2026Journals reads the demo fiscal year from testdata/demo-fy2026.json, the file the sign-off packet
// (tools/signoff-packets, #512) shows the reviewer: an opening entry in period 0, twelve periods of postings, an
// equipment purchase in period 3, a loan in period 6 and the closing entries in period 12.
func demoFY2026Journals(t *testing.T) []demoJournal {
	t.Helper()
	raw, err := os.ReadFile("testdata/demo-fy2026.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Journals []struct {
			Period  int             `json:"period"`
			Debit   string          `json:"debit"`
			Credit  string          `json:"credit"`
			Amount  decimal.Decimal `json:"amount"`
			Closing bool            `json:"closing"`
		} `json:"journals"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Journals) != 78 {
		t.Fatalf("demo dataset has %d journals, want 78", len(file.Journals))
	}
	js := make([]demoJournal, 0, len(file.Journals))
	for _, j := range file.Journals {
		js = append(js, demoJournal{j.Period, j.Debit, j.Credit, j.Amount, j.Closing})
	}
	return js
}

func balancesFromJournals(js []demoJournal, upTo int) []Balance {
	type key struct {
		period  int
		account string
	}
	net, closing := map[key]decimal.Decimal{}, map[key]decimal.Decimal{}
	for _, j := range js {
		if j.period > upTo {
			continue
		}
		net[key{j.period, j.debit}] = net[key{j.period, j.debit}].Add(j.amount)
		net[key{j.period, j.credit}] = net[key{j.period, j.credit}].Sub(j.amount)
		if j.closing {
			closing[key{j.period, j.debit}] = closing[key{j.period, j.debit}].Add(j.amount)
			closing[key{j.period, j.credit}] = closing[key{j.period, j.credit}].Sub(j.amount)
		}
	}
	var out []Balance
	for k, v := range net {
		out = append(out, Balance{LedgerID: "L1", Period: k.period, Account: k.account, Combination: k.account,
			Dims: map[string]string{}, CurrencyType: "rpt", Net: v, ClosingNet: closing[k]})
	}
	return out
}

func demoCalendar(t *testing.T) Calendar {
	t.Helper()
	ps := []Period{{Year: 2026, No: 0, Quarter: 1}}
	for no := 1; no <= 12; no++ {
		ps = append(ps, Period{Year: 2026, No: no, Quarter: quarterOf(no)})
	}
	cal, err := NewCalendar(ps)
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func journalSum(js []demoJournal, from, to int, account string, includeClosing bool) decimal.Decimal {
	sum := decimal.Zero
	for _, j := range js {
		if j.period < from || j.period > to || (j.closing && !includeClosing) {
			continue
		}
		if j.debit == account {
			sum = sum.Add(j.amount)
		}
		if j.credit == account {
			sum = sum.Sub(j.amount)
		}
	}
	return sum
}

func requireValue(t *testing.T, res Result, row, col string, want decimal.Decimal) {
	t.Helper()
	if c := cellAt(t, res, row, col); c.Blank || !c.Value.Equal(want) {
		t.Fatalf("%s/%s = %+v, want %s", row, col, c, want.StringFixed(2))
	}
}

// TestDemoDataset_StatementsTieToTrialBalanceAndCashFlowReconciles is AC1 on a
// deterministic closed fiscal year: 12 periods of cent-level postings with
// closing entries in period 12. In every period the trial balance totals 0.00,
// the balance sheet check is 0.00, the P&L result equals the journal result, and
// the cash flow net equals the change in cash taken from the journals.
func TestDemoDataset_StatementsTieToTrialBalanceAndCashFlowReconciles(t *testing.T) {
	js := demoFY2026Journals(t)
	cal := demoCalendar(t)
	for at := 1; at <= 12; at++ {
		t.Run(fmt.Sprintf("period_%02d", at), func(t *testing.T) {
			bal := balancesFromJournals(js, at)
			run := func(code string) (Input, Result) {
				in := Input{Definition: standardByCode(t, code), LedgerID: "L1", CurrencyType: "rpt", Calendar: cal, At: at, Balances: bal}
				res, err := Evaluate(in)
				if err != nil {
					t.Fatalf("%s: %v", code, err)
				}
				return in, res
			}
			_, tb := run(CodeTBStd)
			_, bs := run(CodeBSStd)
			_, pl := run(CodePLStd)
			cfIn, cf := run(CodeCFStd)

			requireValue(t, tb, "TOTAL", "CLOSE", decimal.Zero)
			requireValue(t, bs, "CHK", "CUR", decimal.Zero)

			result := journalSum(js, 1, at, "400100", false).Add(journalSum(js, 1, at, "500100", false)).
				Add(journalSum(js, 1, at, "610100", false)).Neg()
			requireValue(t, pl, "RES", "YTD", result)

			requireValue(t, cf, "NI", "YTD", result)
			requireValue(t, cf, "CASH", "YTD", journalSum(js, 1, at, "100100", true))
			requireValue(t, cf, "CASH", "P", journalSum(js, at, at, "100100", true))
			requireValue(t, cf, "CF_NET", "YTD", journalSum(js, 1, at, "100100", true))
			requireValue(t, cf, "CF_CHK", "YTD", decimal.Zero)
			requireValue(t, cf, "CF_CHK", "P", decimal.Zero)

			requireValue(t, cf, "CF_ADJ", "P", cents(2083333))
			requireValue(t, cf, "CF_ADJ", "YTD", cents(2083333*int64(at)))
			requireValue(t, cf, "CF_INV", "YTD", journalSum(js, 1, at, "150100", true).Neg())
			requireValue(t, cf, "CF_FIN", "YTD", journalSum(js, 1, at, "220100", true).Neg())
			wantInv, wantFin := decimal.Zero, decimal.Zero
			if at == 3 {
				wantInv = cents(25000099).Neg()
			}
			if at == 6 {
				wantFin = cents(10000000)
			}
			requireValue(t, cf, "CF_INV", "P", wantInv)
			requireValue(t, cf, "CF_FIN", "P", wantFin)
			if at >= 3 && cellAt(t, cf, "CF_INV", "YTD").Value.IsZero() {
				t.Fatal("investing section is empty after the equipment purchase")
			}
			if at >= 6 && cellAt(t, cf, "CF_FIN", "YTD").Value.IsZero() {
				t.Fatal("financing section is empty after the loan")
			}

			requireValue(t, bs, "ASSETS", "CUR", cellAt(t, tb, "ASSETS", "CLOSE").Value)
			tbPL := cellAt(t, tb, "REVENUE", "CLOSE").Value.Add(cellAt(t, tb, "EXPENSE", "CLOSE").Value)
			if at < 12 {
				requireValue(t, pl, "RES", "YTD", tbPL.Neg())
			} else if !tbPL.IsZero() {
				t.Fatalf("closed year: trial balance P&L accounts = %s, want 0.00 after closing entries", tbPL)
			}
			if err := CheckCashFlow(cfIn, cf); err != nil {
				t.Fatalf("cash flow does not reconcile: %v", err)
			}
		})
	}
}
