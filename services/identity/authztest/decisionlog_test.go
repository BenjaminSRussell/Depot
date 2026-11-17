package authztest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/fieldcrypt"
	"github.com/BenjaminSRussell/depot/platform/logpseudo"
)

const (
	repoPolicyDir   = "../../../policies/authz"
	extAuthzOPAYAML = "../../../deploy/gateway/authz-opa.yaml"
)

// bundleRoots are identity's bundle roots (services/identity/policy_bundle.go; TestBundleRootsLeaveSystemToTheLocalPolicies
// there keeps them off data.system).
var bundleRoots = []string{"authz", "assignments", "scoped_assignments", "duties", "service_grants", "service_principals", "operator_tenant_id"}

// configMapBlock is the block scalar under key (two-space indented, "key: |") in the ext_authz OPA's ConfigMap, dedented.
func configMapBlock(t *testing.T, key string) string {
	t.Helper()
	manifest, err := os.ReadFile(extAuthzOPAYAML)
	if err != nil {
		t.Fatal(err)
	}
	_, block, found := strings.Cut(string(manifest), "\n  "+key+": |\n")
	if !found {
		t.Fatalf("authz-opa.yaml has no %s", key)
	}
	var out strings.Builder
	for _, line := range strings.Split(block, "\n") {
		if line != "" && !strings.HasPrefix(line, "    ") {
			break // the end of the block scalar
		}
		out.WriteString(strings.TrimPrefix(line, "    ") + "\n")
	}
	return out.String()
}

// decisionLogConfig is the labels and decision_logs part of the shipped config.yaml (its bundle service, signing keys and
// the Envoy plugin need identity and the opa-envoy build, which this test replaces with a local bundle server).
func decisionLogConfig(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	keep := false
	for _, line := range strings.Split(configMapBlock(t, "config.yaml"), "\n") {
		if line != "" && !strings.HasPrefix(line, " ") {
			key, _, _ := strings.Cut(line, ":")
			keep = key == "labels" || key == "decision_logs"
		}
		if keep {
			out.WriteString(line + "\n")
		}
	}
	if !strings.Contains(out.String(), "mask_decision: /system/log/mask") {
		t.Fatalf("the shipped config does not mask with the local data.system.log.mask:\n%s", out.String())
	}
	return out.String()
}

// bundleArchive is a bundle laid out like identity's: .manifest with identity's roots, data.json, authz.rego.
func bundleArchive(t *testing.T, rego string, data map[string]any) []byte {
	t.Helper()
	manifest, _ := json.Marshal(map[string]any{"revision": "test", "roots": bundleRoots, "rego_version": 1})
	dataJSON, _ := json.Marshal(data)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		body []byte
	}{{".manifest", manifest}, {"data.json", dataJSON}, {"authz.rego", []byte(rego)}} {
		if err := tw.WriteHeader(&tar.Header{Name: "/" + f.name, Mode: 0o644, Size: int64(len(f.body))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(f.body)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

type extAuthzOPA struct {
	api, diag string
	out       *syncBuffer
}

// startExtAuthzOPA runs OPA as authz-opa.yaml does, with archive as the bundle identity serves: the shipped labels and
// decision_logs, the shipped local mask.rego and health.rego, and env.
func startExtAuthzOPA(t *testing.T, archive []byte, env ...string) extAuthzOPA {
	t.Helper()
	bin, err := exec.LookPath("opa")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("opa binary is required in CI")
		}
		t.Skip("opa binary not on PATH")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(archive)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	cfg := fmt.Sprintf("services:\n  identity:\n    url: %s\nbundles:\n  authz:\n    service: identity\n    resource: /bundle.tar.gz\n    polling:\n      min_delay_seconds: 1\n      max_delay_seconds: 2\n%s", server.URL, decisionLogConfig(t))
	files := map[string]string{"config.yaml": cfg, "mask.rego": configMapBlock(t, "mask.rego"), "health.rego": configMapBlock(t, "health.rego")}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	api, diag := freeAddr(t), freeAddr(t)
	out := &syncBuffer{}
	cmd := exec.Command(bin, "run", "--server", "--addr", api, "--diagnostic-addr", diag, "--config-file", filepath.Join(dir, "config.yaml"),
		filepath.Join(dir, "mask.rego"), filepath.Join(dir, "health.rego"))
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		for range exited {
		}
	})
	o := extAuthzOPA{api: "http://" + api, diag: "http://" + diag, out: out}
	// The bundle is active once /health?bundles answers 200.
	waitFor(t, out, func() bool { return status(o.diag+"/health?bundles") == http.StatusOK })
	return o
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().String()
}

