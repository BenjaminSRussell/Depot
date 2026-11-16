package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/openingimport"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payroll/internal/store"
	"github.com/BenjaminSRussell/passage/services/payroll/migrations"
	"github.com/shopspring/decimal"
)

func amt(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func yearLines(worker, ref string, gross, tax, ded, net string) []store.OpeningLine {
	mk := func(el, a string) store.OpeningLine {
		return store.OpeningLine{Reference: ref + ":" + el, WorkerID: worker, TaxYear: "2026", Element: el, Currency: "USD", Amount: amt(a)}
	}
	return []store.OpeningLine{mk("GROSS", gross), mk("TAX", tax), mk("DEDUCTIONS", ded), mk("NET", net)}
}

func TestOpeningYTD_ImportIsIdempotentAndReadBack(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(pool)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "loader", Duties: testkit.ScopeDuties("")})
	le, w1, w2 := ids.New(), ids.New(), ids.New()
	lines := append(yearLines(w1, "PY-1", "90000", "20000", "5000", "65000"), yearLines(w2, "PY-2", "1000", "100", "0", "900")...)
	in := store.OpeningImport{LegalEntityID: le, BatchKey: "b1", ContentHash: "h1", ImportedBy: "loader",
		Totals: map[string]string{"gross.USD": "91000", "tax.USD": "20100", "deductions.USD": "5000", "net.USD": "65900"}, Lines: lines}
	got, created, err := s.ImportOpeningYTD(ctx, in)
	if err != nil || !created {
		t.Fatalf("import: %v %v", err, created)
	}
	if got.LineCount != 8 || got.Totals["gross.USD"] != "91000" || got.Totals["net.USD"] != "65900" || got.Totals["deductions.USD"] != "5000" {
		t.Fatalf("batch: %+v", got)
	}
	if again, created, err := s.ImportOpeningYTD(ctx, in); err != nil || created || again.ID != got.ID {
		t.Fatalf("replay: %v %v", err, created)
	}
	changed := in
	changed.ContentHash = "other"
	if _, _, err := s.ImportOpeningYTD(ctx, changed); !errors.Is(err, openingimport.ErrKeyConflict) {
		t.Fatalf("same key other content: %v", err)
	}
	// Another batch cannot restate a worker's year, nor reuse a reference.
	other := in
	other.BatchKey, other.ContentHash = "b2", "h2"
	var taken *openingimport.TakenError
	if _, _, err := s.ImportOpeningYTD(ctx, other); !errors.As(err, &taken) || taken.References["PY-1:GROSS"] != "b1" || !taken.Keys[w1+"/2026/GROSS"] {
		t.Fatalf("taken: %v %+v", err, taken)
	}
	list, err := s.ListOpeningBatches(ctx, le, "", 10, time.Time{}, "")
	if err != nil || len(list) != 1 || list[0].Totals["tax.USD"] != "20100" {
		t.Fatalf("list: %+v %v", list, err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from payroll_opening_ytd where legal_entity_id = $1::uuid`, le).Scan(&n); err != nil || n != 8 {
		t.Fatalf("balances %d %v", n, err)
	}
	// Another legal entity holds the same worker's year independently.
	elsewhere := in
	elsewhere.LegalEntityID, elsewhere.BatchKey, elsewhere.ContentHash = ids.New(), "b1", "h3"
	for i := range elsewhere.Lines {
		elsewhere.Lines[i].Reference = "X-" + elsewhere.Lines[i].Reference
	}
	if _, created, err := s.ImportOpeningYTD(ctx, elsewhere); err != nil || !created {
		t.Fatalf("other entity: %v %v", err, created)
	}
}
