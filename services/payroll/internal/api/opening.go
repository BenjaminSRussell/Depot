package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	platmoney "github.com/BenjaminSRussell/depot/platform/money"
	"github.com/BenjaminSRussell/depot/platform/openingimport"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/payroll/internal/store"
)

// Opening import of payroll year-to-date balances at cutover (#49): the migration factory sends, per worker and tax year, the gross, tax, deductions and
// net the legacy system had paid, one line per pay element, and reads the stored batches back to reconcile.
const (
	dutyOpeningImport = "payroll.opening-ytd.import"
	dutyOpeningRead   = "payroll.opening-ytd.read"
	pathOpeningYTD    = "/v1/payroll/opening-ytd"

	CodeOpeningInvalid      = "PAY-OPB-001"
	CodeOpeningKeyConflict  = "PAY-OPB-002"
	CodeOpeningControlTotal = "PAY-OPB-003"
	CodeOpeningReference    = "PAY-OPB-004"
)

var (
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	taxYear      = regexp.MustCompile(`^[0-9]{4}$`)
)

// WithOpeningYTD mounts the cutover import of year-to-date balances.
func (a *API) WithOpeningYTD(s store.OpeningImporter) *API {
	a.opening, a.openingCounters = s, &openingimport.Counters{}
	return a
}

func (a *API) registerOpening(r chi.Router) {
	if a.opening == nil {
		return
	}
	r.With(authz.RequireDuty(dutyOpeningImport)).Post(pathOpeningYTD, a.importOpeningYTD)
	r.With(authz.RequireDuty(dutyOpeningRead)).Get(pathOpeningYTD, a.listOpeningYTD)
}

type openingLineRequest struct {
	Reference string `json:"reference"`
	WorkerID  string `json:"workerId"`
	TaxYear   string `json:"taxYear"`
	Element   string `json:"element"`
	Currency  string `json:"currency"`
	Amount    string `json:"amount"`
}

type openingYTDRequest struct {
	LegalEntityID string               `json:"legalEntityId"`
	BatchKey      string               `json:"batchKey"`
	ControlTotals map[string]string    `json:"controlTotals"`
	Lines         []openingLineRequest `json:"lines"`
}

