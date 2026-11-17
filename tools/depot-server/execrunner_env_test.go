package main

import (
	"encoding/base64"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/logpseudo"
)

// B-04: the supervisor's key material reaches a child only when that child's own configuration sets it.
func TestChildEnvDropsTheSupervisorsKeysUnlessTheChildSetsThem(t *testing.T) {
	parent := []string{
		"PATH=/bin",
		"FIELDCRYPT_KEYS=k1=master",
		"fieldcrypt_current_key_id=k1", // Windows names are case-insensitive
		"FIELDCRYPT_KEYS_FILE=/keys",
		"DEPOT_LOG_PSEUDONYM_KEY=pseudonym-key",
		"DEPOT_LOG_PSEUDONYM_KEY_FILE=/pk",
		"DEPOT_TENANT=t1",
	}
	got := childEnv(parent, Process{Name: "ledger", Env: map[string]string{"PORT": "8080"}})
	want := []string{"PATH=/bin", "DEPOT_TENANT=t1", "PORT=8080"}
	if !slices.Equal(got, want) {
		t.Fatalf("env = %v, want %v", got, want)
	}

	got = childEnv(parent, Process{Name: "ledger", Env: map[string]string{"FIELDCRYPT_KEYS": "k9=own", "FIELDCRYPT_CURRENT_KEY_ID": "k9"}})
	want = []string{"PATH=/bin", "DEPOT_TENANT=t1", "FIELDCRYPT_CURRENT_KEY_ID=k9", "FIELDCRYPT_KEYS=k9=own"}
	if !slices.Equal(got, want) {
		t.Fatalf("env of a child configured with its own keys = %v, want %v", got, want)
	}

	// A service that loads field-encryption keys inherits the supervisor's FIELDCRYPT_* (never the pseudonym key); its
	// own configuration still wins, as the later entry (os/exec keeps the last of duplicate names).
	for name := range fieldcryptServices {
		got = childEnv(parent, Process{Name: name})
		want = []string{"PATH=/bin", "FIELDCRYPT_KEYS=k1=master", "fieldcrypt_current_key_id=k1", "FIELDCRYPT_KEYS_FILE=/keys", "DEPOT_TENANT=t1"}
		if !slices.Equal(got, want) {
			t.Fatalf("env of %s = %v, want %v", name, got, want)
		}
	}
	got = childEnv(parent, Process{Name: "hr", Env: map[string]string{"FIELDCRYPT_KEYS": "k9=own"}})
	if got[len(got)-1] != "FIELDCRYPT_KEYS=k9=own" {
		t.Fatalf("hr's own key is not the last entry: %v", got)
	}
}

// The allow-list is exactly the services whose main loads field-encryption keys: a service that starts calling
// fieldcrypt.FromEnv must be added, or it would start without its keys on Windows.
func TestFieldcryptServicesAreTheOnesThatLoadKeys(t *testing.T) {
	mains, err := filepath.Glob("../../services/*/cmd/*/*.go")
	if err != nil || len(mains) < 50 {
		t.Fatalf("service mains: %d, %v", len(mains), err)
	}
	loaders := map[string]bool{}
	for _, f := range mains {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(src), "fieldcrypt.FromEnv") {
			loaders[filepath.Base(filepath.Dir(f))] = true
		}
	}
	if !maps.Equal(loaders, fieldcryptServices) {
		t.Fatalf("services that call fieldcrypt.FromEnv = %v, fieldcryptServices = %v", slices.Sorted(maps.Keys(loaders)), slices.Sorted(maps.Keys(fieldcryptServices)))
	}
}

// The gateway pseudonymizes under the dedicated key and never reads fieldcrypt's master keys for it.
func TestSubjectKeyEnvUsesOnlyTheDedicatedKey(t *testing.T) {
	master := base64.StdEncoding.EncodeToString(make([]byte, 32))
	env := map[string]string{"FIELDCRYPT_KEYS": "k1=" + master, "FIELDCRYPT_CURRENT_KEY_ID": "k1"}
	getenv := subjectKeyEnv(func(k string) string { return env[k] })
	if getenv("FIELDCRYPT_KEYS") != "" || getenv("fieldcrypt_keys_file") != "" {
		t.Fatal("subjectKeyEnv exposes the field-encryption keys")
	}
	// The key LoadSubjectKey(subjectKeyEnv(...)) loads: none from fieldcrypt alone, the dedicated one when set.
	if mac, err := logpseudo.KeyFromEnv(getenv); err != nil || mac != nil {
		t.Fatalf("key from fieldcrypt's variables: %v, %v", mac, err)
	}
	env[logpseudo.EnvKey] = master
	mac, err := logpseudo.KeyFromEnv(getenv)
	if err != nil || mac == nil || logpseudo.PseudonymFor(mac, "t1", "u1") == "" {
		t.Fatalf("dedicated key: %v, %v", mac, err)
	}
}
