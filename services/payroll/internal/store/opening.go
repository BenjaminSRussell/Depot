package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/openingimport"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Opening import of payroll year-to-date balances at cutover (#49). A balance is one amount per worker, tax year and pay element. Nothing is posted and
// nothing is announced: the balances are a starting point the legacy system had already paid and booked.

// OpeningTables are the batch and line tables of the year-to-date import.
var OpeningTables = openingimport.Tables{Batch: "payroll_opening_batch", Line: "payroll_opening_line"}

// Pay elements of a year-to-date balance.
const (
	ElementGross      = "GROSS"
	ElementTax        = "TAX"
	ElementDeductions = "DEDUCTIONS"
	ElementNet        = "NET"
)

// OpeningLine is one pay element of one worker's tax year.
type OpeningLine struct {
	Reference string
	WorkerID  string
	TaxYear   string
	Element   string
	Currency  string
	Amount    decimal.Decimal
}

// Key is the natural key of the balance: worker, tax year and pay element.
func (l OpeningLine) Key() string { return l.WorkerID + "/" + l.TaxYear + "/" + l.Element }

// Measures are the amounts the batch totals add up: the element's amount per currency, named element.CUR in lower case (gross.USD).
func (l OpeningLine) Measures() map[string]decimal.Decimal {
	return map[string]decimal.Decimal{OpeningMeasure(l.Element, l.Currency): l.Amount}
}

// OpeningMeasure names the total of a pay element and currency.
func OpeningMeasure(element, currency string) string {
	return map[string]string{ElementGross: "gross", ElementTax: "tax", ElementDeductions: "deductions", ElementNet: "net"}[element] + "." + currency
}

// OpeningImport is one batch.
type OpeningImport struct {
	LegalEntityID string
	BatchKey      string
	ContentHash   string
	ImportedBy    string
	Totals        map[string]string
	Lines         []OpeningLine
}

// OpeningImporter is the store of the year-to-date import.
type OpeningImporter interface {
	ImportOpeningYTD(ctx context.Context, in OpeningImport) (openingimport.Batch, bool, error)
	ListOpeningBatches(ctx context.Context, legalEntityID, batchKey string, limit int, afterAt time.Time, afterID string) ([]openingimport.Batch, error)
}

var _ OpeningImporter = (*Postgres)(nil)

// ListOpeningBatches returns a legal entity's imported batches, newest first.
func (s *Postgres) ListOpeningBatches(ctx context.Context, legalEntityID, batchKey string, limit int, afterAt time.Time, afterID string) ([]openingimport.Batch, error) {
	return OpeningTables.List(ctx, s.pool, legalEntityID, batchKey, limit, afterAt, afterID)
}

// ImportOpeningYTD stores a batch all or nothing. The same key with the same content is a replay (false); the same key with other content is
// openingimport.ErrKeyConflict; a taken reference or a balance (worker/year/element) the legal entity already holds is *openingimport.TakenError.
func (s *Postgres) ImportOpeningYTD(ctx context.Context, in OpeningImport) (openingimport.Batch, bool, error) {
	p, _ := tenant.FromContext(ctx)
	lines := make([]openingimport.Line, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = openingimport.Line{Reference: l.Reference, Measures: l.Measures()}
	}
	hooks := openingimport.Hooks{
		Taken: func(ctx context.Context, tx pgx.Tx) (map[string]bool, error) {
			workers := make([]string, len(in.Lines))
			for i, l := range in.Lines {
				workers[i] = l.WorkerID
			}
			taken := map[string]bool{}
			rows, err := tx.Query(ctx, `select worker_id::text || '/' || tax_year || '/' || element from payroll_opening_ytd where legal_entity_id = $1::uuid and worker_id = any($2::uuid[])`, in.LegalEntityID, workers)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			held := map[string]bool{}
			for rows.Next() {
				var k string
				if err := rows.Scan(&k); err != nil {
					return nil, err
				}
				held[k] = true
			}
			for _, l := range in.Lines {
				if held[l.Key()] {
					taken[l.Key()] = true
				}
			}
			return taken, rows.Err()
		},
		Insert: func(ctx context.Context, tx pgx.Tx, batchID string) error {
			b := &pgx.Batch{}
			for _, l := range in.Lines {
				b.Queue(`insert into payroll_opening_ytd (tenant_id, legal_entity_id, worker_id, tax_year, element, currency, amount, reference, batch_id)
					values ($1, $2::uuid, $3::uuid, $4, $5, $6, $7::numeric, $8, $9::uuid)`, p.TenantID, in.LegalEntityID, l.WorkerID, l.TaxYear, l.Element, l.Currency, l.Amount.String(), l.Reference, batchID)
			}
			return tx.SendBatch(ctx, b).Close()
		},
	}
	return OpeningTables.Import(ctx, s.pool, openingimport.Input{LegalEntityID: in.LegalEntityID, BatchKey: in.BatchKey, ContentHash: in.ContentHash, ImportedBy: in.ImportedBy, Totals: in.Totals, Lines: lines}, hooks)
}
