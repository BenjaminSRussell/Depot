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

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/platform/openingimport"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/assets/internal/store"
)

// Opening import of the asset register at cutover (#49): the migration factory sends the assets with their books, cost, accumulated
// depreciation and remaining life as of the cutover, and reads the stored batches back to reconcile.
const (
	dutyOpeningImport = "assets.opening-asset.import"
	dutyOpeningRead   = "assets.opening-asset.read"
	pathOpeningAssets = "/v1/assets/opening-assets"

	CodeOpeningInvalid      = "AST-OPB-001"
	CodeOpeningKeyConflict  = "AST-OPB-002"
	CodeOpeningControlTotal = "AST-OPB-003"
	CodeOpeningReference    = "AST-OPB-004"
)

var dayLayout = "2006-01-02"

var periodPattern = regexp.MustCompile(`^[0-9]{4}(-(0[1-9]|1[0-2]))?$`)

// WithOpeningAssets mounts the cutover import of the asset register.
func (a *API) WithOpeningAssets(s store.OpeningImporter) *API {
	a.opening, a.openingCounters = s, &openingimport.Counters{}
	return a
}

type openingLineRequest struct {
	Reference               string `json:"reference"`
	AssetNumber             string `json:"assetNumber"`
	Name                    string `json:"name"`
	ClassCode               string `json:"classCode"`
	BookCode                string `json:"bookCode"`
	Currency                string `json:"currency"`
	Method                  string `json:"method"`
	AcquisitionDate         string `json:"acquisitionDate"`
	UsefulLifePeriods       int    `json:"usefulLifePeriods"`
	RemainingLifePeriods    int    `json:"remainingLifePeriods"`
	AcquisitionCost         string `json:"acquisitionCost"`
	AccumulatedDepreciation string `json:"accumulatedDepreciation"`
	NetBookValue            string `json:"netBookValue"`
	SalvageValue            string `json:"salvageValue"`
	DepreciatedThrough      string `json:"depreciatedThrough"`
}

type openingAssetsRequest struct {
	LegalEntityID string               `json:"legalEntityId"`
	BatchKey      string               `json:"batchKey"`
	OpeningDate   string               `json:"openingDate"`
	Journal       string               `json:"journal"`
	ControlTotals map[string]string    `json:"controlTotals"`
	Lines         []openingLineRequest `json:"lines"`
}

func (a *API) importOpeningAssets(w http.ResponseWriter, r *http.Request) {
	caller, _ := tenant.FromContext(r.Context())
	var req openingAssetsRequest
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
		fields["openingDate"] = "is required: the cutover date, such as 2027-01-01"
	}
	if req.Journal != store.OpeningJournalPost && req.Journal != store.OpeningJournalLinked {
		fields["journal"] = "must be post (the opening journal is booked through the general ledger) or linked (the ledger already holds the balances)"
	}
	if len(fields) > 0 {
		a.openingCounters.Refuse(w, r, http.StatusBadRequest, validationCode, validationDetail, fields)
		return
	}
	in, bad := openingAssetImport(caller.UserID, req)
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
	batch, created, err := a.opening.ImportOpeningAssets(r.Context(), in)
	var taken *openingimport.TakenError
	switch {
	case errors.Is(err, openingimport.ErrKeyConflict):
		a.openingCounters.Refuse(w, r, http.StatusConflict, CodeOpeningKeyConflict, "batch key "+req.BatchKey+" was already imported with different content", nil)
	case errors.As(err, &taken):
		fields, rows := takenOpening(req.Lines, taken)
		a.openingCounters.Refuse(w, r, http.StatusUnprocessableEntity, CodeOpeningReference, "line references, asset numbers or books are already in use; nothing was stored", fields, rows...)
	case err != nil:
		slog.ErrorContext(r.Context(), "opening asset import failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "the opening import failed"))
	default:
		if created {
			slog.InfoContext(r.Context(), "opening assets imported", "legalEntityId", req.LegalEntityID, "batchKey", req.BatchKey, "lines", batch.LineCount, "userId", caller.UserID, "correlationId", middleware.GetReqID(r.Context()))
		}
		a.openingCounters.Answer(w, batch, created)
	}
}

