package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

var (
	// ErrIntakeReplayConflict is a sender document received again with different content.
	ErrIntakeReplayConflict = errors.New("the document was already received with different content")
	// ErrIntakeDuplicateInvoice is a vendor invoice number the legal entity already received from another document.
	ErrIntakeDuplicateInvoice = errors.New("the vendor's invoice number was already received")
	// ErrIntakeDecided is a received invoice that was already decided otherwise (posted against another invoice, or rejected).
	ErrIntakeDecided = errors.New("the received invoice was already decided")
	// ErrIntakeInvoiceUnknown is a vendor invoice that is not one of the vendor's open items in the legal entity (it is not posted yet,
	// or it is another vendor's).
	ErrIntakeInvoiceUnknown = errors.New("the vendor invoice is not posted for this vendor")
	// ErrIntakeInvoiceTaken is a vendor invoice already recorded against another received invoice.
	ErrIntakeInvoiceTaken = errors.New("the vendor invoice already closes another received invoice")
)

// IntakeLine is one line of a received vendor invoice as the vendor sent it.
type IntakeLine struct {
	LineNo    string          `json:"lineNo"`
	Item      string          `json:"item,omitempty"`
	Quantity  decimal.Decimal `json:"quantity"`
	Unit      string          `json:"unit,omitempty"`
	UnitPrice decimal.Decimal `json:"unitPrice"`
	Amount    decimal.Decimal `json:"amount"`
}

// VendorInvoiceIntake is a vendor invoice received electronically (#35, 35-4), waiting for an AP clerk to match it to receipt lines
// and post it in receiving. It posts nothing itself.
type VendorInvoiceIntake struct {
	ID                  string          `json:"id"`
	LegalEntityID       string          `json:"legalEntityId"`
	Source              string          `json:"source"`
	SourceRef           string          `json:"sourceRef"`
	VendorID            string          `json:"vendorId"`
	InvoiceNumber       string          `json:"invoiceNumber"`
	InvoiceDate         string          `json:"invoiceDate"`
	Purpose             string          `json:"purpose"`
	PurchaseOrderID     string          `json:"purchaseOrderId,omitempty"`
	PurchaseOrderNumber string          `json:"purchaseOrderNumber,omitempty"`
	Currency            string          `json:"currency"`
	Net                 decimal.Decimal `json:"net"`
	Gross               decimal.Decimal `json:"gross"`
	Lines               []IntakeLine    `json:"lines"`
	Status              string          `json:"status"`
	ReceivedBy          string          `json:"receivedBy"`
	ReceivedAt          time.Time       `json:"receivedAt"`
	// The decision of an AP clerk: POSTED with the vendor invoice posted from it, or REJECTED with the reason.
	DecidedAt       *time.Time `json:"decidedAt,omitempty"`
	DecidedBy       string     `json:"decidedBy,omitempty"`
	DecisionNote    string     `json:"decisionNote,omitempty"`
	VendorInvoiceID string     `json:"vendorInvoiceId,omitempty"`
	contentHash     string
}

// WithContentHash sets the hash of the sender's document the replay check compares.
func (v VendorInvoiceIntake) WithContentHash(h string) VendorInvoiceIntake {
	v.contentHash = h
	return v
}

const intakeColumns = `id::text, legal_entity_id::text, source, source_ref, content_hash, vendor_id::text, invoice_number, invoice_date::text, purpose,
	coalesce(purchase_order_id::text, ''), purchase_order_number, currency, net::text, gross::text, lines, status, received_by, received_at,
	decided_at, decided_by, decision_note, coalesce(vendor_invoice_id::text, '')`

func scanIntake(row pgx.Row) (VendorInvoiceIntake, error) {
	var v VendorInvoiceIntake
	var net, gross string
	var lines []byte
	if err := row.Scan(&v.ID, &v.LegalEntityID, &v.Source, &v.SourceRef, &v.contentHash, &v.VendorID, &v.InvoiceNumber, &v.InvoiceDate, &v.Purpose,
		&v.PurchaseOrderID, &v.PurchaseOrderNumber, &v.Currency, &net, &gross, &lines, &v.Status, &v.ReceivedBy, &v.ReceivedAt,
		&v.DecidedAt, &v.DecidedBy, &v.DecisionNote, &v.VendorInvoiceID); err != nil {
		return v, err
	}
	v.Net, v.Gross = decimal.RequireFromString(net), decimal.RequireFromString(gross)
	return v, json.Unmarshal(lines, &v.Lines)
}

