package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
)

// Audit policy error codes (listed in the service README).
const (
	CodeAuditPolicyNotFound = "AUD-0720"
	CodeAuditPolicyDupe     = "AUD-0721"
	CodeAuditPolicyRetired  = "AUD-0722"
)

// AuditPolicy is a stored audit rule. Version grows with every change to the rule, so the violation keys of an
// earlier version stay what they were.
type AuditPolicy struct {
	PolicyID string           `json:"policyId"`
	Code     string           `json:"code"`
	Name     string           `json:"name"`
	RuleType string           `json:"ruleType"`
	Spec     domain.AuditSpec `json:"spec"`
	Active   bool             `json:"active"`
	// Version grows only with the rule: violation keys are built on it. RowVersion is the ETag and grows with every change.
	Version    int       `json:"version"`
	RowVersion int       `json:"rowVersion"`
	CreatedBy  string    `json:"createdBy"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// AuditPolicyInput is a new policy; the spec is validated by the caller.
type AuditPolicyInput struct {
	// PolicyID and Version register a policy under the id and version earlier audit runs already used, so a rerun keeps its
	// finding keys; both are optional (a new id, version 1).
	PolicyID string           `json:"policyId,omitempty"`
	Version  int              `json:"version,omitempty"`
	Code     string           `json:"code"`
	Name     string           `json:"name"`
	Spec     domain.AuditSpec `json:"spec"`
}

// AuditPolicyPatch changes a policy: nil fields stay as they are. A new spec makes a new version.
type AuditPolicyPatch struct {
	Name   *string           `json:"name"`
	Spec   *domain.AuditSpec `json:"spec"`
	Active *bool             `json:"active"`
}

// AuditPolicyFilter narrows the list. Empty fields match everything.
type AuditPolicyFilter struct {
	// Q matches the code or name as typed (case-insensitive, any part); Code is the exact code.
	Q          string
	Code       string
	ActiveOnly bool
	Limit      int
}

const auditPolicyColumns = `policy_id, code, name, rule_type, spec, active, version, row_version, created_by, created_at, updated_at`

func scanAuditPolicy(row pgx.Row) (AuditPolicy, error) {
	var p AuditPolicy
	var spec []byte
	if err := row.Scan(&p.PolicyID, &p.Code, &p.Name, &p.RuleType, &spec, &p.Active, &p.Version, &p.RowVersion, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return AuditPolicy{}, err
	}
	return p, json.Unmarshal(spec, &p.Spec)
}

// CreateAuditPolicy stores a policy; a code already in use is AUD-0721. A replayed Idempotency-Key returns the stored policy.
func (s *Postgres) CreateAuditPolicy(ctx context.Context, in AuditPolicyInput, key string, now time.Time) (AuditPolicy, error) {
	p, err := principal(ctx)
	if err != nil {
		return AuditPolicy{}, err
	}
	spec, err := json.Marshal(in.Spec)
	if err != nil {
		return AuditPolicy{}, err
	}
	var out AuditPolicy
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, key, "audit-policy.create", struct {
			Input AuditPolicyInput `json:"input"`
		}{in}, func() (AuditPolicy, error) {
			id, version := in.PolicyID, in.Version
			if id == "" {
				id = ids.New()
			}
			if version == 0 {
				version = 1
			}
			created, err := scanAuditPolicy(tx.QueryRow(ctx, `insert into audit_policy (tenant_id, policy_id, code, name, rule_type, spec, version, created_by, created_at, updated_at)
				values ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $9) returning `+auditPolicyColumns,
				p.TenantID, strings.ToLower(id), in.Code, strings.TrimSpace(in.Name), in.Spec.Type, string(spec), version, p.UserID, now))
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return AuditPolicy{}, problem(http.StatusConflict, CodeAuditPolicyDupe, "an audit policy with this code already exists")
			}
			return created, err
		})
		return err
	})
	return out, err
}

// GetAuditPolicy returns one policy or AUD-0720.
func (s *Postgres) GetAuditPolicy(ctx context.Context, id string) (AuditPolicy, error) {
	p, err := principal(ctx)
	if err != nil {
		return AuditPolicy{}, err
	}
	var out AuditPolicy
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanAuditPolicy(tx.QueryRow(ctx, `select `+auditPolicyColumns+` from audit_policy where tenant_id = $1 and policy_id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeAuditPolicyNotFound, "no audit policy with that id")
		}
		return err
	})
	return out, err
}

// ListAuditPolicies returns policies ordered by code.
func (s *Postgres) ListAuditPolicies(ctx context.Context, f AuditPolicyFilter) ([]AuditPolicy, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	out := []AuditPolicy{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+auditPolicyColumns+` from audit_policy
			where tenant_id = $5 and ($1 = '' or strpos(lower(code), lower($1)) > 0 or strpos(lower(name), lower($1)) > 0)
			and ($2 = '' or code = $2) and (not $3 or active)
			order by code limit $4`, f.Q, f.Code, f.ActiveOnly, f.Limit, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			policy, err := scanAuditPolicy(rows)
			if err != nil {
				return err
			}
			out = append(out, policy)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateAuditPolicy applies a patch when the version is the expected one (412 SOD-1109 otherwise). A changed spec
// is a new version; a rename or a retirement is not.
func (s *Postgres) UpdateAuditPolicy(ctx context.Context, id string, expected int, in AuditPolicyPatch, now time.Time) (AuditPolicy, error) {
	p, err := principal(ctx)
	if err != nil {
		return AuditPolicy{}, err
	}
	var out AuditPolicy
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanAuditPolicy(tx.QueryRow(ctx, `select `+auditPolicyColumns+` from audit_policy where tenant_id = $1 and policy_id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeAuditPolicyNotFound, "no audit policy with that id")
		}
		if err != nil {
			return err
		}
		if cur.RowVersion != expected {
			return problem(http.StatusPreconditionFailed, CodeVersionConflict, "the audit policy changed; reload it")
		}
		if in.Name != nil {
			cur.Name = strings.TrimSpace(*in.Name)
		}
		if in.Active != nil {
			cur.Active = *in.Active
		}
		if in.Spec != nil {
			cur.Spec, cur.RuleType = *in.Spec, in.Spec.Type
			cur.Version++
		}
		spec, err := json.Marshal(cur.Spec)
		if err != nil {
			return err
		}
		out, err = scanAuditPolicy(tx.QueryRow(ctx, `update audit_policy set name = $2, rule_type = $3, spec = $4::jsonb, active = $5, version = $6, row_version = row_version + 1, updated_at = $7
			where tenant_id = $8 and policy_id = $1 returning `+auditPolicyColumns, id, cur.Name, cur.RuleType, string(spec), cur.Active, cur.Version, now, p.TenantID))
		return err
	})
	return out, err
}
