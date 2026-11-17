package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
)

const (
	companyA = "00000000-0000-0000-0000-0000000000a1"
	companyB = "00000000-0000-0000-0000-0000000000b2"
)

func inCompany(tenantID, legalEntity string, duties ...string) context.Context {
	p := tenant.Principal{TenantID: tenantID, LegalEntityID: legalEntity, UserID: "user-test", Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return tenant.With(context.Background(), p)
}

// B-03b (D17): a worker belongs to the legal entity that employs it, and its time and pay follow it. A caller scoped
// to company B sees nothing of company A's worker, registrations, days or pay, cannot book time on it, and its payroll
// export carries only company B's lines.
func TestLegalEntity_WorkerTimeAndPayStayInTheirCompany(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(91)
	ctxA, ctxB := inCompany(tenantID, companyA), inCompany(tenantID, companyB)

	workerA := mustWorker(t, s, ctxA, "LE-A", nil)
	if workerA.LegalEntityID == nil || *workerA.LegalEntityID != companyA {
		t.Fatalf("worker legal entity = %v, want %s", workerA.LegalEntityID, companyA)
	}
	workerB := mustWorker(t, s, ctxB, "LE-B", nil)
	for _, w := range []struct {
		ctx    context.Context
		worker string
	}{{ctxA, workerA.ID}, {ctxB, workerB.ID}} {
		if _, err := s.CreateRegistrations(w.ctx, []Registration{clock(w.worker, "clock-in", day(9, 0)), clock(w.worker, "clock-out", day(17, 0))}); err != nil {
			t.Fatal(err)
		}
		if err := calculate(t, s, w.ctx, w.worker, "2026-10-20", idem("calc-"+w.worker)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ApprovePay(w.ctx, w.worker, "2026-10-20", "2026-10-20", "supervisor-1", idem("approve-"+w.worker)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.GetWorker(ctxB, workerA.ID); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("company B reads company A's worker: %v", err)
	}
	list, err := s.ListWorkers(ctxB, false, 100)
	if err != nil || len(list) != 1 || list[0].ID != workerB.ID {
		t.Fatalf("company B lists %+v, %v; want only its own worker", list, err)
	}
	if regs, err := s.ListRegistrations(ctxB, workerA.ID, day(0, 0), day(23, 0), 100); err != nil || len(regs) != 0 {
		t.Fatalf("company B lists company A's registrations: %d, %v", len(regs), err)
	}
	if days, err := s.ListWorkerDays(ctxB, DayFilter{WorkerID: workerA.ID, FromDate: "2026-10-01", ToDate: "2026-10-31", Limit: 100}); err != nil || len(days) != 0 {
		t.Fatalf("company B lists company A's days: %d, %v", len(days), err)
	}
	if res, err := s.CreateRegistrations(ctxB, []Registration{clock(workerA.ID, "clock-in", day(18, 0))}); err == nil && len(res) == 1 && res[0].Status == StatusAccepted {
		t.Fatal("company B booked time on company A's worker")
	}

	exportB, err := s.ExportPay(ctxB, "2026-10-20", "2026-10-20", "payroll-b", idem("export-b"))
	if err != nil {
		t.Fatal(err)
	}
	if exportB.LineCount != 1 {
		t.Fatalf("company B's export has %d lines, want its worker's one base line", exportB.LineCount)
	}
	if _, err := s.GetPayExport(ctxA, exportB.BatchID); problemCode(err) != CodeExportNotFound {
		t.Fatalf("company A reads company B's export: %v", err)
	}
	exportA, err := s.ExportPay(ctxA, "2026-10-20", "2026-10-20", "payroll-a", idem("export-a"))
	if err != nil || exportA.LineCount != 1 {
		t.Fatalf("company A's export = %+v, %v; want one line, not company B's", exportA, err)
	}

	// Pay lines and exported lines follow the worker even for a direct query in company B's scope.
	err = pgx.BeginFunc(context.Background(), app, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `select set_config('depot.tenant_id', $1, true), set_config('depot.legal_entity_scopes', $2, true)`, tenantID, companyB); err != nil {
			return err
		}
		for table, want := range map[string]int{"worker": 1, "time_registration": 2, "worker_day": 1, "pay_line": 1, "pay_export_line": 1, "pay_export_batch": 1} {
			var n int
			if err := tx.QueryRow(context.Background(), `select count(*) from `+table).Scan(&n); err != nil {
				return err
			}
			if n != want {
				t.Errorf("company B sees %d rows of %s, want %d", n, table, want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A worker is created in the caller's legal entity, or in a requested one within the caller's scope; a caller with
// no legal entity cannot create one.
func TestLegalEntity_CreateWorkerUsesTheCallersLegalEntity(t *testing.T) {
	s, _, _ := setup(t)
	tenantID := testkit.TenantID(92)
	both := inCompany(tenantID, companyA+","+companyB)
	newWorker := func(ctx context.Context, requested *string) error {
		_, err := s.CreateWorker(ctx, Worker{PartyID: ids.New(), Name: "W", CostRate: decimal.NewFromInt(20), Currency: "USD", LegalEntityID: requested}, idem(ids.New()))
		return err
	}
	if err := newWorker(both, nil); problemCode(err) != "COM-0001" {
		t.Fatalf("a caller in two companies without a legalEntityId: %v, want COM-0001", err)
	}
	b := companyB
	if err := newWorker(both, &b); err != nil {
		t.Fatalf("a caller in two companies naming one: %v", err)
	}
	if err := newWorker(inCompany(tenantID, companyA), &b); problemCode(err) != "AUTH-0003" {
		t.Fatalf("a caller in company A naming company B: %v, want AUTH-0003", err)
	}
	if err := newWorker(inCompany(tenantID, "", tenant.DutyTenantAdmin), nil); problemCode(err) != "AUTH-0003" {
		t.Fatalf("a caller with no legal entity: %v, want AUTH-0003", err)
	}
}

// A worker created before workers carried a legal entity is visible to no scoped caller until a tenant administrator
// assigns its legal entity, once; after that it never changes.
func TestLegalEntity_TenantAdminAssignsALegacyWorkerOnce(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(93)
	legacy := ids.New()
	if _, err := admin.Exec(context.Background(), `insert into worker (tenant_id, id, party_id, name, cost_rate, currency) values ($1, $2, $3, 'Legacy', 20, 'USD')`, tenantID, legacy, ids.New()); err != nil {
		t.Fatal(err)
	}
	ctxA := inCompany(tenantID, companyA, "shopfloor.setup.maintain")
	if _, err := s.GetWorker(ctxA, legacy); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("a scoped caller sees a worker with no legal entity: %v", err)
	}
	if _, err := s.AssignWorkerLegalEntity(ctxA, legacy, companyA, "shopfloor.setup.maintain", idem("assign-scoped")); !errors.Is(err, dbx.ErrNotTenantWide) {
		t.Fatalf("a scoped caller assigns a legal entity: %v, want ErrNotTenantWide", err)
	}
	tenantAdmin := inCompany(tenantID, "", tenant.DutyTenantAdmin, "shopfloor.setup.maintain")
	w, err := s.AssignWorkerLegalEntity(tenantAdmin, legacy, companyA, "shopfloor.setup.maintain", idem("assign"))
	if err != nil || w.LegalEntityID == nil || *w.LegalEntityID != companyA || w.Version != 2 {
		t.Fatalf("tenant administrator assigns: %+v, %v", w, err)
	}
	if _, err := s.GetWorker(ctxA, legacy); err != nil {
		t.Fatalf("company A does not see its assigned worker: %v", err)
	}
	if _, err := s.AssignWorkerLegalEntity(tenantAdmin, legacy, companyA, "shopfloor.setup.maintain", idem("assign-again")); err != nil {
		t.Fatalf("assigning the same legal entity again: %v", err)
	}
	if _, err := s.AssignWorkerLegalEntity(tenantAdmin, legacy, companyB, "shopfloor.setup.maintain", idem("assign-other")); problemCode(err) != CodeWorkerLegalEntitySet {
		t.Fatalf("moving the worker to company B: %v, want %s", err, CodeWorkerLegalEntitySet)
	}
	if _, err := admin.Exec(context.Background(), `update worker set legal_entity_id = $3 where tenant_id = $1 and id = $2`, tenantID, legacy, companyB); err == nil {
		t.Fatal("the database let a worker's legal entity change")
	}
}

// The sweeper's auto clock-out keeps the whole tenant's time data consistent, whatever company employs the worker; a
// caller scoped to one company closes only that company's shifts.
func TestLegalEntity_AutoClockOutBySweeperCoversEveryCompany(t *testing.T) {
	s, _, _ := setup(t)
	tenantID := testkit.TenantID(94)
	ctxA, ctxB := inCompany(tenantID, companyA), inCompany(tenantID, companyB)
	workerA, workerB := mustWorker(t, s, ctxA, "AC-A", nil), mustWorker(t, s, ctxB, "AC-B", nil)
	for _, w := range []struct {
		ctx    context.Context
		worker string
	}{{ctxA, workerA.ID}, {ctxB, workerB.ID}} {
		if _, err := s.CreateRegistrations(w.ctx, []Registration{clock(w.worker, "clock-in", day(1, 0))}); err != nil {
			t.Fatal(err)
		}
	}
	now := day(1, 0).Add(20 * time.Hour)
	closed, err := s.AutoClockOut(ctxA, now, 12, 10)
	if err != nil || len(closed) != 1 || closed[0].WorkerID != workerA.ID {
		t.Fatalf("company A's sweep closed %+v, %v; want only its worker", closed, err)
	}
	sweeper := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: SweepActor})
	closed, err = s.AutoClockOut(sweeper, now, 12, 10)
	if err != nil || len(closed) != 1 || closed[0].WorkerID != workerB.ID {
		t.Fatalf("the sweeper closed %+v, %v; want company B's worker", closed, err)
	}
	person := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: "someone"})
	if closed, err := s.AutoClockOut(person, now, 12, 10); err != nil || len(closed) != 0 {
		t.Fatalf("a person with no legal entity swept %+v, %v", closed, err)
	}
}
