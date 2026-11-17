package authz

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/logpseudo"
)

// The authorization decision record (B-04, D36). Every decision of Require, RequireDuty and RequireAnyDuty, and of the
// three gateways (identity's RequireOPA in devgateway and depot-server, data.authz.envoy in the Envoy ext_authz OPA),
// writes one record with this event name and these fields; platform/go/README.md documents the schema.
const (
	// Event is the record's slog message and its event field.
	Event = "authz.decision"
	// SchemaVersion is the record's schema field; a change to the fields below that is not an addition bumps it.
	SchemaVersion = 1
	// LogClass is the record's retention class: the "3 y other" audit class of architecture E.8 ("Retention by class:
	// 7 y fiscal, 3 y other, 7 y agent actions"). The log shipper keeps a record by this field (platform/go/README.md).
	LogClass = "audit.other"
)

// Outcomes.
const (
	OutcomeAllow = "allow"
	OutcomeDeny  = "deny"
)

// Reasons. The set is closed so the reason label of depot_authz_denied_total stays small.
const (
	ReasonDutyHeld            = "duty_held"              // a service route: the principal holds the duty
	ReasonDutyMissing         = "duty_missing"           // a service route: it does not
	ReasonNoPrincipal         = "no_principal"           // a service route: no principal in the request (401)
	ReasonPolicyError         = "policy_error"           // a service route: the Policy failed
	ReasonPolicyAllowed       = "policy_allowed"         // a gateway: OPA allowed
	ReasonPolicyDenied        = "policy_denied"          // a gateway: OPA denied
	ReasonPolicyUnavailable   = "policy_unavailable"     // a gateway: OPA did not answer (503, fail closed)
	ReasonUnauthenticated     = "unauthenticated"        // a gateway: no verified principal (401)
	ReasonLegalEntitySelected = "legal_entity_selection" // a gateway: the legal-entity selection is outside the token
	ReasonNotCanonical        = "not_canonical"          // a gateway: the path is not canonical (400)
)

// Decision is one authorization decision. Subject is the raw user identifier: Record writes only its pseudonym.
type Decision struct {
	// Source is the gateway (envoy, devgateway, depot-server) or the service that decided.
	Source   string
	TenantID string
	Subject  string
	// Duty is the duty the route needs (a service), or the duty that granted or was missing (a gateway).
	Duty   string
	Method string
	// Route is the route template ("/v1/ledger/journal-entries/{id}" in a service, "/v1/ledger/**" at a gateway), never
	// the raw path, which can carry ids.
	Route     string
	Allowed   bool
	Reason    string
	RequestID string
}

// Record writes the decision record and, for a deny, counts it in depot_authz_denied_total.
func Record(ctx context.Context, d Decision) {
	outcome, level := OutcomeAllow, slog.LevelInfo
	if !d.Allowed {
		outcome, level = OutcomeDeny, slog.LevelWarn
		denied.inc(d.Source, d.Duty, d.Reason)
	}
	route := d.Route
	if route == "" {
		route = "unmatched"
	}
	slog.Default().LogAttrs(ctx, level, Event,
		slog.String("event", Event),
		slog.Int("schema", SchemaVersion),
		slog.String("log_class", LogClass),
		slog.String("source", d.Source),
		slog.String("tenant", d.TenantID),
		logpseudo.SubjectAttr("subject", d.TenantID, d.Subject),
		slog.String("duty", d.Duty),
		slog.String("route", strings.TrimSpace(d.Method+" "+route)),
		slog.String("outcome", outcome),
		slog.String("reason", d.Reason),
		slog.String("request_id", d.RequestID),
	)
}

// ServiceSource is the source of a service's own records: DEPOT_SERVICE_NAME, or "service" when it is not set.
var ServiceSource = sync.OnceValue(func() string {
	if name := strings.TrimSpace(os.Getenv("DEPOT_SERVICE_NAME")); name != "" {
		return name
	}
	return "service"
})

// RouteTemplate is the chi route pattern of r, or "" when the router has not matched one (a middleware mounted before
// routing): never the raw path.
func RouteTemplate(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		return rc.RoutePattern()
	}
	return ""
}

var prefixSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// GatewayRoute is the route a gateway names in its record: the first two path segments and /** ("/v1/ledger/**"), or
// "unmatched" when they are not plain lower-case names or routed (nil accepts every name) does not know the second, the
// service prefix. A gateway routes by the prefix, so that is its route template; the rest of the path can carry ids and
// is never logged, and neither is a prefix the gateway does not route, so a client cannot write a name of its choosing
// (an id, say) into the record.
func GatewayRoute(path string, routed func(prefix string) bool) string {
	parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
	if len(parts) < 2 || !prefixSegment.MatchString(parts[0]) || !prefixSegment.MatchString(parts[1]) {
		return "unmatched"
	}
	if routed != nil && !routed(parts[1]) {
		return "unmatched"
	}
	return "/" + parts[0] + "/" + parts[1] + "/**"
}

type gatewayRoutesKey struct{}

// WithGatewayRoutes records on ctx which service prefixes the gateway routes, for GatewayRouteOf.
func WithGatewayRoutes(ctx context.Context, routed func(prefix string) bool) context.Context {
	return context.WithValue(ctx, gatewayRoutesKey{}, routed)
}

// GatewayRouteOf is GatewayRoute of r's path under the prefixes WithGatewayRoutes put on its context; without them every
// path is "unmatched".
func GatewayRouteOf(r *http.Request) string {
	routed, _ := r.Context().Value(gatewayRoutesKey{}).(func(string) bool)
	if routed == nil {
		return "unmatched"
	}
	return GatewayRoute(r.URL.Path, routed)
}

// RequestID is the request id chi's middleware.RequestID put on the request (the services' routers and both single-host
// gateways install it), or "".
func RequestID(r *http.Request) string { return middleware.GetReqID(r.Context()) }

// --- depot_authz_denied_total -----------------------------------------------------------------------------------------

// maxDeniedSeries bounds the label set; source, duty and reason are each from a closed set, so the cap only stops a bug
// from growing memory. Series past it are counted under duty "other".
const maxDeniedSeries = 2000

type deniedKey struct{ source, duty, reason string }

type deniedCounter struct {
	mu     sync.RWMutex
	series map[deniedKey]*atomic.Int64
}

var denied = &deniedCounter{series: map[deniedKey]*atomic.Int64{}}

func init() { httpx.RegisterMetrics(WriteMetrics) }

func (c *deniedCounter) inc(source, duty, reason string) {
	k := deniedKey{source, duty, reason}
	c.mu.RLock()
	n := c.series[k]
	c.mu.RUnlock()
	if n == nil {
		c.mu.Lock()
		if n = c.series[k]; n == nil {
			if len(c.series) >= maxDeniedSeries {
				k.duty = "other"
			}
			if n = c.series[k]; n == nil {
				n = new(atomic.Int64)
				c.series[k] = n
			}
		}
		c.mu.Unlock()
	}
	n.Add(1)
}

// Denied is the current value of depot_authz_denied_total for one label set.
func Denied(source, duty, reason string) int64 {
	denied.mu.RLock()
	defer denied.mu.RUnlock()
	if n := denied.series[deniedKey{source, duty, reason}]; n != nil {
		return n.Load()
	}
	return 0
}

// WriteMetrics writes depot_authz_denied_total{source,duty,reason} in Prometheus text format. httpx.WriteMetrics calls it,
// so every service's /metrics carries it. There is no user or tenant label.
func WriteMetrics(w io.Writer) {
	denied.mu.RLock()
	keys := make([]deniedKey, 0, len(denied.series))
	for k := range denied.series {
		keys = append(keys, k)
	}
	values := make(map[deniedKey]int64, len(keys))
	for _, k := range keys {
		values[k] = denied.series[k].Load()
	}
	denied.mu.RUnlock()
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.source != b.source {
			return a.source < b.source
		}
		if a.duty != b.duty {
			return a.duty < b.duty
		}
		return a.reason < b.reason
	})
	_, _ = fmt.Fprint(w, "# HELP depot_authz_denied_total Authorization decisions that denied a request, by gateway or service, duty and reason.\n# TYPE depot_authz_denied_total counter\n")
	for _, k := range keys {
		_, _ = fmt.Fprintf(w, "depot_authz_denied_total{source=%q,duty=%q,reason=%q} %d\n", k.source, k.duty, k.reason, values[k])
	}
}
