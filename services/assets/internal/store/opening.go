package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/openingimport"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Opening import of the asset register at cutover (#49). Nothing is announced: no asset event is written, because the assets were
// acquired and depreciated in the legacy system.

// Journal choices of an opening batch.
const (
	// OpeningJournalPost leaves the Acquisition transactions unbooked, so POST /v1/assets/gl-postings books them: cost and
	// accumulated depreciation on the class accounts and the NBV against the acquisition clearing account.
	OpeningJournalPost = "post"
	// OpeningJournalLinked marks the transactions booked without a journal entry, for a legal entity whose general ledger opening balances
	// (the migration's gl_balances load) already hold the asset accounts.
	OpeningJournalLinked = "linked"
)

// OpeningSource is the source type of the transactions the opening import writes.
const OpeningSource = "opening"

// OpeningTables are the batch and line tables of the register import.
var OpeningTables = openingimport.Tables{Batch: "asset_opening_batch", Line: "asset_opening_line"}

// OpeningLine is one asset in one depreciation book at the cutover. Lives are in months for StraightLine and years for DecliningBalance.
type OpeningLine struct {
	Reference          string
	AssetNumber        string
	Name               string
	ClassCode          string
	BookCode           string
	Currency           string
	Method             string
	AcquisitionDate    string
	UsefulLife         int
	RemainingLife      int
	Cost               decimal.Decimal
	Accumulated        decimal.Decimal
	Salvage            decimal.Decimal
	DepreciatedThrough string
	// Factor and SwitchToStraightLine are the declining-balance rate (1 to 3, 2 is 200 percent) and whether the book switches to straight line;
	// zero values mean 2 and no switch. They are ignored for straight line.
	Factor               decimal.Decimal
	SwitchToStraightLine bool
}

// NBV is the net book value at the cutover.
func (l OpeningLine) NBV() decimal.Decimal { return l.Cost.Sub(l.Accumulated) }

// Measures are the amounts the batch totals add up: cost, accumulated depreciation and NBV per currency.
func (l OpeningLine) Measures() map[string]decimal.Decimal {
	return map[string]decimal.Decimal{"cost." + l.Currency: l.Cost, "accumulatedDepreciation." + l.Currency: l.Accumulated, "nbv." + l.Currency: l.NBV()}
}

// OpeningImport is one batch.
type OpeningImport struct {
	LegalEntityID string
	BatchKey      string
	ContentHash   string
	ImportedBy    string
	// OpeningDate is the cutover date the opening transactions carry (and the posting date of the opening journal).
	OpeningDate string
	Journal     string
	Totals      map[string]string
	Lines       []OpeningLine
}

// OpeningInService derives where the schedule of a book stands. With elapsed periods already depreciated (useful life less remaining life) the
// last depreciated period is depreciatedThrough and the schedule started elapsed-1 periods before it, so exactly the remaining life is left; with
// none elapsed the schedule starts in the period after depreciatedThrough and nothing is depreciated yet. A StraightLine period is "2026-03", a
// DecliningBalance one "2026".
func OpeningInService(method, depreciatedThrough string, elapsed int) (inService time.Time, last *string, err error) {
	switch method {
	case "StraightLine":
		t, perr := time.Parse("2006-01", depreciatedThrough)
		if perr != nil {
			return time.Time{}, nil, fmt.Errorf("a straight-line book is depreciated monthly: depreciatedThrough must look like 2026-10")
		}
		if elapsed == 0 {
			return t.AddDate(0, 1, 0), nil, nil
		}
		return t.AddDate(0, -(elapsed - 1), 0), &depreciatedThrough, nil
	case "DecliningBalance":
		y, perr := strconv.Atoi(depreciatedThrough)
		if perr != nil || len(depreciatedThrough) != 4 {
			return time.Time{}, nil, fmt.Errorf("a declining-balance book is depreciated yearly: depreciatedThrough must look like 2025")
		}
		if elapsed == 0 {
			return time.Date(y+1, 1, 1, 0, 0, 0, 0, time.UTC), nil, nil
		}
		return time.Date(y-(elapsed-1), 1, 1, 0, 0, 0, 0, time.UTC), &depreciatedThrough, nil
	}
	return time.Time{}, nil, fmt.Errorf("method %q has no opening import", method)
}

