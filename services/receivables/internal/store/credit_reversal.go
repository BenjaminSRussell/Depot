package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	rcvevents "github.com/BenjaminSRussell/passage/services/receivables/internal/events"
)

const (
	// CodeCreditSettlementNotFound: no credit settlement carries that batch id.
	CodeCreditSettlementNotFound = "AR-CREDIT-SETTLEMENT-NOT-FOUND"
	// CodeCreditSettlementReversed: the credit settlement was already reversed.
	CodeCreditSettlementReversed = "AR-CREDIT-SETTLEMENT-REVERSED"
)

// CreditReversalResult is the credit item and the debits after a credit settlement was reversed.
type CreditReversalResult struct {
	BatchID         string                 `json:"batchId"`
	ReversalBatchID string                 `json:"reversalBatchId"`
	CreditItemID    string                 `json:"creditItemId"`
	CreditOpenAfter decimal.Decimal        `json:"creditOpenAfter"`
	CreditStatus    string                 `json:"creditStatus"`
	Total           decimal.Decimal        `json:"total"`
	RealizedFX      decimal.Decimal        `json:"realizedFx"`
	Reopened        []CreditSettlementLine `json:"reopened"`
}

type liveCreditSettlement struct {
	id, debitItemID string
	amount          decimal.Decimal
	debitCurrency   *string
	debitAmount     *decimal.Decimal
	creditRate      *decimal.Decimal
	debitRate       *decimal.Decimal
	fxGainLoss      decimal.Decimal
}

