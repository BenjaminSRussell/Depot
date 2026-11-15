package store

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
)

const outcomeColumns = "id, code, name, active, version"

// OutcomeFilter narrows the outcome list. Empty fields match everything.
type OutcomeFilter struct {
	// Q matches the code or name as typed (case-insensitive, any part); Code is the exact code.
	Q          string
	Code       string
	ActiveOnly bool
	Limit      int
}

func scanOutcome(row pgx.Row) (domain.Outcome, error) {
	var o domain.Outcome
	err := row.Scan(&o.ID, &o.Code, &o.Name, &o.Active, &o.Version)
	return o, err
}

// CreateOutcome stores an outcome; a code already in use is QM-0191. A replayed Idempotency-Key returns the stored outcome.
func (s *Postgres) CreateOutcome(ctx context.Context, in domain.Outcome, key string) (domain.Outcome, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.Outcome
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if key != "" {
			replay, err := scanOutcome(tx.QueryRow(ctx, `select `+outcomeColumns+` from quality_outcome where tenant_id = $1 and idempotency_key = $2`, p.TenantID, key))
			if err == nil {
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		o, err := scanOutcome(tx.QueryRow(ctx, `insert into quality_outcome (tenant_id, id, code, name, active, idempotency_key)
			values ($1, $2, $3, $4, true, $5) returning `+outcomeColumns, p.TenantID, ids.New(), in.Code, in.Name, keyArg(key)))
		if isUnique(err) {
			return httpx.NewProblem(http.StatusConflict, domain.CodeOutcomeDupe, "an outcome with this code already exists")
		}
		if err != nil {
			return err
		}
		out, inserted = o, true
		return nil
	})
	return out, inserted, err
}

// GetOutcome returns one outcome or QM-0190.
func (s *Postgres) GetOutcome(ctx context.Context, id string) (domain.Outcome, error) {
	var out domain.Outcome
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanOutcome(tx.QueryRow(ctx, `select `+outcomeColumns+` from quality_outcome where `+ownTenant+` and id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.CodeOutcomeMissing, "outcome not found")
		}
		return err
	})
	return out, err
}

// ListOutcomes returns outcomes ordered by code.
func (s *Postgres) ListOutcomes(ctx context.Context, f OutcomeFilter) ([]domain.Outcome, error) {
	out := []domain.Outcome{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+outcomeColumns+` from quality_outcome
			where `+ownTenant+` and ($1 = '' or strpos(lower(code), lower($1)) > 0 or strpos(lower(name), lower($1)) > 0)
			and ($2 = '' or code = $2) and (not $3 or active)
			order by code limit $4`, f.Q, f.Code, f.ActiveOnly, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOutcome(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateOutcome renames or retires an outcome when its version is the expected one (412 conflict_version otherwise).
func (s *Postgres) UpdateOutcome(ctx context.Context, id string, expectedVersion int, name *string, active *bool) (domain.Outcome, error) {
	var out domain.Outcome
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanOutcome(tx.QueryRow(ctx, `select `+outcomeColumns+` from quality_outcome where `+ownTenant+` and id = $1 for update`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound(domain.CodeOutcomeMissing, "outcome not found")
		}
		if err != nil {
			return err
		}
		if cur.Version != expectedVersion {
			return httpx.NewProblem(http.StatusPreconditionFailed, CodeVersionConflict, fmt.Sprintf("current version is %d, not %d", cur.Version, expectedVersion))
		}
		if name != nil {
			cur.Name = *name
		}
		if active != nil {
			cur.Active = *active
		}
		out, err = scanOutcome(tx.QueryRow(ctx, `update quality_outcome set name = $3, active = $4, version = version + 1
			where `+ownTenant+` and id = $1 and version = $2 returning `+outcomeColumns, id, expectedVersion, cur.Name, cur.Active))
		return err
	})
	return out, err
}

// OutcomeIDsByCode maps outcome codes to ids; codes the master does not hold are absent.
func (s *Postgres) OutcomeIDsByCode(ctx context.Context, codes []string) (map[string]string, error) {
	out := map[string]string{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select code, id::text from quality_outcome where `+ownTenant+` and code = any($1) and active`, codes)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var code, id string
			if err := rows.Scan(&code, &id); err != nil {
				return err
			}
			out[code] = id
		}
		return rows.Err()
	})
	return out, err
}
