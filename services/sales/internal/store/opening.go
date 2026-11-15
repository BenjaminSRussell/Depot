package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Opening import of open sales orders at cutover (#49). Nothing is announced: no order.confirmed event, no
// allocation and no credit check, because the orders were confirmed and partly shipped in the legacy system.

// ErrOpeningKeyConflict is the same batch key sent again with different content.
var ErrOpeningKeyConflict = errors.New("batch key already imported with different content")

// OpeningTakenError names the line references an earlier batch already imported, with that batch's key, and the
// order numbers the legal entity already uses.
type OpeningTakenError struct {
	References   map[string]string
	OrderNumbers map[string]bool
}

func (e *OpeningTakenError) Error() string { return "references or order numbers already in use" }

// OpeningLine is one open order line at the cutover.
type OpeningLine struct {
	Reference    string
	OrderNumber  string
	LineNo       int
	CustomerID   string
	ProductID    string
	SiteID       string
	WarehouseID  string
	Unit         string
	Currency     string
	OrderedQty   decimal.Decimal
	DeliveredQty decimal.Decimal
	// InvoicedQty is the part of the delivered quantity already invoiced in the legacy system (and so carried as an opening receivable),
	// stored as the line's invoiced_qty so it is not counted again as delivered and not invoiced.
	InvoicedQty  decimal.Decimal
	UnitPrice    decimal.Decimal
	DeliveryDate string
}

// OpenQty is what is still to ship.
func (l OpeningLine) OpenQty() decimal.Decimal { return l.OrderedQty.Sub(l.DeliveredQty) }

// OpenValue is the open quantity at the unit price, to six decimals.
func (l OpeningLine) OpenValue() decimal.Decimal { return l.OpenQty().Mul(l.UnitPrice).Round(6) }

// OpeningImport is one batch.
type OpeningImport struct {
	LegalEntityID string
	BatchKey      string
	ContentHash   string
	ImportedBy    string
	ControlTotals map[string]string
	Lines         []OpeningLine
}

// OpeningBatch is a stored batch with the counts and totals read back from its stored lines.
type OpeningBatch struct {
	ID            string            `json:"id"`
	LegalEntityID string            `json:"legalEntityId"`
	BatchKey      string            `json:"batchKey"`
	OrderCount    int               `json:"orderCount"`
	LineCount     int               `json:"lineCount"`
	OpenQty       string            `json:"openQty"`
	OpenValue     map[string]string `json:"openValue"`
	ImportedBy    string            `json:"importedBy"`
	ImportedAt    time.Time         `json:"importedAt"`
}

// ImportOpeningOrders stores a batch all or nothing. The same key with the same content is a replay (false); the
// same key with other content is ErrOpeningKeyConflict; a taken reference or order number is *OpeningTakenError.
func (s *Postgres) ImportOpeningOrders(ctx context.Context, in OpeningImport) (OpeningBatch, bool, error) {
	out, created, err := s.importOpening(ctx, in)
	if isOpeningRace(err) {
		out, created, err = s.importOpening(ctx, in)
		if isOpeningRace(err) {
			err = &OpeningTakenError{References: map[string]string{}, OrderNumbers: map[string]bool{}}
		}
	}
	return out, created, err
}

func isOpeningRace(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

type openingOrder struct {
	id       string
	number   string
	customer string
	currency string
	lines    []OpeningLine
}

func groupOpeningOrders(lines []OpeningLine) []*openingOrder {
	byNumber := map[string]*openingOrder{}
	var out []*openingOrder
	for _, l := range lines {
		o, ok := byNumber[l.OrderNumber]
		if !ok {
			o = &openingOrder{id: ids.New(), number: l.OrderNumber, customer: l.CustomerID, currency: l.Currency}
			byNumber[l.OrderNumber] = o
			out = append(out, o)
		}
		o.lines = append(o.lines, l)
	}
	return out
}

func (s *Postgres) importOpening(ctx context.Context, in OpeningImport) (OpeningBatch, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out OpeningBatch
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('sales_opening_batch:' || $1 || ':' || $2 || ':' || $3, 0))`, p.TenantID, in.LegalEntityID, in.BatchKey); err != nil {
			return err
		}
		var id, hash string
		err := tx.QueryRow(ctx, `select id::text, content_hash from sales_opening_batch where legal_entity_id = $1::uuid and batch_key = $2`, in.LegalEntityID, in.BatchKey).Scan(&id, &hash)
		if err == nil {
			if hash != in.ContentHash {
				return ErrOpeningKeyConflict
			}
			batches, err := openingBatches(ctx, tx, in.LegalEntityID, in.BatchKey, 1, time.Time{}, "")
			if err != nil || len(batches) == 0 {
				return errors.Join(err, pgx.ErrNoRows)
			}
			out = batches[0]
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := rejectTakenOpening(ctx, tx, in); err != nil {
			return err
		}
		orders := groupOpeningOrders(in.Lines)
		totals, _ := json.Marshal(in.ControlTotals)
		id = ids.New()
		if _, err := tx.Exec(ctx, `insert into sales_opening_batch (tenant_id, id, legal_entity_id, batch_key, content_hash, order_count, line_count, control_totals, imported_by)
			values ($1, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9)`, p.TenantID, id, in.LegalEntityID, in.BatchKey, in.ContentHash, len(orders), len(in.Lines), totals, in.ImportedBy); err != nil {
			return err
		}
		for _, o := range orders {
			if err := insertOpeningOrder(ctx, tx, p.TenantID, in, id, o); err != nil {
				return err
			}
		}
		created = true
		batches, err := openingBatches(ctx, tx, in.LegalEntityID, in.BatchKey, 1, time.Time{}, "")
		if err != nil || len(batches) == 0 {
			return errors.Join(err, pgx.ErrNoRows)
		}
		out = batches[0]
		return nil
	})
	return out, created, err
}

func insertOpeningOrder(ctx context.Context, tx pgx.Tx, tenantID string, in OpeningImport, batchID string, o *openingOrder) error {
	net, status, earliest := decimal.Zero, "CONFIRMED", ""
	for _, l := range o.lines {
		net = net.Add(l.OrderedQty.Mul(l.UnitPrice).Round(4))
		if l.DeliveredQty.IsPositive() {
			status = "PARTIALLY_SHIPPED"
		}
		if earliest == "" || l.DeliveryDate < earliest {
			earliest = l.DeliveryDate
		}
	}
	if _, err := tx.Exec(ctx, `insert into sales_order (tenant_id, id, legal_entity_id, order_number, customer_id, invoice_account_id, order_type, status, currency,
			requested_ship_date, net_total, channel, request_hash)
		values ($1, $2, $3::uuid, $4, $5::uuid, $5::uuid, 'ORDER', $6, $7, $8::date, $9::numeric, 'MIGRATION', $10)`,
		tenantID, o.id, in.LegalEntityID, o.number, o.customer, status, o.currency, earliest, net.String(), in.ContentHash); err != nil {
		return err
	}
	for _, l := range o.lines {
		lineID := ids.New()
		lineNet := l.OrderedQty.Mul(l.UnitPrice).Round(4)
		if _, err := tx.Exec(ctx, `insert into sales_order_line (tenant_id, id, order_id, line_no, product_id, quantity, unit, site_id, warehouse_id,
				unit_price, disc_pct, line_net, requested_ship_date, confirmed_ship_date, line_status, delivered_qty, invoiced_qty)
			values ($1, $2, $3, $4, $5::uuid, $6::numeric, $7, $8::uuid, $9::uuid, $10::numeric, 0, $11::numeric, $12::date, $12::date, CASE WHEN $13::numeric > 0 THEN 'PICKED' ELSE 'OPEN' END, $13::numeric, $14::numeric)`,
			tenantID, lineID, o.id, l.LineNo, l.ProductID, l.OrderedQty.String(), l.Unit, l.SiteID, l.WarehouseID, l.UnitPrice.String(), lineNet.String(),
			l.DeliveryDate, l.DeliveredQty.String(), l.InvoicedQty.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into sales_opening_line (tenant_id, legal_entity_id, reference, batch_id, order_id, line_id, currency, ordered_qty, delivered_qty, open_qty, open_value)
			values ($1, $2::uuid, $3, $4::uuid, $5, $6, $7, $8::numeric, $9::numeric, $10::numeric, $11::numeric)`,
			tenantID, in.LegalEntityID, l.Reference, batchID, o.id, lineID, l.Currency, l.OrderedQty.String(), l.DeliveredQty.String(), l.OpenQty().String(), l.OpenValue().String()); err != nil {
			return err
		}
	}
	return nil
}

