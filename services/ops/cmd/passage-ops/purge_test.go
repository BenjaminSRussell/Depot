package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ops/internal/provision"
)

type purgeAPI struct {
	mu       sync.Mutex
	requests []string
	result   map[string]string
	refuse   bool
}

func (p *purgeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, r.Method+" "+r.URL.Path)
	switch {
	case strings.HasSuffix(r.URL.Path, ":purge"):
		if p.refuse {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"code":"OPS-1739","detail":"no verified export"}`)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	case strings.HasSuffix(r.URL.Path, "/purge-result"):
		_ = json.NewDecoder(r.Body).Decode(&p.result)
		_, _ = io.WriteString(w, `{}`)
	case strings.HasSuffix(r.URL.Path, "/export-verification"):
		_, _ = io.WriteString(w, `{"exportId":"exp-1","exportVerifiedBy":"alice","exportVerifiedAt":"2026-11-16T10:00:00Z"}`)
	}
}

func runPurgeCmd(t *testing.T, api *purgeAPI, env map[string]string, args ...string) (string, error) {
	srv := httptest.NewServer(api)
	defer srv.Close()
	env["OPS_API_URL"], env["OPS_TOKEN"] = srv.URL, "tok"
	var out bytes.Buffer
	err := run(context.Background(), append([]string{"tenant"}, args...), &out, func(k string) string { return env[k] }, deps{httpClient: srv.Client()})
	return out.String(), err
}

func TestPurgeDryRunTouchesNothingAndNeedsItsOwnCredentials(t *testing.T) {
	api := &purgeAPI{}
	out, err := runPurgeCmd(t, api, map[string]string{"OPS_PURGE_DATABASES": `{"ledger":"postgres://never-used"}`}, "purge", "--tenant", "11111111-1111-4111-8111-111111111111", "--dry-run")
	if err != nil || !strings.Contains(out, "would delete") || len(api.requests) != 0 {
		t.Fatalf("%q %v %v", out, err, api.requests)
	}
	// The provisioning databases are not purge credentials.
	_, err = runPurgeCmd(t, api, map[string]string{"OPS_PROVISION_DATABASES": `{"ledger":"postgres://x"}`}, "purge", "--tenant", "11111111-1111-4111-8111-111111111111")
	if err == nil || !strings.Contains(err.Error(), "OPS_PURGE_DATABASES") || len(api.requests) != 0 {
		t.Fatalf("%v %v", err, api.requests)
	}
}

func TestPurgeIsRefusedByTheAPIAndFailuresAreReported(t *testing.T) {
	env := map[string]string{"OPS_PURGE_DATABASES": `{"ledger":"postgres://nobody:pw@127.0.0.1:1/none"}`}
	api := &purgeAPI{refuse: true}
	if _, err := runPurgeCmd(t, api, env, "purge", "--tenant", "11111111-1111-4111-8111-111111111111"); err == nil || !strings.Contains(err.Error(), "OPS-1739") {
		t.Fatalf("the API's refusal must stop the purge: %v", err)
	}
	if len(api.requests) != 1 {
		t.Fatalf("nothing but the authorisation was called: %v", api.requests)
	}
	api = &purgeAPI{}
	_, err := runPurgeCmd(t, api, env, "purge", "--tenant", "11111111-1111-4111-8111-111111111111")
	if err == nil || api.result["status"] != "FAILED" || strings.Contains(api.result["detail"], "pw@") {
		t.Fatalf("%v %v", err, api.result)
	}
	if !strings.Contains(api.result["detail"], "nothing was deleted") {
		t.Fatalf("the failure detail says what happened: %q", api.result["detail"])
	}
}

func TestVerifyExportCommand(t *testing.T) {
	api := &purgeAPI{}
	out, err := runPurgeCmd(t, api, map[string]string{}, "verify-export", "--tenant", "11111111-1111-4111-8111-111111111111", "--export-id", "exp-1")
	if err != nil || !strings.Contains(out, "exp-1 recorded as verified by alice") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := runPurgeCmd(t, api, map[string]string{}, "verify-export", "--tenant", "t"); err == nil {
		t.Fatal("--export-id is required")
	}
	_ = provision.ErrNotConfigured
}
