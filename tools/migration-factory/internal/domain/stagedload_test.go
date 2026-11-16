package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestParseDepreciatedThroughPicksThePeriodOfEachMethod(t *testing.T) {
	sl, db, err := ParseDepreciatedThrough("2026-12")
	if err != nil || sl != "2026-12" || db != "2026" {
		t.Fatalf("december: %s %s %v", sl, db, err)
	}
	sl, db, err = ParseDepreciatedThrough("2026-10")
	if err != nil || sl != "2026-10" || db != "2025" {
		t.Fatalf("october: %s %s %v", sl, db, err)
	}
	for _, bad := range []string{"", "2026", "2026-13", "26-12"} {
		if _, _, err := ParseDepreciatedThrough(bad); err == nil {
			t.Fatalf("%q must be refused", bad)
		}
	}
}

func TestBuildStagedLinesMapsEachPackAndNamesWhatIsMissing(t *testing.T) {
	d := decimal.RequireFromString
	asset := FixedAssetLine{LegacyID: "FA-1", Asset: "PRESS", Book: "GAAP", Currency: "USD", Method: DepDecliningBalance, UsefulLifeMonths: 60, RemainingLifeMonths: 24,
		AcquisitionCost: d("1000"), AccumulatedDepreciation: d("400"), AcquisitionDate: "2023-01-01"}
	m := StagedMaps{DepreciatedThrough: "2026-06", AssetClass: "mach"}
	lines, code, why := BuildStagedLines(PackFixedAssets, asset.Staged(), m)
	if code != "" || len(lines) != 1 {
		t.Fatalf("asset: %s %s", code, why)
	}
	b := lines[0].Body
	// A declining-balance book is kept in years and its period is the last full year.
	if b["method"] != "DecliningBalance" || b["usefulLifePeriods"] != 5 || b["remainingLifePeriods"] != 2 || b["depreciatedThrough"] != "2025" || b["classCode"] != "MACH" || b["netBookValue"] != "600" {
		t.Fatalf("body %v", b)
	}
	if lines[0].Totals["cost.USD"].String() != "1000" || lines[0].Totals["nbv.USD"].String() != "600" {
		t.Fatalf("totals %v", lines[0].Totals)
	}
	asset.UsefulLifeMonths = 50
	if _, code, _ := BuildStagedLines(PackFixedAssets, asset.Staged(), m); code != CodeStagedField {
		t.Fatalf("months that are not whole years: %s", code)
	}
	asset.UsefulLifeMonths, asset.AcquisitionDate = 60, ""
	if _, code, _ := BuildStagedLines(PackFixedAssets, asset.Staged(), m); code != CodeFieldRequired {
		t.Fatalf("no date: %s", code)
	}
	n, sums := StagedExpected(PackFixedAssets, asset.Staged())
	if n != 1 || sums["accumulatedDepreciation.USD"].String() != "400" {
		t.Fatalf("expected %d %v", n, sums)
	}

	wip := WIPProdLine{LegacyID: "W-1", ProductionOrder: "PRD-1", Item: "FG", Currency: "USD", Material: d("1"), Labor: d("2"), Overhead: d("3"), QuantityOpen: d("5")}
	if _, code, _ := BuildStagedLines(PackWIPProd, wip.Staged(), StagedMaps{Products: map[string]string{}}); code != CodeRefUnresolved {
		t.Fatalf("unmapped item: %s", code)
	}
	lines, code, _ = BuildStagedLines(PackWIPProd, wip.Staged(), StagedMaps{Products: map[string]string{"FG": "11111111-1111-4111-8111-111111111111"}, SiteID: "22222222-2222-4222-8222-222222222222", WarehouseID: "33333333-3333-4333-8333-333333333333"})
	if code != "" || lines[0].Body["wipValue"] != "6" || lines[0].Totals["wipValue.USD"].String() != "6" {
		t.Fatalf("wip: %s %v", code, lines)
	}

	pay := PayrollYTDLine{LegacyID: "PY", Worker: "E1", TaxYear: "2026", Currency: "USD", Gross: d("10"), Tax: d("1"), Deductions: d("2"), Net: d("7")}
	lines, code, _ = BuildStagedLines(PackPayrollYTD, pay.Staged(), StagedMaps{Workers: map[string]string{"E1": "44444444-4444-4444-8444-444444444444"}})
	if code != "" || len(lines) != 4 || lines[3].Reference != "PY:NET" || lines[3].Totals["net.USD"].String() != "7" {
		t.Fatalf("payroll: %s %v", code, lines)
	}
	if n, _ := StagedExpected(PackPayrollYTD, pay.Staged()); n != 4 {
		t.Fatalf("payroll lines expected %d", n)
	}
}