func status(url string) int {
	resp, err := (&http.Client{Timeout: time.Second}).Get(url)
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func waitFor(t *testing.T, out *syncBuffer, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out; opa output:\n%s", out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ask sends one ext_authz check as the Envoy plugin would and returns the decision log OPA writes for it.
func (o extAuthzOPA) ask(t *testing.T, headers map[string]string, path, clientIP string) (map[string]any, string) {
	t.Helper()
	before := len(o.out.String())
	input := map[string]any{"input": map[string]any{"attributes": map[string]any{
		"source":  map[string]any{"address": map[string]any{"socketAddress": map[string]any{"address": clientIP}}},
		"request": map[string]any{"http": map[string]any{"method": "POST", "path": path, "headers": headers}},
	}}}
	body, _ := json.Marshal(input)
	resp, err := http.Post(o.api+"/v1/data/authz/envoy", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var answer map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&answer)
	_ = resp.Body.Close()
	var logged string
	waitFor(t, o.out, func() bool {
		for _, line := range strings.Split(o.out.String()[before:], "\n") {
			if strings.Contains(line, `"msg":"Decision Log"`) && strings.Contains(line, `"path":"authz/envoy"`) {
				logged = line
				return true
			}
		}
		return false
	})
	return answer, logged
}

const (
	logTenant   = "00000000-0000-0000-0000-0000000000c7"
	logUser     = "raw-user-5d1e"
	logToken    = "Bearer eyJ.secret.token"
	logClientIP = "203.0.113.9"
	logPath     = "/api/v1/numbering/sequences/secret-seq-77"
)

func logHeaders() map[string]string {
	return map[string]string{"x-depot-tenant": logTenant, "x-depot-user": logUser, "authorization": logToken, "x-request-id": "rid-7"}
}

func pseudonymKey() ([]byte, string) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(255 - i*7)
	}
	return raw, base64.StdEncoding.EncodeToString(raw)
}

// The shipped bundle and configuration: the pod is ready, the decision log keeps the record and erases the input, and
// the record's subject is Go's logpseudo pseudonym of the user under DEPOT_LOG_PSEUDONYM_KEY.
func TestExtAuthzDecisionLogKeepsTheRecordAndNoRawSubject(t *testing.T) {
	rego, err := os.ReadFile(filepath.Join(repoPolicyDir, "authz.rego"))
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]any
	raw, err := os.ReadFile(filepath.Join(repoPolicyDir, "data.json"))
	if err != nil || json.Unmarshal(raw, &data) != nil {
		t.Fatalf("data.json: %v", err)
	}
	data["assignments"] = map[string]any{logTenant: map[string][]string{logUser: {"numbering.read"}}}
	key, keyB64 := pseudonymKey()
	opa := startExtAuthzOPA(t, bundleArchive(t, string(rego), data), logpseudo.EnvKey+"="+keyB64)
	waitFor(t, opa.out, func() bool { return status(opa.diag+"/health/ready") == http.StatusOK })

	answer, logged := opa.ask(t, logHeaders(), logPath+"?x=1", logClientIP)
	result, _ := answer["result"].(map[string]any)
	if result["allowed"] != false {
		t.Fatalf("answer = %v", answer)
	}
	for _, secret := range []string{logUser, "secret.token", logClientIP, "secret-seq-77"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("decision log carries %q: %s", secret, logged)
		}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(logged), &m); err != nil {
		t.Fatal(err)
	}
	if _, has := m["input"]; has {
		t.Fatalf("input not erased: %s", logged)
	}
	mac, err := fieldcrypt.NewKeys("k", map[string][]byte{"k": key})
	if err != nil {
		t.Fatal(err)
	}
	labels, _ := m["labels"].(map[string]any)
	res, _ := m["result"].(map[string]any)
	record, _ := res["record"].(map[string]any)
	if labels["log_class"] != "audit.other" || record["event"] != "authz.decision" || record["tenant"] != logTenant ||
		record["request_id"] != "rid-7" || record["route"] != "POST /v1/numbering/**" || record["reason"] != "policy_denied" ||
		record["subject"] != logpseudo.PseudonymFor(mac, logTenant, logUser) {
		t.Fatalf("decision log = %s; want subject %s", logged, logpseudo.PseudonymFor(mac, logTenant, logUser))
	}
}

// A bundle without the mask rule and without data.authz.envoy (an identity image older than B-04, or a renamed rule): the
// local mask still erases the input, so no raw user, tenant, client address or path is logged, and the pod never turns
// ready, so Envoy is not sent to it.
func TestExtAuthzMaskDoesNotDependOnTheBundle(t *testing.T) {
	old := "package authz\n\nimport rego.v1\n\ndefault allow := false\n"
	opa := startExtAuthzOPA(t, bundleArchive(t, old, map[string]any{"duties": map[string]any{}}))
	_, logged := opa.ask(t, logHeaders(), logPath, logClientIP)
	for _, secret := range []string{logUser, logTenant, "secret.token", logClientIP, "secret-seq-77", "/api/v1/numbering"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("decision log under a bundle without the mask carries %q: %s", secret, logged)
		}
	}
	if !strings.Contains(logged, `"erased":["/input"]`) {
		t.Fatalf("input not erased: %s", logged)
	}
	if got := status(opa.diag + "/health/ready"); got == http.StatusOK {
		t.Fatalf("ready with a bundle that cannot answer data.authz.envoy (%d)", got)
	}
}

