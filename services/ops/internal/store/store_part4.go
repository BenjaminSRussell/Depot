package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
)

// Part 4: tenant decommission (with its audit trail) and masked-clone schedules.

const EventTenantDecommissioned = "dev.passage.ops.tenant.decommissioned.v1"

// DecommissionStep is one reported step of a decommission.
type DecommissionStep struct {
	Order      int       `json:"order"`
	Action     string    `json:"action"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail"`
	ReportedBy string    `json:"reportedBy"`
	RecordedAt time.Time `json:"recordedAt"`
}

// Decommission is a tenant's decommission with its plan and the steps reported so far.
type Decommission struct {
	TenantID    string             `json:"tenantId"`
	Reason      string             `json:"reason"`
	Status      string             `json:"status"`
	Plan        []string           `json:"plan"`
	Steps       []DecommissionStep `json:"steps"`
	RequestedBy string             `json:"requestedBy"`
	CreatedAt   time.Time          `json:"createdAt"`
	CompletedAt *time.Time         `json:"completedAt,omitempty"`
}

// TenantDecommissioned is the payload of tenant.decommissioned.
type TenantDecommissioned struct {
	TenantID         string    `json:"tenantId"`
	Reason           string    `json:"reason"`
	DecommissionedAt time.Time `json:"decommissionedAt"`
}

const decommissionCols = `customer_tenant::text, reason, status, requested_by, created_at, completed_at`

func scanDecommission(row pgx.Row) (Decommission, error) {
	var d Decommission
	err := row.Scan(&d.TenantID, &d.Reason, &d.Status, &d.RequestedBy, &d.CreatedAt, &d.CompletedAt)
	d.Plan = domain.DecommissionSteps
	d.Steps = []DecommissionStep{}
	return d, err
}

func audit(ctx context.Context, tx pgx.Tx, actor, action, subject string, detail map[string]any) error {
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into ops_audit (actor, action, subject, detail) values ($1, $2, $3, $4::jsonb)`, actor, action, subject, b)
	return err
}

// OpenDecommission records the decommission of a tenant that has a provision. The caller has checked that identity reports the
// tenant suspended. A repeat returns the first record (replay true), so the call is idempotent per tenant. The request is audited.
func (s *Postgres) OpenDecommission(ctx context.Context, tenantID, reason, user string) (Decommission, bool, error) {
	replay := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from ops_tenant_provision where customer_tenant = $1::uuid)`, tenantID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return &domain.Error{Code: domain.CodeProvisionUnknown, Detail: "no provision is recorded for tenant " + tenantID}
		}
		tag, err := tx.Exec(ctx, `insert into ops_tenant_decommission (customer_tenant, reason, requested_by) values ($1::uuid, $2, $3) on conflict do nothing`, tenantID, reason, user)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			replay = true
			return nil
		}
		return audit(ctx, tx, user, "tenant.decommission.requested", tenantID, map[string]any{"reason": reason})
	})
	if err != nil {
		return Decommission{}, false, Problem(err)
	}
	d, err := s.Decommission(ctx, tenantID)
	return d, replay, err
}

// Decommission reads a tenant's decommission with its steps.
func (s *Postgres) Decommission(ctx context.Context, tenantID string) (Decommission, error) {
	d, err := scanDecommission(s.pool.QueryRow(ctx, `select `+decommissionCols+` from ops_tenant_decommission where customer_tenant = $1::uuid`, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return d, Problem(&domain.Error{Code: domain.CodeDecommissionUnknown, Detail: "no decommission is recorded for tenant " + tenantID})
	}
	if err != nil {
		return d, err
	}
	rows, err := s.pool.Query(ctx, `select ord, action, status, detail, reported_by, recorded_at from ops_tenant_decommission_step where customer_tenant = $1::uuid order by ord`, tenantID)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var st DecommissionStep
		if err := rows.Scan(&st.Order, &st.Action, &st.Status, &st.Detail, &st.ReportedBy, &st.RecordedAt); err != nil {
			return d, err
		}
		d.Steps = append(d.Steps, st)
	}
	return d, rows.Err()
}

