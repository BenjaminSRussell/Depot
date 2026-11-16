package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Target load of the staged packs (#49): fixed_assets goes to the assets service's opening register (POST /v1/assets/opening-assets),
// wip_prod to production control's opening WIP (POST /v1/production/opening-wip) and payroll_ytd to payroll's year-to-date balances
// (POST /v1/payroll/opening-ytd). Each takes a batch of at most 1000 lines all or nothing under a key, refuses a reference another batch
// holds, recomputes the declared control totals and lists its stored batches back with totals summed from the stored lines, which is what
// the factory reconciles against.

// Targets of the staged loads.
const (
	TargetAssets     = "assets"
	TargetProduction = "production"
	TargetPayroll    = "payroll"
)

// StagedTargetOfPack returns the service a staged pack loads into, or "" for a pack with no staged load.
func StagedTargetOfPack(p Pack) string {
	switch p {
	case PackFixedAssets:
		return TargetAssets
	case PackWIPProd:
		return TargetProduction
	case PackPayrollYTD:
		return TargetPayroll
	}
	return ""
}

// MaxStagedBatchLines is the most lines one batch takes, in every target.
const MaxStagedBatchLines = 1000

// Reject codes the factory raises for a staged line it cannot build target lines from. A missing mapping is CodeRefUnresolved and a
// missing optional extract field CodeFieldRequired; CodeStagedField is a value the target cannot take.
const CodeStagedField = "MIG-LOAD-FIELD-INVALID"

// StagedMaps are what a run fixes for building target lines: the Passage product of a legacy item and the worker of a legacy worker (an id that
// is already a UUID needs no entry), the site and warehouse of the opening production orders, the asset class of an asset the extract gives
// none, the cutover date, and the last period the legacy system depreciated.
type StagedMaps struct {
	Products           map[string]string
	Workers            map[string]string
	SiteID             string
	WarehouseID        string
	AssetClass         string
	DepreciatedThrough string
}

// WireLine is one line of a target batch: the line as the target takes it and the amounts it adds to the batch totals, named
// measure.CURRENCY (cost.USD).
type WireLine struct {
	Reference string
	Body      map[string]any
	Totals    map[string]decimal.Decimal
}

// Hash identifies the line as a target would store it, with the target it goes to.
func (w WireLine) Hash(target string) string {
	raw, _ := json.Marshal(w.Body)
	return hashParts(target, string(raw))
}

var (
	assetBookPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	periodMonth      = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)
)

// ParseDepreciatedThrough checks the request's last depreciated month and returns the period each method uses: the month for a straight-line book and,
// for a declining-balance book, that month's year when it is December and the year before when it is not (the book is depreciated yearly).
func ParseDepreciatedThrough(month string) (straightLine, decliningBalance string, err error) {
	if !periodMonth.MatchString(month) {
		return "", "", fmt.Errorf("depreciatedThrough must be a month such as 2026-12")
	}
	y := month[:4]
	if month[5:] == "12" {
		return month, y, nil
	}
	t, _ := time.Parse("2006", y)
	return month, fmt.Sprintf("%04d", t.Year()-1), nil
}

