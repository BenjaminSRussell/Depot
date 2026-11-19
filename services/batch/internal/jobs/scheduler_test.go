package jobs_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/batch/internal/domain"
	"github.com/BenjaminSRussell/depot/services/batch/internal/jobs"
	"github.com/BenjaminSRussell/depot/services/batch/internal/store"
)

type memSchedules struct {
	mu        sync.Mutex
	schedules []jobs.Schedule
	err       error
}

func (m *memSchedules) Due(context.Context) ([]jobs.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]jobs.Schedule(nil), m.schedules...), m.err
}

func (m *memSchedules) MarkFired(_ context.Context, sc jobs.Schedule, fired time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.schedules {
		if m.schedules[i].JobID == sc.JobID && m.schedules[i].Since.Equal(sc.Since) {
			m.schedules[i].Since = fired
			return true, nil
		}
	}
	return false, nil
}

type recordingStarter struct {
	mu        sync.Mutex
	codes     []string
	principal tenant.Principal
	err       error
}

func (r *recordingStarter) StartRun(ctx context.Context, code string, in store.RunInput) (store.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.codes = append(r.codes, code)
	r.principal, _ = tenant.FromContext(ctx)
	return store.Run{ID: "run-1"}, r.err
}

func (r *recordingStarter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.codes)
}

func at(t *testing.T, layout string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, layout)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func fiveMinuteSchedule(t *testing.T, code string) jobs.Schedule {
	return jobs.Schedule{
		TenantID: "00000000-0000-0000-0000-000000000001", JobID: "job-" + code, Code: code,
		Cron: "*/5 * * * *", TimeZone: "UTC", LegalEntityID: "00000000-0000-0000-0000-000000000009",
		ScheduledBy: "00000000-0000-0000-0000-000000000008", Duties: []string{"product.write"},
		Since: at(t, "2026-11-19T10:00:00Z"),
	}
}

func TestSchedulerFiresStoredCronAtNextMinute(t *testing.T) {
	src := &memSchedules{schedules: []jobs.Schedule{fiveMinuteSchedule(t, "close")}}
	starter := &recordingStarter{}
	s := &jobs.Scheduler{Source: src, Starter: starter}
	ctx := context.Background()

	s.Tick(ctx, at(t, "2026-11-19T10:04:59Z"))
	s.Tick(ctx, at(t, "2026-11-19T09:00:00Z"))
	if starter.count() != 0 {
		t.Fatalf("fired before the minute and with the clock moved back: %v", starter.codes)
	}
	s.Tick(ctx, at(t, "2026-11-19T10:05:00Z"))
	if starter.count() != 1 || starter.codes[0] != "close" {
		t.Fatalf("want one run of close at 10:05, got %v", starter.codes)
	}
	p := starter.principal
	if p.TenantID != src.schedules[0].TenantID || p.LegalEntityID != "00000000-0000-0000-0000-000000000009" ||
		p.UserID != "00000000-0000-0000-0000-000000000008" || !p.HasDuty("product.write") {
		t.Fatalf("run did not carry the saved binding: %+v", p)
	}
	s.Tick(ctx, at(t, "2026-11-19T10:05:30Z"))
	if starter.count() != 1 {
		t.Fatalf("the same minute fired twice: %v", starter.codes)
	}
	s.Tick(ctx, at(t, "2026-11-19T10:10:00Z"))
	if starter.count() != 2 {
		t.Fatalf("the next match did not fire: %v", starter.codes)
	}

	rec := httptest.NewRecorder()
	s.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "batch_schedule_fired_total 2") {
		t.Fatalf("metrics: %s", rec.Body.String())
	}
}

func TestSchedulerFiresOnceAfterALongOutage(t *testing.T) {
	src := &memSchedules{schedules: []jobs.Schedule{fiveMinuteSchedule(t, "close")}}
	starter := &recordingStarter{}
	s := &jobs.Scheduler{Source: src, Starter: starter}
	s.Tick(context.Background(), at(t, "2026-11-19T15:03:20Z"))
	s.Tick(context.Background(), at(t, "2026-11-19T15:03:50Z"))
	if starter.count() != 1 {
		t.Fatalf("a missed backlog must fire once, got %d", starter.count())
	}
}