// RecordDecommissionStep stores one step result. Steps run in DecommissionSteps order: a step is DONE only when the one before it is
// (OPS-1733), so the realm is never removed while a database still holds the tenant. A DONE step is kept; a FAILED one is replaced
// by a later result. Each report is audited, and when the last step turns DONE the decommission is DECOMMISSIONED and
// tenant.decommissioned is written in the same transaction.
func (s *Postgres) RecordDecommissionStep(ctx context.Context, tenantID, action, status, detail, user string) (Decommission, error) {
	order := 0
	for i, a := range domain.DecommissionSteps {
		if a == action {
			order = i + 1
		}
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		d, err := scanDecommission(tx.QueryRow(ctx, `select `+decommissionCols+` from ops_tenant_decommission where customer_tenant = $1::uuid for update`, tenantID))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeDecommissionUnknown, Detail: "no decommission is recorded for tenant " + tenantID}
		}
		if err != nil {
			return err
		}
		if status == "DONE" && order > 1 {
			var prev int
			if err := tx.QueryRow(ctx, `select count(*) from ops_tenant_decommission_step where customer_tenant = $1::uuid and ord = $2 and status = 'DONE'`, tenantID, order-1).Scan(&prev); err != nil {
				return err
			}
			if prev == 0 {
				return &domain.Error{Code: domain.CodeDecommissionOrder, Detail: "step " + action + " follows " + domain.DecommissionSteps[order-2] + ", which is not DONE: a decommission runs in reverse order"}
			}
		}
		if _, err := tx.Exec(ctx, `insert into ops_tenant_decommission_step (customer_tenant, action, ord, status, detail, reported_by)
			values ($1::uuid, $2, $3, $4, $5, $6)
			on conflict (customer_tenant, action) do update set status = excluded.status, detail = excluded.detail, reported_by = excluded.reported_by, recorded_at = now()
			where ops_tenant_decommission_step.status <> 'DONE'`, tenantID, action, order, status, detail, user); err != nil {
			return err
		}
		if err := audit(ctx, tx, user, "tenant.decommission.step", tenantID, map[string]any{"step": action, "status": status, "detail": detail}); err != nil {
			return err
		}
		if status == "FAILED" {
			_, err := tx.Exec(ctx, `update ops_tenant_decommission set status = 'FAILED' where customer_tenant = $1::uuid and status = 'DECOMMISSIONING'`, tenantID)
			return err
		}
		var done int
		if err := tx.QueryRow(ctx, `select count(*) from ops_tenant_decommission_step where customer_tenant = $1::uuid and status = 'DONE'`, tenantID).Scan(&done); err != nil {
			return err
		}
		if done < len(domain.DecommissionSteps) {
			_, err := tx.Exec(ctx, `update ops_tenant_decommission set status = 'DECOMMISSIONING' where customer_tenant = $1::uuid and status = 'FAILED'
				and not exists (select 1 from ops_tenant_decommission_step where customer_tenant = $1::uuid and status = 'FAILED')`, tenantID)
			return err
		}
		var at time.Time
		err = tx.QueryRow(ctx, `update ops_tenant_decommission set status = 'DECOMMISSIONED', completed_at = now() where customer_tenant = $1::uuid
			and status <> 'DECOMMISSIONED' returning completed_at`, tenantID).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := audit(ctx, tx, user, "tenant.decommissioned", tenantID, map[string]any{"reason": d.Reason}); err != nil {
			return err
		}
		return enqueue(ctx, tx, "tenant", tenantID, EventTenantDecommissioned, tenantID, user, TenantDecommissioned{TenantID: tenantID, Reason: d.Reason, DecommissionedAt: at.UTC()})
	})
	if err != nil {
		return Decommission{}, Problem(err)
	}
	return s.Decommission(ctx, tenantID)
}

// AuditEntry is one row of the ops audit trail.
type AuditEntry struct {
	ID      int64          `json:"id"`
	At      time.Time      `json:"at"`
	Actor   string         `json:"actor"`
	Action  string         `json:"action"`
	Subject string         `json:"subject"`
	Detail  map[string]any `json:"detail"`
}

