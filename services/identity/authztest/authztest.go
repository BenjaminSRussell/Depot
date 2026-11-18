// Package authztest runs the B-04 shared route table (policies/authz/testdata/route_table.json) against a gateway: it
// loads the table, starts a real OPA on the policy directory with each case's role assignments, and mints the tokens the
// gateway verifies. devgateway and depot-server run the table with it; Envoy runs it as gateway_test.rego (the OPA
// answer it would get) and deploy/scripts/test_gateway.py (the static checks of its configuration). Only tests import it.
package authztest

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Case is one row: a subject of the table's tenant with tenant-wide Duties, legal-entity-scoped ScopedDuties (legal entity
// -> duties) and the token's le claim LegalEntity, calling Method Path (the public path). Allow is what every gateway must
// answer.
type Case struct {
	Name         string              `json:"name"`
	Method       string              `json:"method"`
	Path         string              `json:"path"`
	Duties       []string            `json:"duties"`
	ScopedDuties map[string][]string `json:"scoped_duties"`
	LegalEntity  string              `json:"legal_entity"`
	Allow        bool                `json:"allow"`
	// Status is the status of a refusal: 400 for a path that is not canonical, else (0) 403.
	Status int `json:"status"`
	// TokenDutiesOnly puts Duties in the token's duties claim only, with no identity role assignment (a machine caller).
	TokenDutiesOnly bool `json:"token_duties_only"`
	// Subject is the token's sub for this case, unique in the table (set by Load).
	Subject string `json:"-"`
}

// Table is the route table.
type Table struct {
	Tenant string `json:"tenant"`
	Cases  []Case `json:"cases"`
}

// Load reads testdata/route_table.json under policyDir.
func Load(t testing.TB, policyDir string) Table {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(policyDir, "testdata", "route_table.json"))
	if err != nil {
		t.Fatal(err)
	}
	var table Table
	if err := json.Unmarshal(raw, &table); err != nil {
		t.Fatal(err)
	}
	if table.Tenant == "" || len(table.Cases) == 0 {
		t.Fatal("route table has no tenant or no cases")
	}
	for i := range table.Cases {
		table.Cases[i].Subject = fmt.Sprintf("route-table-user-%02d", i)
	}
	return table
}

// Prefixes are the service prefixes the table's paths call (/api/v1/<prefix>/... or /v1/<prefix>/...), sorted.
func (tb Table) Prefixes() []string {
	seen := map[string]bool{}
	for _, c := range tb.Cases {
		rest := strings.TrimPrefix(strings.TrimPrefix(c.Path, "/api"), "/v1/")
		prefix, _, _ := strings.Cut(rest, "/")
		seen[prefix] = true
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// StartOPA runs `opa run --server` on policyDir, puts every case's assignments into data.assignments and
// data.scoped_assignments, and returns its base URL. Without opa on PATH the test is skipped, or fails under CI.
func StartOPA(t testing.TB, policyDir string, table Table) string {
	t.Helper()
	bin, err := exec.LookPath("opa")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("opa binary is required in CI")
		}
		t.Skip("opa binary not on PATH")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()

	var out syncBuffer
	cmd := exec.Command(bin, "run", "--server", "--addr", addr, policyDir)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		for range exited { // the kill's error is expected
		}
	})
	base := "http://" + addr
	probe := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(60 * time.Second)
	for {
		select {
		case err := <-exited:
			t.Fatalf("opa exited early: %v\n%s", err, out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("opa server did not become healthy\n%s", out.String())
		}
		if resp, err := probe.Get(base + "/health"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	tenantWide := map[string][]string{}
	scoped := map[string]map[string][]string{}
	for _, c := range table.Cases {
		if !c.TokenDutiesOnly {
			tenantWide[c.Subject] = append([]string{}, c.Duties...)
		}
		scoped[c.Subject] = map[string][]string{}
		for le, duties := range c.ScopedDuties {
			scoped[c.Subject][le] = duties
		}
	}
	put(t, base+"/v1/data/assignments", map[string]any{table.Tenant: tenantWide})
	put(t, base+"/v1/data/scoped_assignments", map[string]any{table.Tenant: scoped})
	return base
}

func put(t testing.TB, url string, doc any) {
	t.Helper()
	body, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT %s: %d", url, resp.StatusCode)
	}
}

// Issuer signs RS256 tokens and serves their JWKS, like the realm the gateways' verifier trusts.
type Issuer struct {
	URL     string // the iss claim
	JWKSURL string
	key     *rsa.PrivateKey
}

// NewIssuer starts the JWKS server.
func NewIssuer(t testing.TB) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "route-table", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(jwks.Close)
	return &Issuer{URL: "http://127.0.0.1/realms/route-table", JWKSURL: jwks.URL, key: key}
}

