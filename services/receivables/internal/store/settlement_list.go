package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

// PaymentSettlement is one application of a payment: what it took off an item, and whether it has been reversed since.
type PaymentSettlement struct {
	ID             string          `json:"id"`
	OpenItemID     string          `json:"openItemId"`
	DocumentNumber string          `json:"documentNumber"`
	Amount         decimal.Decimal `json:"amount"`
	CashDiscount   decimal.Decimal `json:"cashDiscount"`
	WriteOff       decimal.Decimal `json:"writeOff"`
	// ItemCurrency, ItemAmount and RealizedFX are set for a cross-currency settlement.
	ItemCurrency string           `json:"itemCurrency,omitempty"`
	ItemAmount   *decimal.Decimal `json:"itemAmount,omitempty"`
	RealizedFX   *decimal.Decimal `json:"realizedFx,omitempty"`
	SettledAt    time.Time        `json:"settledAt"`
	Reversed     bool             `json:"reversed"`
}

// ListPaymentSettlements lists the applications of a payment, oldest first; the ones with Reversed false are the live ones a
// single-settlement unapply can reverse. Reversal rows themselves are not listed.
func (s *Postgres) ListPaymentSettlements(ctx context.Context, paymentID string) ([]PaymentSettlement, error) {
	p, _ := tenant.FromContext(ctx)
	out := []PaymentSettlement{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var one int
		if err := tx.QueryRow(ctx, `select 1 from payment where id = $1`, paymentID).Scan(&one); err != nil {
			return notFound(err, CodePaymentNotFound, "payment not found")
		}
		rows, err := tx.Query(ctx, `select s.id::text, s.open_item_id::text, o.document_number, s.settled_amount::text, s.cash_discount_taken::text,
				s.write_off_amount::text, s.item_currency::text, s.item_amount::text, s.fx_gain_loss::text, s.settled_at,
				exists (select 1 from settlement r where r.tenant_id = s.tenant_id and r.reversal_of_id = s.id)
			from settlement s join open_item o on o.tenant_id = s.tenant_id and o.id = s.open_item_id
			where s.tenant_id = $1 and s.payment_id = $2 and s.reversal_of_id is null order by s.settled_at, s.id`, p.TenantID, paymentID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l PaymentSettlement
			var amount, discount, off, fx string
			var itemCurrency, itemAmount *string
			if err := rows.Scan(&l.ID, &l.OpenItemID, &l.DocumentNumber, &amount, &discount, &off, &itemCurrency, &itemAmount, &fx, &l.SettledAt, &l.Reversed); err != nil {
				return err
			}
			l.Amount, l.CashDiscount, l.WriteOff = decimal.RequireFromString(amount), decimal.RequireFromString(discount), decimal.RequireFromString(off)
			if itemCurrency != nil && itemAmount != nil {
				l.ItemCurrency = *itemCurrency
				l.ItemAmount = optDecimal(itemAmount)
				l.RealizedFX = optDecimal(&fx)
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, mapWriteErr(err)
}

// CreditSettlementFilter narrows the credit settlements listed.
type CreditSettlementFilter struct {
	CustomerID   string
	CreditItemID string
	// OpenItemID matches a settlement whose credit or debit is that item.
	OpenItemID string
}

// CreditSettlementBatch is one credit settlement as SettleCredit made it, with its debits.
type CreditSettlementBatch struct {
	BatchID        string                 `json:"batchId"`
	LegalEntityID  string                 `json:"legalEntityId"`
	CustomerID     string                 `json:"customerId"`
	Currency       string                 `json:"currency"`
	CreditItemID   string                 `json:"creditItemId"`
	CreditDocument string                 `json:"creditDocument"`
	Total          decimal.Decimal        `json:"total"`
	SettledAt      time.Time              `json:"settledAt"`
	Reversed       bool                   `json:"reversed"`
	Debits         []CreditSettlementLine `json:"debits"`
}

// ListCreditSettlements lists credit settlements, newest first, with whether each was reversed. A debit's OpenAfter and ItemStatus are
// not part of the listing (the item itself carries them).
func (s *Postgres) ListCreditSettlements(ctx context.Context, f CreditSettlementFilter, limit int) ([]CreditSettlementBatch, error) {
	p, _ := tenant.FromContext(ctx)
	out := []CreditSettlementBatch{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select cs.batch_id::text, cs.legal_entity_id::text, cs.customer_id::text, cs.currency::text, cs.credit_item_id::text,
				c.document_number, cs.debit_item_id::text, d.document_number, cs.amount::text, cs.debit_currency::text, cs.debit_amount::text, cs.fx_gain_loss::text,
				cs.settled_at, exists (select 1 from credit_settlement r where r.tenant_id = cs.tenant_id and r.reversal_of_id = cs.id)
			from credit_settlement cs
			join open_item c on c.tenant_id = cs.tenant_id and c.id = cs.credit_item_id
			join open_item d on d.tenant_id = cs.tenant_id and d.id = cs.debit_item_id
			where cs.tenant_id = $1 and cs.reversal_of_id is null
			  and ($2::text = '' or cs.customer_id::text = $2) and ($3::text = '' or cs.credit_item_id::text = $3)
			  and ($4::text = '' or cs.credit_item_id::text = $4 or cs.debit_item_id::text = $4)
			order by cs.settled_at desc, cs.batch_id, cs.id limit $5`, p.TenantID, f.CustomerID, f.CreditItemID, f.OpenItemID, limit*20)
		if err != nil {
			return err
		}
		defer rows.Close()
		byBatch := map[string]int{}
		for rows.Next() {
			var batchID, le, customer, currency, creditID, creditDoc, debitID, debitDoc, amount, fx string
			var debitCurrency, debitAmount *string
			var at time.Time
			var reversed bool
			if err := rows.Scan(&batchID, &le, &customer, &currency, &creditID, &creditDoc, &debitID, &debitDoc, &amount, &debitCurrency, &debitAmount, &fx, &at, &reversed); err != nil {
				return err
			}
			i, ok := byBatch[batchID]
			if !ok {
				if len(out) >= limit {
					continue
				}
				i = len(out)
				byBatch[batchID] = i
				out = append(out, CreditSettlementBatch{BatchID: batchID, LegalEntityID: le, CustomerID: customer, Currency: currency, CreditItemID: creditID,
					CreditDocument: creditDoc, Total: decimal.Zero, SettledAt: at, Reversed: reversed, Debits: []CreditSettlementLine{}})
			}
			amt := decimal.RequireFromString(amount)
			line := CreditSettlementLine{OpenItemID: debitID, DocumentNumber: debitDoc, Amount: amt}
			if debitCurrency != nil && debitAmount != nil {
				line.ItemCurrency, line.ItemAmount, line.RealizedFX = *debitCurrency, optDecimal(debitAmount), optDecimal(&fx)
			}
			out[i].Total = out[i].Total.Add(amt)
			out[i].Debits = append(out[i].Debits, line)
		}
		return rows.Err()
	})
	return out, mapWriteErr(err)
}