// Audit lists the audit entries about a subject (a tenant id), oldest first.
func (s *Postgres) Audit(ctx context.Context, subject string) ([]AuditEntry, error) {
	rows, err := s.pool.Query(ctx, `select id, at, actor, action, subject, detail from ops_audit where subject = $1 order by id limit 1000`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var raw []byte
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.Subject, &raw); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(raw, &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- Masked-clone schedules ----

// CloneRun is one run of a schedule.
type CloneRun struct {
	ID         int64     `json:"id"`
	ScheduleID string    `json:"scheduleId"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail"`
}

const scheduleCols = `id::text, name, source_tenant::text, schema_name, masks_file, interval_hours, enabled, next_run_at, last_run_at, coalesce(last_status, ''), created_by, created_at`

func scanSchedule(row pgx.Row) (domain.CloneSchedule, error) {
	var c domain.CloneSchedule
	err := row.Scan(&c.ID, &c.Name, &c.SourceTenant, &c.Schema, &c.MasksFile, &c.IntervalHours, &c.Enabled, &c.NextRunAt, &c.LastRunAt, &c.LastStatus, &c.CreatedBy, &c.CreatedAt)
	return c, err
}

// CreateCloneSchedule stores a validated schedule; its first run is due now. A second schedule with the same name is OPS-1737.
func (s *Postgres) CreateCloneSchedule(ctx context.Context, c domain.CloneSchedule, user string, now time.Time) (domain.CloneSchedule, error) {
	id := ids.New()
	c2, err := scanSchedule(s.pool.QueryRow(ctx, `insert into ops_clone_schedule (id, name, source_tenant, schema_name, masks_file, interval_hours, next_run_at, created_by)
		values ($1::uuid, $2, $3::uuid, $4, $5, $6, $7, $8) returning `+scheduleCols, id, c.Name, c.SourceTenant, c.Schema, c.MasksFile, c.IntervalHours, now, user))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return c, Problem(&domain.Error{Code: domain.CodeScheduleConflict, Detail: "a clone schedule named " + c.Name + " already exists"})
	}
	return c2, err
}

// CloneSchedules lists the schedules by name.
func (s *Postgres) CloneSchedules(ctx context.Context) ([]domain.CloneSchedule, error) {
	rows, err := s.pool.Query(ctx, `select `+scheduleCols+` from ops_clone_schedule order by name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CloneSchedule{}
	for rows.Next() {
		c, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetCloneScheduleEnabled turns a schedule on or off; turning it on makes it due now.
func (s *Postgres) SetCloneScheduleEnabled(ctx context.Context, id string, enabled bool, now time.Time) (domain.CloneSchedule, error) {
	c, err := scanSchedule(s.pool.QueryRow(ctx, `update ops_clone_schedule set enabled = $2, next_run_at = case when $2 and not enabled then $3 else next_run_at end
		where id = $1::uuid returning `+scheduleCols, id, enabled, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, Problem(&domain.Error{Code: domain.CodeScheduleUnknown, Detail: "no clone schedule " + id})
	}
	return c, err
}

// DeleteCloneSchedule removes a schedule and its runs.
func (s *Postgres) DeleteCloneSchedule(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `delete from ops_clone_schedule where id = $1::uuid`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return Problem(&domain.Error{Code: domain.CodeScheduleUnknown, Detail: "no clone schedule " + id})
	}
	return nil
}

// ClaimDueClones returns the enabled schedules due at now and moves each one's next_run_at forward by its interval in the same
// statement, under a row lock that skips rows another replica holds, so a schedule is claimed (and run) by one replica per period.
func (s *Postgres) ClaimDueClones(ctx context.Context, now time.Time) ([]domain.CloneSchedule, error) {
	rows, err := s.pool.Query(ctx, `update ops_clone_schedule set next_run_at = $1::timestamptz + make_interval(hours => interval_hours)
		where id in (select id from ops_clone_schedule where enabled and next_run_at <= $1 order by next_run_at for update skip locked)
		returning `+scheduleCols, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CloneSchedule{}
	for rows.Next() {
		c, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RecordCloneRun stores the outcome of a run and the schedule's last status.
func (s *Postgres) RecordCloneRun(ctx context.Context, scheduleID string, started, finished time.Time, status, detail string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `insert into ops_clone_run (schedule_id, started_at, finished_at, status, detail) values ($1::uuid, $2, $3, $4, $5)`,
			scheduleID, started, finished, status, detail); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `update ops_clone_schedule set last_run_at = $2, last_status = $3 where id = $1::uuid`, scheduleID, finished, status)
		return err
	})
}

// CloneRuns lists a schedule's latest runs, newest first (at most 50).
func (s *Postgres) CloneRuns(ctx context.Context, scheduleID string) ([]CloneRun, error) {
	rows, err := s.pool.Query(ctx, `select id, schedule_id::text, started_at, finished_at, status, detail from ops_clone_run where schedule_id = $1::uuid order by id desc limit 50`, scheduleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CloneRun{}
	for rows.Next() {
		var r CloneRun
		if err := rows.Scan(&r.ID, &r.ScheduleID, &r.StartedAt, &r.FinishedAt, &r.Status, &r.Detail); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
