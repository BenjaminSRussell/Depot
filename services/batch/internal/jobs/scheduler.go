package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	platformjobs "github.com/BenjaminSRussell/depot/platform/jobs"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/batch/internal/domain"
	"github.com/BenjaminSRussell/depot/services/batch/internal/store"
)

const DefaultScheduleInterval = 30 * time.Second

type Schedule struct {
	TenantID      string
	JobID         string
	Code          string
	Cron          string
	TimeZone      string
	LegalEntityID string
	ScheduledBy   string
	Duties        []string
	Since         time.Time
}

type ScheduleSource interface {
	Due(ctx context.Context) ([]Schedule, error)
	MarkFired(ctx context.Context, s Schedule, fired time.Time) (bool, error)
}

type RunStarter interface {
	StartRun(ctx context.Context, code string, in store.RunInput) (store.Run, error)
}

type PgScheduleSource struct{ pool *pgxpool.Pool }

func NewPgScheduleSource(pool *pgxpool.Pool) *PgScheduleSource { return &PgScheduleSource{pool: pool} }

func (s *PgScheduleSource) Due(ctx context.Context) ([]Schedule, error) {
	rows, err := s.pool.Query(ctx, `select tenant_id::text, job_id::text, code, cron, time_zone, legal_entity_id::text, scheduled_by::text, duties, since
		from batch_due_schedules()`)
	if err != nil {
		return nil, fmt.Errorf("find schedules: %w", err)
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		if err := rows.Scan(&sc.TenantID, &sc.JobID, &sc.Code, &sc.Cron, &sc.TimeZone, &sc.LegalEntityID, &sc.ScheduledBy, &sc.Duties, &sc.Since); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *PgScheduleSource) MarkFired(ctx context.Context, sc Schedule, fired time.Time) (bool, error) {
	var moved bool
	err := s.pool.QueryRow(ctx, `select batch_mark_schedule_fired($1::uuid, $2::uuid, $3, $4)`, sc.TenantID, sc.JobID, sc.Since, fired).Scan(&moved)
	return moved, err
}

type Scheduler struct {
	Source   ScheduleSource
	Starter  RunStarter
	Clock    func() time.Time
	Interval time.Duration

	fired   atomic.Int64
	skipped atomic.Int64
	errs    atomic.Int64
}

func (s *Scheduler) Run(ctx context.Context) {
	interval := s.Interval
	if interval <= 0 {
		interval = DefaultScheduleInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.Tick(ctx, s.now())
		select {
		case <-ctx.Done():
			slog.Info("batch scheduler stopping")
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

func (s *Scheduler) Tick(ctx context.Context, now time.Time) {
	due, err := s.Source.Due(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.errs.Add(1)
			slog.Error("batch scheduler scan failed", "err", err)
		}
		return
	}
	for _, sc := range due {
		if ctx.Err() != nil {
			return
		}
		s.fire(ctx, sc, now)
	}
}

func (s *Scheduler) fire(ctx context.Context, sc Schedule, now time.Time) {
	log := slog.With("jobCode", sc.Code, "tenantId", sc.TenantID, "correlationId", "batch-schedule-"+sc.JobID)
	next, err := nextFire(sc)
	if err != nil {
		s.errs.Add(1)
		log.Error("batch schedule is unusable", "err", err, "cron", sc.Cron, "timeZone", sc.TimeZone)
		return
	}
	if next.After(now) {
		return
	}
	moved, err := s.Source.MarkFired(ctx, sc, now.Truncate(time.Minute))
	if err != nil {
		s.errs.Add(1)
		log.Error("batch schedule could not be marked fired", "err", err)
		return
	}
	if !moved {
		return
	}
	runCtx := tenant.With(ctx, tenant.Principal{TenantID: sc.TenantID, LegalEntityID: sc.LegalEntityID, UserID: sc.ScheduledBy, Service: "batch", Duties: dutySet(sc.Duties)})
	_, err = s.Starter.StartRun(runCtx, sc.Code, store.RunInput{TriggeredBy: sc.ScheduledBy})
	var prob *httpx.Problem
	switch {
	case errors.As(err, &prob) && prob.Status == http.StatusConflict && (prob.Code == domain.CodeRunOverlap || prob.Code == domain.CodeJobHold):
		s.skipped.Add(1)
		log.Info("batch scheduled run skipped", "reason", prob.Code)
	case err != nil:
		s.errs.Add(1)
		log.Error("batch scheduled run could not start", "err", err)
	default:
		s.fired.Add(1)
		log.Info("batch scheduled run started")
	}
}

func nextFire(sc Schedule) (time.Time, error) {
	spec, err := platformjobs.ParseCron(sc.Cron)
	if err != nil {
		return time.Time{}, err
	}
	loc, err := time.LoadLocation(sc.TimeZone)
	if err != nil {
		return time.Time{}, err
	}
	return spec.Next(sc.Since.In(loc))
}

func (s *Scheduler) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, m := range []struct {
			name, help string
			value      int64
		}{
			{"batch_schedule_fired_total", "Scheduled runs this replica started.", s.fired.Load()},
			{"batch_schedule_skipped_total", "Scheduled minutes skipped because the job was busy or on hold.", s.skipped.Load()},
			{"batch_schedule_errors_total", "Schedule scans, unusable schedules and run starts that failed.", s.errs.Load()},
		} {
			_, _ = fmt.Fprintf(w, "# HELP %[1]s %[2]s\n# TYPE %[1]s counter\n%[1]s %[3]d\n", m.name, m.help, m.value)
		}
	})
}
