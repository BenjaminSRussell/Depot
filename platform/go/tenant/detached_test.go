package tenant_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

func TestDetachedSignaturesAreDomainSeparated(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := tenant.NewServiceSigner("party", priv)
	if err != nil {
		t.Fatal(err)
	}
	signer = signer.WithKeyID("k1")
	v := tenant.NewServiceVerifierKids("audit", map[string]map[string]ed25519.PublicKey{"party": {"k1": pub}})
	const domain = "depot.audit-ctx.v1."
	iss, kid, sig, err := signer.SignDetached(domain, []byte("msg"))
	if err != nil || iss != "party" || kid != "k1" {
		t.Fatalf("%s %s %v", iss, kid, err)
	}
	if !v.VerifyDetached(domain, "party", "k1", []byte("msg"), sig) {
		t.Fatal("valid signature refused")
	}
	for name, ok := range map[string]bool{
		"other message": v.VerifyDetached(domain, "party", "k1", []byte("msg2"), sig),
		"other domain":  v.VerifyDetached("depot.other.v1.", "party", "k1", []byte("msg"), sig),
		"other issuer":  v.VerifyDetached(domain, "billing", "k1", []byte("msg"), sig),
		"unknown kid":   v.VerifyDetached(domain, "party", "k2", []byte("msg"), sig),
		"nil verifier":  (*tenant.ServiceVerifier)(nil).VerifyDetached(domain, "party", "k1", []byte("msg"), sig),
	} {
		if ok {
			t.Errorf("%s verified", name)
		}
	}
	for _, d := range []string{"depot.service-token.v1.", "depot.service-token.v1.x.", "depot.service-token.", "depot."} {
		if _, _, _, err := signer.SignDetached(d, []byte("x")); err == nil {
			t.Fatalf("%q shares a prefix with the service-token domain and must be refused", d)
		}
		if v.VerifyDetached(d, "party", "k1", []byte("x"), sig) {
			t.Fatalf("%q verified", d)
		}
	}
	if _, _, _, err := signer.SignDetached("audit", []byte("x")); err == nil {
		t.Fatal("a malformed domain must be refused")
	}
}
