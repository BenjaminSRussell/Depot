package provision

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var tn = Tenant{ID: "11111111-1111-4111-8111-111111111111", Name: "Cedar Ridge Pumps", Region: "eu-west"}

type call struct{ Method, Path, Auth, Body string }

type rec struct {
	mu    sync.Mutex
	calls []call
}

func (r *rec) add(req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	req.Body = io.NopCloser(strings.NewReader(string(b)))
	r.mu.Lock()
	r.calls = append(r.calls, call{req.Method, req.URL.Path, req.Header.Get("Authorization"), string(b)})
	r.mu.Unlock()
}

func (r *rec) has(method, path string) bool {
	for _, c := range r.calls {
		if c.Method == method && c.Path == path {
			return true
		}
	}
	return false
}

func keycloakServer(t *testing.T, r *rec, realmStatus, createStatus, deleteStatus int) *Keycloak {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.add(q)
		switch {
		case q.URL.Path == "/realms/master/protocol/openid-connect/token":
			_ = q.ParseForm()
			if q.Form.Get("client_id") != "ops-admin" || q.Form.Get("client_secret") != "s3cret" || q.Form.Get("grant_type") != "client_credentials" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "tok"})
		case q.Header.Get("Authorization") != "Bearer tok":
			w.WriteHeader(401)
		case q.Method == "GET":
			w.WriteHeader(realmStatus)
		case q.Method == "POST":
			w.WriteHeader(createStatus)
		case q.Method == "DELETE":
			w.WriteHeader(deleteStatus)
		}
	}))
	t.Cleanup(srv.Close)
	return &Keycloak{BaseURL: srv.URL, ClientID: "ops-admin", ClientSecret: "s3cret", HTTP: srv.Client()}
}

func TestKeycloakCreatesMinimalRealm(t *testing.T) {
	r := &rec{}
	k := keycloakServer(t, r, 404, 201, 204)
	d, err := k.EnsureRealm(context.Background(), tn, false)
	if err != nil || !strings.Contains(d, "created realm depot-"+tn.ID) {
		t.Fatalf("%q %v", d, err)
	}
	var post call
	for _, c := range r.calls {
		if c.Method == "POST" && c.Path == "/admin/realms" {
			post = c
		}
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(post.Body), &body); err != nil || body["realm"] != "depot-"+tn.ID || body["displayName"] != tn.Name {
		t.Fatalf("body %s %v", post.Body, err)
	}
	if !strings.Contains(post.Body, tn.ID) || !strings.Contains(post.Body, "audience-depot-api") {
		t.Fatalf("the realm lacks the tenant claim or audience mapper: %s", post.Body)
	}
}

func TestKeycloakTemplateSubstitutesTenantAndDropsIdPs(t *testing.T) {
	dir := t.TempDir()
	tpl := filepath.Join(dir, "realm.json")
	_ = os.WriteFile(tpl, []byte(`{"realm":"depot","displayName":"Depot","clients":[{"clientId":"c"}],"identityProviders":[{"alias":"x"}],"identityProviderMappers":[{"a":"REPLACE_WITH_TENANT_ID"}],"attr":"REPLACE_WITH_TENANT_ID"}`), 0o600)
	r := &rec{}
	k := keycloakServer(t, r, 404, 201, 204)
	k.TemplatePath = tpl
	if _, err := k.EnsureRealm(context.Background(), tn, false); err != nil {
		t.Fatal(err)
	}
	var post call
	for _, c := range r.calls {
		if c.Method == "POST" && c.Path == "/admin/realms" {
			post = c
		}
	}
	if strings.Contains(post.Body, "identityProviders") || strings.Contains(post.Body, "REPLACE_WITH") || !strings.Contains(post.Body, `"attr":"`+tn.ID+`"`) || !strings.Contains(post.Body, `"realm":"depot-`+tn.ID+`"`) {
		t.Fatalf("%s", post.Body)
	}
	_ = os.WriteFile(tpl, []byte(`{"realm":"p","x":"REPLACE_WITH_SECRET"}`), 0o600)
	if _, err := k.EnsureRealm(context.Background(), tn, false); err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Fatalf("%v", err)
	}
}

