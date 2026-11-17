package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/analytics/internal/domain"
)

const savedQueryColumns = `query_id::text, name, measures, dimensions, lookback_days, created_at`

func scanSavedQuery(row pgx.Row) (domain.SavedQuery, error) {
	var q domain.SavedQuery
	var created time.Time
	if err := row.Scan(&q.ID, &q.Name, &q.Measures, &q.Dimensions, &q.LookbackDays, &created); err != nil {
		return q, err
	}
	if q.Dimensions == nil {
		q.Dimensions = []string{}
	}
	q.CreatedAt = created.UTC().Format(time.RFC3339)
	return q, nil
}

// SavedQueryFilter narrows a user's saved queries. Empty fields match everything.
type SavedQueryFilter struct {
	// Q matches the name as typed (case-insensitive, any part); Name is the exact name.
	Q     string
	Name  string
	Limit int
}

// SavedQueryUser is the user a saved query belongs to: the calling principal, or ANA-0112-free failure when there is none.
func savedQueryUser(ctx context.Context) (tenant.Principal, error) {
	p, ok := tenant.FromContext(ctx)
	if !ok || strings.TrimSpace(p.UserID) == "" {
		return p, &domain.Error{Code: "AUTH-0001", Status: 401, Detail: "saved queries belong to a signed-in user"}
	}
	return p, nil
}

// CreateSavedQuery stores a user's saved query under an Idempotency-Key, like CreateSchedule: a replay answers with the
// stored response. A name the user already has is ANA-0115.
func (s *Postgres) CreateSavedQuery(ctx context.Context, key, requestHash string, in domain.SavedQuery, now time.Time) (Created, error) {
	p, err := savedQueryUser(ctx)
	if err != nil {
		return Created{}, err
	}
	var out Created
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		replay, err := claimKey(ctx, tx, p.TenantID, key, requestHash)
		if err != nil || replay != nil {
			if replay != nil {
				out = *replay
			}
			return err
		}
		created, err := scanSavedQuery(tx.QueryRow(ctx, `insert into saved_query (tenant_id, query_id, user_id, name, measures, dimensions, lookback_days, created_at)
			values ($1, $2, $3, $4, $5, $6, $7, $8) returning `+savedQueryColumns,
			p.TenantID, ids.New(), p.UserID, strings.TrimSpace(in.Name), in.Measures, nonNil(in.Dimensions), in.LookbackDays, now))
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return &domain.Error{Code: domain.CodeSavedQueryDupe, Status: 409, Detail: "you already have a saved query with this name"}
		}
		if err != nil {
			return err
		}
		out, err = storeResponse(ctx, tx, key, 201, created)
		return err
	})
	return out, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ListSavedQueries returns the calling user's saved queries by name.
func (s *Postgres) ListSavedQueries(ctx context.Context, f SavedQueryFilter) ([]domain.SavedQuery, error) {
	p, err := savedQueryUser(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.SavedQuery{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+savedQueryColumns+` from saved_query
			where tenant_id = $1 and user_id = $2 and ($3 = '' or strpos(lower(name), lower($3)) > 0) and ($4 = '' or name = $4)
			order by lower(name), query_id limit $5`, p.TenantID, p.UserID, f.Q, f.Name, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			q, err := scanSavedQuery(rows)
			if err != nil {
				return err
			}
			out = append(out, q)
		}
		return rows.Err()
	})
	return out, err
}

// GetSavedQuery returns one of the calling user's saved queries, or ANA-0112 (another user's query is not found).
func (s *Postgres) GetSavedQuery(ctx context.Context, id string) (domain.SavedQuery, error) {
	p, err := savedQueryUser(ctx)
	if err != nil {
		return domain.SavedQuery{}, err
	}
	var out domain.SavedQuery
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if !ids.Valid(id) {
			return domain.NotFound("saved query " + id)
		}
		var err error
		out, err = scanSavedQuery(tx.QueryRow(ctx, `select `+savedQueryColumns+` from saved_query where tenant_id = $1 and user_id = $2 and query_id = $3`, p.TenantID, p.UserID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound("saved query " + id)
		}
		return err
	})
	return out, err
}

// DeleteSavedQuery removes one of the calling user's saved queries, or ANA-0112. A schedule that names it keeps its target
// id and fails at its next run with a plain message.
func (s *Postgres) DeleteSavedQuery(ctx context.Context, id string) error {
	p, err := savedQueryUser(ctx)
	if err != nil {
		return err
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if !ids.Valid(id) {
			return domain.NotFound("saved query " + id)
		}
		tag, err := tx.Exec(ctx, `delete from saved_query where tenant_id = $1 and user_id = $2 and query_id = $3`, p.TenantID, p.UserID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.NotFound("saved query " + id)
		}
		return nil
	})
}
