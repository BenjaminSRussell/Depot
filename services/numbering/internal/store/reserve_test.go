package store_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/numbering/internal/domain"
	"github.com/BenjaminSRussell/depot/services/numbering/internal/store"
	"github.com/BenjaminSRussell/depot/services/numbering/migrations"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func clocked(t *testing.T) (*store.Postgres, context.Context, *fakeClock) {
	t.Helper()
	pool := testkit.DB(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{now: time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)}
	s := store.NewWithClock(pool, clock.Now)
	seed(t, s, ctx, "INV", domain.ModeGapFree)
	return s, ctx, clock
}

func seed(t *testing.T, s *store.Postgres, ctx context.Context, code, mode string) {
	t.Helper()
	in := store.Sequence{Code: code, Name: code, Format: code + "-{######}", LegalEntityID: testkit.TenantID(9), Mode: mode, StartFrom: 1}
	if _, err := s.Create(ctx, in, ""); err != nil {
		t.Fatal(err)
	}
}

func wantProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	var problem *httpx.Problem
	if !errors.As(err, &problem) || problem.Status != status || problem.Code != code {
		t.Fatalf("error = %v; want %d %s", err, status, code)
	}
}

func TestReserveConfirmVoidLog(t *testing.T) {
	s, ctx, _ := clocked(t)
	first, err := s.Reserve(ctx, "INV", "", domain.DefaultLease)
	if err != nil || first.Value != 1 || first.Formatted != "INV-000001" || first.Status != domain.StatusReserved {
		t.Fatalf("first reserve = %+v, %v", first, err)
	}
	second, err := s.Reserve(ctx, "INV", "", domain.DefaultLease)
	if err != nil || second.Value != 2 {
		t.Fatalf("second reserve = %+v, %v", second, err)
	}
	confirmed, err := s.Confirm(ctx, first.ID, "INV-2026-1")
	if err != nil || confirmed.Status != domain.StatusConfirmed || confirmed.DocumentRef != "INV-2026-1" {
		t.Fatalf("confirm = %+v, %v", confirmed, err)
	}
	voided, err := s.Void(ctx, second.ID, "posting rolled back")
	if err != nil || voided.Status != domain.StatusVoided {
		t.Fatalf("void = %+v, %v", voided, err)
	}
	log, err := s.Voided(ctx, "INV", "00000000-0000-0000-0000-000000000000", 50)
	if err != nil || len(log) != 1 || log[0].Value != 2 || log[0].Reason != "posting rolled back" {
		t.Fatalf("voided log = %+v, %v; want only value 2", log, err)
	}
	_, err = s.Confirm(ctx, first.ID, "again")
	wantProblem(t, err, http.StatusConflict, domain.CodeReservationState)
	_, err = s.Void(ctx, first.ID, "too late")
	wantProblem(t, err, http.StatusConflict, domain.CodeReservationState)
	_, err = s.Confirm(ctx, second.ID, "after void")
	wantProblem(t, err, http.StatusConflict, domain.CodeReservationState)
	_, err = s.Confirm(ctx, ids.New(), "unknown")
	wantProblem(t, err, http.StatusNotFound, domain.CodeReservationUnknown)
	_, err = s.Reserve(ctx, "NOPE", "", domain.DefaultLease)
	wantProblem(t, err, http.StatusNotFound, domain.CodeUnknown)
	other := tenant.With(context.Background(), principal(2))
	_, err = s.Confirm(other, second.ID, "cross tenant")
	wantProblem(t, err, http.StatusNotFound, domain.CodeReservationUnknown)
}