func TestKeycloakExistsConflictFailureAndDryRun(t *testing.T) {
	r := &rec{}
	k := keycloakServer(t, r, 200, 201, 204)
	if d, err := k.EnsureRealm(context.Background(), tn, false); err != nil || !strings.Contains(d, "already exists") || r.has("POST", "/admin/realms") {
		t.Fatalf("%q %v %v", d, err, r.calls)
	}
	r = &rec{}
	k = keycloakServer(t, r, 404, 409, 204) // created concurrently
	if d, err := k.EnsureRealm(context.Background(), tn, false); err != nil || !strings.Contains(d, "already exists") {
		t.Fatalf("%q %v", d, err)
	}
	r = &rec{}
	k = keycloakServer(t, r, 404, 500, 204)
	if _, err := k.EnsureRealm(context.Background(), tn, false); err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("%v", err)
	}
	k.ClientSecret = "wrong"
	if _, err := k.EnsureRealm(context.Background(), tn, false); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("%v", err)
	}
	r = &rec{}
	k = keycloakServer(t, r, 404, 201, 204)
	if d, err := k.EnsureRealm(context.Background(), tn, true); err != nil || !strings.Contains(d, "would create") || len(r.calls) != 0 {
		t.Fatalf("dry run: %q %v %v", d, err, r.calls)
	}
	if _, err := k.EnsureRealm(context.Background(), Tenant{ID: "../master"}, true); err == nil {
		t.Fatal("a crafted tenant id was accepted")
	}
}

func TestKeycloakDelete(t *testing.T) {
	r := &rec{}
	k := keycloakServer(t, r, 200, 201, 204)
	rec1 := tn
	rec1.Realm = "acme-prod"
	if d, err := k.DeleteRealm(context.Background(), rec1, false); err != nil || !strings.Contains(d, "deleted") || !r.has("DELETE", "/admin/realms/acme-prod") {
		t.Fatalf("the recorded realm was not the one deleted: %q %v %v", d, err, r.calls)
	}
	k = keycloakServer(t, &rec{}, 200, 201, 404)
	if d, err := k.DeleteRealm(context.Background(), rec1, false); err != nil || !strings.Contains(d, "already absent") {
		t.Fatalf("%q %v", d, err)
	}
	// 404 for a name that was never recorded is a guess, not a success.
	if _, err := k.DeleteRealm(context.Background(), tn, false); err == nil || !strings.Contains(err.Error(), "never recorded") {
		t.Fatalf("%v", err)
	}
	k = keycloakServer(t, &rec{}, 200, 201, 403)
	if _, err := k.DeleteRealm(context.Background(), tn, false); err == nil {
		t.Fatal("403 accepted")
	}
	r = &rec{}
	k = keycloakServer(t, r, 200, 201, 204)
	if d, err := k.DeleteRealm(context.Background(), tn, true); err != nil || len(r.calls) != 0 || !strings.Contains(d, "would delete") {
		t.Fatalf("%q %v", d, err)
	}
}

func catalog(t *testing.T, r *rec, create, del int) IcebergCatalog {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		r.add(q)
		if q.Method == "POST" {
			w.WriteHeader(create)
		} else {
			w.WriteHeader(del)
		}
		_, _ = w.Write([]byte(`{"error":{"message":"m"}}`))
	}))
	t.Cleanup(srv.Close)
	return IcebergCatalog{BaseURL: srv.URL, Token: "ct", HTTP: srv.Client()}
}

