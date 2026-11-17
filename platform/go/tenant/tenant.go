// Package tenant carries the caller's tenant, legal entity, user and duties
// through context. Until wave 2 replaces it with JWT validation at the gateway,
// the values come from headers that are trusted only behind the gateway; the
// gateway must strip client-supplied X-Depot-* headers (issue #82, part b;
// see deploy/gateway/README.md). Middleware validates their shape but cannot
// authenticate their origin.
package tenant

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/go-chi/chi/v5/middleware"
)

const maxLegalEntities = 100

// Principal is who is calling and in which scope.
type Principal struct {
	TenantID      string
	LegalEntityID string
	UserID        string
	Duties        map[string]struct{}
	Service       string
	// VendorID is the vendor a supplier portal caller acts for, from a verified service token. Empty for every other caller.
	VendorID string
	// CustomerID is the customer a customer portal caller acts for, from a verified service token. Empty for every other caller.
	CustomerID string
}

// PortalCustomer is the customer a customer portal service token acts for. It reports false for every other caller, including
// a principal that happens to carry a customer id without the portal's signature, so a handler that narrows to it never
// trusts a header, a query or a body.
func PortalCustomer(ctx context.Context) (string, bool) {
	p, ok := FromContext(ctx)
	if !ok || p.Service != CustomerPortalIssuer || p.CustomerID == "" {
		return "", false
	}
	return strings.ToLower(p.CustomerID), true
}

// LegalEntityScopeList is the one parser of the legal-entity claim: each
// comma-separated part is trimmed and canonicalised, and a part that is not a
// non-nil UUID is dropped. dbx sets the database scope from it and httpx checks
// request entities against it, so the two never disagree.
func LegalEntityScopeList(p Principal) []string {
	var scopes []string
	for _, part := range strings.Split(p.LegalEntityID, ",") {
		if id, ok := ids.Canonical(strings.TrimSpace(part)); ok && id != ids.Nil {
			scopes = append(scopes, id)
		}
	}
	return scopes
}

// HasDuty reports whether the principal holds the named duty.
func (p Principal) HasDuty(duty string) bool { _, ok := p.Duties[duty]; return ok }

// InLegalEntityScope reports whether the principal may act on legal entity le.
// It fails closed like the database scope: a claim lists the entities it grants
// (le must be a non-nil UUID among them, compared canonically), and a principal
// with no legal-entity claim is in scope of any valid entity only when it holds
// DutyAllScope, which Middleware keeps only on an explicit ALL-scope read.
func InLegalEntityScope(p Principal, le string) bool {
	id, ok := ids.Canonical(strings.TrimSpace(le))
	if !ok || id == ids.Nil {
		return false
	}
	if p.LegalEntityID == "" {
		return p.HasDuty(DutyAllScope)
	}
	for _, s := range LegalEntityScopeList(p) {
		if s == id {
			return true
		}
	}
	return false
}

type ctxKey struct{}