func TestLeaseExpiryVoidsNeverReusesFakeClock(t *testing.T) {
	s, ctx, clock := clocked(t)
	held, err := s.Reserve(ctx, "INV", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute + time.Second)
	log, err := s.Voided(ctx, "INV", "00000000-0000-0000-0000-000000000000", 50)
	if err != nil || len(log) != 1 || log[0].Value != held.Value || log[0].Reason != domain.VoidLeaseExpired {
		t.Fatalf("voided log after lease = %+v, %v", log, err)
	}
	_, err = s.Confirm(ctx, held.ID, "late")
	wantProblem(t, err, http.StatusConflict, domain.CodeLeaseExpired)
	next, err := s.Reserve(ctx, "INV", "", time.Minute)
	if err != nil || next.Value <= held.Value {
		t.Fatalf("reserve after expiry = %+v, %v; want a value above %d", next, err, held.Value)
	}
}

func TestConfirmExpiredLeaseVoidsAndLogs(t *testing.T) {
	s, ctx, clock := clocked(t)
	held, err := s.Reserve(ctx, "INV", "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Minute)
	_, err = s.Confirm(ctx, held.ID, "late")
	wantProblem(t, err, http.StatusConflict, domain.CodeLeaseExpired)
	log, err := s.Voided(ctx, "INV", "00000000-0000-0000-0000-000000000000", 50)
	if err != nil || len(log) != 1 || log[0].Value != held.Value {
		t.Fatalf("expired confirm must leave a voided log entry: %+v, %v", log, err)
	}
}

func TestReserveIdempotentReplay(t *testing.T) {
	s, ctx, _ := clocked(t)
	seed(t, s, ctx, "CRN", domain.ModeGapFree)
	first, err := s.Reserve(ctx, "INV", "key-1", domain.DefaultLease)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Reserve(ctx, "INV", "key-1", domain.DefaultLease)
	if err != nil || replay.ID != first.ID || replay.Value != first.Value {
		t.Fatalf("replay = %+v, %v; want %+v", replay, err, first)
	}
	next, err := s.Reserve(ctx, "INV", "", domain.DefaultLease)
	if err != nil || next.Value != first.Value+1 {
		t.Fatalf("a replay must not consume a number: %+v, %v", next, err)
	}
	_, err = s.Reserve(ctx, "CRN", "key-1", domain.DefaultLease)
	wantProblem(t, err, http.StatusUnprocessableEntity, store.CodeIdempotency)
}

func TestRemoteGapFreeNextRefused(t *testing.T) {
	s, ctx, _ := clocked(t)
	seed(t, s, ctx, "PTY", domain.ModeContinuous)
	p := principal(1)
	p.Service = "party"
	remote := tenant.With(context.Background(), p)
	_, err := s.Allocate(remote, "INV", "")
	wantProblem(t, err, http.StatusConflict, domain.CodeGapFreeRemote)
	if got, err := s.Allocate(remote, "PTY", ""); err != nil || got.Value != 1 {
		t.Fatalf("service principal on a continuous sequence = %+v, %v; want 200 value 1", got, err)
	}
	if got, err := s.Allocate(ctx, "INV", ""); err != nil || got.Value != 1 {
		t.Fatalf("user principal on a gap-free sequence = %+v, %v; want 200 value 1", got, err)
	}
	held, err := s.Reserve(remote, "INV", "", domain.DefaultLease)
	if err != nil || held.Value != 2 {
		t.Fatalf("service principal reserving a gap-free number = %+v, %v", held, err)
	}
}

func TestReserveFlowRunsAsAppRole(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), principal(1))
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app)
	seed(t, s, ctx, "INV", domain.ModeGapFree)
	held, err := s.Reserve(ctx, "INV", "", domain.DefaultLease)
	if err != nil {
		t.Fatalf("reserve as app role: %v", err)
	}
	if _, err := s.Confirm(ctx, held.ID, "INV-1"); err != nil {
		t.Fatalf("confirm as app role: %v", err)
	}
	other := tenant.With(context.Background(), principal(2))
	if _, err := s.Void(other, held.ID, "cross tenant"); err == nil {
		t.Fatal("tenant 2 must not void tenant 1 reservation through the app role")
	}
}
