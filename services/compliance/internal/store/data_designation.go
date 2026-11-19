package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

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
	EntityType    string    `json:"entityType,omitempty"`
	RecordID      string    `json:"recordId,omitempty"`
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
	EntityType    string `json:"entityType,omitempty"`
	RecordID      string `json:"recordId,omitempty"`
	Class         string `json:"class"`
	Category      string `json:"category,omitempty"`
	Reason        string `json:"reason"`
}

type ResolveRef struct {
	EntityType    string
	RecordID      string
	LegalEntityID string
}

type ResolvedRef struct {
	EntityType string `json:"entityType"`
	RecordID   string `json:"recordId"`
	Class      string `json:"class"`
	Source     string `json:"source"`
}

const designationColumns = `designation_id, scope, legal_entity_id, entity_type, record_id, class, category, reason, designated_by, designated_at, version`

func scanDesignation(row pgx.Row) (Designation, error) {
	var d Designation
	var legalEntity, entityType, recordID, category *string
	if err := row.Scan(&d.DesignationID, &d.Scope, &legalEntity, &entityType, &recordID, &d.Class, &category, &d.Reason, &d.DesignatedBy, &d.DesignatedAt, &d.Version); err != nil {
		return Designation{}, err
	}
	d.LegalEntityID, d.EntityType, d.RecordID, d.Category = deref(legalEntity), deref(entityType), deref(recordID), deref(category)
	return d, nil
}

func validDesignation(in DesignationInput) bool {
	validLegalEntity := ids.Valid(in.LegalEntityID) && in.LegalEntityID != ids.Nil
	noRecord := in.EntityType == "" && in.RecordID == ""
	scopeShape := (in.Scope == domain.ScopeTenant && in.LegalEntityID == "" && noRecord) ||
		(in.Scope == domain.ScopeLegalEntity && validLegalEntity && noRecord) ||
		(in.Scope == domain.ScopeRecord && validLegalEntity && domain.SupportedEntityTypes[in.EntityType] && ids.Valid(in.RecordID) && in.RecordID != ids.Nil)
	return scopeShape && in.Class == domain.ClassCUI && domain.ValidDesignationReason(in.Reason) &&
		utf8.RuneCountInString(in.Category) <= domain.MaxDesignationCategory
}

func (s *Postgres) Designate(ctx context.Context, in DesignationInput, key string, now time.Time) (Designation, error) {
	p, err := principal(ctx)
	if err != nil {
		return Designation{}, err
	}
	in.Reason, in.Category = strings.TrimSpace(in.Reason), strings.TrimSpace(in.Category)
	in.EntityType = strings.ToLower(strings.TrimSpace(in.EntityType))
	in.RecordID = canonicalEntity(strings.TrimSpace(in.RecordID))
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
			if in.LegalEntityID != "" {
				if err := lockLegalEntity(ctx, tx, p.TenantID, in.LegalEntityID); err != nil {
					return Designation{}, err
				}
			}
			id := ids.New()
			created, err := scanDesignation(tx.QueryRow(ctx, `insert into data_designation (tenant_id, designation_id, scope, legal_entity_id, entity_type, record_id, class, category, reason, designated_by, designated_at)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) returning `+designationColumns,
				p.TenantID, id, in.Scope, nullable(in.LegalEntityID), nullable(in.EntityType), nullable(in.RecordID), in.Class, nullable(in.Category), in.Reason, p.UserID, now))
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
			if _, err = tx.Exec(ctx, `insert into audit_event (tenant_id, audit_id, entity_type, entity_id, action, actor, occurred_at, reason, before, after)
				values ($1, $2, $3, $4, $5, $6, $7, $8, null, $9::jsonb)`,
				p.TenantID, ids.New(), designationAggregate, created.DesignationID, actionDesignate, p.UserID, now, in.Reason, string(after)); err != nil {
				return Designation{}, err
			}
			return created, enqueueDesignationChanged(ctx, tx, p, created, "", now)
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

func (s *Postgres) Resolve(ctx context.Context, refs []ResolveRef) ([]ResolvedRef, error) {
	entities, types, records := make([]string, 0, len(refs)), make([]string, 0, len(refs)), make([]string, 0, len(refs))
	for _, ref := range refs {
		entities, types, records = append(entities, ref.LegalEntityID), append(types, ref.EntityType), append(records, ref.RecordID)
	}
	var tenantCUI bool
	legalEntityCUI, recordCUI := map[string]bool{}, map[[2]string]bool{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select scope, coalesce(legal_entity_id::text, '') from data_designation
			where class = 'CUI' and (scope = 'TENANT' or (scope = 'LEGAL_ENTITY' and legal_entity_id::text = any($1::text[])))`, entities)
		if err != nil {
			return err
		}
		for rows.Next() {
			var scope, legalEntity string
			if err := rows.Scan(&scope, &legalEntity); err != nil {
				rows.Close()
				return err
			}
			if scope == domain.ScopeTenant {
				tenantCUI = true
			} else {
				legalEntityCUI[legalEntity] = true
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `select d.entity_type, d.record_id
			from unnest($1::text[], $2::text[]) as r(entity_type, record_id)
			join data_designation d on d.entity_type = r.entity_type and d.record_id = r.record_id
			where d.scope = 'RECORD' and d.class = 'CUI'`, types, records)
		if err != nil {
			return err
		}
		for rows.Next() {
			var entityType, recordID string
			if err := rows.Scan(&entityType, &recordID); err != nil {
				rows.Close()
				return err
			}
			recordCUI[[2]string{entityType, recordID}] = true
		}
		return rows.Err()
	})
	if err != nil {
		slog.ErrorContext(ctx, "data designation resolve failed", "refs", len(refs), "err", err)
		return nil, err
	}
	out := make([]ResolvedRef, len(refs))
	for i, ref := range refs {
		class, source := domain.Effective(tenantCUI, legalEntityCUI[ref.LegalEntityID], recordCUI[[2]string{ref.EntityType, ref.RecordID}])
		out[i] = ResolvedRef{EntityType: ref.EntityType, RecordID: ref.RecordID, Class: class, Source: source}
	}
	return out, nil
}
