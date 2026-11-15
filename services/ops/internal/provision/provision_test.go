package provision

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// fakeSystems records every call in order and can fail one.
type fakeSystems struct {
	calls  []string
	failOn string
}

func (f *fakeSystems) do(call string, dry bool) (string, error) {
	if dry {
		call += "?"
	}
	f.calls = append(f.calls, call)
	if f.failOn == call {
		return "", errors.New("boom")
	}
	return call + " ok", nil
}

func (f *fakeSystems) EnsureRealm(_ context.Context, _ Tenant, dry bool) (string, error) {
	return f.do("realm+", dry)
}
func (f *fakeSystems) DeleteRealm(_ context.Context, _ Tenant, dry bool) (string, error) {
	return f.do("realm-", dry)
}
func (f *fakeSystems) EnsureDatabases(_ context.Context, _ Tenant, dry bool) (string, error) {
	return f.do("databases+", dry)
}
func (f *fakeSystems) DropDatabases(_ context.Context, _ Tenant, dry bool) (string, error) {
	return f.do("databases-", dry)
}
func (f *fakeSystems) EnsureAccess(_ context.Context, _ Tenant, dry bool) (string, error) {
	return f.do("kafka+", dry)
}
func (f *fakeSystems) RemoveAccess(_ context.Context, _ Tenant, dry bool) (string, error) {
	return f.do("kafka-", dry)
}
func (f *fakeSystems) EnsureNamespace(_ context.Context, _ Tenant, dry bool) (string, error) {
	return f.do("lakehouse+", dry)
}
func (f *fakeSystems) DropNamespace(_ context.Context, _ Tenant, dry bool) (string, error) {
	return f.do("lakehouse-", dry)
}

func (f *fakeSystems) executors() Executors {
	return Executors{Realm: f, Databases: f, Kafka: f, Lakehouse: f}
}

// fakeAPI is the ops API as the runner sees it.
type fakeAPI struct {
	provisionDone, decommissionDone map[string]bool
	reports                         []string
	licenceInstalled                bool
}

func (a *fakeAPI) ProvisionDone(context.Context, string) (map[string]bool, error) {
	return a.provisionDone, nil
}
func (a *fakeAPI) DecommissionDone(context.Context, string) (map[string]bool, error) {
	return a.decommissionDone, nil
}
func (a *fakeAPI) ReportProvisionStep(_ context.Context, _ string, action, status, _ string) error {
	if action == "license" && !a.licenceInstalled {
		return errors.New("OPS-1724: install the tenant's signed licence first")
	}
	a.reports = append(a.reports, "provision:"+action+":"+status)
	return nil
}
func (a *fakeAPI) ReportDecommissionStep(_ context.Context, _ string, action, status, _ string) error {
	a.reports = append(a.reports, "decommission:"+action+":"+status)
	return nil
}

var tenantX = Tenant{ID: "11111111-1111-4111-8111-111111111111", Name: "Cedar Ridge Pumps", Residency: "EU", Region: "eu-west", Tier: "PROD"}

func TestProvisionRunsTheStepsInOrderReportsThemAndWaitsForTheLicence(t *testing.T) {
	sys, api := &fakeSystems{}, &fakeAPI{provisionDone: map[string]bool{}}
	r := Runner{Exec: sys.executors(), Reporter: api}
	_, err := r.Provision(context.Background(), tenantX)
	if !errors.Is(err, ErrLicensePending) {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"realm+", "databases+", "kafka+", "lakehouse+"}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("calls = %v", sys.calls)
	}
	if want := []string{"provision:realm:DONE", "provision:databases:DONE", "provision:kafka-acls:DONE", "provision:lakehouse:DONE"}; !reflect.DeepEqual(api.reports, want) {
		t.Fatalf("reports = %v", api.reports)
	}
	// With the licence installed, a rerun skips the finished steps and completes the license step.
	api.licenceInstalled, api.provisionDone, api.reports, sys.calls = true, map[string]bool{"realm": true, "databases": true, "kafka-acls": true, "lakehouse": true}, nil, nil
	res, err := r.Provision(context.Background(), tenantX)
	if err != nil || len(sys.calls) != 0 || !reflect.DeepEqual(api.reports, []string{"provision:license:DONE"}) || res.Steps[0].Status != "SKIPPED" {
		t.Fatalf("resume: %v calls=%v reports=%v res=%+v", err, sys.calls, api.reports, res)
	}
}