func rejectTakenOpening(ctx context.Context, tx pgx.Tx, in OpeningImport) error {
	refs := make([]string, len(in.Lines))
	numbers := make([]string, len(in.Lines))
	for i, l := range in.Lines {
		refs[i], numbers[i] = l.Reference, l.OrderNumber
	}
	taken := &OpeningTakenError{References: map[string]string{}, OrderNumbers: map[string]bool{}}
	rows, err := tx.Query(ctx, `select l.reference, b.batch_key from sales_opening_line l join sales_opening_batch b on b.tenant_id = l.tenant_id and b.id = l.batch_id
		where l.legal_entity_id = $1::uuid and l.reference = any($2)`, in.LegalEntityID, refs)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ref, key string
		if err := rows.Scan(&ref, &key); err != nil {
			rows.Close()
			return err
		}
		taken.References[ref] = key
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `select order_number from sales_order where legal_entity_id = $1::uuid and order_number = any($2)`, in.LegalEntityID, numbers)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		taken.OrderNumbers[n] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(taken.References) > 0 || len(taken.OrderNumbers) > 0 {
		return taken
	}
	return nil
}

// ListOpeningBatches returns a legal entity's imported batches, newest first, optionally one batch key, after the
// (afterAt, afterID) keyset position.
func (s *Postgres) ListOpeningBatches(ctx context.Context, legalEntityID, batchKey string, limit int, afterAt time.Time, afterID string) ([]OpeningBatch, error) {
	var out []OpeningBatch
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = openingBatches(ctx, tx, legalEntityID, batchKey, min(max(limit, 1), 201), afterAt, afterID)
		return err
	})
	return out, err
}

