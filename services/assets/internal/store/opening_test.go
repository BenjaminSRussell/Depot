package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/openingimport"
	"github.com/BenjaminSRussell/depot/services/assets/internal/domain"
	"github.com/BenjaminSRussell/depot/services/assets/internal/store"
)

func openingLines() []store.OpeningLine {
	mk := func(ref, number, book, cur, cost, acc string, useful, remaining int, through string) store.OpeningLine {
		return store.OpeningLine{Reference: ref, AssetNumber: number, Name: "Press " + number, ClassCode: "MACHINERY", BookCode: book, Currency: cur, Method: "StraightLine",
			AcquisitionDate: "2023-03-01", UsefulLife: useful, RemainingLife: remaining, Cost: d(cost), Accumulated: d(acc), Salvage: d("0"), DepreciatedThrough: through}
	}
	return []store.OpeningLine{
		mk("L1", "FA-100", "IFRS", "USD", "12000", "4000", 60, 40, "2026-12"),
		mk("L2", "FA-100", "TAX", "USD", "12000", "6000", 60, 30, "2026-12"),
		mk("L3", "FA-101", "IFRS", "USD", "500.50", "0", 12, 12, "2026-12"),
	}
}

func openingImport(le, key, hash, journal string) store.OpeningImport {
	return store.OpeningImport{LegalEntityID: le, BatchKey: key, ContentHash: hash, ImportedBy: "loader", OpeningDate: "2027-01-01", Journal: journal,
		Totals: map[string]string{"cost.USD": "24500.5", "accumulatedDepreciation.USD": "10000", "nbv.USD": "14500.5"}, Lines: openingLines()}
}

