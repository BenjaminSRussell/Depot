package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/xlsx"
)

const repoRoot = "../.."

var testMeta = Meta{Commit: "0123456789abcdef0123456789abcdef01234567", Date: "2026-11-16"}

// fixture is a source file read without the packet's own types, so the expectations below do not share the generator's parsing.
func fixture(t *testing.T, rel string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func list(v any, key string) []any {
	m, _ := v.(map[string]any)
	l, _ := m[key].([]any)
	return l
}

func field(v any, key string) string {
	m, _ := v.(map[string]any)
	switch x := m[key].(type) {
	case string:
		return x
	case nil:
		return ""
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func csvRows(t *testing.T, rel string) []map[string]string {
	t.Helper()
	f, err := os.Open(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	all, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]string
	for _, r := range all[1:] {
		m := map[string]string{}
		for i, h := range all[0] {
			m[h] = r[i]
		}
		out = append(out, m)
	}
	return out
}

func sum(t *testing.T, values ...string) decimal.Decimal {
	t.Helper()
	total := decimal.Zero
	for _, v := range values {
		if v == "" {
			continue
		}
		d, err := decimal.NewFromString(v)
		if err != nil {
			t.Fatalf("not a decimal: %q", v)
		}
		total = total.Add(d)
	}
	return total
}

// book is a workbook read back with the xlsx reader.
type book map[string]xlsx.Sheet

// column returns the values under a header, data rows only.
func (b book) column(t *testing.T, sheet, header string) []string {
	t.Helper()
	s, ok := b[sheet]
	if !ok {
		t.Fatalf("no sheet %q", sheet)
	}
	at := -1
	for i, c := range s.Rows[0] {
		if c.Text == header {
			at = i
		}
	}
	if at < 0 {
		t.Fatalf("sheet %q has no column %q", sheet, header)
	}
	out := make([]string, 0, len(s.Rows)-1)
	for r := 1; r < len(s.Rows); r++ {
		out = append(out, s.At(r, at).Text)
	}
	return out
}

func (b book) sum(t *testing.T, sheet, header string) decimal.Decimal {
	t.Helper()
	return sum(t, b.column(t, sheet, header)...)
}

// allZero fails unless every value of a column is 0.
func (b book) allZero(t *testing.T, sheet, header string) {
	t.Helper()
	for i, v := range b.column(t, sheet, header) {
		if v == "" {
			continue
		}
		if !sum(t, v).IsZero() {
			t.Errorf("%s / %s row %d = %s, want 0", sheet, header, i+2, v)
		}
	}
}

type packetCase struct {
	// rows are the data rows each sheet must hold (the header row not counted).
	rows func(t *testing.T) map[string]int
	// totals compares the workbook's totals with the fixture's.
	totals func(t *testing.T, b book)
}

var cases = map[string]packetCase{
	"statements": {
		rows: func(t *testing.T) map[string]int {
			f := fixture(t, statementsSource)
			out := map[string]int{"Journals": len(list(f, "journals")), "Accounts": len(list(f, "accounts")), "Reconciliation": 12}
			seen := map[string]bool{}
			for _, r := range csvRows(t, statementsGolden) {
				if k := r["statement"] + "/" + r["period"] + "/" + r["row"]; !seen[k] {
					seen[k] = true
					out[map[string]string{"TB-STD": "Trial balance", "BS-STD": "Balance sheet", "PL-STD": "Income statement", "CF-STD": "Cash flow"}[r["statement"]]]++
				}
			}
			return out
		},
		totals: func(t *testing.T, b book) {
			var amounts []string
			for _, j := range list(fixture(t, statementsSource), "journals") {
				amounts = append(amounts, field(j, "amount"))
			}
			if got, want := b.sum(t, "Journals", "Amount"), sum(t, amounts...); !got.Equal(want) {
				t.Errorf("journal amounts %s, fixture %s", got, want)
			}
			// The statement sheets carry the engine's figures unchanged.
			var plYTD, cfYTD []string
			for _, r := range csvRows(t, statementsGolden) {
				switch {
				case r["statement"] == "PL-STD" && r["column"] == "YTD":
					plYTD = append(plYTD, r["value"])
				case r["statement"] == "CF-STD" && r["column"] == "YTD":
					cfYTD = append(cfYTD, r["value"])
				}
			}
			if got, want := b.sum(t, "Income statement", "Year to date"), sum(t, plYTD...); !got.Equal(want) {
				t.Errorf("income statement year to date %s, engine %s", got, want)
			}
			if got, want := b.sum(t, "Cash flow", "Year to date"), sum(t, cfYTD...); !got.Equal(want) {
				t.Errorf("cash flow year to date %s, engine %s", got, want)
			}
			for _, col := range []string{"Net income difference", "Cash difference", "Assets difference", "Engine check: trial balance total",
				"Engine check: balance sheet", "Engine check: cash flow"} {
				b.allZero(t, "Reconciliation", col)
			}
		},
	},
	"costing": {
		rows: func(t *testing.T) map[string]int {
			seqs := list(fixture(t, costingSource), "sequences")
			txns, issues := 0, 0
			for _, s := range seqs {
				for _, x := range list(s, "transactions") {
					txns++
					if field(x, "kind") == "I" {
						issues++
					}
				}
			}
			return map[string]int{"Sequences": len(seqs), "Transactions": txns, "Expected": issues + len(seqs), "Reconciliation": len(costMethods)}
		},
		totals: func(t *testing.T, b book) {
			var receipts, fifo []string
			for _, s := range list(fixture(t, costingSource), "sequences") {
				for _, x := range list(s, "transactions") {
					receipts = append(receipts, field(x, "value"))
				}
				exp := s.(map[string]any)["expected"].(map[string]any)["FIFO"].(map[string]any)
				for _, v := range exp["issues"].(map[string]any) {
					fifo = append(fifo, v.(string))
				}
				fifo = append(fifo, exp["remainingValue"].(string))
			}
			if got, want := b.sum(t, "Transactions", "Receipt value"), sum(t, receipts...); !got.Equal(want) {
				t.Errorf("receipt values %s, fixture %s", got, want)
			}
			if got, want := b.sum(t, "Expected", "FIFO"), sum(t, fifo...); !got.Equal(want) {
				t.Errorf("FIFO issues and stock %s, fixture %s", got, want)
			}
			for i, v := range b.column(t, "Reconciliation", "Difference") {
				if b.column(t, "Reconciliation", "Method")[i] != "Standard cost" && !sum(t, v).IsZero() {
					t.Errorf("method row %d does not tie: %s", i+2, v)
				}
			}
			b.allZero(t, "Reconciliation", "Quantity difference")
		},
	},
	"leases": {
		rows: func(t *testing.T) map[string]int {
			leases := list(fixture(t, leasesSource), "leases")
			lines := 0
			for _, l := range leases {
				lines += len(list(l, "lines"))
			}
			return map[string]int{"Leases": len(leases), "Schedule": lines, "Reconciliation": len(leases)}
		},
		totals: func(t *testing.T, b book) {
			var interest, liability []string
			for _, l := range list(fixture(t, leasesSource), "leases") {
				interest = append(interest, field(l, "totalInterest"))
				liability = append(liability, field(l, "liability"))
			}
			if got, want := b.sum(t, "Schedule", "Interest"), sum(t, interest...); !got.Equal(want) {
				t.Errorf("schedule interest %s, fixture total interest %s", got, want)
			}
			if got, want := b.sum(t, "Leases", "Initial liability"), sum(t, liability...); !got.Equal(want) {
				t.Errorf("initial liabilities %s, fixture %s", got, want)
			}
			b.allZero(t, "Reconciliation", "Interest difference")
			b.allZero(t, "Reconciliation", "Depreciation difference")
			b.allZero(t, "Reconciliation", "Closing liability at the end")
		},
	},
	"poc": {
		rows: func(t *testing.T) map[string]int {
			w, e := len(csvRows(t, pocWorkedSource)), len(csvRows(t, pocEstimatesSource))
			return map[string]int{"Worked example inputs": w, "Worked example expected": w, "Estimates inputs": e, "Estimates expected": e, "Reconciliation": w + e}
		},
		totals: func(t *testing.T, b book) {
			var this []string
			for _, r := range csvRows(t, pocEstimatesSource) {
				this = append(this, r["expect_revenue_this_period"])
			}
			if got, want := b.sum(t, "Estimates expected", "Revenue this period"), sum(t, this...); !got.Equal(want) {
				t.Errorf("revenue this period %s, fixture %s", got, want)
			}
			b.allZero(t, "Reconciliation", "Billing difference")
			b.allZero(t, "Reconciliation", "Revenue difference")
		},
	},
	"asc606": {
		rows: func(t *testing.T) map[string]int {
			scenarios := list(fixture(t, asc606Source), "scenarios")
			obligations, lines, months := 0, 0, 0
			for _, s := range scenarios {
				obligations += len(list(s, "obligations"))
				exp := s.(map[string]any)["expected"]
				for _, sch := range list(exp, "schedules") {
					lines += len(sch.([]any))
				}
				months += len(list(exp, "closingByMonth"))
			}
			return map[string]int{"Scenarios": len(scenarios), "Obligations": obligations, "Schedules": lines, "Deferred by month": months, "Reconciliation": len(scenarios)}
		},
		totals: func(t *testing.T, b book) {
			var allocated []string
			for _, s := range list(fixture(t, asc606Source), "scenarios") {
				for _, a := range list(s.(map[string]any)["expected"], "allocated") {
					allocated = append(allocated, a.(string))
				}
			}
			want := sum(t, allocated...)
			if got := b.sum(t, "Schedules", "Amount"); !got.Equal(want) {
				t.Errorf("scheduled %s, fixture allocations %s", got, want)
			}
			if got := b.sum(t, "Obligations", "Allocated (expected)"); !got.Equal(want) {
				t.Errorf("allocated %s, fixture %s", got, want)
			}
			b.allZero(t, "Reconciliation", "Allocation difference")
			b.allZero(t, "Reconciliation", "Schedule difference")
		},
	},
	"tax": {
		rows: func(t *testing.T) map[string]int {
			out := map[string]int{"Scenarios": 500}
			codes := map[string]bool{}
			for _, r := range csvRows(t, taxExpectedSource) {
				out["Expected "+r["region"]]++
				codes[r["region"]+"/"+r["tax_code"]] = true
			}
			out["Reconciliation"] = len(codes)
			return out
		},
		totals: func(t *testing.T, b book) {
			var taxes []string
			for _, r := range csvRows(t, taxExpectedSource) {
				taxes = append(taxes, r["tax"])
			}
			got := decimal.Zero
			for _, region := range []string{"US", "EU", "IN", "CA"} {
				got = got.Add(b.sum(t, "Expected "+region, "Tax"))
			}
			if want := sum(t, taxes...); !got.Equal(want) || !b.sum(t, "Reconciliation", "Tax total").Equal(want) {
				t.Errorf("tax %s (reconciliation %s), fixture %s", got, b.sum(t, "Reconciliation", "Tax total"), want)
			}
			b.allZero(t, "Reconciliation", "Arithmetic check: base plus tax not equal to gross")
		},
	},
	"ebics": {
		rows: func(t *testing.T) map[string]int {
			n := len(list(fixture(t, ebicsSource), "codes"))
			return map[string]int{"Return codes": n, "Dispositions": n + 1, "Reconciliation": 5}
		},
		totals: func(t *testing.T, b book) {
			codes := list(fixture(t, ebicsSource), "codes")
			got := b.column(t, "Dispositions", "Disposition")
			for i, c := range codes {
				if got[i] != field(c, "disposition") {
					t.Errorf("%s: %s, fixture %s", field(c, "annexName"), got[i], field(c, "disposition"))
				}
			}
			if total := b.column(t, "Reconciliation", "Codes"); total[len(total)-1] != "13" || len(codes) != 13 {
				t.Errorf("total codes %v, fixture %d", total, len(codes))
			}
		},
	},
	"consolidation": {
		rows: func(t *testing.T) map[string]int {
			f := fixture(t, consolidationSource)
			rules := map[string]bool{}
			for _, e := range list(f, "expected") {
				rules[field(e, "rule")] = true
			}
			return map[string]int{"Inputs": 15, "Workings": len(f["workings"].(map[string]any)) + 1, "Expected": len(list(f, "expected")), "Reconciliation": len(rules) + 1}
		},
		totals: func(t *testing.T, b book) {
			var dr, cr []string
			for _, e := range list(fixture(t, consolidationSource), "expected") {
				dr, cr = append(dr, field(e, "debit")), append(cr, field(e, "credit"))
			}
			if got, want := b.sum(t, "Expected", "Debit"), sum(t, dr...); !got.Equal(want) || !want.Equal(sum(t, cr...)) {
				t.Errorf("debits %s, fixture debits %s credits %s", got, want, sum(t, cr...))
			}
			b.allZero(t, "Reconciliation", "Difference")
		},
	},
	"cashapp": {
		rows: func(t *testing.T) map[string]int {
			outcomes := csvRows(t, cashAppMatchResults)
			left, cats := 0, map[string]bool{}
			for _, r := range outcomes {
				if r["decision"] != "AUTO_APPLY" {
					left++
				}
			}
			for _, r := range csvRows(t, cashAppAnswerKey) {
				cats[r["category"]] = true
			}
			return map[string]int{"Open items": len(csvRows(t, cashAppOpenItems)), "Answer key": len(csvRows(t, cashAppAnswerKey)), "Match outcomes": len(outcomes),
				"Not auto-applied": left, "Reconciliation": len(cats) + 1}
		},
		totals: func(t *testing.T, b book) {
			var amounts []string
			auto := 0
			for _, r := range csvRows(t, cashAppMatchResults) {
				amounts = append(amounts, r["amount"])
				if r["decision"] == "AUTO_APPLY" {
					auto++
				}
			}
			if got, want := b.sum(t, "Match outcomes", "Amount"), sum(t, amounts...); !got.Equal(want) {
				t.Errorf("payment amounts %s, fixture %s", got, want)
			}
			correct := b.column(t, "Reconciliation", "Auto-applied correctly")
			if !sum(t, correct[len(correct)-1]).Equal(decimal.NewFromInt(int64(auto))) {
				t.Errorf("auto-applied correctly %s, fixture %d auto-applied", correct[len(correct)-1], auto)
			}
			b.allZero(t, "Reconciliation", "Auto-applied wrongly")
		},
	},
	"modifications": {
		rows: func(t *testing.T) map[string]int {
			examples := list(fixture(t, modificationsSource), "examples")
			lines, changes, left := 0, 0, 0
			for _, e := range examples {
				for _, o := range list(e.(map[string]any)["input"], "obligations") {
					lines += len(list(o, "lines"))
				}
				for _, c := range list(e.(map[string]any)["expected"], "obligations") {
					changes++
					left += len(list(c, "lines"))
				}
			}
			return map[string]int{"Examples": len(examples), "Contract before": lines, "Expected changes": changes, "Remaining schedule": left, "Reconciliation": len(examples)}
		},
		totals: func(t *testing.T, b book) {
			var after, reversal []string
			for _, e := range list(fixture(t, modificationsSource), "examples") {
				exp := e.(map[string]any)["expected"]
				reversal = append(reversal, field(exp, "reversal"))
				for _, c := range list(exp, "obligations") {
					for _, l := range list(c, "lines") {
						after = append(after, field(l, "amount"))
					}
				}
			}
			if got, want := b.sum(t, "Remaining schedule", "Amount after"), sum(t, after...); !got.Equal(want) {
				t.Errorf("remaining schedule %s, fixture %s", got, want)
			}
			if got, want := b.sum(t, "Reconciliation", "Revenue reversed"), sum(t, reversal...); !got.Equal(want) {
				t.Errorf("revenue reversed %s, fixture %s", got, want)
			}
			b.allZero(t, "Reconciliation", "Difference")
			b.allZero(t, "Reconciliation", "Allocation difference")
		},
	},
	"ic-elimination": {
		rows: func(t *testing.T) map[string]int {
			f := fixture(t, icElimSource)
			examples := list(f, "examples")
			rows, figures, entries, rules := 0, 0, 0, 0
			for _, e := range examples {
				rows += len(list(e, "partnerRows"))
				exp := e.(map[string]any)["expected"].(map[string]any)
				for _, p := range exp["pairs"].(map[string]any) {
					figures += len(p.(map[string]any))
				}
				seen := map[string]bool{}
				for _, en := range list(exp, "entries") {
					entries++
					seen[field(en, "rule")] = true
				}
				rules += len(seen)
			}
			return map[string]int{"Accounts": len(list(f, "accounts")), "Examples": len(examples), "Partner balances": rows, "Expected pairs": figures,
				"Expected entries": entries, "Reconciliation": rules}
		},
		totals: func(t *testing.T, b book) {
			var dr []string
			for _, e := range list(fixture(t, icElimSource), "examples") {
				for _, en := range list(e.(map[string]any)["expected"], "entries") {
					dr = append(dr, field(en, "debit"))
				}
			}
			if got, want := b.sum(t, "Expected entries", "Debit"), sum(t, dr...); !got.Equal(want) || !got.Equal(b.sum(t, "Expected entries", "Credit")) {
				t.Errorf("debits %s credits %s, fixture debits %s", got, b.sum(t, "Expected entries", "Credit"), want)
			}
			b.allZero(t, "Reconciliation", "Difference")
		},
	},
}

func readBook(t *testing.T, raw []byte) (book, []string) {
	t.Helper()
	sheets, err := xlsx.Read(context.Background(), raw)
	if err != nil {
		t.Fatalf("the workbook does not open: %v", err)
	}
	b := book{}
	var names []string
	for _, s := range sheets {
		b[s.Name] = s
		names = append(names, s.Name)
	}
	return b, names
}

func TestEveryPacketHasACase(t *testing.T) {
	if len(packets) != len(cases) {
		t.Fatalf("%d packets, %d test cases", len(packets), len(cases))
	}
	for _, p := range packets {
		if _, ok := cases[p.Name]; !ok {
			t.Errorf("packet %s has no test case", p.Name)
		}
		for _, s := range p.Sources {
			if _, err := os.Stat(filepath.Join(repoRoot, s)); err != nil {
				t.Errorf("%s: source %s: %v", p.Name, s, err)
			}
		}
	}
}

func TestPackets(t *testing.T) {
	for _, p := range packets {
		t.Run(p.Name, func(t *testing.T) {
			raw, tables, err := Generate(p, repoRoot, testMeta)
			if err != nil {
				t.Fatal(err)
			}
			b, names := readBook(t, raw)
			want := []string{"Cover"}
			for _, tb := range tables {
				want = append(want, tb.Name)
			}
			if !reflect.DeepEqual(names, want) {
				t.Fatalf("sheets %v, want %v", names, want)
			}

			c := cases[p.Name]
			counts := c.rows(t)
			if len(counts) != len(tables) {
				t.Errorf("the case counts %d sheets, the packet has %d", len(counts), len(tables))
			}
			for sheet, n := range counts {
				s, ok := b[sheet]
				if !ok {
					t.Errorf("no sheet %q", sheet)
					continue
				}
				if got := len(s.Rows) - 1; got != n {
					t.Errorf("%s has %d rows, want %d", sheet, got, n)
				}
			}
			c.totals(t, b)

			cover := b["Cover"]
			text := map[string]string{}
			for r := range cover.Rows {
				if label := cover.At(r, 0).Text; label != "" {
					text[label] = cover.At(r, 1).Text
				}
			}
			if text["Git commit"] != testMeta.Commit || text["Generated on"] != testMeta.Date || text["Source fixture"] != p.Sources[0] || text["Serves"] != p.Item {
				t.Errorf("cover = %v", text)
			}
			for _, f := range signOffFields {
				if _, ok := text[f]; !ok {
					t.Errorf("cover has no sign-off field %q", f)
				}
			}
		})
	}
}

// The same commit gives the same bytes; another generation date changes the cover's date cell and nothing else.
func TestPackets_Deterministic(t *testing.T) {
	for _, p := range packets {
		a, _, err := Generate(p, repoRoot, testMeta)
		if err != nil {
			t.Fatal(err)
		}
		again, _, err := Generate(p, repoRoot, testMeta)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a, again) {
			t.Errorf("%s: two runs on one commit differ", p.Name)
		}
		later := testMeta
		later.Date = "2027-01-31"
		c, _, err := Generate(p, repoRoot, later)
		if err != nil {
			t.Fatal(err)
		}
		if diff := zipDiff(t, a, c); !reflect.DeepEqual(diff, []string{"xl/worksheets/sheet1.xml"}) {
			t.Errorf("%s: another date changes %v, want only the cover", p.Name, diff)
		}
		ba, _ := readBook(t, a)
		bc, _ := readBook(t, c)
		ca, cc := ba["Cover"], bc["Cover"]
		for r := range ca.Rows {
			if ca.At(r, 0).Text == "Generated on" {
				continue
			}
			if !reflect.DeepEqual(ca.Rows[r], cc.Rows[r]) {
				t.Errorf("%s: cover row %d differs: %v vs %v", p.Name, r+1, ca.Rows[r], cc.Rows[r])
			}
		}
	}
}

// zipDiff lists the parts whose content differs between two workbooks.
func zipDiff(t *testing.T, a, b []byte) []string {
	t.Helper()
	parts := func(raw []byte) map[string]string {
		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			_, _ = buf.ReadFrom(rc)
			_ = rc.Close()
			out[f.Name] = buf.String()
		}
		return out
	}
	pa, pb := parts(a), parts(b)
	var diff []string
	for name, body := range pa {
		if pb[name] != body {
			diff = append(diff, name)
		}
	}
	for name := range pb {
		if _, ok := pa[name]; !ok {
			diff = append(diff, name)
		}
	}
	return diff
}

func TestRun_WritesTheSelectedPackets(t *testing.T) {
	dir := t.TempDir()
	var out strings.Builder
	if err := run([]string{"-out", dir, "-only", "ebics,consolidation", "-date", "2026-11-16", "-commit", "abc", "-repo", repoRoot}, &out); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !reflect.DeepEqual(names, []string{"consolidation.xlsx", "ebics.xlsx"}) {
		t.Fatalf("wrote %v", names)
	}
	if err := run([]string{"-out", dir, "-only", "nope", "-repo", repoRoot}, &out); err == nil {
		t.Fatal("an unknown packet was accepted")
	}
	if err := run([]string{"-out", dir, "-date", "16/11/2026", "-repo", repoRoot}, &out); err == nil {
		t.Fatal("a bad date was accepted")
	}
	if err := run([]string{"-repo", repoRoot}, &out); err == nil {
		t.Fatal("a missing -out was accepted")
	}
}