// With returns a context carrying p.
func With(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the principal and whether one was set.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// Headers the gateway sets. Internal only; never trusted from the public edge.
const (
	HeaderTenant      = "X-Depot-Tenant"
	HeaderLegalEntity = "X-Depot-Legal-Entity"
	HeaderUser        = "X-Depot-User"
	HeaderDuties      = "X-Depot-Duties"
	// HeaderAuthTime is the `auth_time` claim of the caller's token: the Unix second the person last authenticated at the
	// identity provider (OIDC core 2.). The gateway sets it from the verified token and strips any value a client sends. A
	// step-up (a 21 CFR Part 11 signature) checks it against a maximum age with AuthAge.
	HeaderAuthTime = "X-Depot-Auth-Time"

	HeaderWebLegalEntity      = "X-Depot-LegalEntity"
	HeaderLegalEntityScopeAll = "X-Depot-LegalEntity-Scope"
	LegalEntityScopeAll       = "ALL"
)

// AuthTime is the moment the caller last authenticated, from the gateway's HeaderAuthTime. ok is false when the header is
// missing or is not a positive Unix time. It is meaningful only for a person's request: a service-token request keeps the
// X-Depot-* headers of whoever called it, so a caller must check Principal.Service is empty before trusting it.
func AuthTime(r *http.Request) (time.Time, bool) {
	secs, err := strconv.ParseInt(strings.TrimSpace(r.Header.Get(HeaderAuthTime)), 10, 64)
	if err != nil || secs <= 0 {
		return time.Time{}, false
	}
	return time.Unix(secs, 0).UTC(), true
}

// AuthAge is how long ago the caller last authenticated. ok is false when AuthTime is, which a step-up check treats as
// not fresh (fail closed). A time in the future (clock skew of up to a minute is allowed) reads as zero.
func AuthAge(r *http.Request, now time.Time) (age time.Duration, ok bool) {
	at, ok := AuthTime(r)
	if !ok {
		return 0, false
	}
	age = now.Sub(at)
	if age < -time.Minute {
		return 0, false
	}
	return max(age, 0), true
}

// GatewaySecretEnv names an optional shared secret between the gateway and the services behind it. When it is set, Middleware accepts the
// user-header identity only on a request that carries the same value in HeaderGatewaySecret, so a process that can reach a service's
// loopback port (another user on a shared Windows Server) cannot invent X-Depot-* headers. The gateway (tools/depot-server) generates
// a random value per start and sets it on every proxied request. Unset (the default, and in Kubernetes where NetworkPolicy is the
// boundary) it changes nothing. Service tokens are signed and are not affected.
const (
	GatewaySecretEnv    = "DEPOT_GATEWAY_SECRET"
	HeaderGatewaySecret = "X-Depot-Gateway-Secret"
)

// ForwardGatewaySecret adds the gateway secret (GatewaySecretEnv) to an outbound request that carries the caller's X-Depot-* identity headers
// instead of a signed service token. A service that forwards a user's identity to a peer must call it wherever it sets HeaderTenant, or the
// peer (which shares the secret under depot-server) refuses the call. It does nothing when the variable is unset.
func ForwardGatewaySecret(req *http.Request) {
	if secret := os.Getenv(GatewaySecretEnv); secret != "" {
		req.Header.Set(HeaderGatewaySecret, secret)
	}
}

// canonicalUUID returns the lowercase hyphenated form of s, or false when s is
// not a 36-character UUID.
func canonicalUUID(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	return ids.Canonical(s)
}

func canonicalLegalEntities(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxLegalEntities {
		return "", false
	}
	seen := make(map[string]struct{}, len(parts))
	canonical := parts[:0]
	for _, part := range parts {
		c, ok := canonicalUUID(strings.TrimSpace(part))
		if !ok || c == ids.Nil {
			return "", false
		}
		if _, dup := seen[c]; !dup {
			seen[c] = struct{}{}
			canonical = append(canonical, c)
		}
	}
	return strings.Join(canonical, ","), true
}

const DutyAllScope = "org.switcher.all-scope"

const DutyTenantAdmin = "tenant.administer"

// IsTenantAdmin reports whether p may write tenant-level data: no legal-entity claim AND the
// explicit tenant.administer duty. A missing claim alone never grants it.
func IsTenantAdmin(p Principal) bool { return p.LegalEntityID == "" && p.HasDuty(DutyTenantAdmin) }

type allScopeAskedKey struct{}

type allScopeAsked struct{ tenantID, userID string }

// ReadOnlyPOST marks a POST route that performs no write (a query whose body is
// too rich for a URL). On it, Middleware's all-scope duty is restored when the
// caller held it and sent the ALL scope header, exactly as on a GET. It is the
// only way a non-read method keeps the duty; every unwrapped route, and any
// method other than POST, still loses it.
func ReadOnlyPOST(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if asked, ok := r.Context().Value(allScopeAskedKey{}).(allScopeAsked); ok && r.Method == http.MethodPost {
			if p, ok := FromContext(r.Context()); ok && p.Service == "" && p.TenantID == asked.tenantID && p.UserID == asked.userID {
				duties := make(map[string]struct{}, len(p.Duties)+1)
				for d := range p.Duties {
					duties[d] = struct{}{}
				}
				duties[DutyAllScope] = struct{}{}
				p.Duties = duties
				r = r.WithContext(With(r.Context(), p))
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

var (
	errLegalEntityMalformed  = errors.New("invalid legal entity")
	errSelectionWithoutClaim = errors.New("legal entity selection without a legal-entity claim")
	errSelectionOutsideClaim = errors.New("legal entity selection is outside the token's scope")
)

// requestLegalEntity is the legal-entity scope of a header request (ADR-0006 rule 4, D33). The gateway-asserted claim
// (HeaderLegalEntity, the token's `le`: one entity or a comma list of up to maxLegalEntities) is the scope. The shell's
// selection (HeaderWebLegalEntity) can only narrow it: every selected entity must be in the claim, and the scope becomes
// the selection; a selection outside the claim is errSelectionOutsideClaim. A selection with no claim is
// errSelectionWithoutClaim, except for a tenant administrator (tenant.administer, which never carries a claim), who is
// entitled to every entity of the tenant and may still pick one. A malformed header is errLegalEntityMalformed.
// Without a selection the claim is the scope, as before.
func requestLegalEntity(r *http.Request, duties map[string]struct{}) (string, error) {
	claim, ok := canonicalLegalEntities(r.Header.Get(HeaderLegalEntity))
	if !ok {
		return "", errLegalEntityMalformed
	}
	selection, ok := canonicalLegalEntities(r.Header.Get(HeaderWebLegalEntity))
	if !ok {
		return "", errLegalEntityMalformed
	}
	if selection == "" {
		return claim, nil
	}
	if claim == "" {
		if _, admin := duties[DutyTenantAdmin]; admin {
			return selection, nil
		}
		return "", errSelectionWithoutClaim
	}
	entitled := make(map[string]struct{}, strings.Count(claim, ",")+1)
	for _, id := range strings.Split(claim, ",") {
		entitled[id] = struct{}{}
	}
	for _, id := range strings.Split(selection, ",") {
		if _, in := entitled[id]; !in {
			return "", errSelectionOutsideClaim
		}
	}
	return selection, nil
}

// Middleware extracts the principal from headers, or from a verified service
// token in X-Depot-Service-Token (which then replaces every user header; an
// invalid token is a 401, never a fallback to the user headers). Requests without a tenant,
// with a malformed tenant, or with the nil system tenant are rejected with 401;
// a malformed legal entity scope is rejected with 400 COM-0001, and a legal-entity
// selection outside the claim, or a selection without a claim from anyone but a
// tenant administrator, with 403 AUTH-0003 (requestLegalEntity). Health
// endpoints pass through. The nil tenant is server-side only and is never
// accepted from headers. The all-entity duty survives only on a read that
// explicitly asks for the ALL scope, or on a POST route wrapped in
// ReadOnlyPOST; on any other request it is dropped so an unscoped call sees no
// legal-entity rows.
func Middleware(next http.Handler) http.Handler {
	return middlewareWith(currentServiceVerifier, next)
}

// MiddlewareWith is Middleware verifying service tokens against v instead of
// the process-wide verifier.
func MiddlewareWith(v *ServiceVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return middlewareWith(func() *ServiceVerifier { return v }, next)
	}
}

func middlewareWith(verifier func() *ServiceVerifier, next http.Handler) http.Handler {
	gatewaySecret := []byte(os.Getenv(GatewaySecretEnv))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}
		if tokens := r.Header.Values(HeaderServiceToken); len(tokens) > 0 {
			claims, err := verifier().VerifyContext(r.Context(), tokens[0])
			if errors.Is(err, ErrReplayStoreUnavailable) {
				w.Header().Set("Retry-After", "1")
				reject(w, r, http.StatusServiceUnavailable, "COM-0503", "Service Unavailable", "service token check unavailable; retry")
				return
			}
			if err != nil || len(tokens) > 1 {
				slog.WarnContext(r.Context(), "service token rejected", "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
				reject(w, r, http.StatusUnauthorized, "AUTH-0001", "Unauthorized", "invalid service token")
				return
			}
			slog.InfoContext(r.Context(), "service call", "service", claims.Iss, "actor", claims.Act, "legalEntityId", claims.Le, "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
			next.ServeHTTP(w, r.WithContext(With(r.Context(), claims.Principal())))
			return
		}
		if len(gatewaySecret) > 0 && subtle.ConstantTimeCompare([]byte(r.Header.Get(HeaderGatewaySecret)), gatewaySecret) != 1 {
			slog.WarnContext(r.Context(), "request without the gateway secret rejected", "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
			reject(w, r, http.StatusUnauthorized, "AUTH-0001", "Unauthorized", "missing gateway credentials")
			return
		}
		raw := r.Header.Get(HeaderTenant)
		if raw == "" {
			reject(w, r, http.StatusUnauthorized, "AUTH-0001", "Unauthorized", "missing tenant")
			return
		}
		t, ok := canonicalUUID(raw)
		if !ok || t == ids.Nil {
			reject(w, r, http.StatusUnauthorized, "AUTH-0001", "Unauthorized", "invalid tenant")
			return
		}
		duties := map[string]struct{}{}
		for _, d := range strings.Split(r.Header.Get(HeaderDuties), ",") {
			if d = strings.TrimSpace(d); d != "" {
				duties[d] = struct{}{}
			}
		}
		le, err := requestLegalEntity(r, duties)
		if errors.Is(err, errLegalEntityMalformed) {
			reject(w, r, http.StatusBadRequest, "COM-0001", "Bad Request", "invalid legal entity")
			return
		}
		user := r.Header.Get(HeaderUser)
		if IsServiceActor(user) {
			slog.WarnContext(r.Context(), "user header claims a service actor", "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
			reject(w, r, http.StatusUnauthorized, "AUTH-0001", "Unauthorized", "invalid user")
			return
		}
		if err != nil {
			// tenantId and user identify the caller (logpseudo pseudonymizes user under the tenant's key); the entity
			// values are not logged.
			slog.WarnContext(r.Context(), "legal entity selection refused", "reason", err.Error(), "tenantId", t, "user", user, "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
			reject(w, r, http.StatusForbidden, "AUTH-0003", "Forbidden", err.Error())
			return
		}
		p := Principal{TenantID: t, LegalEntityID: le, UserID: user, Duties: duties}
		ctx := r.Context()
		if r.Header.Get(HeaderLegalEntityScopeAll) != LegalEntityScopeAll {
			delete(p.Duties, DutyAllScope)
		} else if !isRead(r.Method) {
			if p.HasDuty(DutyAllScope) {
				ctx = context.WithValue(ctx, allScopeAskedKey{}, allScopeAsked{tenantID: p.TenantID, userID: p.UserID})
			}
			delete(p.Duties, DutyAllScope)
		}
		next.ServeHTTP(w, r.WithContext(With(ctx, p)))
	})
}

func reject(w http.ResponseWriter, r *http.Request, status int, code, title, detail string) {
	body := map[string]any{
		"type":   "https://depot.dev/problems/" + code,
		"title":  title,
		"status": status,
		"code":   code,
		"detail": detail,
	}
	if id := middleware.GetReqID(r.Context()); id != "" {
		body["traceId"] = id
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
