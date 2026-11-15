package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/print/internal/domain"
	"github.com/BenjaminSRussell/passage/services/print/internal/store"
	"github.com/BenjaminSRussell/passage/services/print/migrations"
)

const (
	leID   = "0190a5f2-7b1c-7d3e-8a4f-123456789a01"
	custA  = "0190a5f2-7b1c-7d3e-8a4f-123456789b01"
	custB  = "0190a5f2-7b1c-7d3e-8a4f-123456789b02"
	rule1  = "0190a5f2-7b1c-7d3e-8a4f-123456789c01"
	rule2  = "0190a5f2-7b1c-7d3e-8a4f-123456789c02"
	custRe = "0190a5f2-7b1c-7d3e-8a4f-123456789b03"
)

func appStore(t *testing.T) (*store.Postgres, context.Context) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	ctx := tenantCtx(1)
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(app), ctx
}

func TestDelivery_ClaimsADestinationOnceAndRetriesAFailedOne(t *testing.T) {
	s, ctx := appStore(t)
	d := store.Delivery{SourceKey: "invoice:1", RuleID: rule1, CopyKind: "ORIGINAL", Channel: "EMAIL", Destination: "ap@bluewater.example"}
	first, claimed, err := s.ClaimDelivery(ctx, d)
	if err != nil || !claimed || first.Status != store.DeliveryPending {
		t.Fatalf("first claim: %+v %v %v", first, claimed, err)
	}
	// A second caller while the first is still working does not claim it.
	if _, claimed, err = s.ClaimDelivery(ctx, d); err != nil || claimed {
		t.Fatalf("claim while pending: %v %v", claimed, err)
	}
	if err := s.FinishDelivery(ctx, first.ID, store.DeliveryFailed, "relay down"); err != nil {
		t.Fatal(err)
	}
	retry, claimed, err := s.ClaimDelivery(ctx, d)
	if err != nil || !claimed || retry.ID != first.ID {
		t.Fatalf("retry of a failed one: %+v %v %v", retry, claimed, err)
	}
	if err := s.FinishDelivery(ctx, retry.ID, store.DeliverySent, ""); err != nil {
		t.Fatal(err)
	}
	done, claimed, err := s.ClaimDelivery(ctx, d)
	if err != nil || claimed || done.Status != store.DeliverySent {
		t.Fatalf("claim of a sent one: %+v %v %v", done, claimed, err)
	}
	other := d
	other.RuleID = rule2
	if _, claimed, err = s.ClaimDelivery(ctx, other); err != nil || !claimed {
		t.Fatalf("another rule of the same document: %v %v", claimed, err)
	}
	list, err := s.ListDeliveries(ctx, "invoice:1")
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list, err = s.ListDeliveries(tenantCtx(2), "invoice:1"); err != nil || len(list) != 0 {
		t.Fatalf("another tenant sees deliveries: %+v %v", list, err)
	}
}