// ReceiveVendorInvoice stores a received vendor invoice. The same sender document again returns the stored row with created false;
// with other content it is ErrIntakeReplayConflict. A vendor invoice number the legal entity already received from another document
// is ErrIntakeDuplicateInvoice.
func (s *Postgres) ReceiveVendorInvoice(ctx context.Context, in VendorInvoiceIntake) (VendorInvoiceIntake, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var (
		out     VendorInvoiceIntake
		created bool
	)
	lines, err := json.Marshal(in.Lines)
	if err != nil {
		return out, false, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		// One sender document at a time, so a concurrent repeat waits for the first and then reads it back.
		if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('ap_vendor_invoice_intake:' || $1 || ':' || $2, 0))`, in.Source, in.SourceRef); err != nil {
			return err
		}
		prev, err := scanIntake(tx.QueryRow(ctx, `select `+intakeColumns+` from ap_vendor_invoice_intake where source = $1 and source_ref = $2`, in.Source, in.SourceRef))
		switch {
		case err == nil:
			if prev.contentHash != in.contentHash {
				return ErrIntakeReplayConflict
			}
			out = prev
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		var taken bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from ap_vendor_invoice_intake where legal_entity_id = $1 and vendor_id = $2 and invoice_number = $3)`,
			in.LegalEntityID, in.VendorID, in.InvoiceNumber).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrIntakeDuplicateInvoice
		}
		out, err = scanIntake(tx.QueryRow(ctx, `insert into ap_vendor_invoice_intake (tenant_id, id, legal_entity_id, source, source_ref, content_hash, vendor_id,
			invoice_number, invoice_date, purpose, purchase_order_id, purchase_order_number, currency, net, gross, lines, received_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::uuid, $12, $13, $14, $15, $16, $17) returning `+intakeColumns,
			p.TenantID, ids.New(), in.LegalEntityID, in.Source, in.SourceRef, in.contentHash, in.VendorID, in.InvoiceNumber, in.InvoiceDate, in.Purpose,
			nullable(in.PurchaseOrderID), in.PurchaseOrderNumber, in.Currency, in.Net.String(), in.Gross.String(), lines, in.ReceivedBy))
		created = err == nil
		return err
	})
	return out, created, err
}

// IntakeFilter selects received vendor invoices of a legal entity, newest first, after the cursor (receivedAt, id). Status, when set,
// keeps only that status.
type IntakeFilter struct {
	LegalEntityID string
	VendorID      string
	Status        string
	AfterAt       time.Time
	AfterID       string
}

func (s *Postgres) ListVendorInvoiceIntake(ctx context.Context, f IntakeFilter, limit int) ([]VendorInvoiceIntake, error) {
	out := []VendorInvoiceIntake{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cursorAt any
		if !f.AfterAt.IsZero() {
			cursorAt = f.AfterAt
		}
		rows, err := tx.Query(ctx, `select `+intakeColumns+` from ap_vendor_invoice_intake
			where legal_entity_id = $1::uuid and ($2 = '' or vendor_id::text = $2) and ($3::timestamptz is null or (received_at, id) < ($3::timestamptz, $4::uuid))
			and ($6 = '' or status = $6)
			order by received_at desc, id desc limit $5`, f.LegalEntityID, f.VendorID, cursorAt, nullable(f.AfterID), min(max(limit, 1), maxLimit+1), f.Status)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanIntake(rows)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) GetVendorInvoiceIntake(ctx context.Context, id string) (VendorInvoiceIntake, error) {
	var out VendorInvoiceIntake
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanIntake(tx.QueryRow(ctx, `select `+intakeColumns+` from ap_vendor_invoice_intake where id = $1::uuid`, id))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	return out, err
}

// IntakeDecision is a clerk's decision on a received invoice: POSTED with the vendor invoice posted from it, or REJECTED with a reason.
type IntakeDecision struct {
	Status          string
	VendorInvoiceID string
	Note            string
	By              string
}

// DecideVendorInvoiceIntake records the decision on a received invoice that waits (status RECEIVED). Deciding the same way again returns the
// stored row with changed false, so a retried call is harmless; deciding otherwise is ErrIntakeDecided. A POSTED decision names a vendor
// invoice that must be an open item of the same vendor in the same legal entity (ErrIntakeInvoiceUnknown) and may close one received
// invoice only (ErrIntakeInvoiceTaken).
func (s *Postgres) DecideVendorInvoiceIntake(ctx context.Context, id string, d IntakeDecision) (VendorInvoiceIntake, bool, error) {
	var (
		out     VendorInvoiceIntake
		changed bool
	)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanIntake(tx.QueryRow(ctx, `select `+intakeColumns+` from ap_vendor_invoice_intake where id = $1::uuid for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Status != "RECEIVED" {
			if cur.Status == d.Status && cur.VendorInvoiceID == d.VendorInvoiceID && (d.Status != "REJECTED" || cur.DecisionNote == d.Note) {
				out = cur
				return nil
			}
			return ErrIntakeDecided
		}
		if d.Status == "POSTED" {
			var known bool
			if err := tx.QueryRow(ctx, `select exists(select 1 from ap_open_item where source_invoice_id = $1::uuid and vendor_id = $2::uuid and legal_entity_id = $3::uuid)`,
				d.VendorInvoiceID, cur.VendorID, cur.LegalEntityID).Scan(&known); err != nil {
				return err
			}
			if !known {
				return ErrIntakeInvoiceUnknown
			}
			var taken bool
			if err := tx.QueryRow(ctx, `select exists(select 1 from ap_vendor_invoice_intake where vendor_invoice_id = $1::uuid)`, d.VendorInvoiceID).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return ErrIntakeInvoiceTaken
			}
		}
		out, err = scanIntake(tx.QueryRow(ctx, `update ap_vendor_invoice_intake set status = $2, decided_at = now(), decided_by = $3, decision_note = $4, vendor_invoice_id = $5::uuid
			where id = $1::uuid returning `+intakeColumns, id, d.Status, d.By, d.Note, nullable(d.VendorInvoiceID)))
		changed = err == nil
		return err
	})
	return out, changed, err
}
