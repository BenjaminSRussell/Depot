package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/openingimport"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/production/internal/domain"
)

// Opening import of open production orders and their work in progress at cutover (#49). Nothing is announced: no order event, no explosion of a BOM or
// route, no release, because the orders were planned, released and partly consumed in the legacy system. Each order is Started with its open quantity.

// Journal choices of an opening batch, and the posting type of the clearing credit.
const (
	OpeningJournalPost   = "post"
	OpeningJournalLinked = "linked"
	PostOpeningClearing  = "opening-clearing"
	// NilEntryID marks WIP rows of a linked opening batch as booked without a ledger entry.
	NilEntryID = "00000000-0000-0000-0000-000000000000"
)

// OpeningTables are the batch and line tables of the WIP import.
var OpeningTables = openingimport.Tables{Batch: "production_opening_batch", Line: "production_opening_line"}

// OpeningLine is one open production order at the cutover.
type OpeningLine struct {
	Reference    string
	OrderNumber  string
	ProductID    string
	SiteID       string
	WarehouseID  string
	Currency     string
	QuantityOpen decimal.Decimal
	DeliveryDate string
	Material     decimal.Decimal
	Labor        decimal.Decimal
	Overhead     decimal.Decimal
}

// WIP is the work in progress: the three cost components.
func (l OpeningLine) WIP() decimal.Decimal { return l.Material.Add(l.Labor).Add(l.Overhead) }

// Measures are the amounts the batch totals add up, per currency.
func (l OpeningLine) Measures() map[string]decimal.Decimal {
	return map[string]decimal.Decimal{"material." + l.Currency: l.Material, "labor." + l.Currency: l.Labor, "overhead." + l.Currency: l.Overhead, "wipValue." + l.Currency: l.WIP()}
}

// OpeningImport is one batch.
type OpeningImport struct {
	LegalEntityID string
	BatchKey      string
	ContentHash   string
	ImportedBy    string
	// OpeningDate is the cutover date the opening journals are dated.
	OpeningDate string
	// Journal is post (the WIP rows are booked by POST /v1/production/opening-journal) or linked (the rows are marked booked with no entry, for a
	// ledger whose gl_balances load already carries the WIP account).
	Journal string
	Totals  map[string]string
	Lines   []OpeningLine
}

// OpeningProfileError says the WIP cannot be booked (no posting profile resolves for a site, or no opening clearing account is set); nothing was stored.
type OpeningProfileError struct{ Detail string }

func (e *OpeningProfileError) Error() string { return e.Detail }

// OpeningImporter is the store of the WIP import.
type OpeningImporter interface {
	ImportOpeningWIP(ctx context.Context, in OpeningImport) (openingimport.Batch, bool, error)
	ListOpeningBatches(ctx context.Context, legalEntityID, batchKey string, limit int, afterAt time.Time, afterID string) ([]openingimport.Batch, error)
}

var _ OpeningImporter = (*Postgres)(nil)

// ListOpeningBatches returns a legal entity's imported batches, newest first.
func (s *Postgres) ListOpeningBatches(ctx context.Context, legalEntityID, batchKey string, limit int, afterAt time.Time, afterID string) ([]openingimport.Batch, error) {
	return OpeningTables.List(ctx, s.pool, legalEntityID, batchKey, limit, afterAt, afterID)
}

// ImportOpeningWIP stores a batch all or nothing. The same key with the same content is a replay (false); the same key with other content is
// openingimport.ErrKeyConflict; a taken reference or order number is *openingimport.TakenError; a site without a posting profile is *OpeningProfileError.
func (s *Postgres) ImportOpeningWIP(ctx context.Context, in OpeningImport) (openingimport.Batch, bool, error) {
	p, _ := tenant.FromContext(ctx)
	lines := make([]openingimport.Line, len(in.Lines))
	for i, l := range in.Lines {
		lines[i] = openingimport.Line{Reference: l.Reference, Measures: l.Measures()}
	}
	clearing := s.openingClearing
	hooks := openingimport.Hooks{
		Taken: func(ctx context.Context, tx pgx.Tx) (map[string]bool, error) {
			numbers := make([]string, len(in.Lines))
			for i, l := range in.Lines {
				numbers[i] = l.OrderNumber
			}
			taken := map[string]bool{}
			rows, err := tx.Query(ctx, `select order_number from production_order where legal_entity_id = $1::uuid and order_number = any($2)`, in.LegalEntityID, numbers)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			for rows.Next() {
				var n string
				if err := rows.Scan(&n); err != nil {
					return nil, err
				}
				taken[n] = true
			}
			return taken, rows.Err()
		},
		Insert: func(ctx context.Context, tx pgx.Tx, batchID string) error {
			if clearing == "" {
				return &OpeningProfileError{Detail: "no opening clearing account is configured (PRODUCTION_OPENING_CLEARING_ACCOUNT): the opening WIP is credited to it"}
			}
			profiles := map[string]domain.Profile{}
			for _, l := range in.Lines {
				if _, ok := profiles[l.SiteID]; ok {
					continue
				}
				prof, err := resolveProfile(ctx, tx, p.TenantID, orderHead{legalEntityID: in.LegalEntityID, siteID: l.SiteID})
				if err != nil {
					return &OpeningProfileError{Detail: fmt.Sprintf("no posting profile resolves for site %s: the opening WIP cannot be booked until the legal entity has one (PUT /v1/production/posting-profiles)", l.SiteID)}
				}
				profiles[l.SiteID] = prof
			}
			for _, l := range in.Lines {
				if err := insertOpeningOrder(ctx, tx, p.TenantID, in, batchID, l, profiles[l.SiteID], clearing); err != nil {
					return fmt.Errorf("order %s: %w", l.OrderNumber, err)
				}
			}
			return nil
		},
	}
	return OpeningTables.Import(ctx, s.pool, openingimport.Input{LegalEntityID: in.LegalEntityID, BatchKey: in.BatchKey, ContentHash: in.ContentHash, ImportedBy: in.ImportedBy, Totals: in.Totals, Lines: lines}, hooks)
}

