package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
)

// Purge is the record that authorises, and then reports, the deletion of a decommissioned tenant's data (#50). A decommission keeps
// the data under the retention policy; the purge needs the decommission DECOMMISSIONED and a verified export on record.
type Purge struct {
	TenantID         string     `json:"tenantId"`
	ExportID         string     `json:"exportId"`
	ExportNote       string     `json:"exportNote,omitempty"`
	ExportVerifiedBy string     `json:"exportVerifiedBy"`
	ExportVerifiedAt time.Time  `json:"exportVerifiedAt"`
	Status           string     `json:"status,omitempty"` // "", REQUESTED, DONE or FAILED
	Detail           string     `json:"detail,omitempty"`
	RequestedBy      string     `json:"requestedBy,omitempty"`
	RequestedAt      *time.Time `json:"requestedAt,omitempty"`
	CompletedAt      *time.Time `json:"completedAt,omitempty"`
}

const purgeCols = `customer_tenant::text, export_id, export_note, export_verified_by, export_verified_at, coalesce(status, ''), detail, coalesce(requested_by, ''), requested_at, completed_at`

func scanPurge(row pgx.Row) (Purge, error) {
	var p Purge
	err := row.Scan(&p.TenantID, &p.ExportID, &p.ExportNote, &p.ExportVerifiedBy, &p.ExportVerifiedAt, &p.Status, &p.Detail, &p.RequestedBy, &p.RequestedAt, &p.CompletedAt)
	return p, err
}

func noPurge(tenantID string) error {
	return &domain.Error{Code: domain.CodePurgeNoExport, Detail: "no verified export is recorded for tenant " + tenantID + ": record it with PUT /v1/ops/tenants/{id}/export-verification"}
}

// decommissioned checks, in tx, that the tenant's decommission is DECOMMISSIONED.
func decommissioned(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var status string
	err := tx.QueryRow(ctx, `select status from ops_tenant_decommission where customer_tenant = $1::uuid`, tenantID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return &domain.Error{Code: domain.CodeDecommissionUnknown, Detail: "no decommission is recorded for tenant " + tenantID}
	}
	if err != nil {
		return err
	}
	if status != "DECOMMISSIONED" {
		return &domain.Error{Code: domain.CodePurgeNotReady, Detail: "the decommission is " + status + ": every decommission step must be DONE before data is purged"}
	}
	return nil
}

// VerifyExport records that the tenant's export was verified, by whom and when. It needs the decommission DECOMMISSIONED. A repeat
// replaces the export id and note while no purge has been requested; once one has, the marker is fixed.
func (s *Postgres) VerifyExport(ctx context.Context, tenantID, exportID, note, user string) (Purge, error) {
	if exportID == "" || len(exportID) > 200 || len(note) > 1000 {
		return Purge{}, Problem(&domain.Error{Code: domain.CodePurgeInvalid, Detail: "give the export id (at most 200 characters) and an optional note (at most 1000)"})
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := decommissioned(ctx, tx, tenantID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `insert into ops_tenant_purge (customer_tenant, export_id, export_note, export_verified_by) values ($1::uuid, $2, $3, $4)
			on conflict (customer_tenant) do update set export_id = excluded.export_id, export_note = excluded.export_note,
			  export_verified_by = excluded.export_verified_by, export_verified_at = now()
			where ops_tenant_purge.status is null`, tenantID, exportID, note, user)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return &domain.Error{Code: domain.CodePurgeNotReady, Detail: "a purge was already requested for this tenant: its export marker is fixed"}
		}
		return audit(ctx, tx, user, "tenant.export.verified", tenantID, map[string]any{"exportId": exportID, "note": note})
	})
	if err != nil {
		return Purge{}, Problem(err)
	}
	return s.Purge(ctx, tenantID)
}

// Purge reads the purge record.
func (s *Postgres) Purge(ctx context.Context, tenantID string) (Purge, error) {
	p, err := scanPurge(s.pool.QueryRow(ctx, `select `+purgeCols+` from ops_tenant_purge where customer_tenant = $1::uuid`, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return p, Problem(&domain.Error{Code: domain.CodePurgeUnknown, Detail: "no export or purge is recorded for tenant " + tenantID})
	}
	return p, err
}

// RequestPurge authorises the purge: the decommission is DECOMMISSIONED and the export is verified. It is audited, and may be repeated
// after a FAILED result to retry; a DONE purge stays DONE.
func (s *Postgres) RequestPurge(ctx context.Context, tenantID, user string) (Purge, error) {
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := decommissioned(ctx, tx, tenantID); err != nil {
			return err
		}
		var status *string
		err := tx.QueryRow(ctx, `select status from ops_tenant_purge where customer_tenant = $1::uuid for update`, tenantID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return noPurge(tenantID)
		}
		if err != nil {
			return err
		}
		if status != nil && *status == "DONE" {
			return &domain.Error{Code: domain.CodePurgeNotReady, Detail: "the tenant's data was already purged"}
		}
		if _, err := tx.Exec(ctx, `update ops_tenant_purge set status = 'REQUESTED', requested_by = $2, requested_at = now(), detail = '', completed_at = null where customer_tenant = $1::uuid`, tenantID, user); err != nil {
			return err
		}
		return audit(ctx, tx, user, "tenant.purge.requested", tenantID, map[string]any{})
	})
	if err != nil {
		return Purge{}, Problem(err)
	}
	return s.Purge(ctx, tenantID)
}

// RecordPurgeResult stores the outcome of a requested purge (DONE or FAILED, with the per-database detail) and audits it.
func (s *Postgres) RecordPurgeResult(ctx context.Context, tenantID, status, detail, user string) (Purge, error) {
	if status != "DONE" && status != "FAILED" {
		return Purge{}, Problem(&domain.Error{Code: domain.CodePurgeInvalid, Detail: "status must be DONE or FAILED"})
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update ops_tenant_purge set status = $2, detail = $3, completed_at = now() where customer_tenant = $1::uuid and status = 'REQUESTED'`, tenantID, status, detail)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return &domain.Error{Code: domain.CodePurgeNotReady, Detail: "no purge is requested for tenant " + tenantID + ": request it first (POST /v1/ops/tenants/{id}:purge)"}
		}
		return audit(ctx, tx, user, "tenant.purge."+map[string]string{"DONE": "completed", "FAILED": "failed"}[status], tenantID, map[string]any{"detail": detail})
	})
	if err != nil {
		return Purge{}, Problem(err)
	}
	return s.Purge(ctx, tenantID)
}
