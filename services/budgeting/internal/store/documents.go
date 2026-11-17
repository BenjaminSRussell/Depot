package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/httpx"
)

// DocumentFilter narrows the document list. Empty fields match everything.
type DocumentFilter struct {
	Type string
	// Q matches the document number as typed (case-insensitive, any part); Number is the exact number.
	Q      string
	Number string
	Limit  int
}

// Document is a source document a budget check has seen, by its readable number.
type Document struct {
	ID            string `json:"id"`
	Number        string `json:"number"`
	DocumentType  string `json:"documentType"`
	LegalEntityID string `json:"legalEntityId"`
	LineCount     int    `json:"lineCount"`
	UpdatedAt     string `json:"updatedAt"`
}

// DocumentLineFilter narrows the document-line list. Empty fields match everything.
type DocumentLineFilter struct {
	DocumentID string
	// Q matches the line number, dimension or span as typed.
	Q     string
	Limit int
}

// DocumentLine is one line of a registered document.
type DocumentLine struct {
	ID             string          `json:"id"`
	DocumentID     string          `json:"documentId"`
	DocumentNumber string          `json:"documentNumber"`
	LineNo         int             `json:"lineNo"`
	DimensionKey   string          `json:"dimensionKey"`
	SpanKey        string          `json:"spanKey"`
	Amount         decimal.Decimal `json:"amount"`
}

const documentNumberSQL = `coalesce(d.number, '')`

func capLimit(n int) int {
	if n <= 0 || n > listLimit {
		return listLimit
	}
	return n
}

// ListDocuments lists registered documents, newest first.
func (s *Postgres) ListDocuments(ctx context.Context, f DocumentFilter) ([]Document, error) {
	out := []Document{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, `select d.id, `+documentNumberSQL+`, d.document_type, d.legal_entity_id, d.updated_at,
				(select count(*) from budget_document_line l where l.tenant_id = d.tenant_id and l.document_id = d.id)
			from budget_document d
			where d.tenant_id = $1 and ($2 = '' or d.document_type = $2)
			and ($3 = '' or strpos(lower(`+documentNumberSQL+`), lower($3)) > 0) and ($4 = '' or `+documentNumberSQL+` = $4)
			order by d.updated_at desc, d.id limit $5`, tenantID, f.Type, f.Q, f.Number, capLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDocument(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

func scanDocument(row pgx.Row) (Document, error) {
	var d Document
	var updated time.Time
	err := row.Scan(&d.ID, &d.Number, &d.DocumentType, &d.LegalEntityID, &updated, &d.LineCount)
	d.UpdatedAt = stamp(updated)
	return d, err
}

// GetDocument returns one registered document or BUD-0005.
func (s *Postgres) GetDocument(ctx context.Context, id string) (Document, error) {
	var d Document
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		d, err = scanDocument(tx.QueryRow(ctx, `select d.id, `+documentNumberSQL+`, d.document_type, d.legal_entity_id, d.updated_at,
				(select count(*) from budget_document_line l where l.tenant_id = d.tenant_id and l.document_id = d.id)
			from budget_document d where d.tenant_id = $1 and d.id = $2`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeDocumentNotFound, "budget document not found")
		}
		return err
	})
	return d, err
}

const documentLineSelect = `select l.id, l.document_id, ` + documentNumberSQL + `, l.line_no, l.dimension_key, l.span_key, l.amount::text
	from budget_document_line l join budget_document d on d.tenant_id = l.tenant_id and d.id = l.document_id`

func scanDocumentLine(row pgx.Row) (DocumentLine, error) {
	var l DocumentLine
	var amount string
	if err := row.Scan(&l.ID, &l.DocumentID, &l.DocumentNumber, &l.LineNo, &l.DimensionKey, &l.SpanKey, &amount); err != nil {
		return DocumentLine{}, err
	}
	l.Amount = decimal.RequireFromString(amount)
	return l, nil
}

// ListDocumentLines lists the lines of registered documents, newest document first and in line order within one.
func (s *Postgres) ListDocumentLines(ctx context.Context, f DocumentLineFilter) ([]DocumentLine, error) {
	out := []DocumentLine{}
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		rows, err := tx.Query(ctx, documentLineSelect+`
			where l.tenant_id = $1 and ($2 = '' or l.document_id::text = $2)
			and ($3 = '' or l.line_no::text = $3 or strpos(lower(l.dimension_key), lower($3)) > 0 or strpos(lower(l.span_key), lower($3)) > 0)
			order by d.updated_at desc, d.id, l.line_no limit $4`, tenantID, f.DocumentID, f.Q, capLimit(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanDocumentLine(rows)
			if err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	return out, err
}

// GetDocumentLine returns one registered document line or BUD-0005.
func (s *Postgres) GetDocumentLine(ctx context.Context, id string) (DocumentLine, error) {
	var l DocumentLine
	err := s.read(ctx, func(tx pgx.Tx, tenantID string) error {
		var err error
		l, err = scanDocumentLine(tx.QueryRow(ctx, documentLineSelect+` where l.tenant_id = $1 and l.id = $2`, tenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeDocumentNotFound, "budget document line not found")
		}
		return err
	})
	return l, err
}