func insertOpeningOrder(ctx context.Context, tx pgx.Tx, tenantID string, in OpeningImport, batchID string, l OpeningLine, prof domain.Profile, clearing string) error {
	orderID := ids.New()
	if _, err := tx.Exec(ctx, `insert into production_order (tenant_id, id, legal_entity_id, order_number, product_id, site_id, warehouse_id, currency, quantity, status,
			delivery_date, origin_type, idempotency_key, request_hash)
		values ($1, $2, $3::uuid, $4, $5::uuid, $6::uuid, $7::uuid, $8, $9::numeric, 'Started', nullif($10, '')::date, 'Manual', $11, $12)`,
		tenantID, orderID, in.LegalEntityID, l.OrderNumber, l.ProductID, l.SiteID, l.WarehouseID, l.Currency, l.QuantityOpen.String(), l.DeliveryDate,
		"opening:"+in.LegalEntityID+":"+l.Reference, in.ContentHash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `insert into production_status_log (tenant_id, id, legal_entity_id, order_id, command, from_status, to_status, actor, idempotency_key, request_hash)
		values ($1, $2, $3::uuid, $4, 'opening-import', 'Created', 'Started', $5, $6, $7)`,
		tenantID, ids.New(), in.LegalEntityID, orderID, in.ImportedBy, "opening-status:"+in.LegalEntityID+":"+l.Reference, in.ContentHash); err != nil {
		return err
	}
	number, err := nextJournalNumber(ctx, tx, tenantID, in.LegalEntityID)
	if err != nil {
		return err
	}
	journalID := ids.New()
	if _, err := tx.Exec(ctx, `insert into production_journal (tenant_id, id, legal_entity_id, order_id, journal_type, journal_number, posting_date, posted, posted_at, posted_by,
			idempotency_key, request_hash, created_by)
		values ($1, $2, $3::uuid, $4, 'opening-wip', $5, $6::date, true, now(), $7, $8, $9, $7)`,
		tenantID, journalID, in.LegalEntityID, orderID, number, in.OpeningDate, in.ImportedBy, "opening-journal:"+in.LegalEntityID+":"+l.Reference, in.ContentHash); err != nil {
		return err
	}
	h := orderHead{id: orderID, legalEntityID: in.LegalEntityID, currency: l.Currency}
	var postings []domain.Posting
	for _, c := range []struct {
		amount  decimal.Decimal
		wipType string
	}{{l.Material, domain.PostWIPMaterial}, {l.Labor, domain.PostWIPLabor}, {l.Overhead, domain.PostWIPOverhead}} {
		if !c.amount.IsPositive() {
			continue
		}
		// The material and the conversion cost were issued in the legacy system: the balance is the WIP the migration brings in, so one clearing
		// account takes the credit, not the inventory or absorption accounts (which carry the migrated stock and the legacy period's absorption).
		postings = append(postings, domain.Posting{Type: c.wipType, Account: prof.WIP, WIP: true, Dr: c.amount}, domain.Posting{Type: PostOpeningClearing, Account: clearing, Cr: c.amount})
	}
	if err := insertPostings(ctx, tx, tenantID, h, &journalID, in.OpeningDate, postings); err != nil {
		return err
	}
	if in.Journal == OpeningJournalLinked {
		// Marked booked with the nil entry id: no ledger entry is made, the general ledger already holds the balance.
		for _, q := range []string{`update production_wip_posting set journal_entry_id = $3::uuid where tenant_id = $1 and journal_id = $2`, `update production_journal set journal_entry_id = $3::uuid where tenant_id = $1 and id = $2`} {
			if _, err := tx.Exec(ctx, q, tenantID, journalID, NilEntryID); err != nil {
				return err
			}
		}
	}
	return nil
}
