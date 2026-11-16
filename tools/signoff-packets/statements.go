package main

import (
	"fmt"
	"strconv"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/xlsx"
)

const statementsSource = "services/finreport/internal/domain/testdata/demo-fy2026.json"

type demoYear struct {
	FiscalYear int `json:"fiscalYear"`
	Accounts   []struct {
		Account string `json:"account"`
		Name    string `json:"name"`
	} `json:"accounts"`
	Journals []struct {
		Period  int    `json:"period"`
		Debit   string `json:"debit"`
		Credit  string `json:"credit"`
		Amount  string `json:"amount"`
		Closing bool   `json:"closing"`
	} `json:"journals"`
}

// accountRange is a range of the finreport standard definitions' default chart (services/finreport/internal/domain/standard.go).
type accountRange struct{ from, to string }

func (r accountRange) has(account string) bool { return account >= r.from && account <= r.to }

var (
	rangeCash       = accountRange{"100000", "109999"}
	rangeReceivable = accountRange{"110000", "149999"}
	rangePPE        = accountRange{"150000", "154999"}
	rangeDeprec     = accountRange{"155000", "159999"}
	rangeOtherNCA   = accountRange{"160000", "199999"}
	rangePayables   = accountRange{"200000", "219999"}
	rangeBorrowing  = accountRange{"220000", "229999"}
	rangeProvisions = accountRange{"230000", "239999"}
	rangeAccruals   = accountRange{"240000", "299999"}
	rangeEquity     = accountRange{"300000", "399999"}
	rangeRevenue    = accountRange{"400000", "499999"}
	rangeCostOfSale = accountRange{"500000", "599999"}
	rangeOpex       = accountRange{"600000", "699999"}
)

func statementClass(account string) string {
	switch account[0] {
	case '1':
		return "Asset"
	case '2':
		return "Liability"
	case '3':
		return "Equity"
	case '4':
		return "Revenue"
	default:
		return "Expense"
	}
}

func buildStatements(root string) ([]*Table, error) {
	var y demoYear
	if err := readJSON(root, statementsSource, &y); err != nil {
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
	accounts := newTable("Accounts", "Inputs: the accounts and the statement each belongs to by number range", "Account", "Name", "Class")
	for _, a := range y.Accounts {
		accounts.add(str(a.Account), str(a.Name), str(statementClass(a.Account)))
	}

	// balance is the cumulative debit-less-credit balance of an account up to and including a period (closing entries in).
	balance := func(account string, upTo int) decimal.Decimal {
		sum := decimal.Zero
		for i, j := range y.Journals {
			if j.Period > upTo {
				continue
			}
			if j.Debit == account {
				sum = sum.Add(amounts[i])
			}
			if j.Credit == account {
				sum = sum.Sub(amounts[i])
			}
		}
		return sum
	}
	// movement is the debit-less-credit movement of a range over periods 1 to `at`, closing entries left out (as the cash
	// flow statement reads it).
	movement := func(r accountRange, at int) decimal.Decimal {
		sum := decimal.Zero
		for i, j := range y.Journals {
			if j.Period < 1 || j.Period > at || j.Closing {
				continue
			}
			if r.has(j.Debit) {
				sum = sum.Add(amounts[i])
			}
			if r.has(j.Credit) {
				sum = sum.Sub(amounts[i])
			}
		}
		return sum
	}

	header := []string{"Account", "Name"}
	for p := 0; p <= 12; p++ {
		header = append(header, "P"+strconv.Itoa(p))
	}
	tb := newTable("Trial balance", "Expected: each account's balance at the end of every period, debit positive; the total is 0.00 in every period", header...)
	totals := make([]decimal.Decimal, 13)
	for _, a := range y.Accounts {
		row := []xlsx.Cell{str(a.Account), str(a.Name)}
		for p := 0; p <= 12; p++ {
			b := balance(a.Account, p)
			totals[p] = totals[p].Add(b)
			row = append(row, money(b))
		}
		tb.add(row...)
	}
	totalRow := []xlsx.Cell{str("Total"), str("")}
	for _, t := range totals {
		totalRow = append(totalRow, money(t))
	}
	tb.add(totalRow...)

	statements := newTable("Statements", "Expected: income statement and indirect cash flow, year to date at each period end (closing entries left out)",
		"Period", "Revenue", "Cost of sales", "Operating expenses", "Net income", "Non-cash adjustments", "Working capital change",
		"Operating cash flow", "Investing cash flow", "Financing cash flow", "Net cash flow", "Change in cash", "Check (must be 0.00)")
	recon := newTable("Reconciliation", "Tie-out: the trial balance totals 0.00, the balance sheet balances and the cash flow equals the change in cash",
		"Period", "Debit balances", "Credit balances", "Trial balance difference", "Assets", "Liabilities and equity incl. result", "Balance sheet difference",
		"Net cash flow", "Change in cash", "Cash flow difference")
	for at := 1; at <= 12; at++ {
		revenue := movement(rangeRevenue, at).Neg()
		cos := movement(rangeCostOfSale, at)
		opex := movement(rangeOpex, at)
		ni := revenue.Sub(cos).Sub(opex)
		adj := movement(rangeDeprec, at).Add(movement(rangeProvisions, at)).Neg()
		wc := movement(rangeReceivable, at).Add(movement(rangePayables, at)).Add(movement(rangeAccruals, at)).Neg()
		op := ni.Add(adj).Add(wc)
		inv := movement(rangePPE, at).Add(movement(rangeOtherNCA, at)).Neg()
		fin := movement(rangeBorrowing, at).Add(movement(rangeEquity, at)).Neg()
		net := op.Add(inv).Add(fin)
		cash := movement(rangeCash, at)
		statements.add(integer(at), money(revenue), money(cos), money(opex), money(ni), money(adj), money(wc), money(op), money(inv), money(fin),
			money(net), money(cash), money(net.Sub(cash)))

		debits, credits, assets, other := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
		for _, a := range y.Accounts {
			b := balance(a.Account, at)
			if b.IsPositive() {
				debits = debits.Add(b)
			} else {
				credits = credits.Sub(b)
			}
			if a.Account[0] == '1' {
				assets = assets.Add(b)
			} else {
				// Liabilities, equity and the P&L accounts (the year's result until the closing entries move it to equity).
				other = other.Sub(b)
			}
		}
		recon.add(integer(at), money(debits), money(credits), money(debits.Sub(credits)), money(assets), money(other), money(assets.Sub(other)),
			money(net), money(cash), money(net.Sub(cash)))
	}
	return []*Table{journals, accounts, tb, statements, recon}, nil
}
