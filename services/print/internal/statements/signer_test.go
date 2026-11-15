package statements_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func testSigner(t *testing.T) *tenant.ServiceSigner {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		tenant.EnvServiceName:       "print",
		tenant.EnvServiceSigningKey: base64.StdEncoding.EncodeToString(priv.Seed()),
	}
	s, err := tenant.ServiceSignerFromEnv(func(k string) string { return env[k] })
	if err != nil || s == nil {
		t.Fatalf("signer: %v %v", s, err)
	}
	return s
}
