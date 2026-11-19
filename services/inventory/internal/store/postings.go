package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/inventory/internal/domain"
)

// issueCostScale is the scale of the unit cost IssueBasis derives (cost prices
// are numeric(20,6)).
const issueCostScale = 6

// CodeInvoiceConflict refuses an invoice line delivered again with another
// quantity or amount than the one recorded for it.
const CodeInvoiceConflict = "INV-PST-007"

// IssueBasis is what the postings consumer issues a sales delivery against on
// the on-hand row of a product and dimension set: the currency the row is
// valued in ("" when the row does not exist or holds no value) and the running
// average unit cost of the stock on it, physical included: the posted value
// plus the physical cost of the transactions that are physically but not
// financially updated (receipts at REGISTERED or RECEIVED, issues at PICKED or
// DEDUCTED), over the same quantities. It is zero when no stock is left. The
// store values an issue at the posted average when there is posted stock and
// at this cost (the issue's fallback cost price) when there is none, so a
// delivery of stock that was received but not yet invoiced is still valued
// at its receipt cost. An issue in another currency than the row's is
// refused (CodeCurrencyMismatch), which is why the currency is read too.
func (s *Postgres) IssueBasis(ctx context.Context, legalEntityID, productID string, dims domain.Dimensions) (string, decimal.Decimal, error) {
	norm, err := dims.Normalize()
	if err != nil {
		return "", decimal.Zero, domainProblem(&domain.Error{Code: domain.CodeInvalid, Detail: err.Error()})
	}
	var currency *string
	var qty, value decimal.Decimal
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select o.currency::text, o.posted_qty + coalesce(p.qty, 0), o.posted_value + coalesce(p.value, 0)
			from on_hand o
			left join lateral (
				select sum(t.qty) as qty, sum(t.cost_amount_physical) as value from inventory_transaction t
				where t.tenant_id = o.tenant_id and t.legal_entity_id = o.legal_entity_id and t.product_id = o.product_id
					and t.dimension_set_hash = o.dimension_set_hash
					and (t.receipt_status in ('REGISTERED', 'RECEIVED') or t.issue_status in ('PICKED', 'DEDUCTED'))
			) p on true
			where o.legal_entity_id = $1 and o.product_id = $2 and o.dimension_set_hash = $3`,
			legalEntityID, productID, norm.Hash()).Scan(&currency, &qty, &value)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", decimal.Zero, nil
	}
	if err != nil {
		return "", decimal.Zero, err
	}
	cur := ""
	if currency != nil {
		cur = *currency
	}
	if !qty.IsPositive() || value.IsNegative() {
		return cur, decimal.Zero, nil
	}
	return cur, value.DivRound(qty, issueCostScale), nil
}

// WarehouseIssueCost is the running average unit cost of a product over the
// on-hand rows a sales delivery books against in one warehouse: the rows whose
// dimensions are exactly site and warehouse, the set bookDelivery issues from
// through IssueBasis. When those rows hold no stock, the stock the warehouse
// holds split by location, batch or any other dimension is valued instead, so
// a caller never supplies the cost of stock that exists. It values the rows as
// IssueBasis does and is zero, with an empty currency, when the warehouse
// holds no value at all.
func (s *Postgres) WarehouseIssueCost(ctx context.Context, legalEntityID, productID, warehouse string) (string, decimal.Decimal, error) {
	const exactRows = ` and d.dims ? 'site' and (d.dims - 'site' - 'warehouse') = '{}'::jsonb`
	cur, cost, qty, err := s.warehouseCost(ctx, legalEntityID, productID, warehouse, exactRows)
	if err != nil || qty.IsPositive() {
		return cur, cost, err
	}
	allSplits, splitCost, _, err := s.warehouseCost(ctx, legalEntityID, productID, warehouse, "")
	if err != nil || allSplits == "" {
		return cur, cost, err
	}
	return allSplits, splitCost, nil
}

func (s *Postgres) warehouseCost(ctx context.Context, legalEntityID, productID, warehouse, dimensionClause string) (string, decimal.Decimal, decimal.Decimal, error) {
	var currency *string
	var qty, value decimal.Decimal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select max(o.currency::text), coalesce(sum(o.posted_qty + coalesce(p.qty, 0)), 0), coalesce(sum(o.posted_value + coalesce(p.value, 0)), 0)
			from on_hand o
			join dimension_set d on d.tenant_id = o.tenant_id and d.hash = o.dimension_set_hash
			left join lateral (
				select sum(t.qty) as qty, sum(t.cost_amount_physical) as value from inventory_transaction t
				where t.tenant_id = o.tenant_id and t.legal_entity_id = o.legal_entity_id and t.product_id = o.product_id
					and t.dimension_set_hash = o.dimension_set_hash
					and (t.receipt_status in ('REGISTERED', 'RECEIVED') or t.issue_status in ('PICKED', 'DEDUCTED'))
			) p on true
			where o.legal_entity_id = $1 and o.product_id = $2 and lower(d.dims->>'warehouse') = lower($3)`+dimensionClause,
			legalEntityID, productID, warehouse).Scan(&currency, &qty, &value)
	})
	if err != nil {
		return "", decimal.Zero, decimal.Zero, err
	}
	cur := ""
	if currency != nil {
		cur = *currency
	}
	if !qty.IsPositive() || value.IsNegative() {
		return cur, decimal.Zero, qty, nil
	}
	return cur, value.DivRound(qty, issueCostScale), qty, nil
}

