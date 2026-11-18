package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const openLineDay = "2006-01-02"

type OpenOrderLine struct {
	LineID      string          `json:"lineId"`
	OrderID     string          `json:"orderId"`
	OrderNumber string          `json:"orderNumber"`
	LineNo      int             `json:"lineNo"`
	ProductID   string          `json:"productId"`
	SiteID      string          `json:"siteId"`
	WarehouseID string          `json:"warehouseId"`
	OpenQty     decimal.Decimal `json:"openQty"`
	DueDate     string          `json:"dueDate"`
}

type OpenLinePage struct {
	Items []OpenOrderLine `json:"items"`
	Next  string          `json:"next"`
}

type OpenLineCursor struct {
	Date   time.Time
	LineID string
}

func (c OpenLineCursor) String() string { return c.Date.Format(openLineDay) + "|" + c.LineID }

func ParseOpenLineCursor(raw string) (*OpenLineCursor, bool) {
	if raw == "" {
		return nil, true
	}
	date, id, found := strings.Cut(raw, "|")
	day, err := time.Parse(openLineDay, date)
	if !found || err != nil || !ids.Valid(id) {
		return nil, false
	}
	return &OpenLineCursor{Date: day, LineID: id}, true
}

type OpenLineFilter struct {
	LegalEntityID string
	SiteID        string
	DueTo         string
	After         *OpenLineCursor
	Limit         int
}

func (s *Postgres) ListOpenOrderLines(ctx context.Context, f OpenLineFilter) (OpenLinePage, error) {
	p, _ := tenant.FromContext(ctx)
	var after *time.Time
	var afterLine *string
	if f.After != nil {
		after, afterLine = &f.After.Date, &f.After.LineID
	}
	page := OpenLinePage{Items: []OpenOrderLine{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select l.id::text, o.id::text, o.order_number, l.line_no, l.product_id::text, l.site_id::text, l.warehouse_id::text,
				(l.quantity - l.delivered_qty)::text, coalesce(l.confirmed_ship_date, l.requested_ship_date)
			from sales_order_line l
			join sales_order o on o.tenant_id = l.tenant_id and o.id = l.order_id
			where l.tenant_id = $1 and o.order_type = 'ORDER' and o.status in ('OPEN', 'CONFIRMED', 'PARTIALLY_SHIPPED')
				and l.line_status not in ('DELIVERED', 'INVOICED', 'CANCELLED') and l.quantity > l.delivered_qty
				and ($2::uuid is null or o.legal_entity_id = $2::uuid)
				and ($3::uuid is null or l.site_id = $3::uuid)
				and ($4::date is null or coalesce(l.confirmed_ship_date, l.requested_ship_date) <= $4::date)
				and ($5::date is null or (coalesce(l.confirmed_ship_date, l.requested_ship_date), l.id) > ($5::date, $6::uuid))
			order by coalesce(l.confirmed_ship_date, l.requested_ship_date), l.id
			limit $7`, p.TenantID, nullIfEmpty(f.LegalEntityID), nullIfEmpty(f.SiteID), nullIfEmpty(f.DueTo), after, afterLine, f.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var l OpenOrderLine
			var qty string
			var due time.Time
			if err := rows.Scan(&l.LineID, &l.OrderID, &l.OrderNumber, &l.LineNo, &l.ProductID, &l.SiteID, &l.WarehouseID, &qty, &due); err != nil {
				return err
			}
			if l.OpenQty, err = decimal.NewFromString(qty); err != nil {
				return fmt.Errorf("open quantity %q: %w", qty, err)
			}
			l.DueDate = due.Format(openLineDay)
			page.Items = append(page.Items, l)
		}
		return rows.Err()
	})
	if err != nil {
		return OpenLinePage{}, err
	}
	if len(page.Items) > f.Limit {
		page.Items = page.Items[:f.Limit]
		last := page.Items[f.Limit-1]
		day, _ := time.Parse(openLineDay, last.DueDate)
		page.Next = OpenLineCursor{Date: day, LineID: last.LineID}.String()
	}
	return page, nil
}
