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
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
)

const (
	needToKnowAggregate = "need_to_know_setting"
	actionEnforce       = "ENABLE"
	actionRelax         = "DISABLE"

	neverSetVersion = 1
	firstVersion    = 2

	needToKnowColumns = `setting_id, legal_entity_id, enabled, reason, changed_by, changed_at, version`
)

type NeedToKnowSetting struct {
	SettingID     string    `json:"settingId"`
	LegalEntityID string    `json:"legalEntityId,omitempty"`
	Enabled       bool      `json:"enabled"`
	Reason        string    `json:"reason"`
	ChangedBy     string    `json:"changedBy"`
	ChangedAt     time.Time `json:"changedAt"`
	Version       int       `json:"version"`
}

type NeedToKnowSettings struct {
	Tenant    *NeedToKnowSetting  `json:"tenant"`
	Overrides []NeedToKnowSetting `json:"overrides"`
}

type NeedToKnowInput struct {
	LegalEntityID   string
	Enabled         bool
	Reason          string
	ExpectedVersion int
}

func (n NeedToKnowSettings) override(legalEntityID string) *NeedToKnowSetting {
	for i := range n.Overrides {
		if n.Overrides[i].LegalEntityID == legalEntityID {
			return &n.Overrides[i]
		}
	}
	return nil
}

func (n NeedToKnowSettings) Flags(legalEntityID string) (tenantFlag, overrideFlag *bool) {
	if n.Tenant != nil {
		tenantFlag = &n.Tenant.Enabled
	}
	if o := n.override(legalEntityID); o != nil {
		overrideFlag = &o.Enabled
	}
	return tenantFlag, overrideFlag
}

func (n NeedToKnowSettings) VersionOf(legalEntityID string) int {
	target := n.Tenant
	if legalEntityID != "" {
		target = n.override(legalEntityID)
	}
	if target == nil {
		return neverSetVersion
	}
	return target.Version
}

func scanNeedToKnow(row pgx.Row) (NeedToKnowSetting, error) {
	var out NeedToKnowSetting
	var legalEntity *string
	if err := row.Scan(&out.SettingID, &legalEntity, &out.Enabled, &out.Reason, &out.ChangedBy, &out.ChangedAt, &out.Version); err != nil {
		return NeedToKnowSetting{}, err
	}
	out.LegalEntityID = deref(legalEntity)
	return out, nil
}

func needToKnowForbidden(detail string) error {
	return problem(http.StatusForbidden, "AUTH-0003", detail)
}

func (s *Postgres) GetNeedToKnow(ctx context.Context) (NeedToKnowSettings, error) {
	p, err := principal(ctx)
	if err != nil {
		return NeedToKnowSettings{}, err
	}
	out := NeedToKnowSettings{Overrides: []NeedToKnowSetting{}}
	read := func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+needToKnowColumns+` from need_to_know_setting where tenant_id = $1 order by legal_entity_id nulls first`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			row, err := scanNeedToKnow(rows)
			if err != nil {
				return err
			}
			if row.LegalEntityID == "" {
				out.Tenant = &row
			} else {
				out.Overrides = append(out.Overrides, row)
			}
		}
		return rows.Err()
	}
	if dbx.TenantWide(p, domain.DutyCUIPolicyRead) {
		err = dbx.WithTenantWideAdminTx(ctx, s.pool, p, domain.DutyCUIPolicyRead, read)
	} else {
		err = dbx.WithTenantTx(ctx, s.pool, read)
	}
	if err != nil {
		slog.WarnContext(ctx, "need-to-know settings read failed", "tenantId", p.TenantID, "actor", p.UserID, "err", err)
		return NeedToKnowSettings{}, err
	}
	return out, nil
}