func TestPortalDocuments_AreScopedToTheCustomerAndTheTenant(t *testing.T) {
	s, ctx := appStore(t)
	pdf := []byte("%PDF-1.4 statement")
	a, err := s.SavePortalDocument(ctx, store.PortalDocument{SourceKey: "k1", AccountID: custA, LegalEntityID: leID, DocumentType: "PERIODIC_STATEMENT", Number: "Statement 2026-11-15", Pages: 1, PDF: pdf})
	if err != nil || a.ID == "" || a.AccountID != custA {
		t.Fatalf("save: %+v %v", a, err)
	}
	again, err := s.SavePortalDocument(ctx, store.PortalDocument{SourceKey: "k1", AccountID: custA, LegalEntityID: leID, DocumentType: "PERIODIC_STATEMENT", Number: "Statement 2026-11-15", Pages: 1, PDF: pdf})
	if err != nil || again.ID != a.ID {
		t.Fatalf("the same source key saved a second document: %+v %v", again, err)
	}
	if _, err := s.SavePortalDocument(ctx, store.PortalDocument{SourceKey: "k2", AccountID: custB, LegalEntityID: leID, DocumentType: "CUSTOMER_INVOICE", Number: "INV-9", Pages: 2, PDF: pdf}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPortalDocuments(ctx, custA, 0)
	if err != nil || len(list) != 1 || list[0].ID != a.ID || list[0].Number != "Statement 2026-11-15" {
		t.Fatalf("list of customer A: %+v %v", list, err)
	}
	got, err := s.GetPortalDocument(ctx, a.ID, custA)
	if err != nil || string(got.PDF) != string(pdf) || got.Pages != 1 {
		t.Fatalf("get: %+v %v", got, err)
	}
	var p *httpx.Problem
	if _, err := s.GetPortalDocument(ctx, a.ID, custB); !errors.As(err, &p) || p.Status != 404 {
		t.Fatalf("customer B reads A's document: %v", err)
	}
	if _, err := s.GetPortalDocument(tenantCtx(2), a.ID, custA); !errors.As(err, &p) || p.Status != 404 {
		t.Fatalf("another tenant reads the document: %v", err)
	}
}

func newSchedule() domain.StatementSchedule {
	return domain.StatementSchedule{Name: "Monthly statements", LegalEntityID: leID, SenderName: "Cedar Ridge Pumps, Inc.", Cadence: domain.CadenceMonthly, RunDay: 3, Language: "en"}
}

func TestSchedules_CreateIdempotentlyPauseAndResume(t *testing.T) {
	s, ctx := appStore(t)
	in := newSchedule()
	a, inserted, err := s.CreateSchedule(ctx, in, "k1")
	if err != nil || !inserted || !a.Enabled || a.NextRunOn <= time.Now().UTC().Format(time.DateOnly) {
		t.Fatalf("create: %+v %v %v", a, inserted, err)
	}
	b, inserted, err := s.CreateSchedule(ctx, in, "k1")
	if err != nil || inserted || b.ID != a.ID {
		t.Fatalf("replay: %+v %v %v", b, inserted, err)
	}
	changed := in
	changed.RunDay = 4
	var p *httpx.Problem
	if _, _, err := s.CreateSchedule(ctx, changed, "k1"); !errors.As(err, &p) || p.Status != 422 {
		t.Fatalf("another body under the key: %v", err)
	}
	off, err := s.SetScheduleEnabled(ctx, a.ID, false)
	if err != nil || off.Enabled {
		t.Fatalf("pause: %+v %v", off, err)
	}
	if _, err := s.SetScheduleEnabled(ctx, "0190a5f2-7b1c-7d3e-8a4f-123456789fff", true); !errors.As(err, &p) || p.Status != 404 || p.Code != domain.CodeScheduleNotFound {
		t.Fatalf("unknown schedule: %v", err)
	}
	if list, _ := s.ListSchedules(tenantCtx(2)); len(list) != 0 {
		t.Fatalf("another tenant lists schedules: %+v", list)
	}
	if err := s.DeleteSchedule(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSchedule(ctx, a.ID); !errors.As(err, &p) || p.Status != 404 {
		t.Fatalf("second delete: %v", err)
	}
}

// Two replicas that look for due schedules at the same moment receive disjoint sets, and a schedule leased by one is not offered
// to another until the lease ends.
func TestSchedules_ClaimDueIsExclusiveBetweenReplicas(t *testing.T) {
	s, ctx := appStore(t)
	for i := 0; i < 6; i++ {
		in := newSchedule()
		in.Name = "Schedule " + string(rune('A'+i))
		if _, _, err := s.CreateSchedule(ctx, in, "k"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	future := time.Now().UTC().AddDate(0, 3, 0) // everything is due by then
	var mu sync.Mutex
	got := map[string]string{}
	var wg sync.WaitGroup
	for _, owner := range []string{"replica-1", "replica-2", "replica-3"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			due, err := s.ClaimDueSchedules(ctx, future, owner, time.Minute, 4)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, d := range due {
				if prev, dup := got[d.ID]; dup {
					t.Errorf("schedule %s claimed by %s and %s", d.ID, prev, owner)
				}
				got[d.ID] = owner
			}
		}()
	}
	wg.Wait()
	if len(got) != 6 {
		t.Fatalf("claimed %d of 6 schedules", len(got))
	}
	if again, err := s.ClaimDueSchedules(ctx, future, "replica-4", time.Minute, 10); err != nil || len(again) != 0 {
		t.Fatalf("leased schedules offered again: %+v %v", again, err)
	}
	// Completing a schedule releases it and moves its date past the run date.
	var one string
	for id := range got {
		one = id
	}
	next := domain.NextRunOn(domain.CadenceMonthly, 3, future)
	if err := s.CompleteSchedule(ctx, one, future, next); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.ClaimDueSchedules(ctx, future, "replica-4", time.Minute, 10); len(again) != 0 {
		t.Fatalf("a completed schedule is due again: %+v", again)
	}
	if due, _ := s.ClaimDueSchedules(ctx, next, "replica-4", time.Minute, 10); len(due) != 1 || due[0].ID != one || due[0].LastRunOn != future.Format(time.DateOnly) {
		t.Fatalf("the completed schedule on its next date: %+v", due)
	}
	// A paused schedule is not due.
	if _, err := s.SetScheduleEnabled(ctx, one, false); err != nil {
		t.Fatal(err)
	}
}

func TestStatementRuns_AreClaimedOncePerCustomerAndDate(t *testing.T) {
	s, ctx := appStore(t)
	sc, _, err := s.CreateSchedule(ctx, newSchedule(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	ok, err := s.BeginStatement(ctx, sc.ID, day, custA)
	if err != nil || !ok {
		t.Fatalf("first: %v %v", ok, err)
	}
	if ok, err = s.BeginStatement(ctx, sc.ID, day, custA); err != nil || ok {
		t.Fatalf("second while pending: %v %v", ok, err)
	}
	if err := s.FinishStatement(ctx, sc.ID, day, custA, false, "relay down"); err != nil {
		t.Fatal(err)
	}
	if ok, err = s.BeginStatement(ctx, sc.ID, day, custA); err != nil || !ok {
		t.Fatalf("after a failure: %v %v", ok, err)
	}
	if err := s.FinishStatement(ctx, sc.ID, day, custA, true, ""); err != nil {
		t.Fatal(err)
	}
	if ok, _ = s.BeginStatement(ctx, sc.ID, day, custA); ok {
		t.Fatal("a done statement was claimed again")
	}
	if ok, _ = s.BeginStatement(ctx, sc.ID, day.AddDate(0, 0, 1), custA); !ok {
		t.Fatal("the next date was refused")
	}
	runs, err := s.ListStatementRuns(ctx, sc.ID, 0)
	if err != nil || len(runs) != 2 || runs[1].Status != "DONE" || runs[0].AsOf != "2026-11-16" {
		t.Fatalf("runs: %+v %v", runs, err)
	}
}
