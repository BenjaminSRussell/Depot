package registry

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

const legalA = "0195f0a0-0000-7000-8000-0000000000e1"

func tenantAdminCtx() context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: t1, UserID: "admin-1",
		Duties: map[string]struct{}{tenant.DutyTenantAdmin: {}, "shopfloor.setup.maintain": {}}})
}

// B-03b re-review R1: the check signs a tenant-level service token for the calling tenant administrator with only the
// lookup duty (the grant shopfloor -> identity carries it), and reads identity's answer strictly.
func TestLegalEntitiesSignsTheLookupAndReadsTheAnswer(t *testing.T) {
	s, pub := signer(t)
	verifier := tenant.NewServiceVerifierKids("identity", map[string]map[string]ed25519.PublicKey{"shopfloor": {"": pub}})
	answers := map[string]struct {
		status int
		body   string
	}{
		"/v1/identity/legal-entities/" + legalA:                            {http.StatusOK, `{"legalEntityId":"` + legalA + `","active":true}`},
		"/v1/identity/legal-entities/0195f0a0-0000-7000-8000-0000000000e2": {http.StatusOK, `{"legalEntityId":"0195f0a0-0000-7000-8000-0000000000e2","active":false}`},
		"/v1/identity/legal-entities/0195f0a0-0000-7000-8000-0000000000e3": {http.StatusNotFound, `{}`},
		"/v1/identity/legal-entities/0195f0a0-0000-7000-8000-0000000000e4": {http.StatusInternalServerError, `{}`},
		"/v1/identity/legal-entities/0195f0a0-0000-7000-8000-0000000000e5": {http.StatusOK, `{"legalEntityId":"` + legalA + `","active":true}`},
	}
	srv := httptest.NewServer(tenant.MiddlewareWith(verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := tenant.FromContext(r.Context())
		if !ok || p.TenantID != t1 || p.LegalEntityID != "" || p.UserID != "admin-1" || len(p.Duties) != 1 || !p.HasDuty(LookupDuty) {
			t.Errorf("principal %+v ok=%v", p, ok)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		a, ok := answers[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(a.status)
		_, _ = w.Write([]byte(a.body))
	})))
	defer srv.Close()
	check, err := NewLegalEntities(srv.URL+"/", s, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenantAdminCtx()
	if active, err := check.Active(ctx, legalA); err != nil || !active {
		t.Fatalf("active legal entity: %v, %v", active, err)
	}
	if active, err := check.Active(ctx, "0195f0a0-0000-7000-8000-0000000000e2"); err != nil || active {
		t.Fatalf("inactive legal entity: %v, %v", active, err)
	}
	if _, err := check.Active(ctx, "0195f0a0-0000-7000-8000-0000000000e3"); !errors.Is(err, ErrLegalEntityUnknown) {
		t.Fatalf("unknown legal entity: %v", err)
	}
	for _, id := range []string{"0195f0a0-0000-7000-8000-0000000000e4", "0195f0a0-0000-7000-8000-0000000000e5"} {
		if _, err := check.Active(ctx, id); err == nil || errors.Is(err, ErrLegalEntityUnknown) {
			t.Fatalf("%s: a failed or mismatched answer must be an error, not unknown: %v", id, err)
		}
	}
	// A caller with a legal-entity claim cannot mint the tenant-level token: an error, never a pass.
	scoped := tenant.With(context.Background(), tenant.Principal{TenantID: t1, LegalEntityID: legalA, UserID: "u", Duties: map[string]struct{}{}})
	if _, err := check.Active(scoped, legalA); err == nil {
		t.Fatal("a scoped caller's check must fail")
	}
}

func TestNewLegalEntitiesNeedsItsSettings(t *testing.T) {
	s, _ := signer(t)
	if _, err := NewLegalEntities("", s, nil); err == nil {
		t.Error("no identity URL")
	}
	if _, err := NewLegalEntities("http://identity", nil, nil); err == nil {
		t.Error("no signer")
	}
}
