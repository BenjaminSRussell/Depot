package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/money"
	"github.com/BenjaminSRussell/depot/platform/openingimport"
	"github.com/BenjaminSRussell/depot/services/production/internal/store"
)

// Opening import of open production orders and their work in progress at cutover (#49): the migration factory sends the orders still open in the legacy
// system with their open quantity and the cost issued to them, and reads the stored batches back to reconcile.
const (
	dutyOpeningImport = "production.opening-wip.import"
	dutyOpeningRead   = "production.opening-wip.read"
	pathOpeningWIP    = "/v1/production/opening-wip"

	dutyOpeningJournal = "production.opening-journal.post"
	pathOpeningJournal = "/v1/production/opening-journal"

	CodeOpeningInvalid      = "PRD-OPB-001"
	CodeOpeningKeyConflict  = "PRD-OPB-002"
	CodeOpeningControlTotal = "PRD-OPB-003"
	CodeOpeningReference    = "PRD-OPB-004"
	CodeOpeningProfile      = "PRD-OPB-005"
)

const dayLayout = "2006-01-02"

// WithOpeningWIP mounts the cutover import of open production orders.
func (a *API) WithOpeningWIP(s store.OpeningImporter) *API {
	a.opening, a.openingCounters = s, &openingimport.Counters{}
	return a
}

func (a *API) registerOpening(r chi.Router) {
	if a.opening == nil {
		return
	}
	r.With(authz.RequireDuty(dutyOpeningImport)).Post(pathOpeningWIP, a.importOpeningWIP)
	r.With(authz.RequireDuty(dutyOpeningJournal)).Post(pathOpeningJournal, a.postOpeningJournal)
	r.With(authz.RequireDuty(dutyOpeningRead)).Get(pathOpeningWIP, a.listOpeningWIP)
}

type openingLineRequest struct {
	Reference    string `json:"reference"`
	OrderNumber  string `json:"orderNumber"`
	ProductID    string `json:"productId"`
	SiteID       string `json:"siteId"`
	WarehouseID  string `json:"warehouseId"`
	Currency     string `json:"currency"`
	QuantityOpen string `json:"quantityOpen"`
	DeliveryDate string `json:"deliveryDate,omitempty"`
	Material     string `json:"material"`
	Labor        string `json:"labor"`
	Overhead     string `json:"overhead"`
	WIPValue     string `json:"wipValue"`
}

type openingWIPRequest struct {
	LegalEntityID string               `json:"legalEntityId"`
	BatchKey      string               `json:"batchKey"`
	OpeningDate   string               `json:"openingDate"`
	Journal       string               `json:"journal"`
	ControlTotals map[string]string    `json:"controlTotals"`
	Lines         []openingLineRequest `json:"lines"`
}

