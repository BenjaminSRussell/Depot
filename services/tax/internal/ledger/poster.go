package ledger

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/services/tax/internal/domain"
)

const settlementSourceType = "tax_settlement"

type Poster struct {
	Client            ledgerclient.Ledger
	RunID             string
	LegalEntityID     string
	LedgerID          string
	Currency          string
	Accounts          map[string]string
	SettlementAccount string
}

func (p Poster) Post(ctx context.Context, key string, s domain.Settlement) (string, error) {
	entry := ledgerclient.Entry{
		LegalEntityID: p.LegalEntityID, LedgerID: p.LedgerID, DocumentID: p.RunID, PostingDate: s.PeriodTo.Format(dateLayout),
		Currency: p.Currency, IdempotencyKey: key, SourceType: settlementSourceType,
	}
	for _, l := range s.Lines {
		account := p.Accounts[domain.MovementKey(l.Code, l.Direction)]
		if account == "" {
			return "", fmt.Errorf("tax settlement %s: no GL account for %s", p.RunID, domain.MovementKey(l.Code, l.Direction))
		}
		entry.Lines = appendLine(entry.Lines, account, l.Direction == domain.DirInput, l.Amount.Decimal)
	}
	entry.Lines = appendLine(entry.Lines, p.SettlementAccount, false, s.NetAmount.Neg())
	if err := entry.Validate(); err != nil {
		return "", fmt.Errorf("tax settlement %s: %w", p.RunID, err)
	}
	return p.Client.Post(ctx, entry)
}

func appendLine(lines []ledgerclient.Line, account string, credit bool, amount decimal.Decimal) []ledgerclient.Line {
	if amount.IsZero() {
		return lines
	}
	if amount.IsNegative() {
		amount, credit = amount.Neg(), !credit
	}
	return append(lines, ledgerclient.Line{Account: account, Credit: credit, Amount: amount})
}

func AccountingCurrency(ctx context.Context, client ledgerclient.Ledger, legalEntityID, ledgerID string) (string, error) {
	reader, ok := client.(ledgerclient.AuditReader)
	if !ok {
		return "", ErrNotConfigured
	}
	info, err := reader.Ledger(ctx, legalEntityID, ledgerID)
	if err != nil {
		return "", fmt.Errorf("%w: ledger %s: %w", ErrRead, ledgerID, err)
	}
	return info.Currency, nil
}
