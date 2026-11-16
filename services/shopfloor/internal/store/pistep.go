package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

// PI step template problem codes (see README).
const (
	CodePIStepNotFound = "SF-0192" // 404
	CodePIStepState    = "SF-0193" // 409: the template is not in a state the command needs
	CodePIStepCode     = "SF-0194" // 409: the code exists, or the code has a draft already
	CodePIStepRef      = "SF-0195" // 422: a sheet or recipe refers to a template version that does not exist or is not published
)

// PIStepTemplate is one version of a step template.
type PIStepTemplate struct {
	ID          string          `json:"id"`
	Code        string          `json:"code"`
	Version     int             `json:"version"`
	Name        string          `json:"name"`
	CategoryID  *string         `json:"categoryId"`
	Kind        string          `json:"kind"`
	Spec        json.RawMessage `json:"spec"`
	Status      string          `json:"status"`
	Rev         int             `json:"rev"`
	CreatedBy   string          `json:"createdBy"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedBy   string          `json:"updatedBy"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	PublishedBy *string         `json:"publishedBy"`
	PublishedAt *time.Time      `json:"publishedAt"`
}

// PIStepInput is what a create or update writes. The kind is the spec's category.
type PIStepInput struct {
	Code       string
	Name       string
	CategoryID *string
	Spec       domain.Instruction
}

// PIStepFilter narrows the list.
type PIStepFilter struct {
	Status     string
	Code       string
	Version    int
	CategoryID string
	Kind       string
	Limit      int
}

const piStepCols = `id::text, code, version, name, category_id::text, kind, spec, status, rev, created_by, created_at, updated_by, updated_at, published_by, published_at`

func scanPIStep(row pgx.Row) (PIStepTemplate, error) {
	var t PIStepTemplate
	err := row.Scan(&t.ID, &t.Code, &t.Version, &t.Name, &t.CategoryID, &t.Kind, &t.Spec, &t.Status, &t.Rev, &t.CreatedBy, &t.CreatedAt, &t.UpdatedBy, &t.UpdatedAt, &t.PublishedBy, &t.PublishedAt)
	return t, err
}

func piStepNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodePIStepNotFound, "PI step template not found")
}

func piStepTaken(err error, detail string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return httpx.NewProblem(http.StatusConflict, CodePIStepCode, detail)
	}
	return err
}

// piStepCategory checks a category the template is filed under: it exists and takes new templates.
func piStepCategory(ctx context.Context, tx pgx.Tx, tenantID string, categoryID *string) error {
	if categoryID == nil {
		return nil
	}
	var active bool
	err := tx.QueryRow(ctx, `select active from pi_category where tenant_id = $1 and id = $2`, tenantID, *categoryID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return piCategoryNotFound()
	}
	if err != nil {
		return err
	}
	if !active {
		return httpx.NewProblem(http.StatusUnprocessableEntity, CodePICategoryInactive, "the PI category is inactive, so no step template can be put in it")
	}
	return nil
}

// CreatePIStep stores a new template as version 1 of its code, a draft.
func (s *Postgres) CreatePIStep(ctx context.Context, in PIStepInput, by string) (PIStepTemplate, error) {
	p, _ := tenant.FromContext(ctx)
	raw, err := json.Marshal(in.Spec)
	if err != nil {
		return PIStepTemplate{}, err
	}
	var out PIStepTemplate
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := piStepCategory(ctx, tx, p.TenantID, in.CategoryID); err != nil {
			return err
		}
		var err error
		out, err = scanPIStep(tx.QueryRow(ctx, `insert into pi_step_template (tenant_id, id, code, version, name, category_id, kind, spec, status, created_by, updated_by)
			values ($1, $2, $3, 1, $4, $5, $6, $7, 'draft', $8, $8) returning `+piStepCols, p.TenantID, ids.New(), in.Code, in.Name, in.CategoryID, in.Spec.Category, raw, by))
		return piStepTaken(err, "a PI step template with the code "+in.Code+" exists; make a new version of it instead")
	})
	return out, err
}

