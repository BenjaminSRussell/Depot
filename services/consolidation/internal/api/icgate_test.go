package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/intercoclient"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/consolidation/internal/api"
	"github.com/BenjaminSRussell/depot/services/consolidation/internal/store"
	"github.com/BenjaminSRussell/depot/services/consolidation/migrations"
)

type fakeMismatches struct {
	open int
	err  error
	seen []string
}

func (f *fakeMismatches) OpenMismatches(_ context.Context, legalEntityID, period string) (int, error) {
	f.seen = append(f.seen, legalEntityID+"|"+period)
	return f.open, f.err
}

type perEntityMismatches struct {
	open map[string]int
	seen []string
}

func (f *perEntityMismatches) OpenMismatches(_ context.Context, legalEntityID, _ string) (int, error) {
	f.seen = append(f.seen, legalEntityID)
	return f.open[legalEntityID], nil
}

func gateAPI(t *testing.T, m intercoclient.Mismatches) (*api.API, *pgxpool.Pool) {
	t.Helper()
	a, pool, _ := gateAPIWithLedger(t, m)
	return a, pool
}

func gateAPIWithLedger(t *testing.T, m intercoclient.Mismatches) (*api.API, *pgxpool.Pool, *ledgerclient.Fake) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool)
	fake := ledgerclient.NewFake()
	return api.New(st).WithGroups(st).WithLedger(fake).WithICGate(m), pool, fake
}

func overrideRows(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select o.group_id || '|' || o.period || '|' || o.reason || '|' || o.open_mismatches
		from consolidation_ic_override o join consolidation_run r on r.tenant_id = o.tenant_id and r.id = o.run_id order by o.created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var row string
		if err := rows.Scan(&row); err != nil {
			t.Fatal(err)
		}
		out = append(out, row)
	}
	return out
}

func gateGroup(t *testing.T, a *api.API, entities ...string) groupOut {
	t.Helper()
	g := created[groupOut](t, call(a, http.MethodPost, "/v1/consol/groups", tenantID, api.DutyConfigure, "g1", `{"code":"CRP","name":"Cedar Ridge Pumps group","currency":"USD"}`))
	for i, le := range entities {
		created[unitOut](t, call(a, http.MethodPost, "/v1/consol/groups/"+g.ID+"/units", tenantID, api.DutyConfigure, fmt.Sprint("u", i), fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-%d","currency":"USD"}`, le, i)))
	}
	return g
}

func gateRunBody(groupID, extra string) string {
	return strings.Replace(runBody, `"groupId":"G1"`, fmt.Sprintf(`"groupId":%q%s`, groupID, extra), 1)
}

func TestRunGate_OpenICMismatchRefusesUnlessApprovedWithReason(t *testing.T) {
	mism := &fakeMismatches{open: 2}
	a, pool := gateAPI(t, mism)
	g := gateGroup(t, a, leUS)
	reason := `,"icMismatchOverrideReason":"  accepted by group controller  "`

	rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "r1", gateRunBody(g.ID, ""))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CNS-0901") || len(overrideRows(t, pool)) != 0 {
		t.Fatalf("open mismatch = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "r2", gateRunBody(g.ID, reason))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "CNS-0937") || len(overrideRows(t, pool)) != 0 {
		t.Fatalf("override without consolidation.approve = %d %s", rec.Code, rec.Body.String())
	}
	rec = call(a, http.MethodPost, runsPath, tenantID, api.DutyRun+","+api.DutyApprove, "r3", gateRunBody(g.ID, reason))
	if rec.Code != http.StatusCreated {
		t.Fatalf("approved override = %d %s", rec.Code, rec.Body.String())
	}
	if want := g.ID + "|2026-06|accepted by group controller|2"; fmt.Sprint(overrideRows(t, pool)) != fmt.Sprint([]string{want}) {
		t.Fatalf("audit rows = %v, want %q linked to the run", overrideRows(t, pool), want)
	}
	if want := leUS + "|2026-06"; mism.seen[0] != want {
		t.Fatalf("reconciliation read for %q, want %q", mism.seen[0], want)
	}
}

func TestRunGate_ReplayIsAnsweredBeforeTheGateAndStoresNoSecondApproval(t *testing.T) {
	mism := &fakeMismatches{open: 2}
	a, pool := gateAPI(t, mism)
	g := gateGroup(t, a, leUS)
	body := gateRunBody(g.ID, `,"icMismatchOverrideReason":"accepted"`)

	if rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun+","+api.DutyApprove, "r1", body); rec.Code != http.StatusCreated {
		t.Fatalf("first = %d %s", rec.Code, rec.Body.String())
	}
	mism.open = 5
	rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "r1", body)
	if rec.Code != http.StatusCreated || rec.Header().Get("Idempotent-Replayed") != "true" || len(overrideRows(t, pool)) != 1 {
		t.Fatalf("replay = %d %s replayed=%q rows=%v", rec.Code, rec.Body.String(), rec.Header().Get("Idempotent-Replayed"), overrideRows(t, pool))
	}
}

func TestRunGate_OverrideReasonIsCappedAt500Characters(t *testing.T) {
	a, pool := gateAPI(t, &fakeMismatches{open: 1})
	g := gateGroup(t, a, leUS)
	duties := api.DutyRun + "," + api.DutyApprove
	long := fmt.Sprintf(`,"icMismatchOverrideReason":%q`, strings.Repeat("x", 501))
	if rec := call(a, http.MethodPost, runsPath, tenantID, duties, "r1", gateRunBody(g.ID, long)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "icMismatchOverrideReason") {
		t.Fatalf("501-character reason = %d %s", rec.Code, rec.Body.String())
	}
	ok := fmt.Sprintf(`,"icMismatchOverrideReason":%q`, strings.Repeat("x", 500))
	if rec := call(a, http.MethodPost, runsPath, tenantID, duties, "r2", gateRunBody(g.ID, ok)); rec.Code != http.StatusCreated {
		t.Fatalf("500-character reason = %d %s", rec.Code, rec.Body.String())
	}
	if rows := overrideRows(t, pool); len(rows) != 1 {
		t.Fatalf("approvals = %v, want only the accepted run's", rows)
	}
}

