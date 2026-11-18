package identity_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/services/identity"
)

type revocationFixture struct {
	*deprovisionFixture
	realm string
	alice scimUserBody
}

func newRevocationFixture(t *testing.T) *revocationFixture {
	t.Helper()
	f := &revocationFixture{deprovisionFixture: newDeprovisionFixture(t)}
	r := chi.NewRouter()
	identity.NewSessionRevocationsAPI(f.app, f.kc.admin(t, "realm-"+f.tenantA, "realm-"+f.tenantB)).Register(r)
	f.router = r
	f.realm = "realm-" + f.tenantA
	f.alice = f.createUser(t, f.as(f.tenantA), "alice")
	return f
}

func (f *revocationFixture) path() string {
	return "/v1/identity/users/" + f.alice.ID + "/session-revocations"
}

func (f *revocationFixture) revoked(t *testing.T) bool {
	t.Helper()
	audit, _ := f.auditAndEvents(t, f.tenantA)
	return slices.Contains(audit, "session.revoked|admin-1")
}

func TestSessionRevocationEndsSessionsKeepsTheUserEnabledAndIsAudited(t *testing.T) {
	f := newRevocationFixture(t)
	f.kc.addUser(f.realm, kcUserID, "alice", f.tenantA, true)

	got := decode[struct {
		UserID, Outcome, KeycloakUserID, RevokedBy string
	}](t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodPost, f.path(), map[string]string{"reason": "duty removed"}), http.StatusOK)
	if got.UserID != f.alice.ID || got.Outcome != "revoked" || got.KeycloakUserID != kcUserID || got.RevokedBy != "admin-1" {
		t.Fatalf("response = %+v", got)
	}
	if !slices.Equal(f.kc.logouts, []string{f.realm + "/" + kcUserID}) || len(f.kc.puts) != 0 || f.kc.user(f.realm, kcUserID)["enabled"] != true {
		t.Fatalf("logouts %v puts %d user %v, want one logout and an enabled user", f.kc.logouts, len(f.kc.puts), f.kc.user(f.realm, kcUserID))
	}
	audit, events := f.auditAndEvents(t, f.tenantA)
	if !slices.Contains(audit, "session.revoked|admin-1") || !slices.Contains(events, "dev.depot.identity.session.revoked.v1") {
		t.Fatalf("revocation not audited: %v %v", audit, events)
	}
}

func TestSessionRevocationAcceptsAnEmptyBodyAndReportsAMissingKeycloakUser(t *testing.T) {
	f := newRevocationFixture(t)
	got := decode[struct{ Outcome string }](t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodPost, f.path(), nil), http.StatusOK)
	if got.Outcome != "not_found" || len(f.kc.logouts) != 0 || !f.revoked(t) {
		t.Fatalf("outcome %q logouts %v audited %v, want not_found, no logout, an audit row", got.Outcome, f.kc.logouts, f.revoked(t))
	}
}

func TestSessionRevocationRefusesCallersWithoutAdminRightsAndNeverCallsKeycloak(t *testing.T) {
	f := newRevocationFixture(t)
	f.kc.addUser(f.realm, kcUserID, "alice", f.tenantA, true)

	wantProblem(t, f.do(caller{user: "admin-1", duties: []string{dutyUsers, dutyTenantAdmin}}, http.MethodPost, f.path(), nil), http.StatusUnauthorized, "AUTH-0001")
	wantProblem(t, f.do(caller{tenantID: f.tenantA, user: "admin-1"}, http.MethodPost, f.path(), nil), http.StatusForbidden, "")
	wantProblem(t, f.do(caller{tenantID: f.tenantA, legalEntity: legalA, user: "clerk-1", duties: []string{dutyUsers}}, http.MethodPost, f.path(), nil), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, f.do(caller{tenantID: f.tenantA, user: "clerk-1", duties: []string{dutyUsers}}, http.MethodPost, f.path(), nil), http.StatusForbidden, "AUTH-0003")
	wantProblem(t, f.do(f.adminOf(f.tenantB, dutyUsers), http.MethodPost, f.path(), nil), http.StatusNotFound, "IDN-USER-NOT-FOUND")
	wantProblem(t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodPost, "/v1/identity/users/"+testTenant+"/session-revocations", nil), http.StatusNotFound, "IDN-USER-NOT-FOUND")
	wantProblem(t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodPost, "/v1/identity/users/not-a-uuid/session-revocations", nil), http.StatusBadRequest, "COM-0001")
	if calls := f.kc.callLog(); len(calls) != 0 || f.revoked(t) {
		t.Fatalf("keycloak calls %v audited %v, want none", calls, f.revoked(t))
	}
}

func TestSessionRevocationValidatesTheReason(t *testing.T) {
	f := newRevocationFixture(t)
	admin := f.adminOf(f.tenantA, dutyUsers)
	wantProblem(t, f.do(admin, http.MethodPost, f.path(), map[string]string{"reason": strings.Repeat("x", 501)}), http.StatusBadRequest, "COM-0001")
	wantProblem(t, f.do(admin, http.MethodPost, f.path(), map[string]any{"reason": 5}), http.StatusBadRequest, "")
	wantProblem(t, f.do(admin, http.MethodPost, f.path(), map[string]any{"reason": map[string]string{}}), http.StatusBadRequest, "")
	wantProblem(t, f.do(admin, http.MethodPost, f.path(), map[string]string{"unknown": "x"}), http.StatusBadRequest, "")
	if len(f.kc.callLog()) != 0 {
		t.Fatalf("an invalid request reached Keycloak: %v", f.kc.callLog())
	}
}

func TestSessionRevocationFailsClosedWhenKeycloakFailsAndWritesNoAuditRow(t *testing.T) {
	f := newRevocationFixture(t)
	f.kc.addUser(f.realm, kcUserID, "alice", f.tenantA, true)
	f.kc.fail(http.StatusServiceUnavailable, 0)

	wantProblem(t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodPost, f.path(), nil), http.StatusBadGateway, "IDN-SESSION-REVOKE-FAILED")
	if f.revoked(t) || len(f.kc.logouts) != 0 {
		t.Fatalf("a failed revocation was audited (%v) or logged out (%v)", f.revoked(t), f.kc.logouts)
	}
}

func TestSessionRevocationRefusesAKeycloakUserOfAnotherTenant(t *testing.T) {
	f := newRevocationFixture(t)
	f.kc.addUser(f.realm, kcUserID, "alice", f.tenantB, true)

	wantProblem(t, f.do(f.adminOf(f.tenantA, dutyUsers), http.MethodPost, f.path(), nil), http.StatusConflict, "IDN-SESSION-REVOKE-REFUSED")
	if len(f.kc.logouts) != 0 || f.revoked(t) {
		t.Fatalf("logouts %v audited %v, want none", f.kc.logouts, f.revoked(t))
	}
}