func (a *API) importOpeningWIP(w http.ResponseWriter, r *http.Request) {
	caller, ok := principal(w, r)
	if !ok {
		return
	}
	var req openingWIPRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	problem, fields := openingimport.Envelope(caller, req.LegalEntityID, req.BatchKey, len(req.Lines), req.ControlTotals)
	if problem != nil {
		httpx.Fail(w, r, problem)
		return
	}
	if _, err := time.Parse(dayLayout, req.OpeningDate); err != nil {
		fields["openingDate"] = "is required: the cutover date the opening WIP journals are dated, such as 2027-01-01"
	}
	if req.Journal != store.OpeningJournalPost && req.Journal != store.OpeningJournalLinked {
		fields["journal"] = "must be post (the opening WIP journal is booked through POST /v1/production/opening-journal) or linked (the ledger already holds the WIP balances)"
	}
	if len(fields) > 0 {
		a.openingCounters.Refuse(w, r, http.StatusBadRequest, validationCode, validationText, fields)
		return
	}
	in, bad := openingImport(caller.UserID, req)
	if len(bad) > 0 {
		a.openingCounters.Refuse(w, r, http.StatusUnprocessableEntity, CodeOpeningInvalid, "the batch has lines that cannot be imported; nothing was stored", bad, rowErrors(bad)...)
		return
	}
	lines := make([]openingimport.Line, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = openingimport.Line{Reference: l.Reference, Measures: l.Measures()}
	}
	if mismatch := openingimport.Mismatch(in.Totals, openingimport.SumTotals(lines)); len(mismatch) > 0 {
		a.openingCounters.Refuse(w, r, http.StatusUnprocessableEntity, CodeOpeningControlTotal, "the lines do not add up to the declared control totals; nothing was stored", mismatch)
		return
	}
	batch, created, err := a.opening.ImportOpeningWIP(r.Context(), in)
	var taken *openingimport.TakenError
	var noProfile *store.OpeningProfileError
	switch {
	case errors.Is(err, openingimport.ErrKeyConflict):
		a.openingCounters.Refuse(w, r, http.StatusConflict, CodeOpeningKeyConflict, "batch key "+req.BatchKey+" was already imported with different content", nil)
	case errors.As(err, &taken):
		fields, rows := takenOpening(req.Lines, taken)
		a.openingCounters.Refuse(w, r, http.StatusUnprocessableEntity, CodeOpeningReference, "line references or order numbers are already in use; nothing was stored", fields, rows...)
	case errors.As(err, &noProfile):
		a.openingCounters.Refuse(w, r, http.StatusUnprocessableEntity, CodeOpeningProfile, noProfile.Detail, nil)
	case err != nil:
		a.fail(w, r, "opening WIP import", err)
	default:
		if created {
			slog.InfoContext(r.Context(), "opening WIP imported", "legalEntityId", req.LegalEntityID, "batchKey", req.BatchKey, "lines", batch.LineCount, "userId", caller.UserID, "correlationId", middleware.GetReqID(r.Context()))
		}
		a.openingCounters.Answer(w, batch, created)
	}
}

// rowErrors turns the lines[n].field messages into structured row errors, in row order.
func rowErrors(bad map[string]string) []httpx.RowError {
	var out []httpx.RowError
	for _, k := range openingimport.SortedKeys(bad) {
		var row int
		if _, err := fmt.Sscanf(k, "lines[%d].", &row); err != nil {
			continue
		}
		out = append(out, httpx.RowError{Row: row, Field: k[strings.Index(k, "].")+2:], Code: httpx.RowInvalid, Detail: bad[k]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Row < out[j].Row })
	return out
}

func openingImport(user string, req openingWIPRequest) (store.OpeningImport, map[string]string) {
	totals, bad := openingimport.ParseTotals(req.ControlTotals)
	in := store.OpeningImport{LegalEntityID: strings.ToLower(req.LegalEntityID), BatchKey: req.BatchKey, ImportedBy: user, OpeningDate: req.OpeningDate, Journal: req.Journal, Totals: totals,
		Lines: make([]store.OpeningLine, 0, len(req.Lines))}
	refs := make([]string, len(req.Lines))
	numbers := map[string]int{}
	for i, l := range req.Lines {
		refs[i] = l.Reference
		if l.OrderNumber == "" || len(l.OrderNumber) > 40 {
			bad[openingimport.Row(i, "orderNumber")] = "is required, at most 40 characters"
		} else if first, dup := numbers[l.OrderNumber]; dup {
			bad[openingimport.Row(i, "orderNumber")] = fmt.Sprintf("appears again; first at line %d", first)
		} else {
			numbers[l.OrderNumber] = i + 1
		}
		for name, v := range map[string]string{"productId": l.ProductID, "siteId": l.SiteID, "warehouseId": l.WarehouseID} {
			if !ids.Valid(v) {
				bad[openingimport.Row(i, name)] = "must be a UUID"
			}
		}
		if !currencyCode.MatchString(l.Currency) {
			bad[openingimport.Row(i, "currency")] = "must be a 3-letter ISO 4217 code in capitals"
		}
		qty, err := decimal.NewFromString(l.QuantityOpen)
		if err != nil || !qty.IsPositive() || !qty.Equal(qty.Round(6)) {
			bad[openingimport.Row(i, "quantityOpen")] = "must be a positive decimal with at most 6 decimal places"
		}
		if l.DeliveryDate != "" {
			if _, err := time.Parse(dayLayout, l.DeliveryDate); err != nil {
				bad[openingimport.Row(i, "deliveryDate")] = "must be a date such as 2027-03-04"
			}
		}
		amount := func(field, raw string) decimal.Decimal {
			d, err := decimal.NewFromString(raw)
			switch {
			case err != nil || d.IsNegative():
				bad[openingimport.Row(i, field)] = "must be a decimal of zero or more"
				return decimal.Zero
			case currencyCode.MatchString(l.Currency) && !d.Equal(d.Round(money.MinorUnits(l.Currency))):
				bad[openingimport.Row(i, field)] = fmt.Sprintf("has more decimals than %s allows", l.Currency)
			}
			return d
		}
		material, labor, overhead, wip := amount("material", l.Material), amount("labor", l.Labor), amount("overhead", l.Overhead), amount("wipValue", l.WIPValue)
		if !wip.Equal(material.Add(labor).Add(overhead)) {
			bad[openingimport.Row(i, "wipValue")] = fmt.Sprintf("must equal material plus labor plus overhead, %s", material.Add(labor).Add(overhead).String())
		}
		in.Lines = append(in.Lines, store.OpeningLine{Reference: l.Reference, OrderNumber: l.OrderNumber, ProductID: strings.ToLower(l.ProductID), SiteID: strings.ToLower(l.SiteID),
			WarehouseID: strings.ToLower(l.WarehouseID), Currency: l.Currency, QuantityOpen: qty, DeliveryDate: l.DeliveryDate, Material: material, Labor: labor, Overhead: overhead})
	}
	openingimport.DuplicateReferences(refs, bad)
	sorted := append([]store.OpeningLine(nil), in.Lines...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Reference < sorted[j].Reference })
	in.ContentHash = openingimport.Hash(in.LegalEntityID, in.Totals, struct {
		Date, Journal string
		Lines         []store.OpeningLine
	}{in.OpeningDate, in.Journal, sorted})
	return in, bad
}

