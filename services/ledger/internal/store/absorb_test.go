package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/services/ledger/internal/domain"
)

func absorbLine(side, amount, txnCurrency string) domain.LineInput {
	return domain.LineInput{Side: side, Amount: decimal.RequireFromString(amount), Currency: "USD", CurrencyTxn: txnCurrency}
}

// A mixed-currency entry has native USD lines (no transaction currency) next to converted EUR lines.
// Rounding must land on a converted line, never on a native one: a native line is its own balanced group.
func TestAbsorbRounding_ConvertedOnlyLeavesNativeLinesAlone(t *testing.T) {
	lines := []domain.LineInput{
		absorbLine(domain.SideDebit, "1000.00", ""),
		absorbLine(domain.SideCredit, "1000.00", ""),
		absorbLine(domain.SideDebit, "108.50", "EUR"),
		absorbLine(domain.SideCredit, "108.51", "EUR"),
	}
	absorbRounding(lines, "USD", decimal.RequireFromString("0.01"), 2, true)
	for i, l := range lines[:2] {
		if l.RoundingAdjustment || !l.Amount.Equal(decimal.RequireFromString("1000.00")) {
			t.Fatalf("native line %d was adjusted: %+v", i, l)
		}
	}
	adjusted := 0
	for _, l := range lines[2:] {
		if l.RoundingAdjustment {
			adjusted++
		}
	}
	if adjusted != 1 {
		t.Fatalf("want exactly one converted line to absorb the rounding, got %d: %+v", adjusted, lines)
	}
}