// rowErrors turns the lines[n].field messages into structured row errors, in row order.
func rowErrors(bad map[string]string) []httpx.RowError {
	var out []httpx.RowError
	for _, k := range openingimport.SortedKeys(bad) {
		var row int
		var field string
		if _, err := fmt.Sscanf(k, "lines[%d].", &row); err != nil {
			continue
		}
		field = k[strings.Index(k, "].")+2:]
		out = append(out, httpx.RowError{Row: row, Field: field, Code: httpx.RowInvalid, Detail: bad[k]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Row < out[j].Row })
	return out
}

func openingAssetImport(user string, req openingAssetsRequest) (store.OpeningImport, map[string]string) {
	totals, bad := openingimport.ParseTotals(req.ControlTotals)
	in := store.OpeningImport{LegalEntityID: strings.ToLower(req.LegalEntityID), BatchKey: req.BatchKey, ImportedBy: user, OpeningDate: req.OpeningDate, Journal: req.Journal,
		Totals: totals, Lines: make([]store.OpeningLine, 0, len(req.Lines))}
	refs := make([]string, len(req.Lines))
	type head struct{ name, class string }
	heads := map[string]head{}
	books := map[string]int{}
	for i, l := range req.Lines {
		refs[i] = l.Reference
		if l.AssetNumber == "" || len(l.AssetNumber) > 40 {
			bad[openingimport.Row(i, "assetNumber")] = "is required, at most 40 characters"
		}
		if l.Name == "" || len(l.Name) > 120 {
			bad[openingimport.Row(i, "name")] = "is required, at most 120 characters"
		}
		if !classCode.MatchString(l.ClassCode) {
			bad[openingimport.Row(i, "classCode")] = "must be upper-case letters, digits, hyphen or underscore"
		}
		if !bookCode.MatchString(l.BookCode) {
			bad[openingimport.Row(i, "bookCode")] = "must be upper-case letters, digits, hyphen or underscore"
		}
		if h, ok := heads[l.AssetNumber]; !ok {
			heads[l.AssetNumber] = head{l.Name, l.ClassCode}
		} else if h.name != l.Name || h.class != l.ClassCode {
			bad[openingimport.Row(i, "assetNumber")] = "the books of one asset must share its name and class"
		}
		key := l.AssetNumber + "\x1f" + l.BookCode
		if first, dup := books[key]; dup {
			bad[openingimport.Row(i, "bookCode")] = fmt.Sprintf("asset %s book %s appears again; first at line %d", l.AssetNumber, l.BookCode, first)
		} else {
			books[key] = i + 1
		}
		if !currency.MatchString(l.Currency) {
			bad[openingimport.Row(i, "currency")] = "must be a 3-letter ISO 4217 code in capitals"
		}
		if l.Method != "StraightLine" && l.Method != "DecliningBalance" {
			bad[openingimport.Row(i, "method")] = "must be StraightLine or DecliningBalance"
		}
		if _, err := time.Parse(dayLayout, l.AcquisitionDate); err != nil {
			bad[openingimport.Row(i, "acquisitionDate")] = "must be a date such as 2019-03-04"
		}
		cost, accumulated, nbv, salvage := amount(bad, i, "acquisitionCost", l.AcquisitionCost, l.Currency), amount(bad, i, "accumulatedDepreciation", l.AccumulatedDepreciation, l.Currency),
			amount(bad, i, "netBookValue", l.NetBookValue, l.Currency), amount(bad, i, "salvageValue", l.SalvageValue, l.Currency)
		switch {
		case !cost.IsPositive():
			bad[openingimport.Row(i, "acquisitionCost")] = "must be above zero"
		case accumulated.IsNegative() || accumulated.GreaterThan(cost):
			bad[openingimport.Row(i, "accumulatedDepreciation")] = "must be between zero and the acquisition cost"
		case !nbv.Equal(cost.Sub(accumulated)):
			bad[openingimport.Row(i, "netBookValue")] = fmt.Sprintf("must equal the acquisition cost less the accumulated depreciation, %s", cost.Sub(accumulated).String())
		case salvage.IsNegative() || salvage.GreaterThan(nbv):
			bad[openingimport.Row(i, "salvageValue")] = "must be between zero and the net book value"
		}
		if l.UsefulLifePeriods < 1 || l.UsefulLifePeriods > 1200 || (l.Method == "DecliningBalance" && l.UsefulLifePeriods > 100) {
			bad[openingimport.Row(i, "usefulLifePeriods")] = "must be 1 to 1200 months (straight line) or 1 to 100 years (declining balance)"
		}
		if l.RemainingLifePeriods < 0 || l.RemainingLifePeriods > l.UsefulLifePeriods {
			bad[openingimport.Row(i, "remainingLifePeriods")] = "must be between zero and the useful life"
		}
		if !periodPattern.MatchString(l.DepreciatedThrough) {
			bad[openingimport.Row(i, "depreciatedThrough")] = "is required: the last period the legacy system depreciated, 2026-10 (straight line) or 2025 (declining balance)"
		} else if _, _, err := store.OpeningInService(l.Method, l.DepreciatedThrough, 0); err != nil && (l.Method == "StraightLine" || l.Method == "DecliningBalance") {
			bad[openingimport.Row(i, "depreciatedThrough")] = err.Error()
		}
		in.Lines = append(in.Lines, store.OpeningLine{Reference: l.Reference, AssetNumber: l.AssetNumber, Name: l.Name, ClassCode: l.ClassCode, BookCode: l.BookCode,
			Currency: l.Currency, Method: l.Method, AcquisitionDate: l.AcquisitionDate, UsefulLife: l.UsefulLifePeriods, RemainingLife: l.RemainingLifePeriods,
			Cost: cost, Accumulated: accumulated, Salvage: salvage, DepreciatedThrough: l.DepreciatedThrough})
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

// amount parses a decimal of zero or more with at most the currency's minor units, recording a problem on the line.
func amount(bad map[string]string, i int, field, raw, cur string) decimal.Decimal {
	d, err := decimal.NewFromString(raw)
	if err != nil {
		bad[openingimport.Row(i, field)] = "must be a decimal"
		return decimal.Zero
	}
	if cur != "" && currency.MatchString(cur) && !d.Equal(d.Round(money.MinorUnits(cur))) {
		bad[openingimport.Row(i, field)] = fmt.Sprintf("has more decimals than %s allows", cur)
	}
	return d
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
		if taken.Keys[l.AssetNumber] {
			msg := "asset number " + l.AssetNumber + " is already used by an asset"
			bad[openingimport.Row(i, "assetNumber")] = msg
			rows = append(rows, httpx.RowError{Row: i + 1, Field: "assetNumber", Code: httpx.RowNumberTaken, Detail: msg})
		} else if taken.Keys[store.OpeningNumberKey(l.AssetNumber, l.BookCode)] {
			msg := "asset " + l.AssetNumber + " already has book " + l.BookCode
			bad[openingimport.Row(i, "bookCode")] = msg
			rows = append(rows, httpx.RowError{Row: i + 1, Field: "bookCode", Code: httpx.RowNumberTaken, Detail: msg})
		}
	}
	return bad, rows
}

func (a *API) listOpeningAssets(w http.ResponseWriter, r *http.Request) {
	caller, _ := tenant.FromContext(r.Context())
	q, bad := openingimport.ParseList(r, caller)
	if bad != nil {
		writeValidation(w, r, bad)
		return
	}
	batches, err := a.opening.ListOpeningBatches(r.Context(), q.LegalEntityID, q.BatchKey, q.Limit+1, q.AfterAt, q.AfterID)
	if err != nil {
		slog.ErrorContext(r.Context(), "opening batch list failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "the opening batch list failed"))
		return
	}
	openingimport.WriteList(w, q, batches, a.now())
}

func (a *API) registerOpening(r chi.Router) {
	if a.opening == nil {
		return
	}
	r.With(authz.RequireDuty(dutyOpeningImport)).Post(pathOpeningAssets, a.importOpeningAssets)
	r.With(authz.RequireDuty(dutyOpeningRead)).Get(pathOpeningAssets, a.listOpeningAssets)
}
