package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/close/internal/api"
	"github.com/BenjaminSRussell/depot/services/close/internal/domain"
	"github.com/BenjaminSRussell/depot/services/close/internal/store"
	"github.com/BenjaminSRussell/depot/services/close/migrations"
)

type moveFunc func(module, status string) *httptest.ResponseRecorder

// storeBackedPeriods serves the period PATCH endpoint from the Postgres store,
// with one registered period whose ALL row is open, and returns the store so
// a test can read the matrix back.
func storeBackedPeriods(t *testing.T) (moveFunc, *store.Postgres, context.Context) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(app)
	entity, ledger, period := testkit.TenantID(9), testkit.TenantID(7), testkit.TenantID(8)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
	if _, err := st.RegisterPeriod(ctx, domain.PeriodRegistration{
		LegalEntityID: entity, LedgerID: ledger, FiscalPeriodID: period, Actor: "test",
		Start: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	a := api.New(st).WithLedgers(fakeLedger{owner: entity}).WithSliceB(st, st, st, st, st, nil)
	r := httpx.NewRouter()
	a.Register(r)
	move := func(module, status string) *httptest.ResponseRecorder {
		body := `{"ledgerId":"` + ledger + `","status":"` + status + `"`
		if status == "on_hold" {
			body += `,"allowedGroupId":"controllers"`
		}
		req := httptest.NewRequest(http.MethodPatch, "/v1/close/periods/"+period+"/modules/"+module, strings.NewReader(body+"}"))
		req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Depot-Duties", "period.maintain")
		req.Header.Set("X-Depot-Legal-Entity", entity)
		req.Header.Set("X-Depot-User", "alice")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	return move, st, ctx
}

func mustMove(t *testing.T, move moveFunc, module, status string) {
	t.Helper()
	if rec := move(module, status); rec.Code != http.StatusOK {
		t.Fatalf("%s to %s: %d %s", module, status, rec.Code, rec.Body)
	}
}

func refused(t *testing.T, rec *httptest.ResponseRecorder, naming string) {
	t.Helper()
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), domain.CodeInvalidTransition) || !strings.Contains(rec.Body.String(), naming) {
		t.Fatalf("want 409 %s naming %q, got %d %s", domain.CodeInvalidTransition, naming, rec.Code, rec.Body)
	}
}

// FR-14.2 (B-08): before the fix the store accepted closing ALL while GL was
// open, and closing GL while ALL (which governs every subledger without a row
// of its own) was open.
func TestPeriodCloseOrderIsEnforcedThroughTheStore(t *testing.T) {
	for _, to := range []string{"closed", "permanently_closed"} {
		t.Run(to, func(t *testing.T) {
			move, _, _ := storeBackedPeriods(t)
			mustMove(t, move, "gl", "on_hold")

			refused(t, move("all", to), "cannot close all: module gl is still on_hold")
			refused(t, move("gl", to), "module ap has no row of its own and all is still open")

			for _, m := range domain.SubledgerModules() {
				mustMove(t, move, string(m), "closed")
			}
			refused(t, move("all", to), "cannot close all: module gl is still on_hold")
			mustMove(t, move, "gl", to)
			mustMove(t, move, "all", to)
		})
	}
}

func TestPeriodGLCloseWaitsForAnOpenSubledgerThroughTheStore(t *testing.T) {
	move, _, _ := storeBackedPeriods(t)
	mustMove(t, move, "ap", "open")
	mustMove(t, move, "all", "on_hold")
	refused(t, move("gl", "closed"), "cannot close gl: module ap is still open")
}

// A module without a row of its own starts from the ALL row: a closed or
// permanently closed ALL cannot be reopened through a module PATCH.
func TestPeriodModuleWithoutRowCannotBypassAClosedAllThroughTheStore(t *testing.T) {
	move, _, _ := storeBackedPeriods(t)
	mustMove(t, move, "all", "closed")
	refused(t, move("ap", "open"), "module ap (through all, it has no row of its own) is closed")
	refused(t, move("gl", "on_hold"), "module gl (through all, it has no row of its own) is closed")
	mustMove(t, move, "all", "permanently_closed")
	refused(t, move("ap", "open"), "module ap (through all, it has no row of its own) is permanently closed")
	refused(t, move("gl", "open"), "module gl (through all, it has no row of its own) is permanently closed")
}

// The BR-14.2 reference configuration for a March close, replayed column by
// column through the endpoint. Cash and production never get a row: the held
// ALL row stops their postings, so GL closes without them.
func TestPeriodBR142ReferenceMatrixThroughTheStore(t *testing.T) {
	move, st, ctx := storeBackedPeriods(t)
	type step struct{ module, to, refuse string }
	columns := [][]step{
		{ // Day 1-3 after period end
			{"inventory", "on_hold", ""}, {"projects", "on_hold", ""}, {"payroll", "on_hold", ""},
			{"ap", "open", ""}, {"ar", "open", ""}, {"bank", "open", ""}, {"tax", "open", ""}, {"assets", "open", ""}, {"gl", "open", ""},
			{"gl", "closed", "cannot close gl: module ap is still open"}, // the store reads rows by module name
		},
		{ // Day 4-5
			{"inventory", "closed", ""}, {"projects", "closed", ""}, {"payroll", "closed", ""},
			{"ap", "on_hold", ""}, {"ar", "on_hold", ""}, {"bank", "on_hold", ""},
			{"assets", "closed", ""}, {"gl", "on_hold", ""}, {"all", "on_hold", ""},
			{"gl", "closed", "cannot close gl: module ap is still on_hold"},
		},
		{ // After GL close
			{"ap", "closed", ""}, {"ar", "closed", ""}, {"bank", "closed", ""},
			{"gl", "closed", "cannot close gl: module tax is still open"},
			{"tax", "closed", ""},
			{"all", "closed", "cannot close all: module gl is still on_hold"},
			{"gl", "closed", ""},
			{"all", "closed", ""},
		},
	}
	for _, col := range columns {
		for _, s := range col {
			if s.refuse != "" {
				refused(t, move(s.module, s.to), s.refuse)
			} else {
				mustMove(t, move, s.module, s.to)
			}
		}
	}
	matrix, err := st.PeriodMatrix(ctx, testkit.TenantID(9), testkit.TenantID(7))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[domain.Module]bool{}
	for _, p := range matrix {
		seen[p.Module] = true
		if p.Status != domain.StatusClosed {
			t.Errorf("after GL close %s is %s", p.Module, p.Status)
		}
	}
	if len(matrix) != 10 || seen[domain.ModuleCash] || seen[domain.ModuleProduction] {
		t.Fatalf("want ten rows without cash and production, got %+v", matrix)
	}
}
