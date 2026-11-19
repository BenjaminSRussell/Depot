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

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
	cevents "github.com/BenjaminSRussell/depot/services/compliance/internal/events"
)

const (
	CodeDesignationNotFound  = "COM-1306"
	CodeDesignationUnchanged = "COM-1307"

	actionDowngrade = "DOWNGRADE"
	actionUpgrade   = "UPGRADE"

	maxDesignationList = 200
)

type DesignationChange struct {
	Class  string `json:"class"`
	Reason string `json:"reason"`
}

type DesignationFilter struct {
	Scope      string
	EntityType string
	RecordID   string
}

type DesignationHistoryEntry struct {
	AuditID    string          `json:"auditId"`
	Action     string          `json:"action"`
	Scope      string          `json:"scope"`
	Actor      string          `json:"actor"`
	OccurredAt time.Time       `json:"occurredAt"`
	Reason     string          `json:"reason"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
}

func notTenantWideProblem() error {
	return problem(http.StatusForbidden, "AUTH-0003", "a tenant-wide designation needs the tenant.administer duty, the designation duty and no legal-entity scope")
}

func enqueueDesignationChanged(ctx context.Context, tx pgx.Tx, p tenant.Principal, d Designation, previousClass string, now time.Time) error {
	return enqueueAggregate(ctx, tx, p, cevents.DesignationChanged, designationAggregate, d.DesignationID, cevents.DesignationChangedData{
		DesignationID: d.DesignationID, Scope: d.Scope, LegalEntityID: d.LegalEntityID, EntityType: d.EntityType, RecordID: d.RecordID,
		Class: d.Class, PreviousClass: previousClass, Version: d.Version, ChangedBy: p.UserID, ChangedAt: now,
	})
}

func readTx(ctx context.Context, s *Postgres, p tenant.Principal, fn func(tx pgx.Tx) error) error {
	if tenant.IsTenantAdmin(p) && p.HasDuty(domain.DutyDataDesignationRead) {
		return dbx.WithTenantWideAdminTx(ctx, s.pool, p, domain.DutyDataDesignationRead, fn)
	}
	return dbx.WithTenantTx(ctx, s.pool, fn)
}

func (s *Postgres) ChangeDesignation(ctx context.Context, id string, expected int, in DesignationChange, now time.Time) (Designation, error) {
	p, err := principal(ctx)
	if err != nil {
		return Designation{}, err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if (in.Class != domain.ClassCUI && in.Class != domain.ClassNONE) || !domain.ValidDesignationReason(in.Reason) {
		return Designation{}, problem(http.StatusBadRequest, "COM-0002", "request failed validation")
	}
	wide := dbx.TenantWide(p, domain.DutyDataDesignationWrite)
	var out Designation
	run := func(tx pgx.Tx) error {
		cur, err := scanDesignation(tx.QueryRow(ctx, `select `+designationColumns+` from data_designation where tenant_id = $1 and designation_id = $2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeDesignationNotFound, "no data designation with that id")
		}
		if err != nil {
			return err
		}
		if cur.Scope == domain.ScopeTenant && !wide {
			return notTenantWideProblem()
		}
		if cur.Version != expected {
			return problem(http.StatusPreconditionFailed, CodeVersionConflict, "the data designation changed; reload it")
		}
		if cur.Class == in.Class {
			return problem(http.StatusConflict, CodeDesignationUnchanged, "the data designation already has that class")
		}
		out, err = scanDesignation(tx.QueryRow(ctx, `update data_designation set class = $3, reason = $4, designated_by = $5, designated_at = $6, version = version + 1
			where tenant_id = $1 and designation_id = $2 returning `+designationColumns, p.TenantID, id, in.Class, in.Reason, p.UserID, now))
		if err != nil {
			return err
		}
		before, err := json.Marshal(cur)
		if err != nil {
			return err
		}
		after, err := json.Marshal(out)
		if err != nil {
			return err
		}
		action := actionUpgrade
		if in.Class == domain.ClassNONE {
			action = actionDowngrade
		}
		if _, err := tx.Exec(ctx, `insert into audit_event (tenant_id, audit_id, entity_type, entity_id, action, actor, occurred_at, reason, before, after)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb)`,
			p.TenantID, ids.New(), designationAggregate, id, action, p.UserID, now, in.Reason, string(before), string(after)); err != nil {
			return err
		}
		return enqueueDesignationChanged(ctx, tx, p, out, cur.Class, now)
	}
	if wide {
		err = dbx.WithTenantWideAdminTx(ctx, s.pool, p, domain.DutyDataDesignationWrite, run)
	} else {
		err = dbx.WithTenantTx(ctx, s.pool, run)
	}
	if err != nil {
		slog.WarnContext(ctx, "data designation change failed", "designationId", id, "tenantId", p.TenantID, "actor", p.UserID, "err", err)
		return Designation{}, err
	}
	slog.InfoContext(ctx, "data designation changed", "designationId", id, "class", out.Class, "version", out.Version, "tenantId", p.TenantID, "actor", p.UserID)
	return out, nil
}

func (s *Postgres) GetDesignation(ctx context.Context, id string) (Designation, error) {
	p, err := principal(ctx)
	if err != nil {
		return Designation{}, err
	}
	var out Designation
	err = readTx(ctx, s, p, func(tx pgx.Tx) error {
		var err error
		out, err = scanDesignation(tx.QueryRow(ctx, `select `+designationColumns+` from data_designation where tenant_id = $1 and designation_id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeDesignationNotFound, "no data designation with that id")
		}
		return err
	})
	return out, err
}

func (s *Postgres) ListDesignations(ctx context.Context, f DesignationFilter) ([]Designation, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	out := []Designation{}
	err = readTx(ctx, s, p, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+designationColumns+` from data_designation
			where tenant_id = $1 and ($2 = '' or scope = $2) and ($3 = '' or entity_type = $3) and ($4 = '' or record_id = $4)
			order by designated_at desc, designation_id limit $5`, p.TenantID, f.Scope, f.EntityType, f.RecordID, maxDesignationList)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDesignation(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Postgres) DesignationHistory(ctx context.Context, id string) ([]DesignationHistoryEntry, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	out := []DesignationHistoryEntry{}
	err = readTx(ctx, s, p, func(tx pgx.Tx) error {
		var scope string
		err := tx.QueryRow(ctx, `select scope from data_designation where tenant_id = $1 and designation_id = $2`, p.TenantID, id).Scan(&scope)
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, CodeDesignationNotFound, "no data designation with that id")
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select audit_id, action, actor, occurred_at, coalesce(reason, ''), before, after from audit_event
			where tenant_id = $1 and entity_type = $2 and entity_id = $3 order by occurred_at desc, audit_id desc`, p.TenantID, designationAggregate, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			e := DesignationHistoryEntry{Scope: scope}
			if err := rows.Scan(&e.AuditID, &e.Action, &e.Actor, &e.OccurredAt, &e.Reason, &e.Before, &e.After); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}
