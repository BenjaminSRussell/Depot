package authztest

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	repoPolicyDir   = "../../../policies/authz"
	extAuthzOPAYAML = "../../../deploy/gateway/authz-opa.yaml"
)

// decisionLogConfig is the labels and decision_logs part of the ext_authz OPA's config.yaml in authz-opa.yaml (the bundle,
// keys and the Envoy plugin need identity and the opa-envoy build, which this test does not have).
func decisionLogConfig(t *testing.T) string {
	t.Helper()
	manifest, err := os.ReadFile(extAuthzOPAYAML)
	if err != nil {
		t.Fatal(err)
	}
	_, block, found := strings.Cut(string(manifest), "  config.yaml: |\n")
	if !found {
		t.Fatal("authz-opa.yaml has no config.yaml")
	}
	var out strings.Builder
	keep := false
	for _, line := range strings.Split(block, "\n") {
		if !strings.HasPrefix(line, "    ") {
			break // the end of the block scalar
		}
		line = strings.TrimPrefix(line, "    ")
		if !strings.HasPrefix(line, " ") {
			key, _, _ := strings.Cut(line, ":")
			keep = key == "labels" || key == "decision_logs"
		}
		if keep {
			out.WriteString(line + "\n")
		}
	}
	if !strings.Contains(out.String(), "mask_decision:") {
		t.Fatalf("no decision_logs.mask_decision in authz-opa.yaml:\n%s", out.String())
	}
	return out.String()
}

// TestExtAuthzDecisionLogKeepsTheRecordAndNoRawSubject runs OPA with the decision-log settings of the shipped ext_authz
// OPA and the shipped policy, asks data.authz.envoy as the Envoy plugin would, and reads the decision log OPA writes: the
// input (raw subject, bearer token, client address) is erased, the record is kept, and the log class label is set.
func TestExtAuthzDecisionLogKeepsTheRecordAndNoRawSubject(t *testing.T) {
	bin, err := exec.LookPath("opa")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("opa binary is required in CI")
		}
		t.Skip("opa binary not on PATH")
	}
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(decisionLogConfig(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	var out syncBuffer
	cmd := exec.Command(bin, "run", "--server", "--addr", addr, "--config-file", cfg, repoPolicyDir)
	cmd.Stdout, cmd.Stderr = &out, &out
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
	base := "http://" + addr
	deadline := time.Now().Add(60 * time.Second)
	for {
		if resp, err := http.Get(base + "/health"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("opa did not start:\n%s", out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}

	const rawUser, token, clientIP = "raw-user-5d1e", "Bearer eyJ.secret.token", "203.0.113.9"
	put(t, base+"/v1/data/assignments", map[string]any{"t1": map[string][]string{rawUser: {"numbering.read"}}})
	input := map[string]any{"input": map[string]any{"attributes": map[string]any{
		"source": map[string]any{"address": map[string]any{"socketAddress": map[string]any{"address": clientIP}}},
		"request": map[string]any{"http": map[string]any{
			"method": "POST", "path": "/api/v1/numbering/sequences?x=1",
			"headers": map[string]string{"x-depot-tenant": "t1", "x-depot-user": rawUser, "authorization": token, "x-request-id": "rid-7"},
		}},
	}}}
	body, _ := json.Marshal(input)
	resp, err := http.Post(base+"/v1/data/authz/envoy", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var answer struct {
		Result struct {
			Allowed bool           `json:"allowed"`
			Record  map[string]any `json:"record"`
		} `json:"result"`
	}
	err = json.NewDecoder(resp.Body).Decode(&answer)
	_ = resp.Body.Close()
	if err != nil || answer.Result.Allowed || answer.Result.Record["reason"] != "policy_denied" {
		t.Fatalf("answer = %+v, %v", answer, err)
	}

	var logged map[string]any
	for time.Now().Before(deadline) && logged == nil {
		for _, line := range strings.Split(out.String(), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "Decision Log" {
				logged = m
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if logged == nil {
		t.Fatalf("no decision log:\n%s", out.String())
	}
	raw, _ := json.Marshal(logged)
	for _, secret := range []string{rawUser, "secret.token", clientIP} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("decision log carries %q: %s", secret, raw)
		}
	}
	if _, has := logged["input"]; has {
		t.Fatalf("input not erased: %s", raw)
	}
	labels, _ := logged["labels"].(map[string]any)
	result, _ := logged["result"].(map[string]any)
	record, _ := result["record"].(map[string]any)
	if labels["log_class"] != "audit.other" || record["event"] != "authz.decision" || record["tenant"] != "t1" ||
		record["request_id"] != "rid-7" || record["route"] != "POST /v1/numbering/**" || record["subject"] != "[redacted]" {
		t.Fatalf("decision log = %s", raw)
	}
}
