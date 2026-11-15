package target

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// The sales and purchasing clients (#49) send open orders to POST /v1/sales/opening-orders and POST /v1/procurement/opening-orders
// and read the stored batches back from GET. Both sign as the factory like the other clients do (own audience, one duty per token).

const (
	audSales      = "sales"
	audPurchasing = "purchasing"

	dutySalesImport          = "sales.opening-order.import"
	dutySalesImportRead      = "sales.opening-order.read"
	dutyPurchasingImport     = "procurement.opening-order.import"
	dutyPurchasingImportRead = "procurement.opening-order.read"

	pathSalesOrders      = "/v1/sales/opening-orders"
	pathPurchasingOrders = "/v1/procurement/opening-orders"

	// Problem codes of the two targets that name rows.
	CodeSalesRowsInvalid      = "SLS-OPB-001"
	CodeSalesRowsTaken        = "SLS-OPB-004"
	CodePurchasingRowsInvalid = "PRC-OPB-001"
	CodePurchasingRowsTaken   = "PRC-OPB-004"
)

// Orders is the opening-order client of one target, sales or purchasing.
type Orders struct {
	c       *Receivables
	target  string
	path    string
	dutyPut string
	dutyGet string
	invalid string
	taken   string
}

// NewSales builds the sales client. A zero Config is valid and reports Configured false.
func NewSales(cfg Config) *Orders {
	c := NewReceivables(cfg)
	c.aud = audSales
	return &Orders{c: c, target: domain.TargetSales, path: pathSalesOrders, dutyPut: dutySalesImport, dutyGet: dutySalesImportRead, invalid: CodeSalesRowsInvalid, taken: CodeSalesRowsTaken}
}

// NewPurchasing builds the purchasing client. A zero Config is valid and reports Configured false.
func NewPurchasing(cfg Config) *Orders {
	c := NewReceivables(cfg)
	c.aud = audPurchasing
	return &Orders{c: c, target: domain.TargetPurchasing, path: pathPurchasingOrders, dutyPut: dutyPurchasingImport, dutyGet: dutyPurchasingImportRead, invalid: CodePurchasingRowsInvalid, taken: CodePurchasingRowsTaken}
}

// Configured reports whether a load can be attempted: an address and a signing key.
func (o *Orders) Configured() bool { return o != nil && o.c.Configured() }

type orderLineWire struct {
	Reference    string `json:"reference"`
	OrderNumber  string `json:"orderNumber"`
	LineNo       int    `json:"lineNo"`
	CustomerID   string `json:"customerId,omitempty"`
	VendorID     string `json:"vendorId,omitempty"`
	ProductID    string `json:"productId"`
	SiteID       string `json:"siteId"`
	WarehouseID  string `json:"warehouseId"`
	Unit         string `json:"unit"`
	Currency     string `json:"currency"`
	OrderedQty   string `json:"orderedQty"`
	DeliveredQty string `json:"deliveredQty,omitempty"`
	InvoicedQty  string `json:"invoicedQty,omitempty"`
	ReceivedQty  string `json:"receivedQty,omitempty"`
	UnitPrice    string `json:"unitPrice"`
	DeliveryDate string `json:"deliveryDate"`
}

type orderBatchRequest struct {
	LegalEntityID string            `json:"legalEntityId"`
	BatchKey      string            `json:"batchKey"`
	ControlTotals map[string]string `json:"controlTotals"`
	Lines         []orderLineWire   `json:"lines"`
}

type orderBatch struct {
	BatchKey  string            `json:"batchKey"`
	LineCount int               `json:"lineCount"`
	OpenQty   string            `json:"openQty"`
	OpenValue map[string]string `json:"openValue"`
}