func TestRunGate_NoOpenMismatchRunsAndUnreadableReconciliationRefuses(t *testing.T) {
	mism := &fakeMismatches{}
	a, pool := gateAPI(t, mism)
	g := gateGroup(t, a, leUS)

	if rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "r1", gateRunBody(g.ID, "")); rec.Code != http.StatusCreated || len(overrideRows(t, pool)) != 0 {
		t.Fatalf("clean reconciliation = %d %s", rec.Code, rec.Body.String())
	}
	mism.err = errors.New("intercompany down")
	if rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun+","+api.DutyApprove, "r2", gateRunBody(g.ID, `,"icMismatchOverrideReason":"x"`)); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "CNS-0936") {
		t.Fatalf("unreadable reconciliation = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRunGate_GroupWithoutAnEntityIsNotGated(t *testing.T) {
	mism := &fakeMismatches{open: 3}
	a, _ := gateAPI(t, mism)
	g := gateGroup(t, a)
	if rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "r1", gateRunBody(g.ID, "")); rec.Code != http.StatusCreated || len(mism.seen) != 0 {
		t.Fatalf("group without an entity = %d %s, reads %v", rec.Code, rec.Body.String(), mism.seen)
	}
}

func TestRunGate_UnknownGroupIsRefused(t *testing.T) {
	a, _ := gateAPI(t, &fakeMismatches{})
	if rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "r1", gateRunBody(ids.New(), "")); rec.Code == http.StatusCreated {
		t.Fatalf("a run for a group that is not stored = %d %s", rec.Code, rec.Body.String())
	}
}

func TestRunGate_ChecksEveryUnitEntity(t *testing.T) {
	mism := &perEntityMismatches{open: map[string]int{leDE: 1}}
	a, _ := gateAPI(t, mism)
	g := gateGroup(t, a, leUS, leDE)

	rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "r1", gateRunBody(g.ID, ""))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CNS-0901") {
		t.Fatalf("mismatch under the second entity = %d %s", rec.Code, rec.Body.String())
	}
	if len(mism.seen) != 2 {
		t.Fatalf("entities read = %v, want each distinct entity once", mism.seen)
	}
}

func TestRunGate_AppliesToARunFromACollection(t *testing.T) {
	a, pool, fake := gateAPIWithLedger(t, &fakeMismatches{open: 1})
	const base = "/v1/consol/groups"
	g := gateGroup(t, a, leUS)
	de := created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units", tenantID, api.DutyConfigure, "u-de", fmt.Sprintf(`{"legalEntityId":%q,"ledgerId":"L-DE","currency":"EUR"}`, leDE)))
	created[unitOut](t, call(a, http.MethodPost, base+"/"+g.ID+"/units/"+de.ID+"/ownership", tenantID, api.DutyConfigure, "", `{"effectiveFrom":"2024-01-01","percent":"80"}`))
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/account-map", tenantID, api.DutyConfigure, "", mappingWithRoles); rec.Code != http.StatusOK {
		t.Fatalf("mapping = %d %s", rec.Code, rec.Body.String())
	}
	terms := `{"investmentCost":"600000","shareCapital":"300000","preAcquisitionRe":"100000","historicalRate":"1.05","sellerIsSubsidiary":true}`
	if rec := call(a, http.MethodPut, base+"/"+g.ID+"/units/"+de.ID+"/terms", tenantID, api.DutyConfigure, "", terms); rec.Code != http.StatusOK {
		t.Fatalf("terms = %d %s", rec.Code, rec.Body.String())
	}
	postLedger(t, fake, leDE, "L-DE", "EUR", "2025-01-05", "de-1", line("1000", false, "1000000"), line("3000", true, "1000000"))
	postLedger(t, fake, leUS, "L-0", "USD", "2025-01-05", "us-1", line("1000", false, "5000000"), line("3000", true, "5000000"))
	created[struct{ ID string }](t, call(a, http.MethodPost, base+"/"+g.ID+"/collections", tenantID, api.DutyRun, "c-prev", `{"period":"2025-12","rates":[{"currency":"EUR","average":"1.06","closing":"1.04"}]}`))
	cur := created[struct{ ID string }](t, call(a, http.MethodPost, base+"/"+g.ID+"/collections", tenantID, api.DutyRun, "c-cur", `{"period":"2026-03","rates":[{"currency":"EUR","average":"1.08","closing":"1.10"}]}`))

	if rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun, "r1", fmt.Sprintf(`{"collectionId":%q}`, cur.ID)); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "CNS-0901") {
		t.Fatalf("collection run over an open mismatch = %d %s", rec.Code, rec.Body.String())
	}
	over := fmt.Sprintf(`{"collectionId":%q,"icMismatchOverrideReason":"accepted"}`, cur.ID)
	if rec := call(a, http.MethodPost, runsPath, tenantID, api.DutyRun+","+api.DutyApprove, "r2", over); rec.Code != http.StatusCreated {
		t.Fatalf("collection run with approval = %d %s", rec.Code, rec.Body.String())
	}
	if want := g.ID + "|2026-03|accepted|2"; fmt.Sprint(overrideRows(t, pool)) != fmt.Sprint([]string{want}) {
		t.Fatalf("audit rows = %v, want %q (one open mismatch per unit entity)", overrideRows(t, pool), want)
	}
}