// ListPISteps returns the templates by code and newest version first.
func (s *Postgres) ListPISteps(ctx context.Context, f PIStepFilter) ([]PIStepTemplate, error) {
	out := []PIStepTemplate{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+piStepCols+` from pi_step_template
			where ($1 = '' or status = $1) and ($2 = '' or code = $2) and ($3 = 0 or version = $3) and ($4 = '' or category_id::text = $4) and ($5 = '' or kind = $5)
			order by code, version desc limit $6`, f.Status, f.Code, f.Version, f.CategoryID, f.Kind, f.Limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanPIStep(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

// GetPIStep returns one template.
func (s *Postgres) GetPIStep(ctx context.Context, id string) (PIStepTemplate, error) {
	var out PIStepTemplate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPIStep(tx.QueryRow(ctx, `select `+piStepCols+` from pi_step_template where id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return piStepNotFound()
		}
		return err
	})
	return out, err
}

func lockPIStep(ctx context.Context, tx pgx.Tx, id string) (PIStepTemplate, error) {
	t, err := scanPIStep(tx.QueryRow(ctx, `select `+piStepCols+` from pi_step_template where id = $1 for update`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, piStepNotFound()
	}
	return t, err
}

// UpdatePIStep changes the name, category and spec of a draft when rev is the stored rev (SF-0143, 412).
func (s *Postgres) UpdatePIStep(ctx context.Context, id string, rev int, in PIStepInput, by string) (PIStepTemplate, error) {
	p, _ := tenant.FromContext(ctx)
	raw, err := json.Marshal(in.Spec)
	if err != nil {
		return PIStepTemplate{}, err
	}
	var out PIStepTemplate
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := lockPIStep(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.Status != "draft" {
			return httpx.NewProblem(http.StatusConflict, CodePIStepState, "a "+t.Status+" version cannot be edited; make a new version of the template")
		}
		if t.Rev != rev {
			return versionMismatch("PI step template")
		}
		if err := piStepCategory(ctx, tx, p.TenantID, in.CategoryID); err != nil {
			return err
		}
		out, err = scanPIStep(tx.QueryRow(ctx, `update pi_step_template set name = $2, category_id = $3, kind = $4, spec = $5, rev = rev + 1, updated_by = $6, updated_at = now()
			where id = $1 returning `+piStepCols, id, in.Name, in.CategoryID, in.Spec.Category, raw, by))
		return err
	})
	return out, err
}

// PublishPIStep makes a draft immutable and the version references resolve to. Publishing a version supersedes the
// code's earlier published version, which stays readable and referable by the sheets that carry it.
func (s *Postgres) PublishPIStep(ctx context.Context, id string, rev int, by string) (PIStepTemplate, error) {
	var out PIStepTemplate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := lockPIStep(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.Status != "draft" {
			return httpx.NewProblem(http.StatusConflict, CodePIStepState, "only a draft can be published")
		}
		if t.Rev != rev {
			return versionMismatch("PI step template")
		}
		if _, err := tx.Exec(ctx, `update pi_step_template set status = 'superseded', rev = rev + 1, updated_by = $3, updated_at = now() where code = $1 and status = 'published' and id <> $2`, t.Code, id, by); err != nil {
			return err
		}
		out, err = scanPIStep(tx.QueryRow(ctx, `update pi_step_template set status = 'published', rev = rev + 1, updated_by = $2, updated_at = now(), published_by = $2, published_at = now() where id = $1 returning `+piStepCols, id, by))
		return err
	})
	return out, err
}

// NewPIStepVersion copies the published version of a code into its next version, a draft. A code has at most one draft.
func (s *Postgres) NewPIStepVersion(ctx context.Context, id string, by string) (PIStepTemplate, error) {
	p, _ := tenant.FromContext(ctx)
	var out PIStepTemplate
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		t, err := lockPIStep(ctx, tx, id)
		if err != nil {
			return err
		}
		if t.Status != "published" {
			return httpx.NewProblem(http.StatusConflict, CodePIStepState, "a new version is made from the published version of a template")
		}
		out, err = scanPIStep(tx.QueryRow(ctx, `insert into pi_step_template (tenant_id, id, code, version, name, category_id, kind, spec, status, created_by, updated_by)
			values ($1, $2, $3, (select max(version) + 1 from pi_step_template where tenant_id = $1 and code = $3), $4, $5, $6, $7, 'draft', $8, $8) returning `+piStepCols,
			p.TenantID, ids.New(), t.Code, t.Name, t.CategoryID, t.Kind, []byte(t.Spec), by))
		return piStepTaken(err, "the PI step template "+t.Code+" has a draft already; finish or publish it first")
	})
	return out, err
}

// ResolvePISteps returns the templates the references name, by reference. A reference with no template is absent from
// the result; the caller decides what that means.
func (s *Postgres) ResolvePISteps(ctx context.Context, refs []domain.TemplateRef) (map[domain.TemplateRef]PIStepTemplate, error) {
	out := map[domain.TemplateRef]PIStepTemplate{}
	if len(refs) == 0 {
		return out, nil
	}
	codes, versions := make([]string, len(refs)), make([]int32, len(refs))
	for i, r := range refs {
		codes[i], versions[i] = r.Code, int32(r.Version)
	}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+piStepCols+` from pi_step_template t
			where (t.code, t.version) in (select * from unnest($1::text[], $2::int[]))`, codes, versions)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanPIStep(rows)
			if err != nil {
				return err
			}
			out[domain.TemplateRef{Code: t.Code, Version: t.Version}] = t
		}
		return rows.Err()
	})
	return out, err
}