// BuildStagedLines maps one staged line onto target lines (a payroll line becomes one line per pay element); a line that cannot be mapped
// comes back with the reject code and reason.
func BuildStagedLines(pack Pack, l StagedLine, m StagedMaps) ([]WireLine, string, string) {
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
	switch pack {
	case PackFixedAssets:
		return buildAsset(l, m)
	case PackWIPProd:
		item := l.Group["item"]
		product := pick(m.Products, item)
		if product == "" {
			return nil, CodeRefUnresolved, fmt.Sprintf("item %q has no Passage product id: name it in products or load the item master through the party migration", item)
		}
		qty := l.Measures["quantity_open"]
		if !qty.IsPositive() {
			return nil, CodeFieldRequired, "the line has no quantityOpen; production's opening import needs the quantity still to produce"
		}
		body := map[string]any{"reference": l.LegacyID, "orderNumber": l.Group["production_order"], "productId": product, "siteId": strings.ToLower(m.SiteID),
			"warehouseId": strings.ToLower(m.WarehouseID), "currency": l.Currency, "quantityOpen": qty.String(), "material": l.Measures["material"].String(),
			"labor": l.Measures["labor"].String(), "overhead": l.Measures["overhead"].String(), "wipValue": l.Measures["wip"].String()}
		if d := l.Group["delivery_date"]; d != "" {
			if _, err := time.Parse("2006-01-02", d); err != nil {
				return nil, CodeStagedField, fmt.Sprintf("delivery date %q is not an ISO date", d)
			}
			body["deliveryDate"] = d
		}
		cur := l.Currency
		return []WireLine{{Reference: l.LegacyID, Body: body, Totals: map[string]decimal.Decimal{"material." + cur: l.Measures["material"], "labor." + cur: l.Measures["labor"],
			"overhead." + cur: l.Measures["overhead"], "wipValue." + cur: l.Measures["wip"]}}}, "", ""
	case PackPayrollYTD:
		worker := pick(m.Workers, l.Group["worker"])
		if worker == "" {
			return nil, CodeRefUnresolved, fmt.Sprintf("worker %q has no Passage worker id: name it in workers", l.Group["worker"])
		}
		var out []WireLine
		for _, e := range payrollElements {
			ref := l.LegacyID + ":" + e.element
			out = append(out, WireLine{Reference: ref, Body: map[string]any{"reference": ref, "workerId": worker, "taxYear": l.Group["tax_year"], "element": e.element,
				"currency": l.Currency, "amount": l.Measures[e.measure].String()}, Totals: map[string]decimal.Decimal{e.measure + "." + l.Currency: l.Measures[e.measure]}})
		}
		return out, "", ""
	}
	return nil, CodeStagedField, "pack " + string(pack) + " has no staged load"
}

// payrollElements are the pay elements a worker's year-to-date line carries, with the measure each reads.
var payrollElements = []struct{ element, measure string }{{"GROSS", "gross"}, {"TAX", "tax"}, {"DEDUCTIONS", "deductions"}, {"NET", "net"}}

func buildAsset(l StagedLine, m StagedMaps) ([]WireLine, string, string) {
	class := strings.ToUpper(strings.TrimSpace(l.Group["asset_class"]))
	if class == "" {
		class = strings.ToUpper(strings.TrimSpace(m.AssetClass))
	}
	if class == "" {
		return nil, CodeFieldRequired, "the line has no assetClass and the load names no default; the register needs a class"
	}
	date := l.Group["acquisition_date"]
	if date == "" {
		return nil, CodeFieldRequired, "the line has no acquisitionDate; the register needs the date the asset was acquired"
	}
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return nil, CodeStagedField, fmt.Sprintf("acquisition date %q is not an ISO date", date)
	}
	book := l.Group["book"]
	if !assetBookPattern.MatchString(book) {
		return nil, CodeStagedField, fmt.Sprintf("book %q is not a book code of upper-case letters, digits, hyphen or underscore (at most 20)", book)
	}
	sl, db, err := ParseDepreciatedThrough(m.DepreciatedThrough)
	if err != nil {
		return nil, CodeStagedField, err.Error()
	}
	useful, remaining := int(l.Measures["useful_life_months"].IntPart()), int(l.Measures["remaining_life_months"].IntPart())
	method, through := "StraightLine", sl
	if l.Group["method"] == DepDecliningBalance {
		if useful%12 != 0 || remaining%12 != 0 {
			return nil, CodeStagedField, fmt.Sprintf("a declining-balance book is kept in years: useful life %d and remaining life %d months are not whole years", useful, remaining)
		}
		method, through, useful, remaining = "DecliningBalance", db, useful/12, remaining/12
	}
	name := l.Group["asset_name"]
	if name == "" {
		name = l.Group["asset"]
	}
	if len(name) > 120 {
		name = name[:120]
	}
	cur := l.Currency
	body := map[string]any{"reference": l.LegacyID, "assetNumber": l.Group["asset"], "name": name, "classCode": class, "bookCode": book, "currency": cur, "method": method,
		"acquisitionDate": date, "usefulLifePeriods": useful, "remainingLifePeriods": remaining, "acquisitionCost": l.Measures["acquisition_cost"].String(),
		"accumulatedDepreciation": l.Measures["accumulated_depreciation"].String(), "netBookValue": l.Measures["nbv"].String(), "salvageValue": l.Measures["salvage"].String(),
		"depreciatedThrough": through}
	return []WireLine{{Reference: l.LegacyID, Body: body, Totals: map[string]decimal.Decimal{"cost." + cur: l.Measures["acquisition_cost"],
		"accumulatedDepreciation." + cur: l.Measures["accumulated_depreciation"], "nbv." + cur: l.Measures["nbv"]}}}, "", ""
}

