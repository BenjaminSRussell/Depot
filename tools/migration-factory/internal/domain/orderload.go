package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Target load of the open_orders pack (#49): the sales service takes the SALES lines as opening orders
// (POST /v1/sales/opening-orders) and purchasing the PURCHASE lines (POST /v1/procurement/opening-orders). Each takes a batch of
// at most 1000 lines all or nothing under a key, and lists its stored batches back, which is what the factory reconciles against.

// Targets of the open_orders load.
const (
	TargetSales      = "sales"
	TargetPurchasing = "purchasing"
)

// MaxOrderBatchLines is the most lines one opening-order batch takes, in both targets.
const MaxOrderBatchLines = 1000

// OrderTargetOf returns the target a line of the order type loads into.
func OrderTargetOf(orderType string) string {
	if orderType == OrderPurchase {
		return TargetPurchasing
	}
	return TargetSales
}

// Reject codes the factory raises for an order line it cannot build an opening line from.
const (
	CodeOrderLineNumber = "MIG-LOAD-ORDER-LINE"
	CodeOrderCurrency   = "MIG-LOAD-ORDER-CURRENCY"
)

// OrderMaps are what a run fixes for building opening order lines: the map from a legacy item to the Passage product id, from a
// legacy customer or vendor to its Passage id (an id that is already a UUID needs no entry), and the site and warehouse the lines
// are delivered from or to (the open_orders extract carries neither).
type OrderMaps struct {
	Products    map[string]string
	Customers   map[string]string
	Vendors     map[string]string
	SiteID      string
	WarehouseID string
}

// OrderItem is one line of an opening-order batch. PartyID is the customer of a sales line or the vendor of a purchase line, and
// DoneQty what was delivered (sales) or received (purchase) before the cutover.
type OrderItem struct {
	Reference    string
	OrderNumber  string
	LineNo       int
	PartyID      string
	ProductID    string
	SiteID       string
	WarehouseID  string
	Unit         string
	Currency     string
	OrderedQty   decimal.Decimal
	DoneQty      decimal.Decimal
	UnitPrice    decimal.Decimal
	DeliveryDate string
}

// OpenQty is the quantity still to deliver or receive.
func (it OrderItem) OpenQty() decimal.Decimal { return it.OrderedQty.Sub(it.DoneQty) }

// OpenValue is the open quantity at the unit price, to six decimals, as the targets store it.
func (it OrderItem) OpenValue() decimal.Decimal { return it.OpenQty().Mul(it.UnitPrice).Round(6) }

// Hash identifies the item as a target would store it, with the target it goes to.
func (it OrderItem) Hash(target string) string {
	return hashParts(target, it.Reference, it.OrderNumber, strconv.Itoa(it.LineNo), strings.ToLower(it.PartyID), strings.ToLower(it.ProductID),
		strings.ToLower(it.SiteID), strings.ToLower(it.WarehouseID), it.Unit, it.Currency, it.OrderedQty.String(), it.DoneQty.String(), it.UnitPrice.String(), it.DeliveryDate)
}

// BuildOrderItem maps a staged line onto a batch item; a line that cannot be mapped comes back with the reject code and reason.
func BuildOrderItem(o OpenOrderLine, m OrderMaps) (OrderItem, string, string) {
	pick := func(maps map[string]string, key string) string {
		id := strings.TrimSpace(maps[key])
		if id == "" && uuidPattern.MatchString(key) {
			id = key
		}
		if !uuidPattern.MatchString(id) {
			return ""
		}
		return strings.ToLower(id)
	}
	product := pick(m.Products, o.Item)
	if product == "" {
		return OrderItem{}, CodeRefUnresolved, fmt.Sprintf("item %q has no Passage product id: name it in products or load the item master through the party migration", o.Item)
	}
	partyMap, label := m.Customers, "customer"
	if o.OrderType == OrderPurchase {
		partyMap, label = m.Vendors, "vendor"
	}
	party := pick(partyMap, o.Party)
	if party == "" {
		return OrderItem{}, CodeRefUnresolved, fmt.Sprintf("%s %q has no Passage %s id: name it in %ss or load it through the party migration", label, o.Party, label, label)
	}
	lineNo, err := strconv.Atoi(strings.TrimSpace(o.LineNumber))
	if err != nil || lineNo < 1 {
		return OrderItem{}, CodeOrderLineNumber, fmt.Sprintf("line number %q is not a whole number of 1 or more; the targets number order lines with integers", o.LineNumber)
	}
	date := o.DeliveryDate
	if date == "" {
		return OrderItem{}, CodeFieldRequired, "the line has no delivery date; the targets need the date of the open quantity"
	}
	return OrderItem{Reference: o.LegacyID, OrderNumber: o.OrderNumber, LineNo: lineNo, PartyID: party, ProductID: product, SiteID: strings.ToLower(m.SiteID),
		WarehouseID: strings.ToLower(m.WarehouseID), Unit: o.Unit, Currency: o.Currency, OrderedQty: o.OrderedQty, DoneQty: o.DeliveredQty, UnitPrice: o.UnitPrice, DeliveryDate: date}, "", ""
}

// OrderKeyPrefix starts every batch key the factory writes to a target for the open orders of a legacy company.
func OrderKeyPrefix(target, system, company string) string {
	sum := sha256.Sum256([]byte(company))
	return "mf-orders-" + target + "-" + system + "-" + hex.EncodeToString(sum[:])[:12] + "-"
}

// OrderBatchKey is the idempotency key of one batch: stable for the same items, so a retry is the same request to the target.
func OrderBatchKey(target, system, company string, hashes []string) string {
	sum := sha256.Sum256([]byte(strings.Join(hashes, ",")))
	return OrderKeyPrefix(target, system, company) + hex.EncodeToString(sum[:])[:OpeningKeyHash]
}

// IsOrderBatchKey reports whether a batch key was written by the factory for the open orders of the company at the target.
func IsOrderBatchKey(target, system, company, key string) bool {
	prefix := OrderKeyPrefix(target, system, company)
	return strings.HasPrefix(key, prefix) && len(key) == len(prefix)+OpeningKeyHash
}

// OrderLoad is the load of one open_orders run into the sales and purchasing targets.
type OrderLoad struct {
	RunID         string
	LegalEntityID string
	Status        string
	Staged        int
	Loaded        int
	Rejected      int
	Pending       int
	Attempts      int
	LastError     string
	StartedBy     string
	StartedAt     *time.Time
	FinishedAt    *time.Time
	Version       int
}

// OrderTotals is what a target holds for the open orders of a company: lines and open quantity, and per currency the open value.
type OrderTotals struct {
	Lines int
	Qty   decimal.Decimal
	Value map[string]decimal.Decimal
}

// NewOrderTotals returns empty totals.
func NewOrderTotals() OrderTotals { return OrderTotals{Value: map[string]decimal.Decimal{}} }

// ReconcileOrders compares what the factory loaded into a target (expected) with what the target reports for the company's
// batches (actual): the line count and open quantity, and per currency the open value, exactly. orderType is SALES or PURCHASE.
func ReconcileOrders(legalEntityID, orderType string, expected, actual OrderTotals) ([]ControlTotal, error) {
	key := map[string]string{"legalEntityId": legalEntityID, "orderType": orderType}
	lines, err := NewControlTotal("target.open_orders.line_count", key, decimal.NewFromInt(int64(expected.Lines)), decimal.NewFromInt(int64(actual.Lines)), Tolerance{})
	if err != nil {
		return nil, err
	}
	qty, err := NewControlTotal("target.open_orders.open_quantity", key, expected.Qty, actual.Qty, Tolerance{})
	if err != nil {
		return nil, err
	}
	out := []ControlTotal{lines, qty}
	currencies := map[string]struct{}{}
	for c := range expected.Value {
		currencies[c] = struct{}{}
	}
	for c := range actual.Value {
		currencies[c] = struct{}{}
	}
	sorted := make([]string, 0, len(currencies))
	for c := range currencies {
		sorted = append(sorted, c)
	}
	sort.Strings(sorted)
	for _, c := range sorted {
		v, err := NewControlTotal("target.open_orders.open_value", map[string]string{"legalEntityId": legalEntityID, "orderType": orderType, "currency": c}, expected.Value[c], actual.Value[c], Tolerance{})
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