// No document of the bundle's policy evaluates to the pseudonymization key (B-04 re-review N3): data.authz, evaluated from
// exactly the files identity's bundle carries with DEPOT_LOG_PSEUDONYM_KEY set, never contains it, although the policy
// does read it (log_subject answers a pseudonym). GET /v1/data/authz on the OPA therefore cannot show the key.
func TestNoAuthzDocumentHoldsThePseudonymKey(t *testing.T) {
	bin, err := exec.LookPath("opa")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("opa binary is required in CI")
		}
		t.Skip("opa binary not on PATH")
	}
	_, keyB64 := pseudonymKey()
	// An Envoy check, so the envoy answer and its record are evaluated too.
	input, _ := json.Marshal(map[string]any{"attributes": map[string]any{"request": map[string]any{"http": map[string]any{
		"method": "GET", "path": "/api/v1/numbering/sequences/s1", "headers": map[string]string{"x-depot-tenant": "t1", "x-depot-user": "u1"},
	}}}})
	inputFile := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(inputFile, input, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(query string) string {
		cmd := exec.Command(bin, "eval", "--format", "raw", "-d", filepath.Join(repoPolicyDir, "authz.rego"),
			"-d", filepath.Join(repoPolicyDir, "data.json"), "-i", inputFile, query)
		cmd.Env = append(os.Environ(), logpseudo.EnvKey+"="+keyB64)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("opa %s: %v\n%s", query, err, out)
		}
		return string(out)
	}
	if got := strings.TrimSpace(run(`data.authz.log_subject("t1", "u1")`)); !strings.HasPrefix(got, logpseudo.Prefix) {
		t.Fatalf("the policy did not read the key: log_subject = %s", got)
	}
	whole := run("data.authz")
	var doc map[string]any
	if err := json.Unmarshal([]byte(whole), &doc); err != nil {
		t.Fatal(err)
	}
	envoy, _ := doc["envoy"].(map[string]any)
	record, _ := envoy["record"].(map[string]any)
	if subject, _ := record["subject"].(string); !strings.HasPrefix(subject, logpseudo.Prefix) {
		t.Fatalf("data.authz.envoy was not evaluated with the key: %v", envoy)
	}
	for _, form := range []string{keyB64, strings.TrimRight(keyB64, "=")} {
		if strings.Contains(whole, form) {
			t.Fatalf("data.authz carries the pseudonymization key")
		}
	}
}

// data.authz.log_pseudonym is logpseudo's pseudonym: the same answer as Go for several keys, tenants and values
// (whitespace, case, non-ASCII, a key with bytes that are not UTF-8).
func TestRegoPseudonymMatchesGo(t *testing.T) {
	bin, err := exec.LookPath("opa")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("opa binary is required in CI")
		}
		t.Skip("opa binary not on PATH")
	}
	type vector struct {
		Key    string `json:"key"`
		Tenant string `json:"tenant"`
		Value  string `json:"value"`
		want   string
	}
	var vectors []vector
	for k := 0; k < 3; k++ {
		raw := make([]byte, 32)
		for i := range raw {
			raw[i] = byte(i*37 + k*91 + 200)
		}
		mac, err := fieldcrypt.NewKeys("k", map[string][]byte{"k": raw})
		if err != nil {
			t.Fatal(err)
		}
		for _, tenant := range []string{"00000000-0000-0000-0000-000000000001", "tenant-b"} {
			for _, value := range []string{"user-1", "  User-ÄB ", "8a6b0c1d-2e3f-4a5b-8c6d-7e8f9a0b1c2d", "ÉLODIE@example.com"} {
				vectors = append(vectors, vector{base64.StdEncoding.EncodeToString(raw), tenant, value, logpseudo.PseudonymFor(mac, tenant, value)})
			}
		}
	}
	input, _ := json.Marshal(vectors)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "input.json"), input, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "eval", "--format", "raw", "-d", filepath.Join(repoPolicyDir, "authz.rego"), "-i", filepath.Join(dir, "input.json"),
		"[data.authz.log_pseudonym(v.key, v.tenant, v.value) | some v in input]")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opa: %v\n%s", err, out)
	}
	var got []string
	if err := json.Unmarshal(out, &got); err != nil || len(got) != len(vectors) {
		t.Fatalf("opa answered %s (%v)", out, err)
	}
	for i, v := range vectors {
		if got[i] != v.want {
			t.Errorf("tenant %q value %q: rego %s, Go %s", v.Tenant, v.Value, got[i], v.want)
		}
	}
}
