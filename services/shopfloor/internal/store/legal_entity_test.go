package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	sfevents "github.com/BenjaminSRussell/depot/services/shopfloor/internal/events"
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
	s, app, admin := setup(t)
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

	// B-03b re-review R2: company A cannot put company B's pay line into its own export batch (foreign keys ignore
	// row-level security, so only the policy's WITH CHECK stops it).
	var lineB string
	if err := admin.QueryRow(context.Background(), `select id::text from pay_line where tenant_id = $1 and worker_id = $2`, tenantID, workerB.ID).Scan(&lineB); err != nil {
		t.Fatal(err)
	}
	err = pgx.BeginFunc(context.Background(), app, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), `select set_config('depot.tenant_id', $1, true), set_config('depot.legal_entity_scopes', $2, true)`, tenantID, companyA); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `insert into pay_export_line (tenant_id, pay_line_id, batch_id) values ($1, $2, $3)`, tenantID, lineB, exportA.BatchID)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("company A exported company B's pay line into its batch: %v, want a row-level security refusal", err)
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

const setupDuty = "shopfloor.setup.maintain"

func insertLegacyWorker(t *testing.T, admin *pgxpool.Pool, tenantID string) string {
	t.Helper()
	id := ids.New()
	if _, err := admin.Exec(context.Background(), `insert into worker (tenant_id, id, party_id, name, cost_rate, currency) values ($1, $2, $3, 'Legacy', 20, 'USD')`, tenantID, id, ids.New()); err != nil {
		t.Fatal(err)
	}
	return id
}

