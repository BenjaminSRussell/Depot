package tenant

import (
	"crypto/ed25519"
	"errors"
	"strings"
)

// ErrDetachedDomain refuses a detached signature under the service-token domain or a malformed one, so a detached
// signature can never be presented as (part of) a service token, nor a token signature as a detached one.
var ErrDetachedDomain = errors.New("detached signature domain must be a dotted prefix ending in '.' other than the service-token one")

func detachedDomainOK(domain string) bool {
	return strings.HasSuffix(domain, ".") && strings.HasPrefix(domain, "depot.") && domain != serviceTokenContext
}

// SignDetached signs msg with the service's ADR-0010 key under the domain-separation prefix domain (for example
// "depot.audit-ctx.v1."): the signature covers domain followed by msg. It returns the issuing service and key id a
// verifier selects the public key by.
func (s *ServiceSigner) SignDetached(domain string, msg []byte) (iss, kid string, sig []byte, err error) {
	if s == nil {
		return "", "", nil, errors.New("no service signer")
	}
	if !detachedDomainOK(domain) {
		return "", "", nil, ErrDetachedDomain
	}
	return s.name, s.kid, ed25519.Sign(s.key, append([]byte(domain), msg...)), nil
}

// VerifyDetached reports whether sig is iss's signature (key kid, from the same DEPOT_SERVICE_PUBLIC_KEYS registry
// service tokens are verified with) over domain followed by msg. An unknown issuer or key id is false.
func (v *ServiceVerifier) VerifyDetached(domain, iss, kid string, msg, sig []byte) bool {
	if v == nil || !detachedDomainOK(domain) || len(sig) != ed25519.SignatureSize {
		return false
	}
	pub, known := v.keys[iss][kid]
	return known && ed25519.Verify(pub, append([]byte(domain), msg...), sig)
}
