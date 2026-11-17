package logpseudo_test

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/logpseudo"
)

func TestSubjectIsTheTenantKeyedPseudonymAndNeverTheValue(t *testing.T) {
	t.Cleanup(logpseudo.ResetSubjectKey)
	k := keys(t)
	logpseudo.SetSubjectKey(k)
	a := logpseudo.Subject(tenantA, "user-1")
	if a != logpseudo.PseudonymFor(k, tenantA, "user-1") || !strings.HasPrefix(a, logpseudo.Prefix) {
		t.Fatalf("Subject = %q", a)
	}
	if b := logpseudo.Subject(tenantB, "user-1"); b == a {
		t.Fatal("the same pseudonym in two tenants")
	}
	if got := logpseudo.Subject("", "user-1"); got != logpseudo.Redacted {
		t.Fatalf("no tenant: %q", got)
	}
	if got := logpseudo.Subject(tenantA, ""); got != "" {
		t.Fatalf("empty value: %q", got)
	}
	logpseudo.SetSubjectKey(nil)
	if got := logpseudo.Subject(tenantA, "user-1"); got != logpseudo.Redacted {
		t.Fatalf("no key: %q", got)
	}
	logpseudo.ResetSubjectKey()
	if got := logpseudo.Subject(tenantA, "user-1"); got != logpseudo.Redacted {
		t.Fatalf("key never loaded: %q", got)
	}
}

func TestLoadSubjectKeyReadsFieldcryptOnceAndRedactsOnABadConfiguration(t *testing.T) {
	t.Cleanup(logpseudo.ResetSubjectKey)
	logpseudo.ResetSubjectKey()
	if err := logpseudo.LoadSubjectKey(func(k string) string {
		return map[string]string{"FIELDCRYPT_KEYS": "k1=not-a-key"}[k]
	}); err == nil {
		t.Fatal("a bad key configuration loaded")
	}
	if got := logpseudo.Subject(tenantA, "user-1"); got != logpseudo.Redacted {
		t.Fatalf("after a bad configuration: %q", got)
	}

	logpseudo.ResetSubjectKey()
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	env := func(k string) string {
		return map[string]string{"FIELDCRYPT_KEYS": "k1=" + key, "FIELDCRYPT_CURRENT_KEY_ID": "k1"}[k]
	}
	if err := logpseudo.LoadSubjectKey(env); err != nil {
		t.Fatal(err)
	}
	first := logpseudo.Subject(tenantA, "user-1")
	if !strings.HasPrefix(first, logpseudo.Prefix) {
		t.Fatalf("Subject = %q", first)
	}
	// A second load (each httpx.Serve listener calls it) changes nothing.
	if err := logpseudo.LoadSubjectKey(func(string) string { return "" }); err != nil || logpseudo.Subject(tenantA, "user-1") != first {
		t.Fatal("a second load replaced the key")
	}
}

// Only SubjectAttr's value passes the handler as it is; a string that merely looks like a pseudonym (a user who set their
// name to another user's pseudonym, read from a shared log) is pseudonymized like any personal value.
func TestHandlerPassesOnlySubjectAttrAndTheRedactedMarker(t *testing.T) {
	t.Cleanup(logpseudo.ResetSubjectKey)
	k := keys(t)
	logpseudo.SetSubjectKey(k)
	log, buf := capture(k)
	victim := logpseudo.PseudonymFor(k, tenantA, "victim")
	log.InfoContext(ctxOf(tenantA), "x", logpseudo.SubjectAttr("subject", tenantA, "user-1"), "user", logpseudo.Redacted,
		"actor", victim, "caller", "pn_not-a-pseudonym")
	got := lines(t, buf)[0]
	if got["subject"] != logpseudo.PseudonymFor(k, tenantA, "user-1") || got["user"] != logpseudo.Redacted {
		t.Fatalf("subject attribute or marker rewritten: %v", got)
	}
	if got["actor"] == victim || got["actor"] != logpseudo.PseudonymFor(k, tenantA, victim) {
		t.Fatalf("a look-alike pseudonym passed through: %v", got)
	}
	if got["caller"] == "pn_not-a-pseudonym" {
		t.Fatalf("a value that only starts like a pseudonym passed through: %v", got)
	}
}