// workerAssignedEvents returns the legal entity and actor of each worker.assigned event of the worker, oldest first.
func workerAssignedEvents(t *testing.T, pool *pgxpool.Pool, tenantID, workerID string) [][2]string {
	t.Helper()
	var out [][2]string
	inTenant(t, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `select coalesce(payload->'data'->>'legalEntityId', ''), coalesce(payload->'data'->>'assignedBy', '')
			from outbox where type = $1 and aggregate_id = $2 order by created_at, id`, sfevents.WorkerAssigned, workerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e [2]string
			if err := rows.Scan(&e[0], &e[1]); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out
}

// A worker created before workers carried a legal entity is visible to no scoped caller until a tenant administrator
// assigns its legal entity. The assignment is recorded (worker.assigned with the legal entity and the actor). It may
// be corrected while the worker has no time or pay; after that it never changes.
func TestLegalEntity_TenantAdminAssignsALegacyWorker(t *testing.T) {
	s, app, admin := setup(t)
	tenantID := testkit.TenantID(93)
	legacy := insertLegacyWorker(t, admin, tenantID)
	ctxA := inCompany(tenantID, companyA, setupDuty)
	if _, err := s.GetWorker(ctxA, legacy); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("a scoped caller sees a worker with no legal entity: %v", err)
	}
	if _, err := s.AssignWorkerLegalEntity(ctxA, legacy, companyA, setupDuty, "user-test", idem("assign-scoped")); !errors.Is(err, dbx.ErrNotTenantWide) {
		t.Fatalf("a scoped caller assigns a legal entity: %v, want ErrNotTenantWide", err)
	}
	tenantAdmin := inCompany(tenantID, "", tenant.DutyTenantAdmin, setupDuty)

	// A typo (company B instead of A) is corrected while the worker has no time or pay.
	w, err := s.AssignWorkerLegalEntity(tenantAdmin, legacy, companyB, setupDuty, "admin-1", idem("assign"))
	if err != nil || w.LegalEntityID == nil || *w.LegalEntityID != companyB || w.Version != 2 {
		t.Fatalf("tenant administrator assigns: %+v, %v", w, err)
	}
	w, err = s.AssignWorkerLegalEntity(tenantAdmin, legacy, companyA, setupDuty, "admin-2", idem("correct"))
	if err != nil || w.LegalEntityID == nil || *w.LegalEntityID != companyA || w.Version != 3 {
		t.Fatalf("tenant administrator corrects before any time: %+v, %v", w, err)
	}
	if got := workerAssignedEvents(t, app, tenantID, legacy); len(got) != 2 || got[0] != [2]string{companyB, "admin-1"} || got[1] != [2]string{companyA, "admin-2"} {
		t.Fatalf("worker.assigned events = %v, want the two assignments with their actors", got)
	}
	if _, err := s.GetWorker(ctxA, legacy); err != nil {
		t.Fatalf("company A does not see its assigned worker: %v", err)
	}
	if again, err := s.AssignWorkerLegalEntity(tenantAdmin, legacy, companyA, setupDuty, "admin-2", idem("assign-again")); err != nil || again.Version != 3 {
		t.Fatalf("assigning the same legal entity again: %+v, %v", again, err)
	}
	if got := workerAssignedEvents(t, app, tenantID, legacy); len(got) != 2 {
		t.Fatalf("a repeated assignment published an event: %v", got)
	}

	// Once the worker has time, the legal entity is frozen: in the store and in the database.
	if _, err := s.CreateRegistrations(ctxA, []Registration{clock(legacy, "clock-in", day(9, 0))}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AssignWorkerLegalEntity(tenantAdmin, legacy, companyB, setupDuty, "admin-1", idem("assign-other")); problemCode(err) != CodeWorkerLegalEntitySet {
		t.Fatalf("moving a worker with time to company B: %v, want %s", err, CodeWorkerLegalEntitySet)
	}
	for _, to := range []any{companyB, nil} {
		if _, err := admin.Exec(context.Background(), `update worker set legal_entity_id = $3 where tenant_id = $1 and id = $2`, tenantID, legacy, to); err == nil {
			t.Fatalf("the database let a worker with time change its legal entity to %v", to)
		}
	}
}

// The deploy-time backfill: one call assigns a legal entity to every worker that has none (or to listed ones),
// leaves workers of other legal entities alone, records each assignment, and gives the old export batches whose lines
// all belong to the legal entity's workers that legal entity.
func TestLegalEntity_BulkAssignmentBackfillsWorkersAndExportBatches(t *testing.T) {
	s, app, admin := setup(t)
	tenantID := testkit.TenantID(95)
	ctxB := inCompany(tenantID, companyB)
	tenantAdmin := inCompany(tenantID, "", tenant.DutyTenantAdmin, setupDuty)
	legacy1, legacy2 := insertLegacyWorker(t, admin, tenantID), insertLegacyWorker(t, admin, tenantID)
	inB := mustWorker(t, s, ctxB, "BULK-B", nil)

	// legacy1 has an approved, exported day from before workers carried a legal entity.
	if _, err := admin.Exec(context.Background(), `update worker set legal_entity_id = $3 where tenant_id = $1 and id = $2`, tenantID, legacy1, companyA); err != nil {
		t.Fatal(err)
	}
	ctxA := inCompany(tenantID, companyA)
	if _, err := s.CreateRegistrations(ctxA, []Registration{clock(legacy1, "clock-in", day(9, 0)), clock(legacy1, "clock-out", day(17, 0))}); err != nil {
		t.Fatal(err)
	}
	if err := calculate(t, s, ctxA, legacy1, "2026-10-20", idem("bulk-calc")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePay(ctxA, legacy1, "2026-10-20", "2026-10-20", "supervisor-1", idem("bulk-approve")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExportPay(ctxA, "2026-10-20", "2026-10-20", "payroll-a", idem("bulk-export")); err != nil {
		t.Fatal(err)
	}
	// Make it look like the rows written before 0039: the worker and its batch without a legal entity.
	for _, q := range []string{
		`alter table worker disable trigger worker_legal_entity_guard`,
		`update worker set legal_entity_id = null where tenant_id = '` + tenantID + `' and id = '` + legacy1 + `'`,
		`alter table worker enable trigger worker_legal_entity_guard`,
		`alter table pay_export_batch disable trigger pay_export_batch_legal_entity_guard`,
		`update pay_export_batch set legal_entity_id = null where tenant_id = '` + tenantID + `'`,
		`alter table pay_export_batch enable trigger pay_export_batch_legal_entity_guard`,
	} {
		if _, err := admin.Exec(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	if list, err := s.ListPayExports(ctxA, 10); err != nil || len(list) != 0 {
		t.Fatalf("company A sees %d legacy export batches before the backfill, %v", len(list), err)
	}
	if _, err := s.AssignWorkersLegalEntity(inCompany(tenantID, companyA, setupDuty), BulkLegalEntityAssignment{LegalEntityID: companyA, AllUnassigned: true}, setupDuty, "user-test", idem("bulk-scoped")); !errors.Is(err, dbx.ErrNotTenantWide) {
		t.Fatalf("a scoped caller runs the bulk assignment: %v", err)
	}

	missing := ids.New()
	res, err := s.AssignWorkersLegalEntity(tenantAdmin, BulkLegalEntityAssignment{LegalEntityID: companyA, WorkerIDs: []string{legacy2, inB.ID, missing}}, setupDuty, "admin-1", idem("bulk-list"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Assigned) != 1 || res.Assigned[0] != legacy2 || len(res.Conflicts) != 1 || res.Conflicts[0] != inB.ID ||
		len(res.NotFound) != 1 || res.NotFound[0] != missing || len(res.Unchanged) != 0 {
		t.Fatalf("listed bulk assignment = %+v", res)
	}
	// A dry run reports what the call would do and writes nothing.
	dry, err := s.AssignWorkersLegalEntity(tenantAdmin, BulkLegalEntityAssignment{LegalEntityID: companyA, AllUnassigned: true, DryRun: true}, setupDuty, "admin-1", idem("bulk-dry"))
	if err != nil || !dry.DryRun || dry.AssignedCount != 1 || dry.Remaining != 0 || dry.ExportBatches != 1 {
		t.Fatalf("dry run = %+v, %v; want legacy1 and its export batch", dry, err)
	}
	if got := workerAssignedEvents(t, app, tenantID, legacy1); len(got) != 0 {
		t.Fatalf("the dry run published %v", got)
	}
	if list, err := s.ListPayExports(ctxA, 10); err != nil || len(list) != 0 {
		t.Fatalf("the dry run assigned %d export batches, %v", len(list), err)
	}
	res, err = s.AssignWorkersLegalEntity(tenantAdmin, BulkLegalEntityAssignment{LegalEntityID: companyA, AllUnassigned: true}, setupDuty, "admin-1", idem("bulk-all"))
	if err != nil {
		t.Fatal(err)
	}
	if res.AssignedCount != 1 || len(res.Assigned) != 0 || res.Remaining != 0 || res.ExportBatches != 1 || len(res.Conflicts) != 0 {
		t.Fatalf("all-unassigned bulk assignment = %+v, want legacy1 (counted, not listed) and its export batch", res)
	}
	if got := workerAssignedEvents(t, app, tenantID, legacy1); len(got) != 1 || got[0] != [2]string{companyA, "admin-1"} {
		t.Fatalf("worker.assigned events of legacy1 = %v", got)
	}
	if list, err := s.ListPayExports(ctxA, 10); err != nil || len(list) != 1 {
		t.Fatalf("company A sees %d export batches after the backfill, %v; want its one", len(list), err)
	}
	if _, err := s.ExportPay(ctxA, "2026-10-20", "2026-10-20", "payroll-a", idem("bulk-export-again")); problemCode(err) != CodeNothingToExport {
		t.Fatalf("a line exported before the backfill was exported again: %v", err)
	}
	if w, err := s.GetWorker(ctxB, inB.ID); err != nil || w.LegalEntityID == nil || *w.LegalEntityID != companyB {
		t.Fatalf("the bulk assignment moved company B's worker: %+v, %v", w, err)
	}
	if _, err := admin.Exec(context.Background(), `update pay_export_batch set legal_entity_id = $2 where tenant_id = $1`, tenantID, companyB); err == nil {
		t.Fatal("the database let an export batch change its legal entity")
	}
}

// A caller scoped to several legal entities is told to select one before a payroll export.
func TestLegalEntity_ExportNeedsOneLegalEntity(t *testing.T) {
	s, _, _ := setup(t)
	tenantID := testkit.TenantID(96)
	_, err := s.ExportPay(inCompany(tenantID, companyA+","+companyB), "2026-10-20", "2026-10-20", "payroll", idem("multi"))
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != "COM-0001" || p.Errors["legalEntity"] == "" {
		t.Fatalf("export by a caller in two legal entities: %v", err)
	}
	if _, err := s.ExportPay(inCompany(tenantID, ""), "2026-10-20", "2026-10-20", "payroll", idem("none")); problemCode(err) != "AUTH-0003" {
		t.Fatalf("export by a caller with no legal entity: %v", err)
	}
}

// The sweeper's auto clock-out keeps the whole tenant's time data consistent, whatever company employs the worker, but
// only through its in-process context marker: a principal that merely carries the sweeper's user id gets nothing. A
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
	sweeperID := tenant.With(context.Background(), tenant.Principal{TenantID: tenantID, UserID: SweepActor})
	if closed, err := s.AutoClockOut(sweeperID, now, 12, 10); err != nil || len(closed) != 0 {
		t.Fatalf("the sweeper's user id without the marker swept %+v, %v", closed, err)
	}
	if closed, err := s.AutoClockOut(WithSystemScope(ctxA), now, 12, 10); err != nil || len(closed) != 0 {
		t.Fatalf("the marker widened a caller with a legal entity: %+v, %v", closed, err)
	}
	closed, err = s.AutoClockOut(WithSystemScope(sweeperID), now, 12, 10)
	if err != nil || len(closed) != 1 || closed[0].WorkerID != workerB.ID {
		t.Fatalf("the sweeper closed %+v, %v; want company B's worker", closed, err)
	}
}

// B-03b re-review R4: one allUnassigned call assigns at most MaxBulkLegalEntityWorkers workers and says how many are
// left, so the backfill repeats it.
func TestLegalEntity_BulkAllUnassignedIsBounded(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(97)
	if _, err := admin.Exec(context.Background(), `insert into worker (tenant_id, id, party_id, name, cost_rate, currency)
		select $1, gen_random_uuid(), gen_random_uuid(), 'Legacy ' || g, 20, 'USD' from generate_series(1, $2::int) g`, tenantID, MaxBulkLegalEntityWorkers+1); err != nil {
		t.Fatal(err)
	}
	tenantAdmin := inCompany(tenantID, "", tenant.DutyTenantAdmin, setupDuty)
	first, err := s.AssignWorkersLegalEntity(tenantAdmin, BulkLegalEntityAssignment{LegalEntityID: companyA, AllUnassigned: true}, setupDuty, "admin-1", idem("bound-1"))
	if err != nil || first.AssignedCount != MaxBulkLegalEntityWorkers || first.Remaining != 1 || len(first.Assigned) != 0 {
		t.Fatalf("first call = assigned %d remaining %d listed %d, %v", first.AssignedCount, first.Remaining, len(first.Assigned), err)
	}
	second, err := s.AssignWorkersLegalEntity(tenantAdmin, BulkLegalEntityAssignment{LegalEntityID: companyA, AllUnassigned: true}, setupDuty, "admin-1", idem("bound-2"))
	if err != nil || second.AssignedCount != 1 || second.Remaining != 0 {
		t.Fatalf("second call = %+v, %v", second, err)
	}
}