// StagedExpected is what a staged line must have put in its target: the number of target lines and the amounts it adds to the totals, from the
// staged line alone (a line that was rejected or held back still counts, so it shows as a difference).
func StagedExpected(pack Pack, l StagedLine) (int, map[string]decimal.Decimal) {
	cur := l.Currency
	switch pack {
	case PackFixedAssets:
		return 1, map[string]decimal.Decimal{"cost." + cur: l.Measures["acquisition_cost"], "accumulatedDepreciation." + cur: l.Measures["accumulated_depreciation"], "nbv." + cur: l.Measures["nbv"]}
	case PackWIPProd:
		return 1, map[string]decimal.Decimal{"material." + cur: l.Measures["material"], "labor." + cur: l.Measures["labor"], "overhead." + cur: l.Measures["overhead"], "wipValue." + cur: l.Measures["wip"]}
	case PackPayrollYTD:
		out := map[string]decimal.Decimal{}
		for _, e := range payrollElements {
			out[e.measure+"."+cur] = l.Measures[e.measure]
		}
		return len(payrollElements), out
	}
	return 0, nil
}

// StagedKeyPrefix starts every batch key the factory writes to a target for the staged pack of a legacy company.
func StagedKeyPrefix(pack Pack, system, company string) string {
	sum := sha256.Sum256([]byte(company))
	return "mf-" + strings.ReplaceAll(string(pack), "_", "") + "-" + system + "-" + hex.EncodeToString(sum[:])[:12] + "-"
}

// StagedBatchKey is the idempotency key of one batch: stable for the same lines, so a retry is the same request to the target.
func StagedBatchKey(pack Pack, system, company string, hashes []string) string {
	sum := sha256.Sum256([]byte(strings.Join(hashes, ",")))
	return StagedKeyPrefix(pack, system, company) + hex.EncodeToString(sum[:])[:OpeningKeyHash]
}

// IsStagedBatchKey reports whether a batch key was written by the factory for the staged pack of the company.
func IsStagedBatchKey(pack Pack, system, company, key string) bool {
	prefix := StagedKeyPrefix(pack, system, company)
	return strings.HasPrefix(key, prefix) && len(key) == len(prefix)+OpeningKeyHash
}

// StagedLoad is the load of one staged run into its target.
type StagedLoad struct {
	RunID         string
	Pack          Pack
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

// StagedTotals is what a target holds for the staged pack of a company: lines and the named totals (measure.CURRENCY).
type StagedTotals struct {
	Lines int
	Sums  map[string]decimal.Decimal
}

// NewStagedTotals returns empty totals.
func NewStagedTotals() StagedTotals { return StagedTotals{Sums: map[string]decimal.Decimal{}} }

// Add adds a line count and named amounts.
func (t *StagedTotals) Add(lines int, sums map[string]decimal.Decimal) {
	t.Lines += lines
	for k, v := range sums {
		t.Sums[k] = t.Sums[k].Add(v)
	}
}

// ReconcileStagedTarget compares what the factory loaded into a target (expected) with what the target reports for the company's batches
// (actual): the line count and every named total, exactly. A total is named target.<pack>.<measure> and keyed by legal entity and currency.
func ReconcileStagedTarget(pack Pack, legalEntityID string, expected, actual StagedTotals) ([]ControlTotal, error) {
	key := map[string]string{"legalEntityId": legalEntityID}
	lines, err := NewControlTotal("target."+string(pack)+".line_count", key, decimal.NewFromInt(int64(expected.Lines)), decimal.NewFromInt(int64(actual.Lines)), Tolerance{})
	if err != nil {
		return nil, err
	}
	out := []ControlTotal{lines}
	names := map[string]struct{}{}
	for n := range expected.Sums {
		names[n] = struct{}{}
	}
	for n := range actual.Sums {
		names[n] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		measure, cur, _ := strings.Cut(n, ".")
		v, err := NewControlTotal("target."+string(pack)+"."+measure, map[string]string{"legalEntityId": legalEntityID, "currency": cur}, expected.Sums[n], actual.Sums[n], Tolerance{})
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}
