package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
)

const (
	CodeDesignationDupe = "COM-1302"

	designationAggregate = "data_designation"
	actionDesignate      = "DESIGNATE"
)

type Designation struct {
	DesignationID string    `json:"designationId"`
	Scope         string    `json:"scope"`
	LegalEntityID string    `json:"legalEntityId,omitempty"`
	Class         string    `json:"class"`
	Category      string    `json:"category,omitempty"`
	Reason        string    `json:"reason"`
	DesignatedBy  string    `json:"designatedBy"`
	DesignatedAt  time.Time `json:"designatedAt"`
	Version       int       `json:"version"`
}

type DesignationInput struct {
	Scope         string `json:"scope"`
	LegalEntityID string `json:"legalEntityId,omitempty"`
	Class         string `json:"class"`
	Category      string `json:"category,omitempty"`
	Reason        string `json:"reason"`
}

const designationColumns = `designation_id, scope, legal_entity_id, class, category, reason, designated_by, designated_at, version`

func scanDesignation(row pgx.Row) (Designation, error) {
	var d Designation
	var legalEntity, category *string
	if err := row.Scan(&d.DesignationID, &d.Scope, &legalEntity, &d.Class, &category, &d.Reason, &d.DesignatedBy, &d.DesignatedAt, &d.Version); err != nil {
		return Designation{}, err
	}
	d.LegalEntityID, d.Category = deref(legalEntity), deref(category)
	return d, nil
}

func validDesignation(in DesignationInput) bool {
	scopeShape := (in.Scope == domain.ScopeTenant && in.LegalEntityID == "") ||
		(in.Scope == domain.ScopeLegalEntity && ids.Valid(in.LegalEntityID) && in.LegalEntityID != ids.Nil)
	return scopeShape && in.Class == domain.ClassCUI && domain.ValidDesignationReason(in.Reason) &&
		len(in.Category) <= domain.MaxDesignationCategory
}

func (s *Postgres) Designate(ctx context.Context, in DesignationInput, key string, now time.Time) (Designation, error) {
	p, err := principal(ctx)
	if err != nil {
		return Designation{}, err
	}
	in.Reason, in.Category = strings.TrimSpace(in.Reason), strings.TrimSpace(in.Category)
	if in.LegalEntityID != "" {
		in.LegalEntityID = canonicalEntity(in.LegalEntityID)
	}
	if !validDesignation(in) {
		return Designation{}, problem(http.StatusBadRequest, "COM-0002", "request failed validation")
	}
	var out Designation
	run := func(tx pgx.Tx) error {
		request := struct {
			Input      DesignationInput `json:"input"`
			Designator string           `json:"designator"`
		}{in, p.UserID}
		var err error
		out, err = idempotent(ctx, tx, p.TenantID, key, "datadesignation.create", request, func() (Designation, error) {
			if in.Scope == domain.ScopeLegalEntity {
				if err := lockLegalEntity(ctx, tx, p.TenantID, in.LegalEntityID); err != nil {
					return Designation{}, err
				}
			}
			id := ids.New()
			created, err := scanDesignation(tx.QueryRow(ctx, `insert into data_designation (tenant_id, designation_id, scope, legal_entity_id, class, category, reason, designated_by, designated_at)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning `+designationColumns,
				p.TenantID, id, in.Scope, nullable(in.LegalEntityID), in.Class, nullable(in.Category), in.Reason, p.UserID, now))
			var pg *pgconn.PgError
			if errors.As(err, &pg) && pg.Code == "23505" {
				return Designation{}, problem(http.StatusConflict, CodeDesignationDupe, "this target already has a data designation")
			}
			if err != nil {
				return Designation{}, err
			}
			after, err := json.Marshal(created)
			if err != nil {
				return Designation{}, err
			}
			_, err = tx.Exec(ctx, `insert into audit_event (tenant_id, audit_id, entity_type, entity_id, action, actor, occurred_at, reason, before, after)
				values ($1, $2, $3, $4, $5, $6, $7, $8, null, $9::jsonb)`,
				p.TenantID, ids.New(), designationAggregate, created.DesignationID, actionDesignate, p.UserID, now, in.Reason, string(after))
			return created, err
		})
		return err
	}
	if in.Scope == domain.ScopeTenant {
		err = dbx.WithTenantWideAdminTx(ctx, s.pool, p, domain.DutyDataDesignationWrite, run)
	} else {
		err = dbx.WithTenantTx(ctx, s.pool, run)
	}
	if errors.Is(err, dbx.ErrNotTenantWide) {
		slog.WarnContext(ctx, "tenant data designation refused", "tenantId", p.TenantID, "actor", p.UserID)
		return Designation{}, problem(http.StatusForbidden, "AUTH-0003", "a tenant-wide designation needs the tenant.administer duty, the designation duty and no legal-entity scope")
	}
	if err != nil {
		slog.ErrorContext(ctx, "data designation failed", "scope", in.Scope, "tenantId", p.TenantID, "actor", p.UserID, "err", err)
		return Designation{}, err
	}
	slog.InfoContext(ctx, "data designated", "designationId", out.DesignationID, "scope", out.Scope, "class", out.Class, "tenantId", p.TenantID, "actor", p.UserID)
	return out, nil
}

func (s *Postgres) CUISource(ctx context.Context, legalEntityID string) (string, error) {
	var source string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select scope from data_designation
			where class = 'CUI' and (scope = 'TENANT' or (scope = 'LEGAL_ENTITY' and legal_entity_id = $1))
			order by scope = 'TENANT' desc limit 1`, canonicalEntity(legalEntityID)).Scan(&source)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return source, err
}