func TestOpeningAssets_ImportIsIdempotentReadBackAndPosts(t *testing.T) {
	s, pool, ctx := setup(t)
	s.WithOpeningClearingAccount("3900")
	le := ids.New()
	in := openingImport(le, "b1", "h1", store.OpeningJournalPost)
	got, created, err := s.ImportOpeningAssets(ctx, in)
	if err != nil || !created {
		t.Fatalf("import: %v %v", err, created)
	}
	if got.LineCount != 3 || got.Totals["cost.USD"] != "24500.5" || got.Totals["accumulatedDepreciation.USD"] != "10000" || got.Totals["nbv.USD"] != "14500.5" {
		t.Fatalf("batch: %+v", got)
	}
	again, created, err := s.ImportOpeningAssets(ctx, in)
	if err != nil || created || again.ID != got.ID {
		t.Fatalf("replay: %v %v", err, created)
	}
	changed := in
	changed.ContentHash = "other"
	if _, _, err := s.ImportOpeningAssets(ctx, changed); !errors.Is(err, openingimport.ErrKeyConflict) {
		t.Fatalf("same key other content: %v", err)
	}
	other := openingImport(le, "b2", "h2", store.OpeningJournalPost)
	var taken *openingimport.TakenError
	if _, _, err := s.ImportOpeningAssets(ctx, other); !errors.As(err, &taken) || taken.References["L1"] != "b1" || !taken.Keys["FA-100/IFRS"] && !taken.Keys["FA-100"] {
		t.Fatalf("taken: %v %+v", err, taken)
	}
	list, err := s.ListOpeningBatches(ctx, le, "", 10, time.Time{}, "")
	if err != nil || len(list) != 1 || list[0].Totals["nbv.USD"] != "14500.5" {
		t.Fatalf("list: %+v %v", list, err)
	}
	// Two assets, three books; the second book of FA-100 shares its asset.
	var assets, books, txs int
	if err := pool.QueryRow(ctx, `select (select count(*) from asset where legal_entity_id = $1::uuid), (select count(*) from asset_book where legal_entity_id = $1::uuid),
		(select count(*) from asset_transaction where legal_entity_id = $1::uuid and source_type = 'opening')`, le).Scan(&assets, &books, &txs); err != nil {
		t.Fatal(err)
	}
	if assets != 2 || books != 3 || txs != 3 {
		t.Fatalf("stored %d assets %d books %d transactions", assets, books, txs)
	}
	// 60 months, 40 remaining: 20 depreciated through 2026-12, so the schedule started 2025-05 and the next run is 2027-01.
	var inService, last string
	var nbv decimal.Decimal
	if err := pool.QueryRow(ctx, `select b.in_service_date::text, b.last_depreciated_period, b.nbv from asset_book b join asset a on a.tenant_id = b.tenant_id and a.id = b.asset_id
		where a.asset_number = 'FA-100' and b.book_code = 'IFRS'`).Scan(&inService, &last, &nbv); err != nil {
		t.Fatal(err)
	}
	if inService != "2025-05-01" || last != "2026-12" || !nbv.Equal(d("8000")) {
		t.Fatalf("book: %s %s %s", inService, last, nbv)
	}
	// The remaining life is honoured: the schedule has exactly 40 periods after the last depreciated one.
	detail, err := s.GetAsset(ctx, assetIDOf(t, pool, ctx, "FA-100"))
	if err != nil || len(detail.Books) != 2 {
		t.Fatalf("asset: %v %+v", err, detail)
	}
	for _, b := range detail.Books {
		if b.BookCode != "IFRS" {
			continue
		}
		lines, err := domain.Schedule(b.RunProfile())
		if err != nil {
			t.Fatal(err)
		}
		after := 0
		for _, l := range lines {
			if l.Period > *b.LastDepreciatedPeriod {
				after++
			}
		}
		if after != 40 || domain.DecideRun(lines, *b.LastDepreciatedPeriod, "2027-01").Outcome != domain.RunDue {
			t.Fatalf("remaining periods %d, next run %v", after, domain.DecideRun(lines, *b.LastDepreciatedPeriod, "2027-01"))
		}
	}
	// The opening journal books cost, accumulated depreciation and the NBV against the clearing account.
	fake, ledgerID := ledgerclient.NewFake(), ids.New()
	res, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: le, LedgerID: ledgerID, BookCode: "IFRS", Currency: "USD", OpeningOnly: true})
	if err != nil || len(res.Failed) != 0 || len(res.Booked) != 2 {
		t.Fatalf("post: %v %+v", err, res)
	}
	if c := net(t, fake, ledgerID, "1500"); !c.Equal(d("12500.5")) {
		t.Fatalf("cost account %s", c)
	}
	if a := net(t, fake, ledgerID, "1590"); !a.Equal(d("-4000")) {
		t.Fatalf("accumulated account %s", a)
	}
	if clr := net(t, fake, ledgerID, "3900"); !clr.Equal(d("-8500.5")) || !net(t, fake, ledgerID, "2190").IsZero() {
		t.Fatalf("clearing account %s", clr)
	}
}

