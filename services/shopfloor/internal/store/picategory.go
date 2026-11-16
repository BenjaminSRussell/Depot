package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// PI category problem codes (see README).
const (
	CodePICategoryNotFound = "SF-0190" // 404
	CodePICategoryInactive = "SF-0191" // 422: a template cannot be put in an inactive category
)

// PICategory groups PI step templates.
type PICategory struct {
	ID          string    `json:"id"`
	Code        string    `json:"code"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Active      bool      `json:"active"`
	Version     int       `json:"version"`
	CreatedBy   string    `json:"createdBy"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// PICategoryInput is what a create or update writes. The code never changes after creation.
type PICategoryInput struct {
	Code        string
	Name        string
	Description string
	Active      bool
}

const piCategoryColumns = `id::text, code, name, description, active, version, created_by, created_at, updated_at`

func scanPICategory(row pgx.Row) (PICategory, error) {
	var c PICategory
	err := row.Scan(&c.ID, &c.Code, &c.Name, &c.Description, &c.Active, &c.Version, &c.CreatedBy, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

func piCategoryNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodePICategoryNotFound, "PI category not found")
}

// CreatePICategory stores a category under the Idempotency-Key. A code already used is SF-0142.
func (s *Postgres) CreatePICategory(ctx context.Context, in PICategoryInput, createdBy string, idem Idem) (PICategory, error) {
	p, _ := tenant.FromContext(ctx)
	var out PICategory
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPICategory(tx.QueryRow(ctx, `insert into pi_category (tenant_id, id, code, name, description, active, created_by)
			values ($1, $2, $3, $4, $5, $6, $7) returning `+piCategoryColumns, p.TenantID, ids.New(), in.Code, in.Name, in.Description, in.Active, createdBy))
		if err != nil {
			return codeTaken(err)
		}
		return remember(ctx, tx, idem, http.StatusCreated, out)
	})
	return out, err
}

// UpdatePICategory replaces a category's name, description and status when version is the stored version (SF-0143, 412).
func (s *Postgres) UpdatePICategory(ctx context.Context, id string, version int, in PICategoryInput, _ string) (PICategory, error) {
	p, _ := tenant.FromContext(ctx)
	var out PICategory
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var cur int
		err := tx.QueryRow(ctx, `select version from pi_category where tenant_id = $1 and id = $2 for update`, p.TenantID, id).Scan(&cur)
		if errors.Is(err, pgx.ErrNoRows) {
			return piCategoryNotFound()
		}
		if err != nil {
			return err
		}
		if cur != version {
			return versionMismatch("PI category")
		}
		out, err = scanPICategory(tx.QueryRow(ctx, `update pi_category set name = $3, description = $4, active = $5, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2 returning `+piCategoryColumns, p.TenantID, id, in.Name, in.Description, in.Active))
		return err
	})
	return out, err
}

// GetPICategory returns a category, or SF-0190 (404).
func (s *Postgres) GetPICategory(ctx context.Context, id string) (PICategory, error) {
	p, _ := tenant.FromContext(ctx)
	var out PICategory
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPICategory(tx.QueryRow(ctx, `select `+piCategoryColumns+` from pi_category where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return piCategoryNotFound()
		}
		return err
	})
	return out, err
}

// ListPICategories returns the categories by code; activeOnly drops inactive ones.
func (s *Postgres) ListPICategories(ctx context.Context, activeOnly bool, limit int) ([]PICategory, error) {
	p, _ := tenant.FromContext(ctx)
	out := []PICategory{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+piCategoryColumns+` from pi_category where tenant_id = $1 and ($2 = false or active) order by code, id limit $3`, p.TenantID, activeOnly, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanPICategory(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}