func openingBatches(ctx context.Context, tx pgx.Tx, legalEntityID, batchKey string, limit int, afterAt time.Time, afterID string) ([]OpeningBatch, error) {
	var cursorAt any
	if !afterAt.IsZero() {
		cursorAt = afterAt
	}
	rows, err := tx.Query(ctx, `select b.id::text, b.legal_entity_id::text, b.batch_key, b.imported_by, b.imported_at, b.order_count, b.line_count,
			coalesce(l.currency, ''), coalesce(sum(l.open_qty), 0)::text, coalesce(sum(l.open_value), 0)::text
		from (select * from sales_opening_batch where legal_entity_id = $1::uuid and ($2 = '' or batch_key = $2)
			and ($4::timestamptz is null or (imported_at, id) < ($4::timestamptz, $5::uuid)) order by imported_at desc, id desc limit $3) b
		left join sales_opening_line l on l.tenant_id = b.tenant_id and l.batch_id = b.id
		group by b.id, b.legal_entity_id, b.batch_key, b.imported_by, b.imported_at, b.order_count, b.line_count, l.currency
		order by b.imported_at desc, b.id desc, l.currency`, legalEntityID, batchKey, limit, cursorAt, nullable(afterID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OpeningBatch{}
	qty := map[string]decimal.Decimal{}
	for rows.Next() {
		var b OpeningBatch
		var currency, q, v string
		if err := rows.Scan(&b.ID, &b.LegalEntityID, &b.BatchKey, &b.ImportedBy, &b.ImportedAt, &b.OrderCount, &b.LineCount, &currency, &q, &v); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].ID != b.ID {
			b.OpenValue = map[string]string{}
			out = append(out, b)
		}
		cur := &out[len(out)-1]
		dq, err := decimal.NewFromString(q)
		if err != nil {
			return nil, err
		}
		qty[cur.ID] = qty[cur.ID].Add(dq)
		cur.OpenQty = qty[cur.ID].String()
		if currency != "" {
			dv, err := decimal.NewFromString(v)
			if err != nil {
				return nil, err
			}
			cur.OpenValue[currency] = dv.String()
		}
	}
	for i := range out {
		if out[i].OpenQty == "" {
			out[i].OpenQty = "0"
		}
	}
	return out, rows.Err()
}
