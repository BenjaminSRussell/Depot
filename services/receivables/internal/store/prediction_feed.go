package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// PredictionInvoice is one customer invoice as the payment-prediction service learns from it: the terms (document and due date),
// the amount, how often the customer was dunned and disputed it, and, for a settled invoice, the day it was paid (#47).
type PredictionInvoice struct {
	ID            string          `json:"id"`
	LegalEntityID string          `json:"legalEntityId"`
	CustomerID    string          `json:"customerId"`
	Currency      string          `json:"currency"`
	Amount        decimal.Decimal `json:"amount"`
	DocumentDate  string          `json:"documentDate"`
	DueDate       string          `json:"dueDate"`
	DunningLevel  int             `json:"dunningLevel"`
	Disputes      int             `json:"disputes"`
	PaidDate      *string         `json:"paidDate,omitempty"`
	UpdatedAt     time.Time       `json:"updatedAt"`
}

// PredictionFeedFilter selects open or paid invoices changed at or after UpdatedSince, continuing after (AfterAt, AfterID).
type PredictionFeedFilter struct {
	Paid         bool
	UpdatedSince time.Time
	AfterAt      time.Time
	AfterID      string
}

// ListPredictionInvoices returns INVOICE items in the caller's legal entities ordered by (updated_at, id): the open ones (OPEN or
// PARTIALLY_SETTLED) or the settled ones with the day of their last settlement. A change to an item (a settlement, a dunning
// step, a dispute raised or resolved, which also touches the item) moves its updated_at, so a caller that remembers the last
// updated_at it read sees every later change.
func (s *Postgres) ListPredictionInvoices(ctx context.Context, f PredictionFeedFilter, limit int) ([]PredictionInvoice, error) {
	out := []PredictionInvoice{}
	var since, after, afterID any
	if !f.UpdatedSince.IsZero() {
		since = f.UpdatedSince
	}
	if !f.AfterAt.IsZero() {
		after, afterID = f.AfterAt, f.AfterID
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select o.id::text, o.legal_entity_id::text, o.customer_id::text, o.currency::text, o.amount::text,
				to_char(o.document_date, 'YYYY-MM-DD'), to_char(o.due_date, 'YYYY-MM-DD'), o.dunning_level,
				(select count(*) from dispute d where d.tenant_id = o.tenant_id and d.open_item_id = o.id),
				case when o.status = 'SETTLED' then to_char((select max(s.settled_at) from settlement s where s.tenant_id = o.tenant_id and s.open_item_id = o.id and s.reversal_of_id is null), 'YYYY-MM-DD') end,
				o.updated_at
			from open_item o
			where o.source_type = 'INVOICE'
			  and (case when $1::bool then o.status = 'SETTLED' else o.status in ('OPEN', 'PARTIALLY_SETTLED') end)
			  and ($2::timestamptz is null or o.updated_at >= $2::timestamptz)
			  and ($3::timestamptz is null or (o.updated_at, o.id) > ($3::timestamptz, $4::uuid))
			order by o.updated_at, o.id limit $5`, f.Paid, since, after, afterID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v PredictionInvoice
			var amount string
			if err := rows.Scan(&v.ID, &v.LegalEntityID, &v.CustomerID, &v.Currency, &amount, &v.DocumentDate, &v.DueDate, &v.DunningLevel, &v.Disputes, &v.PaidDate, &v.UpdatedAt); err != nil {
				return err
			}
			if v.Amount, err = decimal.NewFromString(amount); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}
