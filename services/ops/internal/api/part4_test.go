package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

type fakeDirectory struct {
	status string
	users  map[string]int
	err    error
}

func (f fakeDirectory) Status(_ context.Context, id string) (string, int, error) {
	return f.status, f.users[id], f.err
}

func provisionTenant(t *testing.T, e *env3, name string) string {
	t.Helper()
	tid := ids.New()
	body := `{"tenantId":"` + tid + `","name":"` + name + `","residency":"EU","region":"eu-west"}`
	if rec := e.do("POST", "/v1/ops/tenants", "alice", DutyProvision, "k-"+tid, body); rec.Code != http.StatusCreated {
		t.Fatalf("provision = %d %s", rec.Code, rec.Body)
	}
	return tid
}

func TestDecommissionNeedsASuspendedTenantRunsInReverseOrderAndIsAudited(t *testing.T) {
	dir := &fakeDirectory{status: "active"}
	e := newEnv3(t, time.Now().UTC(), func(c *Config) { c.Directory = dir })
	tid := provisionTenant(t, e, "Cedar Ridge Pumps")
	path := "/v1/ops/tenants/" + tid + ":decommission"
	reason := `{"reason":"contract ended"}`

	if rec := e.do("POST", path, "alice", DutyProvision, "", reason); rec.Code != http.StatusForbidden {
		t.Fatalf("decommission needs its own duty: %d", rec.Code)
	}
	if rec := e.do("POST", path, "alice", DutyDecommission, "", `{"reason":" "}`); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "OPS-1734") {
		t.Fatalf("no reason = %d %s", rec.Code, rec.Body)
	}
	// An active tenant is refused, and so is one identity cannot be asked about; nothing is recorded either time.
	if rec := e.do("POST", path, "alice", DutyDecommission, "", reason); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "OPS-1731") {
		t.Fatalf("active tenant = %d %s", rec.Code, rec.Body)
	}
	dir.err = errors.New("identity down")
	if rec := e.do("POST", path, "alice", DutyDecommission, "", reason); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "OPS-1730") {
		t.Fatalf("identity down = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("GET", "/v1/ops/tenants/"+tid+"/decommission", "alice", DutyView, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("nothing recorded yet = %d", rec.Code)
	}
	dir.err, dir.status = nil, "suspended"
	rec := e.do("POST", path, "alice", DutyDecommission, "", reason)
	if rec.Code != http.StatusCreated {
		t.Fatalf("decommission = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("POST", path, "alice", DutyDecommission, "", reason); rec.Code != http.StatusOK {
		t.Fatalf("a repeat is the first record: %d", rec.Code)
	}
	steps := "/v1/ops/tenants/" + tid + "/decommission-steps/"
	// The realm goes last: it cannot be DONE before the databases.
	if rec := e.do("PUT", steps+"realm", "runner", DutyDecommission, "", `{"status":"DONE"}`); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "OPS-1733") {
		t.Fatalf("realm first = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do("PUT", steps+"license", "runner", DutyDecommission, "", `{"status":"DONE"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown step = %d", rec.Code)
	}
	for _, a := range domain.DecommissionSteps {
		if rec := e.do("PUT", steps+a, "runner", DutyDecommission, "", `{"status":"DONE","detail":"removed"}`); rec.Code != http.StatusOK {
			t.Fatalf("step %s = %d %s", a, rec.Code, rec.Body)
		}
	}
	var got struct {
		Decommission store.Decommission `json:"decommission"`
		Audit        []store.AuditEntry `json:"audit"`
	}
	rec = e.do("GET", "/v1/ops/tenants/"+tid+"/decommission", "alice", DutyView, "", "")
	decodeJSON(t, rec, &got)
	if got.Decommission.Status != domain.DecommissionDone || got.Decommission.CompletedAt == nil || len(got.Decommission.Steps) != 4 || got.Decommission.Reason != "contract ended" {
		t.Fatalf("decommission = %+v", got.Decommission)
	}
	// Requested once (the repeat wrote nothing), four steps and the completion, each with the actor.
	var actions []string
	for _, a := range got.Audit {
		actions = append(actions, a.Action)
		if a.Actor == "" {
			t.Fatalf("audit entry without an actor: %+v", a)
		}
	}
	if want := "tenant.decommission.requested,tenant.decommission.step,tenant.decommission.step,tenant.decommission.step,tenant.decommission.step,tenant.decommissioned"; strings.Join(actions, ",") != want {
		t.Fatalf("audit = %v", actions)
	}
	if events := e.outbox(t, tid); events[len(events)-1] != store.EventTenantDecommissioned {
		t.Fatalf("events = %v", events)
	}
	if rec := e.do("POST", "/v1/ops/tenants/"+ids.New()+":decommission", "alice", DutyDecommission, "", reason); rec.Code != http.StatusNotFound {
		t.Fatalf("a tenant never provisioned = %d", rec.Code)
	}
}

