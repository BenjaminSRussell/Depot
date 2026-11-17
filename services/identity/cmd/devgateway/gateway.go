package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/identity"
)

const (
	headerTier       = "X-Depot-Tier"
	identityHdrStart = "X-Depot-"
	// gatewayName is the source of this gateway's authorization decision records.
	gatewayName = "devgateway"
)

type tokenClaims struct {
	LegalEntity string `json:"le"`
	Duties      string `json:"duties"`
	Tier        string `json:"tier"`
	// AuthTime is the Unix second the person last authenticated at (OIDC auth_time); a step-up reads it as X-Depot-Auth-Time.
	AuthTime int64 `json:"auth_time"`
}

type selectionKey struct{}

type selection struct{ legalEntity, scope string }

func newGateway(verifier *identity.JWTVerifier, opa *identity.OPAClient, upstreams map[string]*url.URL) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			target := upstreams[serviceOf(pr.In.URL.Path)]
			pr.SetURL(target)
			pr.Out.Host = target.Host
			setIdentity(pr)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, "GW-0502", "upstream service is unavailable"))
		},
	}
	routed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreams[serviceOf(r.URL.Path)] == nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "GW-0404", "no route"))
			return
		}
		selected, _ := r.Context().Value(selectionKey{}).(selection)
		if selected.legalEntity != "" && selected.legalEntity != claimsOf(r.Header.Get("Authorization")).LegalEntity {
			principal, _ := tenant.FromContext(r.Context())
			recordGateway(r, principal, authz.ReasonLegalEntitySelected)
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "legal entity selection is outside the token's scope"))
			return
		}
		proxy.ServeHTTP(w, r)
	})
	guarded := verifier.Middleware(identity.RequireOPAForGateway(opa, gatewayName)(routed))
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trimmed, ok := strings.CutPrefix(r.URL.Path, "/api")
		if !ok || !strings.HasPrefix(trimmed, "/") {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "GW-0404", "no route"))
			return
		}
		selected := selection{legalEntity: r.Header.Get(tenant.HeaderWebLegalEntity), scope: r.Header.Get(tenant.HeaderLegalEntityScopeAll)}
		ctx := authz.WithGatewayRoutes(context.WithValue(r.Context(), selectionKey{}, selected), func(p string) bool { return upstreams[p] != nil })
		forwarded := r.Clone(ctx)
		stripIdentity(forwarded.Header)
		forwarded.URL.Path = trimmed
		guarded.ServeHTTP(w, forwarded)
	}))
	return withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !canonicalPath(r) {
			recordGateway(r, tenant.Principal{}, authz.ReasonNotCanonical)
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "GW-0400", "request path is not canonical"))
			return
		}
		mux.ServeHTTP(w, r)
	}))
}

// withRequestID gives every request an id of the gateway's own (a client's X-Request-Id is dropped, so it cannot steer the
// id of a decision record) and forwards it to the service as X-Request-Id, where httpx's router takes it: the gateway's
// record and the service's record of one request carry the same request_id.
func withRequestID(next http.Handler) http.Handler {
	ids := middleware.RequestID(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del(middleware.RequestIDHeader)
		ids.ServeHTTP(w, r)
	})
}

// recordGateway writes the allow/deny record of a decision the gateway takes itself (identity's RequireOPAForGateway
// records the policy decision): always a deny.
func recordGateway(r *http.Request, principal tenant.Principal, reason string) {
	route := "unmatched"
	if reason != authz.ReasonNotCanonical {
		route = authz.GatewayRouteOf(r)
	}
	authz.Record(r.Context(), authz.Decision{
		Source: gatewayName, TenantID: principal.TenantID, Subject: principal.UserID, Method: r.Method,
		Route: route, Reason: reason, RequestID: authz.RequestID(r),
	})
}

// canonicalPath refuses any percent-encoded path, like data.authz.envoy's canonical_path: an escape the router decodes
// (%3A to ':', the glob's segment delimiter) could be decided on one path and served on another (B-04).
func canonicalPath(r *http.Request) bool {
	p := r.URL.Path
	return r.URL.RawPath == "" && !strings.Contains(r.URL.EscapedPath(), "%") && strings.HasPrefix(p, "/") && path.Clean(p) == p
}

func serviceOf(p string) string {
	rest, ok := strings.CutPrefix(p, "/v1/")
	if !ok {
		return ""
	}
	service, _, _ := strings.Cut(rest, "/")
	return service
}

func stripIdentity(h http.Header) {
	for name := range h {
		if strings.HasPrefix(http.CanonicalHeaderKey(name), identityHdrStart) {
			h.Del(name)
		}
	}
}

// setIdentity runs inside Rewrite, after ReverseProxy has removed the hop-by-hop headers, so a client's Connection
// header cannot name an identity header and have it dropped on the way to the service.
func setIdentity(pr *httputil.ProxyRequest) {
	stripIdentity(pr.Out.Header)
	if id := middleware.GetReqID(pr.In.Context()); id != "" {
		pr.Out.Header.Set(middleware.RequestIDHeader, id)
	}
	pr.Out.Header.Del("Authorization")
	principal, ok := tenant.FromContext(pr.In.Context())
	if !ok {
		return
	}
	claims := claimsOf(pr.In.Header.Get("Authorization"))
	tenant.ForwardGatewaySecret(pr.Out)
	pr.Out.Header.Set(tenant.HeaderTenant, principal.TenantID)
	pr.Out.Header.Set(tenant.HeaderUser, principal.UserID)
	pr.Out.Header.Set(tenant.HeaderLegalEntity, claims.LegalEntity)
	pr.Out.Header.Set(tenant.HeaderDuties, claims.Duties)
	pr.Out.Header.Set(headerTier, claims.Tier)
	if claims.AuthTime > 0 {
		pr.Out.Header.Set(tenant.HeaderAuthTime, strconv.FormatInt(claims.AuthTime, 10))
	}
	selected, _ := pr.In.Context().Value(selectionKey{}).(selection)
	if selected.legalEntity != "" {
		pr.Out.Header.Set(tenant.HeaderWebLegalEntity, selected.legalEntity)
	}
	if selected.scope != "" {
		pr.Out.Header.Set(tenant.HeaderLegalEntityScopeAll, selected.scope)
	}
}

// claimsOf reads the claims of a token the verifier has already accepted; it checks nothing itself.
func claimsOf(authorization string) tokenClaims {
	var claims tokenClaims
	fields := strings.Fields(authorization)
	if len(fields) != 2 {
		return claims
	}
	parts := strings.Split(fields[1], ".")
	if len(parts) != 3 {
		return claims
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims
	}
	_ = json.Unmarshal(payload, &claims)
	return claims
}
