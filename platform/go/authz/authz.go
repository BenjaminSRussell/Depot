// Package authz enforces duties on routes. Wave 2 swaps the local check for an
// OPA policy decision point behind the same Policy interface.
//
// Every decision of Require, RequireDuty and RequireAnyDuty writes one allow/deny record (Record, event
// "authz.decision") and counts a deny in depot_authz_denied_total (B-04, D36).
package authz

import (
	"context"
	"net/http"
	"strings"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

// Policy answers whether a principal may perform an action on a resource.
type Policy interface {
	Allow(ctx context.Context, p tenant.Principal, action, resource string) (bool, error)
}

// DutyPolicy allows an action when the principal holds the duty named by action.
type DutyPolicy struct{}

// Allow implements Policy.
func (DutyPolicy) Allow(_ context.Context, p tenant.Principal, action, _ string) (bool, error) {
	return p.HasDuty(action), nil
}

// RequireDuty is route middleware: 401 without a principal, 403 without the duty.
func RequireDuty(duty string) func(http.Handler) http.Handler {
	return Require(DutyPolicy{}, duty, "")
}

// RequireAnyDuty is RequireDuty satisfied by any one of duties; with none it denies everything.
func RequireAnyDuty(duties ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := tenant.FromContext(r.Context())
			if !ok {
				recordService(r, p, strings.Join(duties, "|"), false, ReasonNoPrincipal)
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
				return
			}
			for _, d := range duties {
				if p.HasDuty(d) {
					recordService(r, p, d, true, ReasonDutyHeld)
					next.ServeHTTP(w, r)
					return
				}
			}
			recordService(r, p, strings.Join(duties, "|"), false, ReasonDutyMissing)
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty, need one of: "+strings.Join(duties, ", ")))
		})
	}
}

// Require is route middleware over an arbitrary Policy.
func Require(pol Policy, action, resource string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := tenant.FromContext(r.Context())
			if !ok {
				recordService(r, p, action, false, ReasonNoPrincipal)
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
				return
			}
			allowed, err := pol.Allow(r.Context(), p, action, resource)
			if err != nil {
				recordService(r, p, action, false, ReasonPolicyError)
				httpx.Fail(w, r, err)
				return
			}
			if !allowed {
				recordService(r, p, action, false, ReasonDutyMissing)
				httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+action))
				return
			}
			recordService(r, p, action, true, ReasonDutyHeld)
			next.ServeHTTP(w, r)
		})
	}
}

// recordService writes the record of a service route's decision: the service as source, the chi route template.
func recordService(r *http.Request, p tenant.Principal, duty string, allowed bool, reason string) {
	Record(r.Context(), Decision{
		Source: ServiceSource(), TenantID: p.TenantID, Subject: p.UserID, Duty: duty,
		Method: r.Method, Route: RouteTemplate(r), Allowed: allowed, Reason: reason, RequestID: RequestID(r),
	})
}