func TestProvisionStopsAtAFailedStepAndReportsItAndARerunRetriesIt(t *testing.T) {
	sys, api := &fakeSystems{failOn: "kafka+"}, &fakeAPI{provisionDone: map[string]bool{}}
	r := Runner{Exec: sys.executors(), Reporter: api}
	if _, err := r.Provision(context.Background(), tenantX); err == nil {
		t.Fatal("a failed step passed")
	}
	if want := []string{"realm+", "databases+", "kafka+"}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("calls = %v", sys.calls)
	}
	if last := api.reports[len(api.reports)-1]; last != "provision:kafka-acls:FAILED" {
		t.Fatalf("reports = %v", api.reports)
	}
	sys.failOn, sys.calls = "", nil
	api.provisionDone = map[string]bool{"realm": true, "databases": true}
	if _, err := r.Provision(context.Background(), tenantX); !errors.Is(err, ErrLicensePending) {
		t.Fatal(err)
	}
	if want := []string{"kafka+", "lakehouse+"}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("retry calls = %v", sys.calls)
	}
}

func TestDryRunAsksEachAdapterAndChangesNothingAndReportsNothing(t *testing.T) {
	sys, api := &fakeSystems{}, &fakeAPI{provisionDone: map[string]bool{"realm": true}}
	r := Runner{Exec: sys.executors(), Reporter: api, DryRun: true}
	res, err := r.Provision(context.Background(), tenantX)
	if err != nil || len(api.reports) != 0 {
		t.Fatalf("dry run: %v reports=%v", err, api.reports)
	}
	// A dry run plans every step, DONE or not: the API state is not consulted.
	if want := []string{"realm+?", "databases+?", "kafka+?", "lakehouse+?"}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("calls = %v", sys.calls)
	}
	for _, s := range res.Steps {
		if s.Status != "PLANNED" {
			t.Fatalf("step %+v", s)
		}
	}
}

func TestDecommissionRunsInReverseOrderIsIdempotentAndResumes(t *testing.T) {
	sys, api := &fakeSystems{failOn: "databases-"}, &fakeAPI{decommissionDone: map[string]bool{}}
	r := Runner{Exec: sys.executors(), Reporter: api}
	if _, err := r.Decommission(context.Background(), tenantX); err == nil {
		t.Fatal("a failed step passed")
	}
	if want := []string{"lakehouse-", "kafka-", "databases-"}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("calls = %v", sys.calls)
	}
	// The realm is never touched while the databases still hold the tenant.
	for _, c := range sys.calls {
		if c == "realm-" {
			t.Fatal("the realm was deleted after a failed databases step")
		}
	}
	sys.failOn, sys.calls, api.reports = "", nil, nil
	api.decommissionDone = map[string]bool{"lakehouse": true, "kafka-acls": true}
	if _, err := r.Decommission(context.Background(), tenantX); err != nil {
		t.Fatal(err)
	}
	if want := []string{"databases-", "realm-"}; !reflect.DeepEqual(sys.calls, want) {
		t.Fatalf("resume calls = %v", sys.calls)
	}
	// A third run does nothing.
	api.decommissionDone = map[string]bool{"lakehouse": true, "kafka-acls": true, "databases": true, "realm": true}
	sys.calls = nil
	if _, err := r.Decommission(context.Background(), tenantX); err != nil || len(sys.calls) != 0 {
		t.Fatalf("done decommission: %v %v", err, sys.calls)
	}
}

func TestEveryExecutorIsRequiredAndTheLogAdapterAdmitsItChangesNothing(t *testing.T) {
	if _, err := (&Runner{Reporter: &fakeAPI{}}).Provision(context.Background(), tenantX); err == nil {
		t.Fatal("missing executors accepted")
	}
	detail, err := LogAdapter{}.EnsureRealm(context.Background(), tenantX, false)
	if err != nil || !strings.Contains(detail, "logged only") {
		t.Fatalf("detail = %q err=%v", detail, err)
	}
	if err := LogExecutors(nil).validate(); err != nil {
		t.Fatal(err)
	}
}