func TestLakehouseNamespace(t *testing.T) {
	r := &rec{}
	c := catalog(t, r, 200, 204)
	if d, err := c.EnsureNamespace(context.Background(), tn, false); err != nil || !strings.Contains(d, "created") {
		t.Fatalf("%q %v", d, err)
	}
	if r.calls[0].Path != "/v1/namespaces" || r.calls[0].Auth != "Bearer ct" || !strings.Contains(r.calls[0].Body, `"tenant_11111111_1111_4111_8111_111111111111"`) {
		t.Fatalf("%+v", r.calls[0])
	}
	c = catalog(t, &rec{}, 409, 204)
	if d, err := c.EnsureNamespace(context.Background(), tn, false); err != nil || !strings.Contains(d, "already exists") {
		t.Fatalf("%q %v", d, err)
	}
	c = catalog(t, &rec{}, 500, 204)
	if _, err := c.EnsureNamespace(context.Background(), tn, false); err == nil {
		t.Fatal("500 accepted")
	}
	r = &rec{}
	c = catalog(t, r, 200, 204)
	c.Prefix = "wh"
	if d, err := c.EnsureNamespace(context.Background(), tn, true); err != nil || len(r.calls) != 0 || !strings.Contains(d, "/v1/wh/namespaces") {
		t.Fatalf("%q %v", d, err)
	}
}

func TestLakehouseDrop(t *testing.T) {
	r := &rec{}
	c := catalog(t, r, 200, 204)
	if d, err := c.DropNamespace(context.Background(), tn, false); err != nil || !strings.Contains(d, "dropped") || r.calls[0].Method != "DELETE" {
		t.Fatalf("%q %v", d, err)
	}
	c = catalog(t, &rec{}, 200, 404)
	if d, err := c.DropNamespace(context.Background(), tn, false); err != nil || !strings.Contains(d, "already absent") {
		t.Fatalf("%q %v", d, err)
	}
	c = catalog(t, &rec{}, 200, 409)
	if _, err := c.DropNamespace(context.Background(), tn, false); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("%v", err)
	}
	r = &rec{}
	c = catalog(t, r, 200, 204)
	if _, err := c.DropNamespace(context.Background(), tn, true); err != nil || len(r.calls) != 0 {
		t.Fatalf("%v", err)
	}
}

func TestSharedTopicKafkaIsDoneWithTheReason(t *testing.T) {
	for _, f := range []func(context.Context, Tenant, bool) (string, error){SharedTopicKafka{}.EnsureAccess, SharedTopicKafka{}.RemoveAccess} {
		d, err := f(context.Background(), tn, false)
		if err != nil || !strings.Contains(d, "not needed under the shared-topic model") {
			t.Fatalf("%q %v", d, err)
		}
	}
}

func TestFromEnv(t *testing.T) {
	ex, missing, err := FromEnv(func(string) string { return "" }, nil, nil)
	if err != nil || len(missing) != 3 {
		t.Fatalf("%v %v", missing, err)
	}
	if _, ok := ex.Kafka.(SharedTopicKafka); !ok {
		t.Fatal("kafka")
	}
	env := map[string]string{"OPS_KEYCLOAK_URL": "http://k", "OPS_KEYCLOAK_CLIENT_ID": "a", "OPS_KEYCLOAK_CLIENT_SECRET": "b",
		"OPS_PROVISION_DATABASES": `{"ledger":"postgres://x"}`, "OPS_LAKEHOUSE_CATALOG_URL": "http://c"}
	ex, missing, err = FromEnv(func(k string) string { return env[k] }, nil, nil)
	if err != nil || len(missing) != 0 {
		t.Fatalf("%v %v", missing, err)
	}
	if _, ok := ex.Realm.(Keycloak); !ok {
		t.Fatal("realm")
	}
	if _, ok := ex.Databases.(Databases); !ok {
		t.Fatal("databases")
	}
	delete(env, "OPS_KEYCLOAK_CLIENT_SECRET")
	if _, _, err := FromEnv(func(k string) string { return env[k] }, nil, nil); err == nil {
		t.Fatal("a Keycloak URL without a secret was accepted")
	}
	env["OPS_PROVISION_DATABASES"] = "nope"
	env["OPS_KEYCLOAK_CLIENT_SECRET"] = "b"
	if _, _, err := FromEnv(func(k string) string { return env[k] }, nil, nil); err == nil {
		t.Fatal("bad databases json accepted")
	}
}

