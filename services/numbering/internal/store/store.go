package store

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/outbox"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/numbering/internal/domain"
)

const (
	CodeDuplicate   = "NUM-SEQ-DUPLICATE"
	CodeIdempotency = "idempotency_key_reuse"
	CodeAmbiguous   = "NUM-SEQ-AMBIGUOUS"
	uniqueViolation = "23505"
	allocatedType   = "dev.depot.numbering.sequence.allocated.v1"
)

// Sequence is a persisted definition.
type Sequence struct {
	ID            string `json:"id"`
	Code          string `json:"code"`
	Name          string `json:"name"`
	Format        string `json:"format"`
	LegalEntityID string `json:"legalEntityId"`
	Mode          string `json:"mode"`
	StartFrom     int64  `json:"startFrom"`
}

// Issued is one allocated number.
type Issued struct {
	Value         int64  `json:"value"`
	Formatted     string `json:"formatted"`
	Sequence      string `json:"sequence"`
	LegalEntityID string `json:"legalEntityId"`
}

// Listed is a sequence as the list shows it: the definition plus the number the next allocation will issue.
type Listed struct {
	Sequence
	// NextValue is the value the next allocation issues: one past the last issued value, or StartFrom before the first.
	NextValue int64 `json:"nextValue"`
	// NextFormatted is NextValue in the sequence's format; empty when the padded width is exhausted.
	NextFormatted string `json:"nextFormatted,omitempty"`
}

// Page is one page of the sequence list. NextCursor is empty on the last page.
type Page struct {
	Items      []Listed `json:"items"`
	NextCursor string   `json:"nextCursor,omitempty"`
}

// Store is the numbering persistence port.
type Store interface {
	Create(ctx context.Context, in Sequence, idempotencyKey string) (Sequence, error)
	Get(ctx context.Context, code string) (Sequence, error)
	List(ctx context.Context, cursor string, limit int) (Page, error)
	Allocate(ctx context.Context, code, idempotencyKey string) (Issued, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store.
type Postgres struct{ pool *pgxpool.Pool }

// New wraps pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the pool.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func scanSeq(row pgx.Row) (Sequence, error) {
	var e Sequence
	err := row.Scan(&e.ID, &e.Code, &e.Name, &e.Format, &e.LegalEntityID, &e.Mode, &e.StartFrom)
	return e, err
}

// Create inserts a sequence definition.
func (s *Postgres) Create(ctx context.Context, in Sequence, idempotencyKey string) (Sequence, error) {
	p, _ := tenant.FromContext(ctx)
	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var e Sequence
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		e, err = scanSeq(tx.QueryRow(ctx, `insert into number_sequence
			(tenant_id, id, code, name, format, legal_entity_id, mode, start_from, idempotency_key)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			on conflict (tenant_id, idempotency_key) do update set start_from = number_sequence.start_from
			returning id, code, name, format, legal_entity_id, mode, start_from`,
			p.TenantID, ids.New(), in.Code, in.Name, in.Format, in.LegalEntityID, in.Mode, in.StartFrom, key))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return Sequence{}, httpx.NewProblem(http.StatusConflict, CodeDuplicate, "a sequence with this code already exists for the legal entity")
	}
	if err == nil && key != nil && (e.Code != in.Code || e.LegalEntityID != in.LegalEntityID || e.Mode != in.Mode || e.Format != in.Format) {
		return Sequence{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotency, "Idempotency-Key was already used with a different request body")
	}
	return e, err
}

// Get loads a sequence by code in the caller's legal-entity scope.
func (s *Postgres) Get(ctx context.Context, code string) (Sequence, error) {
	p, _ := tenant.FromContext(ctx)
	var e Sequence
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		e, err = selectSequence(ctx, tx, p, code, "")
		return err
	})
	return e, err
}

