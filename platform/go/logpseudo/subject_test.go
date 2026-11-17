package logpseudo_test

import (
	"encoding/base64"
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
	env := func(k string) string { return map[string]string{"FIELDCRYPT_KEYS": "k1=" + key, "FIELDCRYPT_CURRENT_KEY_ID": "k1"}[k] }
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

func TestHandlerLeavesAPseudonymOrTheRedactedMarkerAsItIs(t *testing.T) {
	k := keys(t)
	log, buf := capture(k)
	pn := logpseudo.PseudonymFor(k, tenantA, "user-1")
	log.InfoContext(ctxOf(tenantA), "x", "subject", pn, "user", logpseudo.Redacted, "actor", "pn_not-a-pseudonym")
	got := lines(t, buf)[0]
	if got["subject"] != pn || got["user"] != logpseudo.Redacted {
		t.Fatalf("pseudonym or marker rewritten: %v", got)
	}
	if got["actor"] == "pn_not-a-pseudonym" {
		t.Fatalf("a value that only starts like a pseudonym passed through: %v", got)
	}
}
