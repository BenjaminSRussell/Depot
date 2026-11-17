package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io/fs"
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
)

// The gateway below is the devgateway logic (services/identity/cmd/devgateway) hardened for a listener that is not loopback-only:
// canonical paths only, identity headers stripped from the client and rebuilt from the verified token, the legal-entity
// selection checked against the token, and the web app served from the embedded build for every non-/api path.

const (
	headerTier       = "X-Depot-Tier"
	identityHdrStart = "X-Depot-"
	// gatewayName is the source of this gateway's authorization decision records.
	gatewayName = "depot-server"
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

// Guard authenticates and authorizes a request. In production it is verifier.Middleware(RequireOPAForGateway(opa, gatewayName)).
type Guard func(http.Handler) http.Handler

// HealthFunc reports aggregated supervisor health.
type HealthFunc func() (ready bool, statuses []Status)

// NewGateway builds the handler. web may be nil (API only). secret, when not empty, is added to every proxied request as
// tenant.HeaderGatewaySecret; the children started with the same DEPOT_GATEWAY_SECRET refuse identity headers without it, so only this
// gateway can speak for a user on the loopback ports. features are the optional MSI features (node, python); the web app reads them
// from /installation.json and hides the screens of a feature that is off. forwardAuth names the routes whose service verifies the
// bearer token itself (agents-ts): only those receive Authorization, and only after the guard has accepted it; for every other
// route the header is removed.
func NewGateway(guard Guard, upstreams map[string]*url.URL, web fs.FS, health HealthFunc, secret string, features map[string]bool, forwardAuth map[string]bool) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			route := serviceOf(pr.In.URL.Path)
			target := upstreams[route]
			pr.SetURL(target)
			pr.Out.Host = target.Host
			setIdentity(pr, secret, forwardAuth[route])
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
	guarded := guard(routed)
	static := staticHandler(web)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// The public listener says only whether the server is ready; per-service state and error text stay in the log and in doctor.
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		ready, _ := health()
		code := http.StatusOK
		if !ready {
			code = http.StatusServiceUnavailable
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]bool{"ready": ready})
	})
	// Which optional features this installation has. Like /readyz it needs no token: it names only the runtimes installed, and the
	// web app reads it before sign-in completes. The services of a feature that is off are not started either (Config.Resolve).
	installation := installationOf(features)
	mux.HandleFunc("/installation.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(installation)
	})
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		trimmed, ok := strings.CutPrefix(r.URL.Path, "/api")
		if !ok || !strings.HasPrefix(trimmed, "/") {
			static.ServeHTTP(w, r)
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
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if r.TLS != nil {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		mux.ServeHTTP(w, r)
	}))
}

// optionalFeatures are the MSI features the installation reports; every one is listed, on or off.
var optionalFeatures = []string{"node", "python"}

type installationInfo struct {
	Features map[string]bool `json:"features"`
}

func installationOf(features map[string]bool) installationInfo {
	out := installationInfo{Features: map[string]bool{}}
	for _, f := range optionalFeatures {
		out.Features[f] = features[f]
	}
	return out
}

// staticHandler serves the embedded web build, falling back to index.html for client-side routes.
func staticHandler(web fs.FS) http.Handler {
	if web == nil {
		return http.NotFoundHandler()
	}
	files := http.FileServerFS(web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			name = "index.html"
		}
		if f, err := web.Open(name); err == nil {
			_ = f.Close()
			files.ServeHTTP(w, r)
			return
		}
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		index, err := fs.ReadFile(web, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
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
func setIdentity(pr *httputil.ProxyRequest, secret string, forwardAuth bool) {
	stripIdentity(pr.Out.Header)
	if id := middleware.GetReqID(pr.In.Context()); id != "" {
		pr.Out.Header.Set(middleware.RequestIDHeader, id)
	}
	if !forwardAuth {
		pr.Out.Header.Del("Authorization")
	}
	if secret != "" {
		pr.Out.Header.Set(tenant.HeaderGatewaySecret, secret)
	}
	principal, ok := tenant.FromContext(pr.In.Context())
	if !ok {
		return
	}
	claims := claimsOf(pr.In.Header.Get("Authorization"))
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