func (a *API) importOpeningYTD(w http.ResponseWriter, r *http.Request) {
	caller, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(caller.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	var req openingYTDRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	problem, fields := openingimport.Envelope(caller, req.LegalEntityID, req.BatchKey, len(req.Lines), req.ControlTotals)
	if problem != nil {
		httpx.Fail(w, r, problem)
		return
	}
	if len(fields) > 0 {
		a.openingCounters.Refuse(w, r, http.StatusBadRequest, "COM-0001", "request failed validation", fields)
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
	batch, created, err := a.opening.ImportOpeningYTD(r.Context(), in)
	var taken *openingimport.TakenError
	switch {
	case errors.Is(err, openingimport.ErrKeyConflict):
		a.openingCounters.Refuse(w, r, http.StatusConflict, CodeOpeningKeyConflict, "batch key "+req.BatchKey+" was already imported with different content", nil)
	case errors.As(err, &taken):
		fields, rows := takenOpening(in.Lines, req.Lines, taken)
		a.openingCounters.Refuse(w, r, http.StatusUnprocessableEntity, CodeOpeningReference, "line references or year-to-date balances are already in use; nothing was stored", fields, rows...)
	case err != nil:
		slog.ErrorContext(r.Context(), "opening ytd import failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "the opening import failed"))
	default:
		if created {
			slog.InfoContext(r.Context(), "opening year-to-date balances imported", "legalEntityId", req.LegalEntityID, "batchKey", req.BatchKey, "lines", batch.LineCount, "userId", caller.UserID, "correlationId", middleware.GetReqID(r.Context()))
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

func openingImport(user string, req openingYTDRequest) (store.OpeningImport, map[string]string) {
	totals, bad := openingimport.ParseTotals(req.ControlTotals)
	in := store.OpeningImport{LegalEntityID: strings.ToLower(req.LegalEntityID), BatchKey: req.BatchKey, ImportedBy: user, Totals: totals, Lines: make([]store.OpeningLine, 0, len(req.Lines))}
	refs := make([]string, len(req.Lines))
	type year struct {
		rows   map[string]int
		amount map[string]decimal.Decimal
	}
	years := map[string]*year{}
	seen := map[string]int{}
	for i, l := range req.Lines {
		refs[i] = l.Reference
		if !ids.Valid(l.WorkerID) {
			bad[openingimport.Row(i, "workerId")] = "must be a UUID"
		}
		if !taxYear.MatchString(l.TaxYear) {
			bad[openingimport.Row(i, "taxYear")] = "must be a four-digit year such as 2026"
		}
		if l.Element != store.ElementGross && l.Element != store.ElementTax && l.Element != store.ElementDeductions && l.Element != store.ElementNet {
			bad[openingimport.Row(i, "element")] = "must be GROSS, TAX, DEDUCTIONS or NET"
		}
		amount, err := decimal.NewFromString(l.Amount)
		switch {
		case err != nil:
			bad[openingimport.Row(i, "amount")] = "must be a decimal"
		case l.Element != store.ElementNet && amount.IsNegative():
			bad[openingimport.Row(i, "amount")] = "must be a decimal of zero or more (only NET can be negative)"
		case !currencyCode.MatchString(l.Currency):
			bad[openingimport.Row(i, "currency")] = "must be a 3-letter ISO 4217 code in capitals"
		case platmoney.MinorUnits(l.Currency) > 2 || !amount.Equal(amount.Round(platmoney.MinorUnits(l.Currency))):
			bad[openingimport.Row(i, "amount")] = "must be in whole minor units of the currency, at most 2 decimal places"
		}
		line := store.OpeningLine{Reference: l.Reference, WorkerID: strings.ToLower(l.WorkerID), TaxYear: l.TaxYear, Element: l.Element, Currency: l.Currency, Amount: amount}
		if first, dup := seen[line.Key()]; dup {
			bad[openingimport.Row(i, "element")] = fmt.Sprintf("the %s of this worker and year appears again; first at line %d", l.Element, first)
		} else {
			seen[line.Key()] = i + 1
		}
		k := line.WorkerID + "/" + l.TaxYear + "/" + l.Currency
		if years[k] == nil {
			years[k] = &year{rows: map[string]int{}, amount: map[string]decimal.Decimal{}}
		}
		years[k].rows[l.Element], years[k].amount[l.Element] = i, amount
		in.Lines = append(in.Lines, line)
	}
	// When a worker's year arrives whole, its net must be gross less tax less deductions.
	for _, y := range years {
		if len(y.rows) != 4 {
			continue
		}
		if want := y.amount[store.ElementGross].Sub(y.amount[store.ElementTax]).Sub(y.amount[store.ElementDeductions]); !y.amount[store.ElementNet].Equal(want) {
			bad[openingimport.Row(y.rows[store.ElementNet], "amount")] = fmt.Sprintf("net must equal gross less tax less deductions, %s", want.String())
		}
	}
	openingimport.DuplicateReferences(refs, bad)
	sorted := append([]store.OpeningLine(nil), in.Lines...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Reference < sorted[j].Reference })
	in.ContentHash = openingimport.Hash(in.LegalEntityID, in.Totals, sorted)
	return in, bad
}

func takenOpening(lines []store.OpeningLine, reqLines []openingLineRequest, taken *openingimport.TakenError) (map[string]string, []httpx.RowError) {
	bad := map[string]string{}
	var rows []httpx.RowError
	for i, l := range lines {
		if key, ok := taken.References[reqLines[i].Reference]; ok {
			msg := "already imported by batch " + key
			bad[openingimport.Row(i, "reference")] = msg
			rows = append(rows, httpx.RowError{Row: i + 1, Field: "reference", Code: httpx.RowReferenceTaken, Detail: msg, TakenByBatch: key})
		}
		if taken.Keys[l.Key()] {
			msg := "the " + l.Element + " of this worker and year is already held"
			bad[openingimport.Row(i, "element")] = msg
			rows = append(rows, httpx.RowError{Row: i + 1, Field: "element", Code: httpx.RowNumberTaken, Detail: msg})
		}
	}
	return bad, rows
}

func (a *API) listOpeningYTD(w http.ResponseWriter, r *http.Request) {
	caller, _ := tenant.FromContext(r.Context())
	q, bad := openingimport.ParseList(r, caller)
	if bad != nil {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		p.Errors = bad
		httpx.Fail(w, r, p)
		return
	}
	batches, err := a.opening.ListOpeningBatches(r.Context(), q.LegalEntityID, q.BatchKey, q.Limit+1, q.AfterAt, q.AfterID)
	if err != nil {
		slog.ErrorContext(r.Context(), "opening batch list failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "the opening batch list failed"))
		return
	}
	openingimport.WriteList(w, q, batches, time.Now())
}