// InvoicedTotal is what the invoices posted so far recorded against one
// receipt line: the invoiced quantity and amount.
type InvoicedTotal struct {
	Qty    decimal.Decimal
	Amount decimal.Decimal
}

// RecordInvoicePart records what one invoice line invoiced against a receipt
// line and returns the line's cumulative totals over every invoice. It is
// idempotent per invoice line: a redelivery finds its own row, and one that
// carries another quantity or amount is refused (INV-PST-007).
func (s *Postgres) RecordInvoicePart(ctx context.Context, legalEntityID, lineSourceID, invoiceID string, invoiceLineNo int, qty, amount decimal.Decimal) (InvoicedTotal, error) {
	p, _ := tenant.FromContext(ctx)
	var total InvoicedTotal
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into receipt_line_invoice (tenant_id, legal_entity_id, line_source_id, invoice_id, invoice_line_no, qty, amount)
			values ($1, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7) on conflict do nothing`,
			p.TenantID, legalEntityID, lineSourceID, invoiceID, invoiceLineNo, qty, amount); err != nil {
			return err
		}
		var storedQty, storedAmount decimal.Decimal
		if err := tx.QueryRow(ctx, `select qty, amount from receipt_line_invoice
			where tenant_id = $1 and line_source_id = $2::uuid and invoice_id = $3::uuid and invoice_line_no = $4`,
			p.TenantID, lineSourceID, invoiceID, invoiceLineNo).Scan(&storedQty, &storedAmount); err != nil {
			return err
		}
		if !storedQty.Equal(qty) || !storedAmount.Equal(amount) {
			return problem(http.StatusConflict, CodeInvoiceConflict, "invoice line "+strconv.Itoa(invoiceLineNo)+" was recorded with quantity "+storedQty.String()+" and amount "+storedAmount.String())
		}
		return tx.QueryRow(ctx, `select coalesce(sum(qty), 0), coalesce(sum(amount), 0) from receipt_line_invoice
			where tenant_id = $1 and line_source_id = $2::uuid`, p.TenantID, lineSourceID).Scan(&total.Qty, &total.Amount)
	})
	return total, err
}

// OriginBookings returns the transactions registered for the given origins of
// one source type in a legal entity, keyed by source id: what the postings
// consumer booked for a receipt line or a delivery line, read back so a
// correction or a cancellation offsets exactly that booking (#480). An origin
// without a transaction is absent from the map.
func (s *Postgres) OriginBookings(ctx context.Context, legalEntityID, sourceType string, sourceIDs []string) (map[string]TransactionRow, error) {
	out := map[string]TransactionRow{}
	if len(sourceIDs) == 0 {
		return out, nil
	}
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+transactionColumns+transactionFrom+`
			where t.tenant_id = $1 and t.legal_entity_id = $2::uuid and o.source_type = $3 and o.source_id = any($4::uuid[])`,
			p.TenantID, legalEntityID, sourceType, sourceIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanTransaction(rows)
			if err != nil {
				return err
			}
			out[r.SourceID] = r
		}
		return rows.Err()
	})
	return out, err
}

// LineBookings returns, for each document line origin, every transaction
// booked for it in a legal entity, oldest first: the line's own booking for
// an untracked line, or each entry of a tracked one (origins whose
// line_source_id names the line, #480). A line without a booking is absent.
// Corrections and cancellations read the line back this way so they offset
// every entry, whatever the entries were.
func (s *Postgres) LineBookings(ctx context.Context, legalEntityID, sourceType string, lineSourceIDs []string) (map[string][]TransactionRow, error) {
	out := map[string][]TransactionRow{}
	if len(lineSourceIDs) == 0 {
		return out, nil
	}
	p, _ := tenant.FromContext(ctx)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select coalesce(o.line_source_id, o.source_id)::text, `+transactionColumns+transactionFrom+`
			where t.tenant_id = $1 and t.legal_entity_id = $2::uuid and o.source_type = $3
				and (o.source_id = any($4::uuid[]) or o.line_source_id = any($4::uuid[]))
			order by t.created_at, t.id`,
			p.TenantID, legalEntityID, sourceType, lineSourceIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			r, err := scanTransactionWith(rows, &line)
			if err != nil {
				return err
			}
			out[line] = append(out[line], r)
		}
		return rows.Err()
	})
	return out, err
}