func TestDecommissionIsUnavailableWithoutIdentity(t *testing.T) {
	e := newEnv3(t, time.Now().UTC(), nil)
	tid := provisionTenant(t, e, "Acme")
	rec := e.do("POST", "/v1/ops/tenants/"+tid+":decommission", "alice", DutyDecommission, "", `{"reason":"x"}`)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "OPS-1730") {
		t.Fatalf("no directory = %d %s", rec.Code, rec.Body)
	}
}

type fakePrinter struct {
	doc []byte
	err error
}

func (f *fakePrinter) Render(_ context.Context, doc []byte) ([]byte, error) {
	f.doc = doc
	return []byte("%PDF-fake"), f.err
}

func TestCostStatementRendersAsAPDFThroughPrint(t *testing.T) {
	pr := &fakePrinter{}
	rates := mustRates(t)
	e := newEnv3(t, time.Now().UTC(), func(c *Config) { c.CostRates = &rates; c.Printer = pr; c.Seller = "Passage Cloud" })
	tid := ids.New()
	path := "/v1/ops/tenants/" + tid + "/costs/2026-10"
	if rec := e.do("PUT", path, "job", DutyCostRecord, "", `{"quantities":`+testUsage+`,"usdPerEur":"1.08","modelVersion":"v1"}`); rec.Code != http.StatusOK {
		t.Fatalf("record = %d %s", rec.Code, rec.Body)
	}
	rec := e.do("GET", path+"?format=pdf", "alice", DutyView, "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/pdf" || rec.Body.String() != "%PDF-fake" {
		t.Fatalf("pdf = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	var doc struct {
		DocumentType string                          `json:"documentType"`
		LegalEntity  struct{ Name string }           `json:"legalEntity"`
		Lines        [][]string                      `json:"lines"`
		Totals       []struct{ Label, Value string } `json:"totals"`
	}
	if err := json.Unmarshal(pr.doc, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.DocumentType != "PERIODIC_STATEMENT" || doc.LegalEntity.Name != "Passage Cloud" || len(doc.Lines) != len(domain.CostDimensions) || doc.Totals[0].Value != "137.65" {
		t.Fatalf("document = %+v", doc)
	}
	if rec := e.do("GET", path, "alice", DutyView, "", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Type"), "json") {
		t.Fatalf("json still the default: %d", rec.Code)
	}
	pr.err = errors.New("print down")
	if rec := e.do("GET", path+"?format=pdf", "alice", DutyView, "", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("print down = %d", rec.Code)
	}
	noPrint := newEnv3(t, time.Now().UTC(), func(c *Config) { c.CostRates = &rates })
	noPrint.do("PUT", path, "job", DutyCostRecord, "", `{"quantities":`+testUsage+`,"usdPerEur":"1.08","modelVersion":"v1"}`)
	if rec := noPrint.do("GET", path+"?format=pdf", "alice", DutyView, "", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no printer = %d", rec.Code)
	}
}

func mustRates(t *testing.T) domain.CostRates {
	t.Helper()
	r, err := domain.ParseCostRates(`{"cpu_core_hours":"0.04","memory_gb_hours":"0.005","storage_gb":"0.10","kafka_gb":"0.02","lakehouse_gb":"0.023","ai_tokens":"0.000002"}`)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestUserCountReporterFeedsTheLicenceUsage(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	tid := ids.New()
	dir := &fakeDirectory{status: "active", users: map[string]int{tid: 12}}
	e := newEnv3(t, now, func(c *Config) { c.Directory = dir })
	if rec := e.put("/v1/ops/tenants/"+tid+"/license", "alice", DutyLicenseInstall, "0", signedLicense(e.priv, tid, "2027-06-30", 9)); rec.Code != http.StatusOK {
		t.Fatalf("install = %d %s", rec.Code, rec.Body)
	}
	if n := e.api.ReportUsersOnce(context.Background()); n != 1 {
		t.Fatalf("reported %d tenants", n)
	}
	// 12 named users on a 9-user licence: GRACE, from the count identity gave, with no manual PUT.
	rec := e.do("GET", "/v1/ops/tenants/"+tid+"/license", "alice", DutyView, "", "")
	if !strings.Contains(rec.Body.String(), `"state":"GRACE"`) || !strings.Contains(rec.Body.String(), "12 full users active") {
		t.Fatalf("licence = %s", rec.Body)
	}
	if rec := e.do("GET", "/v1/ops/licenses", "alice", DutyView, "", ""); !strings.Contains(rec.Body.String(), `"activeFull":12`) {
		t.Fatalf("licences = %s", rec.Body)
	}
	// An unreachable identity keeps the last counts; no directory reports nothing.
	dir.err = errors.New("down")
	if n := e.api.ReportUsersOnce(context.Background()); n != 0 {
		t.Fatalf("reported %d with identity down", n)
	}
	if n := newEnv3(t, now, nil).api.ReportUsersOnce(context.Background()); n != 0 {
		t.Fatalf("reported %d without a directory", n)
	}
}

type fakeMeter struct{ calls []string }

func (f *fakeMeter) Quantities(_ context.Context, tenantID, month string) (map[string]decimal.Decimal, string, error) {
	f.calls = append(f.calls, tenantID+" "+month)
	q := map[string]decimal.Decimal{}
	for _, d := range domain.CostDimensions {
		q[d] = decimal.NewFromInt(100)
	}
	return q, "promql-v1", nil
}

func TestMonthlyMeteringProducesOneStatementPerProvisionedTenantAndIsIdempotent(t *testing.T) {
	now := time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC)
	rates := mustRates(t)
	meter := &fakeMeter{}
	e := newEnv3(t, now, func(c *Config) { c.CostRates = &rates; c.Meter = meter; c.FX = StaticFX("1.08") })
	tid := provisionTenant(t, e, "Cedar Ridge Pumps")
	// A tenant still PROVISIONING is not metered: only PROVISIONED tenants are.
	if n := e.api.MeterOnce(context.Background(), now); n != 0 {
		t.Fatalf("metered %d before provisioning completed", n)
	}
	if _, err := e.admin.Exec(context.Background(), `update ops_tenant_provision set status = 'PROVISIONED' where customer_tenant = $1`, tid); err != nil {
		t.Fatal(err)
	}
	if n := e.api.MeterOnce(context.Background(), now); n != 1 {
		t.Fatalf("metered %d", n)
	}
	if n := e.api.MeterOnce(context.Background(), now); n != 0 || len(meter.calls) != 1 || meter.calls[0] != tid+" 2026-10" {
		t.Fatalf("a second run metered %d, calls %v", n, meter.calls)
	}
	rec := e.do("GET", "/v1/ops/tenants/"+tid+"/costs/2026-10", "alice", DutyView, "", "")
	var st store.CostRecord
	decodeJSON(t, rec, &st)
	if st.ModelVersion != "promql-v1" || st.RecordedBy != "service:ops-reporter" || st.USDPerEUR != "1.08" || st.CostUSD == "" {
		t.Fatalf("statement = %+v", st)
	}
	// With no EUR rate the job records nothing and says why in the log; it never invents one.
	e2 := newEnv3(t, now, func(c *Config) { c.CostRates = &rates; c.Meter = &fakeMeter{}; c.FX = StaticFX("") })
	tid2 := provisionTenant(t, e2, "Acme")
	_, _ = e2.admin.Exec(context.Background(), `update ops_tenant_provision set status = 'PROVISIONED' where customer_tenant = $1`, tid2)
	if n := e2.api.MeterOnce(context.Background(), now); n != 0 {
		t.Fatalf("metered %d without an EUR rate", n)
	}
}

func TestMeterQueriesNeedEveryDimension(t *testing.T) {
	all := `{"cpu_core_hours":"a{tenant=\"{tenant}\"}","memory_gb_hours":"b{tenant=\"{tenant}\"}","storage_gb":"c{tenant=\"{tenant}\"}","kafka_gb":"d{tenant=\"{tenant}\"}","lakehouse_gb":"e{tenant=\"{tenant}\"}","ai_tokens":"f{tenant=\"{tenant}\"}"}`
	if _, err := ParseMeterQueries(all); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseMeterQueries(`{"cpu_core_hours":"a{tenant=\"{tenant}\"}"}`); err == nil {
		t.Fatal("missing dimensions accepted")
	}
	if _, err := ParseMeterQueries(strings.Replace(all, `a{tenant=\"{tenant}\"}`, "a", 1)); err == nil {
		t.Fatal("a query with no {tenant} accepted")
	}
}

type fakeCloner struct {
	ran  []string
	fail bool
}

func (f *fakeCloner) Clone(_ context.Context, c domain.CloneSchedule) (string, error) {
	f.ran = append(f.ran, c.Name)
	if f.fail {
		return "partial output", errors.New("clone failed")
	}
	return "ok: 12 tables", nil
}

func TestCloneSchedulesAreStoredClaimedOncePerPeriodAndRecorded(t *testing.T) {
	clock := time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC)
	cl := &fakeCloner{}
	e := newEnv3(t, clock, func(c *Config) { c.Cloner = cl; c.Now = func() time.Time { return clock } })
	src := ids.New()
	body := `{"name":"nightly-cedar-ridge","sourceTenant":"` + src + `","schema":"party","masksFile":"/etc/passage/masks-party.json","intervalHours":24}`
	if rec := e.do("POST", "/v1/ops/clone-schedules", "alice", DutyView, "", body); rec.Code != http.StatusForbidden {
		t.Fatalf("create needs its duty: %d", rec.Code)
	}
	for _, bad := range []string{
		`{"name":"","sourceTenant":"` + src + `","schema":"party","masksFile":"m","intervalHours":24}`,
		`{"name":"x","sourceTenant":"nope","schema":"party","masksFile":"m","intervalHours":24}`,
		`{"name":"x","sourceTenant":"` + src + `","schema":"Party; drop","masksFile":"m","intervalHours":24}`,
		`{"name":"x","sourceTenant":"` + src + `","schema":"party","masksFile":"m","intervalHours":0}`,
	} {
		if rec := e.do("POST", "/v1/ops/clone-schedules", "alice", DutyCloneSchedule, "", bad); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d", bad, rec.Code)
		}
	}
	rec := e.do("POST", "/v1/ops/clone-schedules", "alice", DutyCloneSchedule, "", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var sched domain.CloneSchedule
	decodeJSON(t, rec, &sched)
	if rec := e.do("POST", "/v1/ops/clone-schedules", "alice", DutyCloneSchedule, "", body); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "OPS-1737") {
		t.Fatalf("duplicate name = %d %s", rec.Code, rec.Body)
	}
	// Due now: one run. The same tick again, and an hour later, run nothing; a day later runs it again.
	if n := e.api.CloneOnce(context.Background()); n != 1 || len(cl.ran) != 1 {
		t.Fatalf("first tick ran %d", n)
	}
	if n := e.api.CloneOnce(context.Background()); n != 0 {
		t.Fatalf("same tick ran %d", n)
	}
	clock = clock.Add(time.Hour)
	if n := e.api.CloneOnce(context.Background()); n != 0 {
		t.Fatalf("an hour later ran %d", n)
	}
	clock = clock.Add(24 * time.Hour)
	cl.fail = true
	if n := e.api.CloneOnce(context.Background()); n != 1 {
		t.Fatalf("a day later ran %d", n)
	}
	var runs struct{ Items []store.CloneRun }
	decodeJSON(t, e.do("GET", "/v1/ops/clone-schedules/"+sched.ID+"/runs", "alice", DutyView, "", ""), &runs)
	if len(runs.Items) != 2 || runs.Items[0].Status != "FAILED" || !strings.Contains(runs.Items[0].Detail, "clone failed") || runs.Items[1].Status != "OK" {
		t.Fatalf("runs = %+v", runs.Items)
	}
	var list struct{ Items []domain.CloneSchedule }
	decodeJSON(t, e.do("GET", "/v1/ops/clone-schedules", "alice", DutyView, "", ""), &list)
	if len(list.Items) != 1 || list.Items[0].LastStatus != "FAILED" {
		t.Fatalf("list = %+v", list.Items)
	}
	// A disabled schedule does not run; enabling makes it due.
	if rec := e.do("PUT", "/v1/ops/clone-schedules/"+sched.ID, "alice", DutyCloneSchedule, "", `{"enabled":false}`); rec.Code != http.StatusOK {
		t.Fatalf("disable = %d", rec.Code)
	}
	clock = clock.Add(48 * time.Hour)
	if n := e.api.CloneOnce(context.Background()); n != 0 {
		t.Fatalf("disabled schedule ran %d", n)
	}
	if rec := e.do("PUT", "/v1/ops/clone-schedules/"+sched.ID, "alice", DutyCloneSchedule, "", `{"enabled":true}`); rec.Code != http.StatusOK {
		t.Fatalf("enable = %d", rec.Code)
	}
	if n := e.api.CloneOnce(context.Background()); n != 1 {
		t.Fatalf("re-enabled schedule ran %d", n)
	}
	if rec := e.do("DELETE", "/v1/ops/clone-schedules/"+sched.ID, "alice", DutyCloneSchedule, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	if rec := e.do("DELETE", "/v1/ops/clone-schedules/"+sched.ID, "alice", DutyCloneSchedule, "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("delete again = %d", rec.Code)
	}
}

func TestCommandClonerRunsTenantRestoreWithTheSchedulesArguments(t *testing.T) {
	var gotName string
	var gotArgs []string
	c := CommandCloner{Command: "/opt/tenant-restore", Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return []byte("copied 3 tables\n"), nil
	}}
	detail, err := c.Clone(context.Background(), domain.CloneSchedule{SourceTenant: "t-1", Schema: "party", MasksFile: "/m.json"})
	if err != nil || detail != "copied 3 tables" || gotName != "/opt/tenant-restore" {
		t.Fatalf("detail %q err %v name %s", detail, err, gotName)
	}
	if strings.Join(gotArgs, " ") != "clone --tenant t-1 --schema party --masks /m.json --apply" {
		t.Fatalf("args = %v", gotArgs)
	}
	for _, a := range gotArgs {
		if strings.Contains(a, "postgres://") {
			t.Fatal("a database URL reached the command line")
		}
	}
}

func TestPreviousMonthNeverOverflowsOnTheEndOfAMonth(t *testing.T) {
	for now, want := range map[string]string{
		"2026-03-30T04:00:00Z": "2026-02", "2026-03-29T04:00:00Z": "2026-02", "2026-03-31T23:59:00Z": "2026-02",
		"2026-05-31T04:00:00Z": "2026-04", "2026-12-31T04:00:00Z": "2026-11", "2026-01-15T00:00:00Z": "2025-12", "2026-11-01T00:00:00Z": "2026-10",
	} {
		ts, _ := time.Parse(time.RFC3339, now)
		if got := previousMonth(ts); got != want {
			t.Errorf("previousMonth(%s) = %s, want %s", now, got, want)
		}
	}
}

func TestMeteringOnTheLastDaysOfAMonthBillsTheFinishedMonth(t *testing.T) {
	now := time.Date(2026, 3, 30, 4, 0, 0, 0, time.UTC)
	rates := mustRates(t)
	meter := &fakeMeter{}
	e := newEnv3(t, now, func(c *Config) { c.CostRates = &rates; c.Meter = meter; c.FX = StaticFX("1.08") })
	tid := provisionTenant(t, e, "Cedar Ridge Pumps")
	_, _ = e.admin.Exec(context.Background(), `update ops_tenant_provision set status = 'PROVISIONED' where customer_tenant = $1`, tid)
	if n := e.api.MeterOnce(context.Background(), now); n != 1 || len(meter.calls) != 1 || meter.calls[0] != tid+" 2026-02" {
		t.Fatalf("metered %d, calls %v", n, meter.calls)
	}
}

func TestCostUnknownIsTheOnlyReadErrorThatLetsMeteringGoOn(t *testing.T) {
	if !isCostUnknown(store.Problem(&domain.Error{Code: domain.CodeCostUnknown})) {
		t.Fatal("not-found should allow metering")
	}
	if isCostUnknown(errors.New("connection reset")) || isCostUnknown(store.Problem(&domain.Error{Code: domain.CodeCostInvalid})) {
		t.Fatal("another error must not")
	}
}

func TestMeterQueryGapsAreErrorsNotZeros(t *testing.T) {
	answers := map[string]string{
		"empty": `{"status":"success","data":{"result":[]}}`,
		"nan":   `{"status":"success","data":{"result":[{"metric":{},"value":[1,"NaN"]}]}}`,
		"inf":   `{"status":"success","data":{"result":[{"metric":{},"value":[1,"+Inf"]}]}}`,
		"two":   `{"status":"success","data":{"result":[{"metric":{},"value":[1,"1"]},{"metric":{},"value":[1,"2"]}]}}`,
		"one":   `{"status":"success","data":{"result":[{"metric":{},"value":[1,"42.5"]}]}}`,
	}
	for name, body := range answers {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		_, err := PromSource{URL: srv.URL}.scalar(context.Background(), "q", time.Now())
		srv.Close()
		if (name == "one") != (err == nil) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestOperationSLOsRefuseAMethod(t *testing.T) {
	if _, err := domain.ParseOperationSLOs(`[{"name":"a","service":"s","route":"/x","method":"POST"}]`); err == nil {
		t.Fatal("a method was accepted although the series have no method label")
	}
}

func TestCloneLeaseKeepsASecondReplicaOutAndAFailedLeaseIsRetried(t *testing.T) {
	clock := time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC)
	e := newEnv3(t, clock, func(c *Config) { c.Now = func() time.Time { return clock } })
	ctx := context.Background()
	src := ids.New()
	s, err := e.api.store.CreateCloneSchedule(ctx, domain.CloneSchedule{Name: "n", SourceTenant: src, Schema: "party", MasksFile: "m", IntervalHours: 1}, "alice", clock)
	if err != nil {
		t.Fatal(err)
	}
	lease := 2 * time.Hour
	if got, _ := e.api.store.ClaimDueClones(ctx, clock, lease); len(got) != 1 {
		t.Fatalf("first claim = %d", len(got))
	}
	// A second replica, a day into a "long" clone's lease, claims nothing; after the lease expires the lost run is claimed again.
	if got, _ := e.api.store.ClaimDueClones(ctx, clock.Add(time.Hour), lease); len(got) != 0 {
		t.Fatalf("a schedule with a run in flight was claimed again: %d", len(got))
	}
	if got, _ := e.api.store.ClaimDueClones(ctx, clock.Add(3*time.Hour), lease); len(got) != 1 {
		t.Fatalf("a dead run was not retried after its lease: %d", len(got))
	}
	// Recording the run releases the lease and moves the next run one interval after the start.
	if err := e.api.store.RecordCloneRun(ctx, s.ID, clock, clock.Add(10*time.Minute), "OK", "done"); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.api.store.ClaimDueClones(ctx, clock.Add(30*time.Minute), lease); len(got) != 0 {
		t.Fatalf("claimed before the next period: %d", len(got))
	}
	if got, _ := e.api.store.ClaimDueClones(ctx, clock.Add(61*time.Minute), lease); len(got) != 1 {
		t.Fatalf("not claimed in the next period: %d", len(got))
	}
}

func TestCloneScheduleChangesAreAuditedHistorySurvivesDeleteAndDecommissionedTenantsAreSkipped(t *testing.T) {
	clock := time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC)
	cl := &fakeCloner{}
	dir := &fakeDirectory{status: "suspended"}
	e := newEnv3(t, clock, func(c *Config) { c.Cloner = cl; c.Directory = dir; c.Now = func() time.Time { return clock } })
	src := provisionTenant(t, e, "Cedar Ridge Pumps")
	body := `{"name":"nightly","sourceTenant":"` + src + `","schema":"party","masksFile":"/m.json","intervalHours":24}`
	var sched domain.CloneSchedule
	decodeJSON(t, e.do("POST", "/v1/ops/clone-schedules", "alice", DutyCloneSchedule, "", body), &sched)
	if n := e.api.CloneOnce(context.Background()); n != 1 {
		t.Fatalf("ran %d", n)
	}
	e.do("PUT", "/v1/ops/clone-schedules/"+sched.ID, "bob", DutyCloneSchedule, "", `{"enabled":false}`)
	e.do("PUT", "/v1/ops/clone-schedules/"+sched.ID, "bob", DutyCloneSchedule, "", `{"enabled":true}`)
	// Decommissioning the source tenant stops its schedule.
	if rec := e.do("POST", "/v1/ops/tenants/"+src+":decommission", "alice", DutyDecommission, "", `{"reason":"gone"}`); rec.Code != http.StatusCreated {
		t.Fatalf("decommission = %d %s", rec.Code, rec.Body)
	}
	clock = clock.Add(72 * time.Hour)
	if n := e.api.CloneOnce(context.Background()); n != 0 {
		t.Fatalf("a decommissioned tenant's schedule ran %d", n)
	}
	if rec := e.do("DELETE", "/v1/ops/clone-schedules/"+sched.ID, "carol", DutyCloneSchedule, "", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", rec.Code)
	}
	// The run history outlives the schedule, and a second schedule may reuse the name.
	var n int
	if err := e.admin.QueryRow(context.Background(), `select count(*) from ops_clone_run where schedule_id = $1`, sched.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("runs after delete = %d (%v)", n, err)
	}
	if rec := e.do("POST", "/v1/ops/clone-schedules", "alice", DutyCloneSchedule, "", body); rec.Code != http.StatusCreated {
		t.Fatalf("reusing a deleted schedule's name = %d %s", rec.Code, rec.Body)
	}
	trail, err := e.api.store.Audit(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, a := range trail {
		if strings.HasPrefix(a.Action, "clone.") {
			got = append(got, a.Action+"/"+a.Actor)
		}
	}
	want := "clone.schedule.created/alice,clone.run.ok/service:ops-clone,clone.schedule.disabled/bob,clone.schedule.enabled/bob,clone.schedule.deleted/carol,clone.schedule.created/alice"
	if strings.Join(got, ",") != want {
		t.Fatalf("audit = %v", got)
	}
}