func TestKeycloakRealRealmTemplate(t *testing.T) {
	r := &rec{}
	k := keycloakServer(t, r, 404, 201, 204)
	k.TemplatePath = filepath.Join("..", "..", "..", "..", "deploy", "keycloak", "realm-depot.json")
	if _, err := k.EnsureRealm(context.Background(), tn, true); err != nil {
		t.Fatalf("the repo's realm template is refused (dry run): %v", err)
	}
	if _, err := k.EnsureRealm(context.Background(), tn, false); err != nil {
		t.Fatalf("the repo's realm template is refused: %v", err)
	}
	var post call
	for _, c := range r.calls {
		if c.Method == "POST" && c.Path == "/admin/realms" {
			post = c
		}
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(post.Body), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["realm"] != "depot-"+tn.ID || doc["identityProviders"] != nil || strings.Contains(post.Body, "REPLACE_WITH") {
		t.Fatalf("%v", doc["realm"])
	}
	var tid, usermodelTid int
	for _, c := range doc["clients"].([]any) {
		cl := c.(map[string]any)
		if cl["clientId"] != "depot-services" {
			continue
		}
		for _, m := range cl["protocolMappers"].([]any) {
			mm := m.(map[string]any)
			if mm["protocolMapper"] == "oidc-hardcoded-claim-mapper" && mm["config"].(map[string]any)["claim.value"] == tn.ID {
				tid++
			}
			if mm["name"] == "tid" {
				usermodelTid++
			}
		}
	}
	if tid != 1 || usermodelTid != 0 {
		t.Fatalf("depot-services needs one hard-coded tid mapper and no attribute-based one: %d %d", tid, usermodelTid)
	}
	if !strings.Contains(post.Body, "audience-depot-api") {
		t.Fatal("the audience mapper is missing")
	}
}

type realmReporter struct{ realm string }

func (r *realmReporter) ProvisionDone(context.Context, string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (r *realmReporter) ReportProvisionStep(context.Context, string, string, string, string) error {
	return nil
}
func (r *realmReporter) DecommissionDone(context.Context, string) (map[string]bool, error) {
	return map[string]bool{}, nil
}
func (r *realmReporter) ReportDecommissionStep(context.Context, string, string, string, string) error {
	return nil
}
func (r *realmReporter) ReportRealmStep(_ context.Context, _, status, _, realm string) error {
	r.realm = realm
	return nil
}

func TestRunnerReportsTheRealmName(t *testing.T) {
	r := &rec{}
	k := keycloakServer(t, r, 404, 201, 204)
	k.RealmPrefix = "acme-"
	rep := &realmReporter{}
	run := &Runner{Exec: Executors{Realm: *k, Databases: SharedTopicKafka2{}, Kafka: SharedTopicKafka{}, Lakehouse: SharedTopicKafka2{}}, Reporter: rep}
	_, _ = run.Provision(context.Background(), tn)
	if rep.realm != "acme-"+tn.ID {
		t.Fatalf("realm reported %q", rep.realm)
	}
}

// SharedTopicKafka2 is a do-nothing database and lakehouse for the runner test.
type SharedTopicKafka2 struct{}

func (SharedTopicKafka2) EnsureDatabases(context.Context, Tenant, bool) (string, error) {
	return "ok", nil
}
func (SharedTopicKafka2) DropDatabases(context.Context, Tenant, bool) (string, error) {
	return "ok", nil
}
func (SharedTopicKafka2) EnsureNamespace(context.Context, Tenant, bool) (string, error) {
	return "ok", nil
}
func (SharedTopicKafka2) DropNamespace(context.Context, Tenant, bool) (string, error) {
	return "ok", nil
}
