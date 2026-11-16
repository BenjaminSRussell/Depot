package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ops/internal/provision"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

// tenantAPI is the ops tenant routes the command talks to.
type tenantAPI struct {
	mu       sync.Mutex
	requests []string
	refuse   bool
}

func (f *tenantAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/decommission"):
		_ = json.NewEncoder(w).Encode(map[string]any{"decommission": store.Decommission{Steps: []store.DecommissionStep{{Action: "lakehouse", Status: "DONE"}}}})
	case r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(store.Provision{Steps: []store.ProvisionStep{{Action: "realm", Status: "DONE"}}})
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":decommission"):
		if f.refuse {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "OPS-1731", "detail": "the tenant is active: suspend it first", "status": 409})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(store.Decommission{})
	default:
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}
}

type recordingSystems struct{ calls []string }

func (r *recordingSystems) rec(s string, dry bool) (string, error) {
	if dry {
		s += "?"
	}
	r.calls = append(r.calls, s)
	return s, nil
}
func (r *recordingSystems) EnsureRealm(_ context.Context, _ provision.Tenant, d bool) (string, error) {
	return r.rec("realm+", d)
}
func (r *recordingSystems) DeleteRealm(_ context.Context, _ provision.Tenant, d bool) (string, error) {
	return r.rec("realm-", d)
}
func (r *recordingSystems) EnsureDatabases(_ context.Context, _ provision.Tenant, d bool) (string, error) {
	return r.rec("databases+", d)
}
func (r *recordingSystems) DropDatabases(_ context.Context, _ provision.Tenant, d bool) (string, error) {
	return r.rec("databases-", d)
}
func (r *recordingSystems) EnsureAccess(_ context.Context, _ provision.Tenant, d bool) (string, error) {
	return r.rec("kafka+", d)
}
func (r *recordingSystems) RemoveAccess(_ context.Context, _ provision.Tenant, d bool) (string, error) {
	return r.rec("kafka-", d)
}
func (r *recordingSystems) EnsureNamespace(_ context.Context, _ provision.Tenant, d bool) (string, error) {
	return r.rec("lakehouse+", d)
}
func (r *recordingSystems) DropNamespace(_ context.Context, _ provision.Tenant, d bool) (string, error) {
	return r.rec("lakehouse-", d)
}

func runTenantCmd(t *testing.T, api *tenantAPI, sys *recordingSystems, args ...string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(api)
	defer srv.Close()
	d := deps{httpClient: srv.Client(), provisioners: func(func(string) string) (provision.Executors, []string, error) {
		return provision.Executors{Realm: sys, Databases: sys, Kafka: sys, Lakehouse: sys}, nil, nil
	}}
	var out bytes.Buffer
	env := map[string]string{"OPS_API_URL": srv.URL, "OPS_TOKEN": "tok"}
	err := run(context.Background(), append([]string{"tenant"}, args...), &out, func(k string) string { return env[k] }, d)
	return out.String(), err
}

func TestTenantProvisionRunsTheMissingStepsAndReportsThem(t *testing.T) {
	api, sys := &tenantAPI{}, &recordingSystems{}
	out, err := runTenantCmd(t, api, sys, "provision", "--tenant", "11111111-1111-4111-8111-111111111111")
	// The realm is DONE in the API; the other three run, and the license step is reported (the fake API accepts it).
	if err != nil || strings.Join(sys.calls, ",") != "databases+,kafka+,lakehouse+" {
		t.Fatalf("err=%v calls=%v out=%s", err, sys.calls, out)
	}
	if !strings.Contains(out, "realm") || !strings.Contains(out, "SKIPPED") {
		t.Fatalf("out = %s", out)
	}
	puts := 0
	for _, r := range api.requests {
		if strings.HasPrefix(r, "PUT ") && strings.Contains(r, "/provision-steps/") {
			puts++
		}
	}
	if puts != 4 { // databases, kafka-acls, lakehouse, license
		t.Fatalf("requests = %v", api.requests)
	}
}

func TestTenantDecommissionOpensItThenRunsInReverseOrder(t *testing.T) {
	api, sys := &tenantAPI{}, &recordingSystems{}
	if _, err := runTenantCmd(t, api, sys, "decommission", "--tenant", "11111111-1111-4111-8111-111111111111"); err == nil || !strings.Contains(err.Error(), "--reason") {
		t.Fatalf("no reason: %v", err)
	}
	_, err := runTenantCmd(t, api, sys, "decommission", "--tenant", "11111111-1111-4111-8111-111111111111", "--reason", "contract ended")
	// The lakehouse is DONE already; the rest go in reverse order.
	if err != nil || strings.Join(sys.calls, ",") != "kafka-,databases-,realm-" {
		t.Fatalf("err=%v calls=%v", err, sys.calls)
	}
	var opened bool
	for _, r := range api.requests {
		opened = opened || strings.HasSuffix(r, ":decommission")
	}
	if !opened {
		t.Fatalf("the decommission was not opened first: %v", api.requests)
	}
}

func TestTenantDecommissionOfAnActiveTenantChangesNothing(t *testing.T) {
	api, sys := &tenantAPI{refuse: true}, &recordingSystems{}
	_, err := runTenantCmd(t, api, sys, "decommission", "--tenant", "11111111-1111-4111-8111-111111111111", "--reason", "x")
	if err == nil || !strings.Contains(err.Error(), "OPS-1731") || len(sys.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, sys.calls)
	}
}

func TestTenantDryRunAsksTheAdaptersAndReportsNothing(t *testing.T) {
	api, sys := &tenantAPI{}, &recordingSystems{}
	if _, err := runTenantCmd(t, api, sys, "decommission", "--tenant", "11111111-1111-4111-8111-111111111111", "--dry-run"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sys.calls, ",") != "lakehouse-?,kafka-?,databases-?,realm-?" {
		t.Fatalf("calls = %v", sys.calls)
	}
	for _, r := range api.requests {
		if r != "GET /v1/ops/tenants/11111111-1111-4111-8111-111111111111" {
			t.Fatalf("a dry run made the request %s", r)
		}
	}
}

func TestTenantRealRunWithoutAdaptersIsRefusedBeforeTheAPIIsTouched(t *testing.T) {
	api := &tenantAPI{}
	srv := httptest.NewServer(api)
	defer srv.Close()
	env := map[string]string{"OPS_API_URL": srv.URL}
	for _, args := range [][]string{{"tenant", "provision", "--tenant", "t1"}, {"tenant", "decommission", "--tenant", "t1", "--reason", "x"}} {
		err := run(context.Background(), args, &bytes.Buffer{}, func(k string) string { return env[k] }, deps{httpClient: srv.Client()})
		if err == nil || !strings.Contains(err.Error(), "only --dry-run") {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if len(api.requests) != 0 {
		t.Fatalf("the API was called: %v", api.requests)
	}
}