// OpeningImporter is the store of the register import.
type OpeningImporter interface {
	ImportOpeningAssets(ctx context.Context, in OpeningImport) (openingimport.Batch, bool, error)
	ListOpeningBatches(ctx context.Context, legalEntityID, batchKey string, limit int, afterAt time.Time, afterID string) ([]openingimport.Batch, error)
}

var _ OpeningImporter = (*Postgres)(nil)

// ListOpeningBatches returns a legal entity's imported batches, newest first.
func (s *Postgres) ListOpeningBatches(ctx context.Context, legalEntityID, batchKey string, limit int, afterAt time.Time, afterID string) ([]openingimport.Batch, error) {
	return OpeningTables.List(ctx, s.pool, legalEntityID, batchKey, limit, afterAt, afterID)
}

// ImportOpeningAssets stores a batch all or nothing. The same key with the same content is a replay (false); the same key with other content is
// openingimport.ErrKeyConflict; a taken reference, asset number or (asset number, book) is *openingimport.TakenError whose Keys name the asset
// number or "number/book".
func (s *Postgres) ImportOpeningAssets(ctx context.Context, in OpeningImport) (openingimport.Batch, bool, error) {
	p, _ := tenant.FromContext(ctx)
	lines := make([]openingimport.Line, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = openingimport.Line{Reference: l.Reference, Measures: l.Measures()}
	}
	// An asset an earlier opening batch created takes a further book; any other asset with the number is taken.
	var existing map[string]string
	hooks := openingimport.Hooks{
		Taken: func(ctx context.Context, tx pgx.Tx) (map[string]bool, error) {
			numbers := make([]string, len(in.Lines))
			for i, l := range in.Lines {
				numbers[i] = l.AssetNumber
			}
			// Rebuilt on every attempt: Import retries after a unique violation and the assets of the rolled-back attempt are gone.
			existing = map[string]string{}
			taken := map[string]bool{}
			rows, err := tx.Query(ctx, `select a.asset_number, a.id::text, exists (select 1 from asset_opening_link k where k.tenant_id = a.tenant_id and k.asset_id = a.id)
				from asset a where a.legal_entity_id = $1::uuid and a.asset_number = any($2)`, in.LegalEntityID, numbers)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var number, id string
				var ours bool
				if err := rows.Scan(&number, &id, &ours); err != nil {
					rows.Close()
					return nil, err
				}
				if ours {
					existing[number] = id
				} else {
					taken[number] = true
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			brows, err := tx.Query(ctx, `select a.asset_number || '/' || b.book_code from asset_book b join asset a on a.tenant_id = b.tenant_id and a.id = b.asset_id
				where a.legal_entity_id = $1::uuid and a.asset_number = any($2)`, in.LegalEntityID, numbers)
			if err != nil {
				return nil, err
			}
			defer brows.Close()
			held := map[string]bool{}
			for brows.Next() {
				var k string
				if err := brows.Scan(&k); err != nil {
					return nil, err
				}
				held[k] = true
			}
			for _, l := range in.Lines {
				if k := l.AssetNumber + "/" + l.BookCode; held[k] && !taken[l.AssetNumber] {
					taken[k] = true
				}
			}
			return taken, brows.Err()
		},
		Insert: func(ctx context.Context, tx pgx.Tx, batchID string) error {
			for _, l := range in.Lines {
				if err := insertOpeningAsset(ctx, tx, p.TenantID, in, batchID, l, existing); err != nil {
					return fmt.Errorf("asset %s book %s: %w", l.AssetNumber, l.BookCode, err)
				}
			}
			return nil
		},
	}
	return OpeningTables.Import(ctx, s.pool, openingimport.Input{LegalEntityID: in.LegalEntityID, BatchKey: in.BatchKey, ContentHash: in.ContentHash, ImportedBy: in.ImportedBy, Totals: in.Totals, Lines: lines}, hooks)
}

func insertOpeningAsset(ctx context.Context, tx pgx.Tx, tenantID string, in OpeningImport, batchID string, l OpeningLine, existing map[string]string) error {
	assetID, ok := existing[l.AssetNumber]
	if !ok {
		assetID = ids.New()
		if _, err := tx.Exec(ctx, `insert into asset (tenant_id, id, legal_entity_id, asset_number, name, class_code, status, acquired_date)
			values ($1, $2, $3::uuid, $4, $5, $6, 'Open', $7::date)`, tenantID, assetID, in.LegalEntityID, l.AssetNumber, l.Name, l.ClassCode, l.AcquisitionDate); err != nil {
			return err
		}
		existing[l.AssetNumber] = assetID
	}
	elapsed := l.UsefulLife - l.RemainingLife
	inService, last, err := OpeningInService(l.Method, l.DepreciatedThrough, elapsed)
	if err != nil {
		return err
	}
	factor, convention, switchSL := "0", "None", false
	if l.Method == "DecliningBalance" {
		factor, switchSL = "2", l.SwitchToStraightLine
		if l.Factor.IsPositive() {
			factor = l.Factor.String()
		}
	}
	bookID := ids.New()
	if _, err := tx.Exec(ctx, `insert into asset_book (tenant_id, id, asset_id, legal_entity_id, book_code, currency, method, factor, switch_to_straight_line, service_life_periods,
			salvage_value, convention, in_service_date, cost, acquisition_cost, accumulated_depreciation, nbv, status, last_depreciated_period)
		values ($1, $2, $3, $4::uuid, $5, $6, $7, $8::numeric, $17, $9, $10::numeric, $11, $12::date, $13::numeric, $13::numeric, $14::numeric, $15::numeric, 'Open', $16)`,
		tenantID, bookID, assetID, in.LegalEntityID, l.BookCode, l.Currency, l.Method, factor, l.UsefulLife, l.Salvage.String(), convention,
		inService.Format("2006-01-02"), l.Cost.String(), l.Accumulated.String(), l.NBV().String(), last, switchSL); err != nil {
		return err
	}
	txID := ids.New()
	if _, err := tx.Exec(ctx, `insert into asset_transaction (tenant_id, id, asset_book_id, legal_entity_id, type, transaction_date, cost_amount, accumulated_amount, source_type, source_id)
		values ($1, $2, $3, $4::uuid, 'Acquisition', $5::date, $6::numeric, $7::numeric, 'opening', $8::uuid)`,
		tenantID, txID, bookID, in.LegalEntityID, in.OpeningDate, l.Cost.String(), l.Accumulated.String(), batchID); err != nil {
		return err
	}
	if in.Journal == OpeningJournalLinked {
		if _, err := tx.Exec(ctx, `insert into asset_gl_entry (tenant_id, transaction_id, legal_entity_id, journal_entry_id, posting_date) values ($1, $2, $3::uuid, null, $4::date)`,
			tenantID, txID, in.LegalEntityID, in.OpeningDate); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `insert into asset_opening_link (tenant_id, legal_entity_id, reference, asset_id, asset_book_id) values ($1, $2::uuid, $3, $4, $5)`,
		tenantID, in.LegalEntityID, l.Reference, assetID, bookID)
	return err
}

// OpeningNumberKey is the Keys entry of an (asset number, book) pair.
func OpeningNumberKey(assetNumber, book string) string {
	return strings.Join([]string{assetNumber, book}, "/")
}