// LoadBatch sends one batch of 1 to 1000 lines. A refusal for lines (the target's rows-invalid code, or its taken code for a
// reference another batch imported) is a *RowsRefused; any other 4xx is ErrRefused.
func (o *Orders) LoadBatch(ctx context.Context, tenantID, legalEntityID, batchKey string, items []domain.OrderItem) (OpeningBatchResult, error) {
	if !o.Configured() {
		return OpeningBatchResult{}, ErrNotConfigured
	}
	if len(items) == 0 || len(items) > domain.MaxOrderBatchLines {
		return OpeningBatchResult{}, fmt.Errorf("a batch holds 1 to %d lines, got %d", domain.MaxOrderBatchLines, len(items))
	}
	req := orderBatchRequest{LegalEntityID: legalEntityID, BatchKey: batchKey, ControlTotals: map[string]string{}, Lines: make([]orderLineWire, len(items))}
	totals := map[string]decimal.Decimal{}
	for i, it := range items {
		w := orderLineWire{Reference: it.Reference, OrderNumber: it.OrderNumber, LineNo: it.LineNo, ProductID: it.ProductID, SiteID: it.SiteID, WarehouseID: it.WarehouseID,
			Unit: it.Unit, Currency: it.Currency, OrderedQty: it.OrderedQty.String(), UnitPrice: it.UnitPrice.String(), DeliveryDate: it.DeliveryDate}
		if o.target == domain.TargetPurchasing {
			w.VendorID, w.ReceivedQty = it.PartyID, it.DoneQty.String()
		} else {
			// What was delivered in the legacy system was invoiced there too (its open invoices are the AR opening items), so sales
			// must not count it again as delivered and not invoiced.
			w.CustomerID, w.DeliveredQty, w.InvoicedQty = it.PartyID, it.DoneQty.String(), it.DoneQty.String()
		}
		req.Lines[i] = w
		totals[it.Currency] = totals[it.Currency].Add(it.OpenValue())
	}
	for c, v := range totals {
		req.ControlTotals[c] = v.String()
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return OpeningBatchResult{}, err
	}
	status, body, err := o.c.do(ctx, http.MethodPost, o.path, tenantID, legalEntityID, o.dutyPut, "", payload)
	if err != nil {
		return OpeningBatchResult{}, err
	}
	switch status {
	case http.StatusOK, http.StatusCreated:
		var b orderBatch
		if json.Unmarshal(body, &b) != nil || b.BatchKey != batchKey || b.LineCount != len(items) {
			return OpeningBatchResult{}, &Error{Kind: ErrBadAnswer, Status: status, Detail: "the answer does not describe the batch sent"}
		}
		return OpeningBatchResult{ItemCount: b.LineCount, Replayed: status == http.StatusOK}, nil
	case http.StatusUnprocessableEntity:
		var pb problemBody
		_ = json.Unmarshal(body, &pb)
		if pb.Code == o.invalid || pb.Code == o.taken {
			if rows := rowErrors(pb.Errors, len(items)); len(rows) > 0 {
				return OpeningBatchResult{}, &RowsRefused{Code: pb.Code, Rows: rows, Errors: pb.RowErrors}
			}
		}
	}
	return OpeningBatchResult{}, answerError(status, body)
}

// BatchTotals reads every opening batch the target holds for the legal entity and sums the ones the factory wrote for the open
// orders of the company (keep reports whether a batch key is one): the number of lines, the open quantity and, per currency,
// the open value.
func (o *Orders) BatchTotals(ctx context.Context, tenantID, legalEntityID string, keep func(key string) bool) (domain.OrderTotals, error) {
	sum := domain.NewOrderTotals()
	if !o.Configured() {
		return sum, ErrNotConfigured
	}
	err := readPages(ctx, o.c, o.path, tenantID, legalEntityID, o.dutyGet, func(raw json.RawMessage) error {
		var b orderBatch
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		if !keep(b.BatchKey) {
			return nil
		}
		q, err := decimal.NewFromString(b.OpenQty)
		if err != nil {
			return err
		}
		sum.Lines += b.LineCount
		sum.Qty = sum.Qty.Add(q)
		for cur, raw := range b.OpenValue {
			v, err := decimal.NewFromString(raw)
			if err != nil {
				return err
			}
			sum.Value[cur] = sum.Value[cur].Add(v)
		}
		return nil
	})
	return sum, err
}