// Without the handler SubjectAttr logs as the plain pseudonym string.
func TestSubjectAttrLogsAsAStringWithoutTheHandler(t *testing.T) {
	t.Cleanup(logpseudo.ResetSubjectKey)
	k := keys(t)
	logpseudo.SetSubjectKey(k)
	a := logpseudo.SubjectAttr("subject", tenantA, "user-1")
	if got := a.Value.Resolve().String(); got != logpseudo.PseudonymFor(k, tenantA, "user-1") {
		t.Fatalf("resolved = %q", got)
	}
}

// The dedicated key (DEPOT_LOG_PSEUDONYM_KEY or _FILE) comes first and goes through fieldcrypt's MAC derivation, so the
// same 32 bytes as fieldcrypt's current key give the same pseudonym; without it fieldcrypt's key is used; a dedicated
// key that is set but bad is an error, never a fall back.
func TestKeyFromEnvPrefersTheDedicatedKey(t *testing.T) {
	raw := make([]byte, 32)
	raw[0] = 7
	b64 := base64.StdEncoding.EncodeToString(raw)
	other := base64.StdEncoding.EncodeToString(make([]byte, 32))
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	fc, err := logpseudo.KeyFromEnv(env(map[string]string{"FIELDCRYPT_KEYS": "k1=" + b64, "FIELDCRYPT_CURRENT_KEY_ID": "k1"}))
	if err != nil || fc == nil {
		t.Fatalf("fieldcrypt key: %v", err)
	}
	ded, err := logpseudo.KeyFromEnv(env(map[string]string{logpseudo.EnvKey: b64, "FIELDCRYPT_KEYS": "k1=" + other, "FIELDCRYPT_CURRENT_KEY_ID": "k1"}))
	if err != nil || ded == nil {
		t.Fatalf("dedicated key: %v", err)
	}
	if logpseudo.PseudonymFor(ded, tenantA, "user-1") != logpseudo.PseudonymFor(fc, tenantA, "user-1") {
		t.Fatal("the dedicated key does not derive like fieldcrypt's MAC (or fieldcrypt's key was used instead)")
	}
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte(b64+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := logpseudo.KeyFromEnv(env(map[string]string{logpseudo.EnvKeyFile: file}))
	if err != nil || logpseudo.PseudonymFor(fromFile, tenantA, "user-1") != logpseudo.PseudonymFor(fc, tenantA, "user-1") {
		t.Fatalf("key file: %v", err)
	}
	if none, err := logpseudo.KeyFromEnv(env(nil)); err != nil || none != nil {
		t.Fatalf("no key: %v, %v", none, err)
	}
	for _, bad := range []string{"not base64!", base64.StdEncoding.EncodeToString(make([]byte, 16))} {
		if _, err := logpseudo.KeyFromEnv(env(map[string]string{logpseudo.EnvKey: bad, "FIELDCRYPT_KEYS": "k1=" + other, "FIELDCRYPT_CURRENT_KEY_ID": "k1"})); err == nil {
			t.Fatalf("bad dedicated key %q accepted", bad)
		}
	}

	// LoadSubjectKey uses it too: a service with no fieldcrypt keys pseudonymizes instead of redacting.
	t.Cleanup(logpseudo.ResetSubjectKey)
	logpseudo.ResetSubjectKey()
	if err := logpseudo.LoadSubjectKey(env(map[string]string{logpseudo.EnvKey: b64})); err != nil {
		t.Fatal(err)
	}
	if got := logpseudo.Subject(tenantA, "user-1"); got != logpseudo.PseudonymFor(fc, tenantA, "user-1") {
		t.Fatalf("Subject = %q", got)
	}
}
