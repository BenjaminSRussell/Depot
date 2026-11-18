package domain_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/services/tax/internal/domain"
)

func taxLine(code, direction, tax, recoverable string) domain.TaxLine {
	return domain.TaxLine{Code: code, Jurisdiction: "DE", Direction: direction, TaxAmount: domain.D(tax), Recoverable: domain.D(recoverable)}
}

func resultOf(taxes ...domain.TaxLine) domain.Result {
	return domain.Result{Lines: []domain.LineResult{{LineRef: "1", Taxes: taxes}}}
}

var postingAccounts = map[string]string{"VAT19|output": "2200", "VAT19|input": "1400", "USE|use": "2300"}

func TestPostingLines(t *testing.T) {
	tests := []struct {
		name     string
		res      domain.Result
		want     []domain.PostingLine
		warnings int
	}{
		{"output credits the liability", resultOf(taxLine("VAT19", "output", "19.00", "19.00")),
			[]domain.PostingLine{{Account: "2200", Credit: true, Amount: domain.D("19.00"), TaxCode: "VAT19", Jurisdiction: "DE"}}, 0},
		{"input debits only the recoverable part", resultOf(taxLine("VAT19", "input", "19.00", "9.50")),
			[]domain.PostingLine{{Account: "1400", Amount: domain.D("9.50"), TaxCode: "VAT19", Jurisdiction: "DE"}}, 0},
		{"reverse charge posts both legs", resultOf(taxLine("VAT19", "output", "10", "10"), taxLine("VAT19", "input", "10", "10")),
			[]domain.PostingLine{
				{Account: "2200", Credit: true, Amount: domain.D("10"), TaxCode: "VAT19", Jurisdiction: "DE"},
				{Account: "1400", Amount: domain.D("10"), TaxCode: "VAT19", Jurisdiction: "DE"}}, 0},
		{"use tax credits the use-tax account", resultOf(taxLine("USE", "use", "4.00", "4.00")),
			[]domain.PostingLine{{Account: "2300", Credit: true, Amount: domain.D("4.00"), TaxCode: "USE", Jurisdiction: "DE"}}, 0},
		{"credit note flips the side and keeps the amount positive", resultOf(taxLine("VAT19", "output", "-19.00", "-19.00")),
			[]domain.PostingLine{{Account: "2200", Amount: domain.D("19.00"), TaxCode: "VAT19", Jurisdiction: "DE"}}, 0},
		{"lines of one code add up", resultOf(taxLine("VAT19", "output", "1.10", "1.10"), taxLine("VAT19", "output", "2.20", "2.20")),
			[]domain.PostingLine{{Account: "2200", Credit: true, Amount: domain.D("3.30"), TaxCode: "VAT19", Jurisdiction: "DE"}}, 0},
		{"zero tax has no line", resultOf(taxLine("VAT19", "output", "0", "0")), []domain.PostingLine{}, 0},
		{"unmapped code warns and has no line", resultOf(taxLine("VAT7", "output", "7.00", "7.00")), []domain.PostingLine{}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, warnings := domain.PostingLines(tc.res, postingAccounts)
			if len(warnings) != tc.warnings {
				t.Fatalf("warnings %v, want %d", warnings, tc.warnings)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			for i, w := range tc.want {
				g := got[i]
				if g.Account != w.Account || g.Credit != w.Credit || !g.Amount.Equal(w.Amount.Decimal) || g.TaxCode != w.TaxCode || g.Jurisdiction != w.Jurisdiction {
					t.Errorf("line %d: got %+v, want %+v", i, g, w)
				}
			}
		})
	}
}

func TestPostingLines_CallerCounterLegBalancesTheJournal(t *testing.T) {
	res := resultOf(taxLine("VAT19", "output", "19.00", "19.00"))
	lines, _ := domain.PostingLines(res, postingAccounts)
	debit, credit := decimal.Zero, decimal.Zero
	for _, l := range lines {
		if l.Credit {
			credit = credit.Add(l.Amount.Decimal)
		} else {
			debit = debit.Add(l.Amount.Decimal)
		}
	}
	gross := decimal.RequireFromString("119.00")
	debit = debit.Add(gross)
	credit = credit.Add(decimal.RequireFromString("100.00"))
	if !debit.Equal(credit) {
		t.Fatalf("receivable %s against revenue and tax %s", debit, credit)
	}
}
