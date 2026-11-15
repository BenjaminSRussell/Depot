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
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

// Opening import of open purchase orders at cutover (#49). Nothing is announced: no po.confirmed event, no approval and
// no budget call, because the orders were approved and partly received in the legacy system.

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
	VendorID     string
	ProductID    string
	SiteID       string
	WarehouseID  string
	Unit         string
	Currency     string
	OrderedQty   decimal.Decimal
	ReceivedQty  decimal.Decimal
	UnitPrice    decimal.Decimal
	DeliveryDate string
}

// OpenQty is what is still to receive.
func (l OpeningLine) OpenQty() decimal.Decimal { return l.OrderedQty.Sub(l.ReceivedQty) }

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
	vendor   string
	currency string
	lines    []OpeningLine
}

func groupOpeningOrders(lines []OpeningLine) []*openingOrder {
	byNumber := map[string]*openingOrder{}
	var out []*openingOrder
	for _, l := range lines {
		o, ok := byNumber[l.OrderNumber]
		if !ok {
			o = &openingOrder{id: ids.New(), number: l.OrderNumber, vendor: l.VendorID, currency: l.Currency}
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
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('po_opening_batch:' || $1 || ':' || $2 || ':' || $3, 0))`, p.TenantID, in.LegalEntityID, in.BatchKey); err != nil {
			return err
		}
		var id, hash string
		err := tx.QueryRow(ctx, `select id::text, content_hash from po_opening_batch where legal_entity_id = $1::uuid and batch_key = $2`, in.LegalEntityID, in.BatchKey).Scan(&id, &hash)
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
		if _, err := tx.Exec(ctx, `insert into po_opening_batch (tenant_id, id, legal_entity_id, batch_key, content_hash, order_count, line_count, control_totals, imported_by)
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
	total, docStatus := decimal.Zero, "NONE"
	snapLines := make([]domain.SnapshotLine, 0, len(o.lines))
	for _, l := range o.lines {
		amount := l.OrderedQty.Mul(l.UnitPrice).Round(6)
		total = total.Add(amount)
		if l.ReceivedQty.IsPositive() {
			docStatus = "RECEIVED_PARTIAL"
		}
		day, err := domain.ParseDay(l.DeliveryDate)
		if err != nil {
			return err
		}
		snapLines = append(snapLines, domain.SnapshotLine{LineNo: l.LineNo, ProductID: l.ProductID, Qty: l.OrderedQty, Unit: l.Unit,
			UnitPrice: l.UnitPrice, PriceUnit: decimal.NewFromInt(1), LineAmount: amount, DeliveryDate: day, Status: "OPEN", SiteID: l.SiteID, WarehouseID: l.WarehouseID})
	}
	if _, err := tx.Exec(ctx, `insert into purchase_order (tenant_id, id, legal_entity_id, number, vendor_id, currency, approval_status, document_status,
			version_no, change_management, subtotal, discount_total, charges_total, tax_total, total, created_by)
		values ($1, $2, $3::uuid, $4, $5::uuid, $6, 'CONFIRMED', $7, 1, true, $8::numeric, 0, 0, 0, $8::numeric, $9)`,
		tenantID, o.id, in.LegalEntityID, o.number, o.vendor, o.currency, docStatus, total.String(), userOrNil(in.ImportedBy)); err != nil {
		return err
	}
	for _, l := range o.lines {
		amount := l.OrderedQty.Mul(l.UnitPrice).Round(6)
		if _, err := tx.Exec(ctx, `insert into purchase_order_line (tenant_id, purchase_order_id, line_no, product_id, qty, unit, unit_price, price_unit,
				gross_amount, line_amount, delivery_date, price_source, site_id, warehouse_id, status, received_qty)
			values ($1, $2, $3, $4::uuid, $5::numeric, $6, $7::numeric, 1, $8::numeric, $8::numeric, $9::date, 'OPENING', $10::uuid, $11::uuid, 'OPEN', $12::numeric)`,
			tenantID, o.id, l.LineNo, l.ProductID, l.OrderedQty.String(), l.Unit, l.UnitPrice.String(), amount.String(), l.DeliveryDate, l.SiteID, l.WarehouseID, l.ReceivedQty.String()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `insert into po_opening_line (tenant_id, legal_entity_id, reference, batch_id, purchase_order_id, line_no, currency, ordered_qty, received_qty, open_qty, open_value)
			values ($1, $2::uuid, $3, $4::uuid, $5, $6, $7, $8::numeric, $9::numeric, $10::numeric, $11::numeric)`,
			tenantID, in.LegalEntityID, l.Reference, batchID, o.id, l.LineNo, l.Currency, l.OrderedQty.String(), l.ReceivedQty.String(), l.OpenQty().String(), l.OpenValue().String()); err != nil {
			return err
		}
	}
	snap := domain.NewSnapshot(o.currency, total, snapLines, nil)
	if err := writeVersion(ctx, tx, o.id, 1, snap, nil, false); err != nil {
		return err
	}
	return markConfirmed(ctx, tx, o.id, 1)
}

func rejectTakenOpening(ctx context.Context, tx pgx.Tx, in OpeningImport) error {
	refs := make([]string, len(in.Lines))
	numbers := make([]string, len(in.Lines))
	for i, l := range in.Lines {
		refs[i], numbers[i] = l.Reference, l.OrderNumber
	}
	taken := &OpeningTakenError{References: map[string]string{}, OrderNumbers: map[string]bool{}}
	rows, err := tx.Query(ctx, `select l.reference, b.batch_key from po_opening_line l join po_opening_batch b on b.tenant_id = l.tenant_id and b.id = l.batch_id
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
	rows, err = tx.Query(ctx, `select number from purchase_order where number = any($1)`, numbers)
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
		from (select * from po_opening_batch where legal_entity_id = $1::uuid and ($2 = '' or batch_key = $2)
			and ($4::timestamptz is null or (imported_at, id) < ($4::timestamptz, $5::uuid)) order by imported_at desc, id desc limit $3) b
		left join po_opening_line l on l.tenant_id = b.tenant_id and l.batch_id = b.id
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