func TestSchedulerTwoSchedulersFireOnce(t *testing.T) {
	src := &memSchedules{schedules: []jobs.Schedule{fiveMinuteSchedule(t, "close")}}
	starter := &recordingStarter{}
	a := &jobs.Scheduler{Source: src, Starter: starter}
	b := &jobs.Scheduler{Source: src, Starter: starter}
	var wg sync.WaitGroup
	for _, s := range []*jobs.Scheduler{a, b} {
		wg.Go(func() { s.Tick(context.Background(), at(t, "2026-11-19T10:05:00Z")) })
	}
	wg.Wait()
	if starter.count() != 1 {
		t.Fatalf("two replicas started %d runs", starter.count())
	}
}

func TestSchedulerEvaluatesTheCronInItsTimeZone(t *testing.T) {
	sc := fiveMinuteSchedule(t, "morning")
	sc.Cron, sc.TimeZone, sc.Since = "0 9 * * *", "America/New_York", at(t, "2026-11-19T13:59:00Z")
	starter := &recordingStarter{}
	s := &jobs.Scheduler{Source: &memSchedules{schedules: []jobs.Schedule{sc}}, Starter: starter}
	s.Tick(context.Background(), at(t, "2026-11-19T13:59:30Z"))
	if starter.count() != 0 {
		t.Fatal("fired before 09:00 New York")
	}
	s.Tick(context.Background(), at(t, "2026-11-19T14:00:00Z"))
	if starter.count() != 1 {
		t.Fatal("did not fire at 09:00 New York")
	}
}

func TestSchedulerSkipsABusyJobAndStillAdvances(t *testing.T) {
	src := &memSchedules{schedules: []jobs.Schedule{fiveMinuteSchedule(t, "close")}}
	starter := &recordingStarter{err: httpx.NewProblem(http.StatusConflict, domain.CodeRunOverlap, "job already has a running instance")}
	s := &jobs.Scheduler{Source: src, Starter: starter}
	s.Tick(context.Background(), at(t, "2026-11-19T10:05:00Z"))
	s.Tick(context.Background(), at(t, "2026-11-19T10:05:40Z"))
	if starter.count() != 1 {
		t.Fatalf("a skipped minute must not retry: %d starts", starter.count())
	}
	rec := httptest.NewRecorder()
	s.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, "batch_schedule_skipped_total 1") || !strings.Contains(body, "batch_schedule_errors_total 0") {
		t.Fatalf("metrics: %s", body)
	}
}

func TestSchedulerSkipsAnUnparseableCronAndFiresTheRest(t *testing.T) {
	bad := fiveMinuteSchedule(t, "bad")
	bad.Cron = "61 * * * *"
	src := &memSchedules{schedules: []jobs.Schedule{bad, fiveMinuteSchedule(t, "good")}}
	starter := &recordingStarter{}
	s := &jobs.Scheduler{Source: src, Starter: starter}
	s.Tick(context.Background(), at(t, "2026-11-19T10:05:00Z"))
	if len(starter.codes) != 1 || starter.codes[0] != "good" {
		t.Fatalf("got %v, want only good", starter.codes)
	}
	rec := httptest.NewRecorder()
	s.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "batch_schedule_errors_total 1") {
		t.Fatalf("metrics: %s", rec.Body.String())
	}
}

func TestSchedulerSurvivesAnUnavailableSource(t *testing.T) {
	src := &memSchedules{err: errors.New("database down")}
	starter := &recordingStarter{}
	s := &jobs.Scheduler{Source: src, Starter: starter}
	s.Tick(context.Background(), at(t, "2026-11-19T10:05:00Z"))
	if starter.count() != 0 {
		t.Fatal("a source error must not start runs")
	}
	rec := httptest.NewRecorder()
	s.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "batch_schedule_errors_total 1") {
		t.Fatalf("metrics: %s", rec.Body.String())
	}
}

func TestSchedulerRunTicksUntilCanceled(t *testing.T) {
	src := &memSchedules{schedules: []jobs.Schedule{fiveMinuteSchedule(t, "close")}}
	starter := &recordingStarter{}
	now := at(t, "2026-11-19T10:05:00Z")
	s := &jobs.Scheduler{Source: src, Starter: starter, Clock: func() time.Time { return now }, Interval: time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.After(2 * time.Second)
	for starter.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("run loop never fired")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
}