func takenOpening(lines []openingLineRequest, taken *openingimport.TakenError) (map[string]string, []httpx.RowError) {
	bad := map[string]string{}
	var rows []httpx.RowError
	for i, l := range lines {
		if key, ok := taken.References[l.Reference]; ok {
			msg := "already imported by batch " + key
			bad[openingimport.Row(i, "reference")] = msg
			rows = append(rows, httpx.RowError{Row: i + 1, Field: "reference", Code: httpx.RowReferenceTaken, Detail: msg, TakenByBatch: key})
		}
		if taken.Keys[l.OrderNumber] {
			msg := "order number " + l.OrderNumber + " is already used by a production order"
			bad[openingimport.Row(i, "orderNumber")] = msg
			rows = append(rows, httpx.RowError{Row: i + 1, Field: "orderNumber", Code: httpx.RowNumberTaken, Detail: msg})
		}
	}
	return bad, rows
}

func (a *API) listOpeningWIP(w http.ResponseWriter, r *http.Request) {
	caller, ok := principal(w, r)
	if !ok {
		return
	}
	q, bad := openingimport.ParseList(r, caller)
	if bad != nil {
		p := httpx.NewProblem(http.StatusBadRequest, validationCode, validationText)
		p.Errors = bad
		httpx.Fail(w, r, p)
		return
	}
	batches, err := a.opening.ListOpeningBatches(r.Context(), q.LegalEntityID, q.BatchKey, q.Limit+1, q.AfterAt, q.AfterID)
	if err != nil {
		a.fail(w, r, "opening batch list", err)
		return
	}
	openingimport.WriteList(w, q, batches, time.Now())
}

// postOpeningJournal books the WIP rows of the opening-wip journals, and nothing else, in the general ledger.
func (a *API) postOpeningJournal(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok || !a.glReady(w, r) {
		return
	}
	var req glPostRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	entity, ok := a.glEntity(w, r, p, req.LegalEntityID, req.LedgerID, fields)
	if !ok {
		return
	}
	res, err := a.gl.PostToGL(r.Context(), a.ledger, store.GLPostRequest{LegalEntityID: entity, LedgerID: req.LedgerID, OpeningOnly: true})
	if err != nil {
		a.fail(w, r, "book the opening WIP in the general ledger", err)
		return
	}
	a.glBooked.Add(int64(len(res.Booked)))
	httpx.JSON(w, http.StatusOK, glPostView(res))
}