// ReverseCreditSettlement undoes one credit settlement, every row of the batch SettleCredit returned (#30). Each row gets a
// reversal row with its amounts negated (credit_settlement.reversal_of_id), each debit gets back what the settlement took off it
// (in the debit's currency, so a cross-currency settlement restores its debit amount) and the credit holds what it had. A
// cross-currency settlement's realized exchange difference is reversed with it as its own functional-currency journal (the
// gain's Dr gain account / Cr 1200, the loss's Dr 1200 / Cr loss account); a same-currency settlement owes the ledger nothing,
// both sides being 1200. Statements and balance confirmations dated before the reversal still show the items settled. A
// settlement that was already reversed is 409 AR-CREDIT-SETTLEMENT-REVERSED, so a repeat never gives the room back twice.
func (s *Postgres) ReverseCreditSettlement(ctx context.Context, batchID, reason string) (CreditReversalResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out CreditReversalResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var legalEntity, customer, currency, creditID string
		if err := tx.QueryRow(ctx, `select legal_entity_id::text, customer_id::text, currency::text, credit_item_id::text
			from credit_settlement where tenant_id = $1 and batch_id = $2 and reversal_of_id is null limit 1`, p.TenantID, batchID).
			Scan(&legalEntity, &customer, &currency, &creditID); err != nil {
			return notFound(err, CodeCreditSettlementNotFound, "credit settlement not found")
		}
		creditItem, err := lockAnyOpenItem(ctx, tx, creditID)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select cs.id::text, cs.debit_item_id::text, cs.amount::text, cs.debit_currency::text, cs.debit_amount::text,
				cs.credit_rate::text, cs.debit_rate::text, cs.fx_gain_loss::text,
				exists (select 1 from credit_settlement r where r.tenant_id = cs.tenant_id and r.reversal_of_id = cs.id)
			from credit_settlement cs where cs.tenant_id = $1 and cs.batch_id = $2 and cs.reversal_of_id is null order by cs.settled_at, cs.id`, p.TenantID, batchID)
		if err != nil {
			return err
		}
		var live []liveCreditSettlement
		reversed := 0
		for rows.Next() {
			var l liveCreditSettlement
			var amount, fx string
			var debitAmount, creditRate, debitRate *string
			var done bool
			if err := rows.Scan(&l.id, &l.debitItemID, &amount, &l.debitCurrency, &debitAmount, &creditRate, &debitRate, &fx, &done); err != nil {
				rows.Close()
				return err
			}
			if done {
				reversed++
				continue
			}
			l.amount, l.fxGainLoss = decimal.RequireFromString(amount), decimal.RequireFromString(fx)
			l.debitAmount, l.creditRate, l.debitRate = optDecimal(debitAmount), optDecimal(creditRate), optDecimal(debitRate)
			live = append(live, l)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(live) == 0 && reversed > 0 {
			return httpx.NewProblem(http.StatusConflict, CodeCreditSettlementReversed, "the credit settlement is already reversed")
		}
		out = CreditReversalResult{BatchID: batchID, ReversalBatchID: ids.New(), CreditItemID: creditItem.ID, Reopened: []CreditSettlementLine{}}
		total, gain, loss := decimal.Zero, decimal.Zero, decimal.Zero
		event := rcvevents.CreditSettlementReversedData{BatchID: batchID, ReversalBatchID: out.ReversalBatchID, LegalEntityID: legalEntity,
			CustomerID: customer, Currency: currency, CreditItemID: creditItem.ID, Reason: reason, Debits: make([]rcvevents.CreditSettledDebit, 0, len(live))}
		for _, l := range live {
			item, err := lockAnyOpenItem(ctx, tx, l.debitItemID)
			if err != nil {
				return err
			}
			back := l.amount
			if l.debitAmount != nil {
				back = *l.debitAmount
			}
			newOpen := item.OpenAmount.Add(back)
			if newOpen.GreaterThan(item.Amount) {
				return httpx.NewProblem(http.StatusConflict, CodePaymentState, fmt.Sprintf("open item %s would be open for more than its amount", item.DocumentNumber))
			}
			status := domain.ApplyStatus(newOpen)
			if newOpen.Equal(item.Amount) {
				status = "OPEN"
			}
			if _, err := tx.Exec(ctx, `update open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
				where tenant_id = $1 and id = $2`, p.TenantID, item.ID, newOpen.String(), status); err != nil {
				return err
			}
			var negDebit any
			if l.debitAmount != nil {
				negDebit = l.debitAmount.Neg().String()
			}
			var creditRate, debitRate any
			if l.creditRate != nil {
				creditRate = l.creditRate.String()
			}
			if l.debitRate != nil {
				debitRate = l.debitRate.String()
			}
			if _, err := tx.Exec(ctx, `insert into credit_settlement (tenant_id, id, legal_entity_id, batch_id, customer_id, currency, credit_item_id,
					debit_item_id, amount, settled_by, debit_currency, debit_amount, credit_rate, debit_rate, fx_gain_loss, reversal_of_id, reversal_reason)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9::numeric, $10, $11, $12::numeric, $13::numeric, $14::numeric, $15::numeric, $16, $17)`,
				p.TenantID, ids.New(), legalEntity, out.ReversalBatchID, customer, currency, creditItem.ID, item.ID, l.amount.Neg().String(), p.UserID,
				l.debitCurrency, negDebit, creditRate, debitRate, l.fxGainLoss.Neg().String(), l.id, reason); err != nil {
				return err
			}
			total = total.Add(l.amount)
			line := CreditSettlementLine{OpenItemID: item.ID, DocumentNumber: item.DocumentNumber, Amount: l.amount, OpenAfter: newOpen, ItemStatus: status}
			cd := rcvevents.CreditSettledDebit{OpenItemID: item.ID, Amount: l.amount}
			if l.debitAmount != nil && l.debitCurrency != nil {
				ia, fx := *l.debitAmount, l.fxGainLoss
				line.ItemCurrency, line.ItemAmount, line.RealizedFX = *l.debitCurrency, &ia, &fx
				cd.ItemCurrency, cd.ItemAmount = *l.debitCurrency, ia
				if fx.IsPositive() {
					gain = gain.Add(fx)
				} else {
					loss = loss.Add(fx.Neg())
				}
			}
			out.Reopened = append(out.Reopened, line)
			event.Debits = append(event.Debits, cd)
		}
		creditOpen := creditItem.OpenAmount.Sub(total)
		if creditOpen.LessThan(creditItem.Amount) {
			return httpx.NewProblem(http.StatusConflict, CodePaymentState, fmt.Sprintf("credit %s would hold more than its amount", creditItem.DocumentNumber))
		}
		out.Total, out.CreditOpenAfter, out.CreditStatus = total, creditOpen, domain.ApplyStatus(creditOpen)
		if creditOpen.Equal(creditItem.Amount) {
			out.CreditStatus = "OPEN"
		}
		if _, err := tx.Exec(ctx, `update open_item set open_amount = $3::numeric, status = $4, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, creditItem.ID, creditOpen.String(), out.CreditStatus); err != nil {
			return err
		}
		out.RealizedFX = gain.Sub(loss)
		if gain.IsPositive() || loss.IsPositive() {
			// The original journal is flipped line by line (as ReversePayment does), in its own currency and on its own accounts: the entity's
			// FX setting may have changed since the settlement.
			var raw []byte
			var currency string
			err := tx.QueryRow(ctx, `select currency::text, lines from gl_journal where tenant_id = $1 and legal_entity_id = $2 and source_key = $3`,
				p.TenantID, legalEntity, "credit-fx:"+batchID).Scan(&currency, &raw)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			var lines []journalLine
			if err == nil {
				if err := json.Unmarshal(raw, &lines); err != nil {
					return err
				}
				for i := range lines {
					lines[i].Credit = !lines[i].Credit
					lines[i].Text = "Reversal: " + lines[i].Text
				}
			} else {
				// The settlement's journal is gone (it predates the postings): book the difference on the current setting.
				set, err := loadFXSetting(ctx, tx, legalEntity)
				if err != nil {
					return err
				}
				currency = set.FunctionalCurrency
				if gain.IsPositive() {
					lines = append(lines, debit(set.GainAccount, gain, "Realized exchange gain reversed"), credit(domain.AccountReceivable, gain, "Realized exchange gain reversed"))
				}
				if loss.IsPositive() {
					lines = append(lines, debit(domain.AccountReceivable, loss, "Realized exchange loss reversed"), credit(set.LossAccount, loss, "Realized exchange loss reversed"))
				}
			}
			if err := insertJournal(ctx, tx, p.TenantID, journalSpec{LegalEntityID: legalEntity, SourceType: JournalRealizedFX, SourceID: creditItem.ID,
				SourceKey: "credit-fx-reversal:" + batchID, DocumentID: "CS-" + batchID + "-REVERSAL", PostingDate: today(), Currency: currency}, lines); err != nil {
				return err
			}
		}
		event.Total, event.RealizedFX = total, out.RealizedFX
		return enqueue(ctx, tx, "creditsettlement", out.ReversalBatchID, rcvevents.CreditSettlementReversed, p.TenantID, event)
	})
	return out, mapWriteErr(err)
}

func optDecimal(s *string) *decimal.Decimal {
	if s == nil {
		return nil
	}
	d := decimal.RequireFromString(*s)
	return &d
}