func assetIDOf(t *testing.T, pool interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}, ctx context.Context, number string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `select id::text from asset where asset_number = $1`, number).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOpeningAssets_LinkedJournalLeavesNothingToPostAndLaterBookFindsAsset(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	first := openingImport(le, "b1", "h1", store.OpeningJournalLinked)
	first.Lines = first.Lines[:1]
	first.Totals = map[string]string{"cost.USD": "12000", "accumulatedDepreciation.USD": "4000", "nbv.USD": "8000"}
	if _, _, err := s.ImportOpeningAssets(ctx, first); err != nil {
		t.Fatal(err)
	}
	// A later batch adds the TAX book of the asset the first batch created.
	second := openingImport(le, "b2", "h2", store.OpeningJournalLinked)
	second.Lines = second.Lines[1:2]
	second.Totals = map[string]string{"cost.USD": "12000", "accumulatedDepreciation.USD": "6000", "nbv.USD": "6000"}
	if _, created, err := s.ImportOpeningAssets(ctx, second); err != nil || !created {
		t.Fatalf("second book: %v %v", err, created)
	}
	fake := ledgerclient.NewFake()
	for _, book := range []string{"IFRS", "TAX"} {
		res, err := s.PostToGL(ctx, fake, store.GLPostRequest{LegalEntityID: le, LedgerID: ids.New(), BookCode: book, Currency: "USD"})
		if err != nil || len(res.Booked) != 0 || len(res.Failed) != 0 {
			t.Fatalf("linked %s must post nothing: %v %+v", book, err, res)
		}
	}
	// The same book of the same asset twice is taken.
	third := openingImport(le, "b3", "h3", store.OpeningJournalLinked)
	third.Lines = third.Lines[:1]
	third.Lines[0].Reference = "L9"
	third.Totals = first.Totals
	var taken *openingimport.TakenError
	if _, _, err := s.ImportOpeningAssets(ctx, third); !errors.As(err, &taken) || !taken.Keys["FA-100/IFRS"] {
		t.Fatalf("taken book: %v %+v", err, taken)
	}
}

func TestOpeningInService(t *testing.T) {
	in, last, err := store.OpeningInService("StraightLine", "2026-12", 20)
	if err != nil || in.Format("2006-01-02") != "2025-05-01" || last == nil || *last != "2026-12" {
		t.Fatalf("sl: %v %v %v", in, last, err)
	}
	in, last, err = store.OpeningInService("StraightLine", "2026-12", 0)
	if err != nil || in.Format("2006-01-02") != "2027-01-01" || last != nil {
		t.Fatalf("sl none elapsed: %v %v %v", in, last, err)
	}
	in, last, err = store.OpeningInService("DecliningBalance", "2025", 3)
	if err != nil || in.Format("2006-01-02") != "2023-01-01" || last == nil || *last != "2025" {
		t.Fatalf("db: %v %v %v", in, last, err)
	}
	if _, _, err := store.OpeningInService("StraightLine", "2026", 1); err == nil {
		t.Fatal("a yearly period must not do for a monthly book")
	}
}

// Without the opening clearing account the document fails (it must not fall back to the acquisition clearing account), and the opening-only
// booking leaves an ordinary acquisition alone.
func TestOpeningAssets_JournalNeedsTheClearingAccountAndSkipsOrdinaryTransactions(t *testing.T) {
	s, _, ctx := setup(t)
	le := ids.New()
	if _, _, err := s.ImportOpeningAssets(ctx, openingImport(le, "b1", "h1", store.OpeningJournalPost)); err != nil {
		t.Fatal(err)
	}
	asset := newAsset(t, s, ctx, le, "FA-ORD")
	if _, err := s.AddBook(ctx, asset.ID, store.Book{BookCode: "IFRS", Currency: "USD", Method: "StraightLine", ServiceLifePeriods: 60, Convention: "None", InServiceDate: day("2026-01-01"), Cost: d("999"), SalvageValue: d("0")}, "ord-ifrs"); err != nil {
		t.Fatal(err)
	}
	fake, ledgerID := ledgerclient.NewFake(), ids.New()
	req := store.GLPostRequest{LegalEntityID: le, LedgerID: ledgerID, BookCode: "IFRS", Currency: "USD", OpeningOnly: true}
	if res, err := s.PostToGL(ctx, fake, req); err != nil || len(res.Booked) != 0 || len(res.Failed) != 2 || res.Failed[0].Code != store.CodeGLUnmapped {
		t.Fatalf("no clearing account: %+v %v", res, err)
	}
	s.WithOpeningClearingAccount("3900")
	res, err := s.PostToGL(ctx, fake, req)
	if err != nil || len(res.Booked) != 2 || len(res.Failed) != 0 || !net(t, fake, ledgerID, "1500").Equal(d("12500.5")) {
		t.Fatalf("opening only: %+v %v", res, err)
	}
}