func (s *Postgres) SetNeedToKnow(ctx context.Context, in NeedToKnowInput, now time.Time) (NeedToKnowSetting, error) {
	p, err := principal(ctx)
	if err != nil {
		return NeedToKnowSetting{}, err
	}
	in.Reason, in.LegalEntityID = strings.TrimSpace(in.Reason), canonicalEntity(strings.TrimSpace(in.LegalEntityID))
	if !domain.ValidDesignationReason(in.Reason) || in.ExpectedVersion < neverSetVersion ||
		(in.LegalEntityID != "" && (!ids.Valid(in.LegalEntityID) || in.LegalEntityID == ids.Nil)) {
		return NeedToKnowSetting{}, problem(http.StatusBadRequest, "COM-0002", "request failed validation")
	}
	if refusal := NeedToKnowRefusal(p, in); refusal != nil {
		slog.WarnContext(ctx, "need-to-know change refused", "tenantId", p.TenantID, "actor", p.UserID, "legalEntityId", in.LegalEntityID, "enabled", in.Enabled)
		return NeedToKnowSetting{}, refusal
	}
	var out NeedToKnowSetting
	run := func(tx pgx.Tx) error {
		cur, err := scanNeedToKnow(tx.QueryRow(ctx, `select `+needToKnowColumns+` from need_to_know_setting
			where tenant_id = $1 and legal_entity_id is not distinct from $2::uuid for update`, p.TenantID, nullable(in.LegalEntityID)))
		found := !errors.Is(err, pgx.ErrNoRows)
		if err != nil && found {
			return err
		}
		version := neverSetVersion
		if found {
			version = cur.Version
		}
		if version != in.ExpectedVersion {
			return problem(http.StatusPreconditionFailed, CodeVersionConflict, "the need-to-know setting changed; reload it")
		}
		if found {
			out, err = scanNeedToKnow(tx.QueryRow(ctx, `update need_to_know_setting set enabled = $3, reason = $4, changed_by = $5, changed_at = $6, version = version + 1
				where tenant_id = $1 and setting_id = $2 returning `+needToKnowColumns, p.TenantID, cur.SettingID, in.Enabled, in.Reason, p.UserID, now))
		} else {
			out, err = scanNeedToKnow(tx.QueryRow(ctx, `insert into need_to_know_setting (tenant_id, setting_id, legal_entity_id, enabled, reason, changed_by, changed_at, version)
				values ($1, $2, $3, $4, $5, $6, $7, $8) returning `+needToKnowColumns, p.TenantID, ids.New(), nullable(in.LegalEntityID), in.Enabled, in.Reason, p.UserID, now, firstVersion))
		}
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23505" {
			return problem(http.StatusPreconditionFailed, CodeVersionConflict, "the need-to-know setting changed; reload it")
		}
		if err != nil {
			return err
		}
		var before *string
		if found {
			raw, err := json.Marshal(cur)
			if err != nil {
				return err
			}
			text := string(raw)
			before = &text
		}
		after, err := json.Marshal(out)
		if err != nil {
			return err
		}
		action := actionRelax
		if in.Enabled {
			action = actionEnforce
		}
		_, err = tx.Exec(ctx, `insert into audit_event (tenant_id, audit_id, entity_type, entity_id, action, actor, occurred_at, reason, before, after)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb)`,
			p.TenantID, ids.New(), needToKnowAggregate, out.SettingID, action, p.UserID, now, in.Reason, before, string(after))
		return err
	}
	if dbx.TenantWide(p, domain.DutyCUIPolicyAdmin) {
		err = dbx.WithTenantWideAdminTx(ctx, s.pool, p, domain.DutyCUIPolicyAdmin, run)
	} else {
		err = dbx.WithTenantTx(ctx, s.pool, run)
	}
	if err != nil {
		slog.WarnContext(ctx, "need-to-know change failed", "tenantId", p.TenantID, "actor", p.UserID, "legalEntityId", in.LegalEntityID, "err", err)
		return NeedToKnowSetting{}, err
	}
	slog.InfoContext(ctx, "need-to-know changed", "tenantId", p.TenantID, "actor", p.UserID, "legalEntityId", out.LegalEntityID, "enabled", out.Enabled, "version", out.Version)
	return out, nil
}

func NeedToKnowRefusal(p tenant.Principal, in NeedToKnowInput) error {
	wide := dbx.TenantWide(p, domain.DutyCUIPolicyAdmin)
	switch {
	case !p.HasDuty(domain.DutyCUIPolicyAdmin):
		return needToKnowForbidden("changing need-to-know enforcement needs the cui.policy.admin duty")
	case in.LegalEntityID == "" && !wide:
		return needToKnowForbidden("a tenant-level change needs the tenant.administer duty, cui.policy.admin and no legal-entity scope")
	case in.LegalEntityID != "" && !wide && !tenant.InLegalEntityScope(p, in.LegalEntityID):
		return needToKnowForbidden("legal entity is outside the caller's scope")
	case !domain.MayChangeNeedToKnow(wide, in.Enabled):
		return needToKnowForbidden("only a tenant-wide administrator may turn need-to-know enforcement off")
	}
	return nil
}