// List pages through the sequences of the caller's legal-entity scope in id order, after cursor (a sequence id), with
// the next value each would issue. It reads under the same tenant and legal-entity RLS as every other query; the
// explicit scope filter repeats it so an all-scope caller sees every legal entity and a scoped caller only its own.
func (s *Postgres) List(ctx context.Context, cursor string, limit int) (Page, error) {
	p, _ := tenant.FromContext(ctx)
	entities, err := scopeEntities(p)
	if err != nil {
		return Page{}, err
	}
	// A nil scope (all-scope caller) or an empty cursor turns its filter off.
	var after *string
	if cursor != "" {
		after = &cursor
	}
	var items []Listed
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select s.id, s.code, s.name, s.format, s.legal_entity_id, s.mode, s.start_from,
				coalesce(st.next_value + 1, s.start_from)
			from number_sequence s
			left join number_sequence_state st
				on st.tenant_id = s.tenant_id and st.legal_entity_id = s.legal_entity_id and st.sequence_code = s.code
			where s.tenant_id = $1
				and ($2::uuid[] is null or s.legal_entity_id = any($2::uuid[]))
				and ($3::uuid is null or s.id > $3::uuid)
			order by s.id
			limit $4`, p.TenantID, entities, after, limit+1)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Listed, error) {
			var l Listed
			err := row.Scan(&l.ID, &l.Code, &l.Name, &l.Format, &l.LegalEntityID, &l.Mode, &l.StartFrom, &l.NextValue)
			return l, err
		})
		return err
	})
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = page.Items[limit-1].ID
	}
	if page.Items == nil {
		page.Items = []Listed{}
	}
	for i := range page.Items {
		if formatted, err := domain.FormatValue(page.Items[i].Format, page.Items[i].NextValue); err == nil {
			page.Items[i].NextFormatted = formatted
		}
	}
	return page, nil
}

// Allocate issues the next gap-free value under a row lock.
func (s *Postgres) Allocate(ctx context.Context, code, idempotencyKey string) (Issued, error) {
	p, _ := tenant.FromContext(ctx)
	var out Issued
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idempotencyKey != "" {
			var replay Issued
			err := tx.QueryRow(ctx, `select value, formatted, sequence_code, legal_entity_id from number_allocation
				where tenant_id=$1 and idempotency_key=$2`, p.TenantID, idempotencyKey).Scan(&replay.Value, &replay.Formatted, &replay.Sequence, &replay.LegalEntityID)
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		seq, err := loadForUpdate(ctx, tx, p, code)
		if err != nil {
			return err
		}
		var next int64
		err = tx.QueryRow(ctx, `insert into number_sequence_state (tenant_id, legal_entity_id, sequence_code, next_value)
			values ($1,$2,$3,$4)
			on conflict (tenant_id, legal_entity_id, sequence_code)
			do update set next_value = number_sequence_state.next_value + 1
			returning next_value`, p.TenantID, seq.LegalEntityID, seq.Code, seq.StartFrom).Scan(&next)
		if err != nil {
			return err
		}
		formatted, err := domain.FormatValue(seq.Format, next)
		if err != nil {
			return err
		}
		var key *string
		if idempotencyKey != "" {
			key = &idempotencyKey
		}
		if _, err := tx.Exec(ctx, `insert into number_allocation
			(tenant_id, id, legal_entity_id, sequence_code, value, formatted, idempotency_key)
			values ($1,$2,$3,$4,$5,$6,$7)`, p.TenantID, ids.New(), seq.LegalEntityID, seq.Code, next, formatted, key); err != nil {
			return err
		}
		evt, err := events.New(allocatedType, "depot/numbering", formatted, p.TenantID, map[string]any{
			"sequence": seq.Code, "value": next, "formatted": formatted, "legalEntityId": seq.LegalEntityID,
		})
		if err != nil {
			return err
		}
		if err := outbox.Enqueue(ctx, tx, "sequence", seq.Code, evt); err != nil {
			return err
		}
		out = Issued{Value: next, Formatted: formatted, Sequence: seq.Code, LegalEntityID: seq.LegalEntityID}
		return nil
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return Issued{}, httpx.NewProblem(http.StatusConflict, CodeDuplicate, "allocation conflict")
	}
	return out, err
}

func scopeEntities(p tenant.Principal) ([]string, error) {
	if p.LegalEntityID == "" {
		if p.HasDuty(tenant.DutyAllScope) {
			return nil, nil
		}
		return nil, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller has no legal-entity scope")
	}
	entities := tenant.LegalEntityScopeList(p)
	if len(entities) == 0 {
		return nil, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the caller's legal-entity scope holds no valid legal entity")
	}
	return entities, nil
}

func loadForUpdate(ctx context.Context, tx pgx.Tx, p tenant.Principal, code string) (Sequence, error) {
	return selectSequence(ctx, tx, p, code, " for update")
}

func selectSequence(ctx context.Context, tx pgx.Tx, p tenant.Principal, code, lock string) (Sequence, error) {
	entities, err := scopeEntities(p)
	if err != nil {
		return Sequence{}, err
	}
	q := `select id, code, name, format, legal_entity_id, mode, start_from from number_sequence
		where tenant_id=$1 and code=$2`
	args := []any{p.TenantID, code}
	if entities != nil {
		q += ` and legal_entity_id = any($3::uuid[])`
		args = append(args, entities)
	}
	rows, err := tx.Query(ctx, q+` order by legal_entity_id limit 2`+lock, args...)
	if err != nil {
		return Sequence{}, err
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Sequence, error) { return scanSeq(row) })
	if err != nil {
		return Sequence{}, err
	}
	switch len(found) {
	case 0:
		return Sequence{}, httpx.NewProblem(http.StatusNotFound, domain.CodeUnknown, "sequence not found")
	case 1:
		return found[0], nil
	}
	return Sequence{}, httpx.NewProblem(http.StatusConflict, CodeAmbiguous, "several legal entities in scope own this sequence code; narrow the request with the Depot-Legal-Entity header")
}
