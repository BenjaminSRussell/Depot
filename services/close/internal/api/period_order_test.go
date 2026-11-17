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

// storeBackedPeriods serves the period PATCH endpoint from the Postgres store,
// with one registered period whose ALL row is open.
func storeBackedPeriods(t *testing.T) func(module, status string) *httptest.ResponseRecorder {
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
		req := httptest.NewRequest(http.MethodPatch, "/v1/close/periods/"+period+"/modules/"+module, strings.NewReader(`{"ledgerId":"`+ledger+`","status":"`+status+`"}`))
		req.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Depot-Duties", "period.maintain")
		req.Header.Set("X-Depot-Legal-Entity", entity)
		req.Header.Set("X-Depot-User", "alice")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	return move
}

func mustMove(t *testing.T, move func(string, string) *httptest.ResponseRecorder, module, status string) {
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
			move := storeBackedPeriods(t)
			mustMove(t, move, "gl", "open")

			refused(t, move("all", to), "gl")
			refused(t, move("gl", to), "all is still open")

			for _, m := range domain.SubledgerModules() {
				mustMove(t, move, string(m), "open")
				mustMove(t, move, string(m), "closed")
			}
			refused(t, move("all", to), "gl")
			mustMove(t, move, "gl", to)
			mustMove(t, move, "all", to)
		})
	}
}

func TestPeriodGLCloseWaitsForAnOpenSubledgerThroughTheStore(t *testing.T) {
	move := storeBackedPeriods(t)
	mustMove(t, move, "gl", "open")
	mustMove(t, move, "ap", "open")
	refused(t, move("gl", "closed"), "ap")
}