// Token is the access token of a case: sub, tid, audience depot-api, the case's duties as the duties claim and its legal
// entity as le (left out when empty).
func (i *Issuer) Token(t testing.TB, tenantID string, c Case) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss": i.URL, "sub": c.Subject, "tid": tenantID, "aud": []string{"depot-api"},
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "duties": strings.Join(c.Duties, ","), "tier": "starter",
	}
	if c.LegalEntity != "" {
		claims["le"] = c.LegalEntity
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "route-table"})
	body, _ := json.Marshal(claims)
	signed := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	digest := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// Run sends every case through gateway (an http.Handler for the public paths) and checks its answer and its record: an
// allowed case reaches the upstream (reached reports whether the last request did, and forgets it), a denied one is
// answered 403 by the gateway; each case writes one authz.decision record from source with the expected outcome, a
// request id and the route prefix, and the case's raw subject appears nowhere in the log. takeLog returns the JSON log
// written since its last call.
func Run(t *testing.T, gateway http.Handler, issuer *Issuer, table Table, source string, reached func() bool, takeLog func() string) {
	t.Helper()
	takeLog()
	for _, c := range table.Cases {
		t.Run(c.Name, func(t *testing.T) {
			req := httptest.NewRequest(c.Method, c.Path, nil)
			req.Header.Set("Authorization", "Bearer "+issuer.Token(t, table.Tenant, c))
			if c.LegalEntity != "" {
				req.Header.Set("X-Depot-LegalEntity", c.LegalEntity)
			}
			rec := httptest.NewRecorder()
			gateway.ServeHTTP(rec, req)
			hit := reached()
			log := takeLog()
			refusal := http.StatusForbidden
			if c.Status != 0 {
				refusal = c.Status
			}
			switch {
			case c.Allow && (!hit || rec.Code >= 400):
				t.Fatalf("%s %s: want allowed, got %d (upstream reached: %v): %s", c.Method, c.Path, rec.Code, hit, rec.Body)
			case !c.Allow && (hit || rec.Code != refusal):
				t.Fatalf("%s %s: want %d, got %d (upstream reached: %v): %s", c.Method, c.Path, refusal, rec.Code, hit, rec.Body)
			}
			recs := Records(t, log)
			if len(recs) != 1 {
				t.Fatalf("want one decision record, got %d:\n%s", len(recs), log)
			}
			r := recs[0]
			outcome := map[bool]string{true: "allow", false: "deny"}[c.Allow]
			prefix := strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(c.Path, "/api"), "/"), "/", 3)
			route, tenantID := c.Method+" /"+prefix[0]+"/"+prefix[1]+"/**", table.Tenant
			if refusal == http.StatusBadRequest {
				// Refused before the token is read: no route, no tenant.
				route, tenantID = c.Method+" unmatched", ""
				if r["reason"] != "not_canonical" {
					t.Fatalf("record = %v; want reason not_canonical", r)
				}
			}
			if r["source"] != source || r["outcome"] != outcome || r["tenant"] != tenantID || r["route"] != route || r["request_id"] == "" {
				t.Fatalf("record = %v; want source %s, outcome %s, route %s and a request id", r, source, outcome, route)
			}
			if c.Allow && r["duty"] == "" {
				t.Fatalf("allow record names no granting duty: %v", r)
			}
			if strings.Contains(log, c.Subject) {
				t.Fatalf("the raw subject %q is in the log:\n%s", c.Subject, log)
			}
		})
	}
}

// Records are the authz.decision records in a JSON log (one object per line).
func Records(t testing.TB, log string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "authz.decision" {
			out = append(out, m)
		}
	}
	return out
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
