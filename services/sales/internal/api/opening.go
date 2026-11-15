package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// Opening import of open sales orders at cutover (#49): the migration factory sends the orders still open in the
// legacy system with their remaining quantities, prices and dates, and reads the stored batches back to reconcile.
const (
	dutyOpeningImport = "sales.opening-order.import"
	dutyOpeningRead   = "sales.opening-order.read"
	pathOpeningOrders = "/v1/sales/opening-orders"

	CodeOpeningInvalid      = "SLS-OPB-001"
	CodeOpeningKeyConflict  = "SLS-OPB-002"
	CodeOpeningControlTotal = "SLS-OPB-003"
	CodeOpeningReference    = "SLS-OPB-004"

	maxOpeningLines = 1000
)

var openingKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,100}$`)

// OpeningStore is the persistence of the opening import.
type OpeningStore interface {
	ImportOpeningOrders(ctx context.Context, in store.OpeningImport) (store.OpeningBatch, bool, error)
	ListOpeningBatches(ctx context.Context, legalEntityID, batchKey string, limit int, afterAt time.Time, afterID string) ([]store.OpeningBatch, error)
}

type openingDeps struct {
	store    OpeningStore
	imported atomic.Int64
	replayed atomic.Int64
	refused  atomic.Int64
	lines    atomic.Int64
}

// WithOpeningOrders mounts the cutover import of open sales orders.
func (a *API) WithOpeningOrders(s OpeningStore) *API {
	a.opening = &openingDeps{store: s}
	return a
}

func (a *API) registerOpening(r chi.Router) {
	if a.opening == nil {
		return
	}
	r.With(authz.RequireDuty(dutyOpeningImport)).Post(pathOpeningOrders, a.importOpeningOrders)
	r.With(authz.RequireDuty(dutyOpeningRead)).Get(pathOpeningOrders, a.listOpeningOrders)
}

type openingLineRequest struct {
	Reference    string `json:"reference"`
	OrderNumber  string `json:"orderNumber"`
	LineNo       int    `json:"lineNo"`
	CustomerID   string `json:"customerId"`
	ProductID    string `json:"productId"`
	SiteID       string `json:"siteId"`
	WarehouseID  string `json:"warehouseId"`
	Unit         string `json:"unit"`
	Currency     string `json:"currency"`
	OrderedQty   string `json:"orderedQty"`
	DeliveredQty string `json:"deliveredQty"`
	InvoicedQty  string `json:"invoicedQty,omitempty"`
	UnitPrice    string `json:"unitPrice"`
	DeliveryDate string `json:"deliveryDate"`
}

type openingOrdersRequest struct {
	LegalEntityID string               `json:"legalEntityId"`
	BatchKey      string               `json:"batchKey"`
	ControlTotals map[string]string    `json:"controlTotals"`
	Lines         []openingLineRequest `json:"lines"`
}

func openingRow(i int, name string) string { return "lines[" + strconv.Itoa(i+1) + "]." + name }

func (a *API) importOpeningOrders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	caller, _ := tenant.FromContext(r.Context())
	var req openingOrdersRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	} else if !tenant.InLegalEntityScope(caller, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "COM-0404", "legal entity not found"))
		return
	}
	if !openingKeyPattern.MatchString(req.BatchKey) {
		fields["batchKey"] = "is required: 1 to 100 letters, digits, dots, dashes, colons or underscores"
	}
	if n := len(req.Lines); n == 0 || n > maxOpeningLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines; send larger snapshots in several batches", maxOpeningLines)
	}
	if len(req.ControlTotals) == 0 {
		fields["controlTotals"] = "is required: the open value of the batch per currency"
	}
	if len(fields) > 0 {
		a.refuseOpening(w, r, http.StatusBadRequest, "COM-0001", validationDetail, fields)
		return
	}
	in, rowErrors := openingImport(caller.UserID, req)
	if len(rowErrors) > 0 {
		a.refuseOpening(w, r, http.StatusUnprocessableEntity, CodeOpeningInvalid, "the batch has lines that cannot be imported; nothing was stored", rowErrors)
		return
	}
	if mismatch := openingTotalMismatch(in.ControlTotals, in.Lines); len(mismatch) > 0 {
		a.refuseOpening(w, r, http.StatusUnprocessableEntity, CodeOpeningControlTotal, "the lines do not add up to the declared control totals; nothing was stored", mismatch)
		return
	}
	batch, created, err := a.opening.store.ImportOpeningOrders(r.Context(), in)
	var taken *store.OpeningTakenError
	switch {
	case errors.Is(err, store.ErrOpeningKeyConflict):
		a.refuseOpening(w, r, http.StatusConflict, CodeOpeningKeyConflict, "batch key "+req.BatchKey+" was already imported with different content", nil)
	case errors.As(err, &taken):
		fields, rowErrs := takenOpening(req.Lines, taken)
		a.refuseOpening(w, r, http.StatusUnprocessableEntity, CodeOpeningReference, "line references or order numbers are already in use; nothing was stored", fields, rowErrs...)
	case err != nil:
		a.fail(w, r, "opening order import", err)
	case created:
		a.opening.imported.Add(1)
		a.opening.lines.Add(int64(batch.LineCount))
		slog.Info("opening orders imported", "legalEntityId", req.LegalEntityID, "batchKey", req.BatchKey, "lines", batch.LineCount, "userId", caller.UserID, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusCreated, batch)
	default:
		a.opening.replayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, batch)
	}
}

func (a *API) refuseOpening(w http.ResponseWriter, r *http.Request, status int, code, detail string, fields map[string]string, rows ...httpx.RowError) {
	a.opening.refused.Add(1)
	p := httpx.NewProblem(status, code, detail)
	p.Errors = fields
	p.RowErrors = rows
	httpx.Fail(w, r, p)
}

func openingImport(user string, req openingOrdersRequest) (store.OpeningImport, map[string]string) {
	in := store.OpeningImport{LegalEntityID: req.LegalEntityID, BatchKey: req.BatchKey, ImportedBy: user, ControlTotals: map[string]string{}, Lines: make([]store.OpeningLine, 0, len(req.Lines))}
	bad := map[string]string{}
	for cur, raw := range req.ControlTotals {
		v, err := decimal.NewFromString(raw)
		if !currencyCode.MatchString(cur) || err != nil || v.IsNegative() {
			bad["controlTotals."+cur] = "must be a decimal open value of zero or more for a 3-letter currency"
			continue
		}
		in.ControlTotals[cur] = v.String()
	}
	seenRef := map[string]int{}
	seenLine := map[string]int{}
	type head struct{ customer, currency string }
	heads := map[string]head{}
	for i, l := range req.Lines {
		if l.Reference == "" || len(l.Reference) > 100 {
			bad[openingRow(i, "reference")] = "is required, at most 100 characters"
		} else if first, dup := seenRef[l.Reference]; dup {
			bad[openingRow(i, "reference")] = fmt.Sprintf("appears again; first at line %d", first)
		} else {
			seenRef[l.Reference] = i + 1
		}
		if l.OrderNumber == "" || len(l.OrderNumber) > 40 {
			bad[openingRow(i, "orderNumber")] = "is required, at most 40 characters"
		}
		if l.LineNo < 1 {
			bad[openingRow(i, "lineNo")] = "must be 1 or more"
		}
		key := l.OrderNumber + "\x1f" + strconv.Itoa(l.LineNo)
		if first, dup := seenLine[key]; dup {
			bad[openingRow(i, "lineNo")] = fmt.Sprintf("order %s line %d appears again; first at line %d", l.OrderNumber, l.LineNo, first)
		} else {
			seenLine[key] = i + 1
		}
		for name, v := range map[string]string{"customerId": l.CustomerID, "productId": l.ProductID, "siteId": l.SiteID, "warehouseId": l.WarehouseID} {
			if !ids.Valid(v) {
				bad[openingRow(i, name)] = "must be a UUID"
			}
		}
		if l.Unit == "" || len(l.Unit) > 20 {
			bad[openingRow(i, "unit")] = "is required, at most 20 characters"
		}
		if !currencyCode.MatchString(l.Currency) {
			bad[openingRow(i, "currency")] = "must be a 3-letter ISO 4217 code in capitals"
		}
		if h, ok := heads[l.OrderNumber]; !ok {
			heads[l.OrderNumber] = head{customer: strings.ToLower(l.CustomerID), currency: l.Currency}
		} else if h.customer != strings.ToLower(l.CustomerID) || h.currency != l.Currency {
			bad[openingRow(i, "orderNumber")] = "the lines of one order must share one customer and one currency"
		}
		ordered, e1 := decimal.NewFromString(l.OrderedQty)
		delivered, e2 := decimal.NewFromString(l.DeliveredQty)
		price, e3 := decimal.NewFromString(l.UnitPrice)
		invoiced := decimal.Zero
		if l.InvoicedQty != "" {
			var e4 error
			if invoiced, e4 = decimal.NewFromString(l.InvoicedQty); e4 != nil || invoiced.IsNegative() || invoiced.GreaterThan(delivered) {
				bad[openingRow(i, "invoicedQty")] = "must be a decimal of zero or more and not above the delivered quantity"
			}
		}
		switch {
		case e1 != nil || !ordered.IsPositive():
			bad[openingRow(i, "orderedQty")] = "must be a positive decimal"
		case e2 != nil || delivered.IsNegative():
			bad[openingRow(i, "deliveredQty")] = "must be a decimal of zero or more"
		case !delivered.LessThan(ordered):
			bad[openingRow(i, "deliveredQty")] = "must be below the ordered quantity: a fully delivered line is not open"
		}
		if e3 != nil || price.IsNegative() {
			bad[openingRow(i, "unitPrice")] = "must be a decimal of zero or more"
		}
		if _, err := time.Parse(dayLayout, l.DeliveryDate); err != nil {
			bad[openingRow(i, "deliveryDate")] = "must be a date such as 2027-03-04"
		}
		in.Lines = append(in.Lines, store.OpeningLine{Reference: l.Reference, OrderNumber: l.OrderNumber, LineNo: l.LineNo,
			CustomerID: strings.ToLower(l.CustomerID), ProductID: strings.ToLower(l.ProductID), SiteID: strings.ToLower(l.SiteID), WarehouseID: strings.ToLower(l.WarehouseID),
			Unit: l.Unit, Currency: l.Currency, OrderedQty: ordered, DeliveredQty: delivered, InvoicedQty: invoiced, UnitPrice: price, DeliveryDate: l.DeliveryDate})
	}
	in.ContentHash = openingHash(in)
	return in, bad
}

func openingHash(in store.OpeningImport) string {
	lines := append([]store.OpeningLine(nil), in.Lines...)
	sort.Slice(lines, func(i, j int) bool { return lines[i].Reference < lines[j].Reference })
	raw, _ := json.Marshal(struct {
		LegalEntityID string
		Totals        map[string]string
		Lines         []store.OpeningLine
	}{in.LegalEntityID, in.ControlTotals, lines})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func openingTotalMismatch(declared map[string]string, lines []store.OpeningLine) map[string]string {
	actual := map[string]decimal.Decimal{}
	for _, l := range lines {
		actual[l.Currency] = actual[l.Currency].Add(l.OpenValue())
	}
	bad := map[string]string{}
	for cur, raw := range declared {
		want, _ := decimal.NewFromString(raw)
		if !actual[cur].Equal(want) {
			bad["controlTotals."+cur] = fmt.Sprintf("declared %s but the lines add up to %s", want.String(), actual[cur].String())
		}
	}
	for cur, got := range actual {
		if _, ok := declared[cur]; !ok {
			bad["controlTotals."+cur] = "no total declared; the lines add up to " + got.String()
		}
	}
	return bad
}

func takenOpening(lines []openingLineRequest, taken *store.OpeningTakenError) (map[string]string, []httpx.RowError) {
	bad := map[string]string{}
	var rows []httpx.RowError
	for i, l := range lines {
		if key, ok := taken.References[l.Reference]; ok {
			msg := "already imported by batch " + key
			bad[openingRow(i, "reference")] = msg
			rows = append(rows, httpx.RowError{Row: i + 1, Field: "reference", Code: httpx.RowReferenceTaken, Detail: msg, TakenByBatch: key})
		}
		if taken.OrderNumbers[l.OrderNumber] {
			msg := "order number " + l.OrderNumber + " is already used by an order"
			bad[openingRow(i, "orderNumber")] = msg
			rows = append(rows, httpx.RowError{Row: i + 1, Field: "orderNumber", Code: httpx.RowOrderNumberTaken, Detail: msg})
		}
	}
	return bad, rows
}

func (a *API) listOpeningOrders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	caller, _ := tenant.FromContext(r.Context())
	le := r.URL.Query().Get("legal_entity")
	if !ids.Valid(le) || !tenant.InLegalEntityScope(caller, le) {
		validationFailed(w, r, map[string]string{"legal_entity": "must be a legal entity UUID in your scope"})
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			validationFailed(w, r, map[string]string{"limit": "must be a whole number from 1 to 200"})
			return
		}
		limit = n
	}
	afterAt, afterID, ok := parseOpeningCursor(r.URL.Query().Get("cursor"))
	if !ok {
		validationFailed(w, r, map[string]string{"cursor": "is not a cursor this list returned"})
		return
	}
	batches, err := a.opening.store.ListOpeningBatches(r.Context(), le, r.URL.Query().Get("batch_key"), limit+1, afterAt, afterID)
	if err != nil {
		a.fail(w, r, "opening batch list", err)
		return
	}
	next := ""
	if len(batches) > limit {
		batches = batches[:limit]
		last := batches[limit-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(last.ImportedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": batches, "nextCursor": next, "asOf": a.now().UTC().Format(time.RFC3339)})
}

func parseOpeningCursor(raw string) (time.Time, string, bool) {
	if raw == "" {
		return time.Time{}, "", true
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	at, id, found := strings.Cut(string(decoded), "|")
	when, perr := time.Parse(time.RFC3339Nano, at)
	if err != nil || !found || perr != nil || !ids.Valid(id) {
		return time.Time{}, "", false
	}
	return when, id, true
}

func (a *API) writeOpeningMetrics(w http.ResponseWriter) {
	if a.opening == nil {
		return
	}
	fmt.Fprintf(w, "# HELP sales_opening_batches_total Opening order batches by outcome.\n# TYPE sales_opening_batches_total counter\nsales_opening_batches_total{outcome=\"imported\"} %d\nsales_opening_batches_total{outcome=\"replayed\"} %d\nsales_opening_batches_total{outcome=\"refused\"} %d\n", a.opening.imported.Load(), a.opening.replayed.Load(), a.opening.refused.Load())
	fmt.Fprintf(w, "# HELP sales_opening_lines_total Opening order lines stored by imported batches.\n# TYPE sales_opening_lines_total counter\nsales_opening_lines_total %d\n", a.opening.lines.Load())
}
