package api

import (
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/audit/internal/domain"
)

// agentIssuer is the service whose tokens are an AI agent acting for their user (agents-ts, #47).
const agentIssuer = "agents"

// actor is who a change is recorded against.
type actor struct{ userID, kind, onBehalfOf string }

// verifiedService reports whether p came from a verified service token. Only then does the audit service know who
// is calling: identity headers (X-Depot-User, X-Depot-Duties) are trusted from the gateway, but audit has no
// gateway route, no NetworkPolicy and no gateway secret in Kubernetes, so any workload could set them.
func verifiedService(p tenant.Principal) bool { return p.Service != "" && p.UserID != "" }

// verifiedActor derives the actor of a change from a verified service principal alone (AUD-API-1, ADR-0010):
//
//   - a token whose act is a service principal ("service:<name>", which the verifier accepts only from that
//     principal's own issuer): that principal acting on its own, actorKind service;
//   - a token whose act is an end user: the calling service ("service:<iss>") recording the change for that user,
//     actorKind service (agent when the issuer is the agent runtime), onBehalfOf the user.
//
// bodyKind and bodyOnBehalfOf are what the request body claims. They are optional and never change the result:
// a claim that disagrees with the verified caller is refused, and the returned text says why. A principal that is
// not a verified service is refused too; the handler has already answered it with AUD-0007.
func verifiedActor(p tenant.Principal, bodyKind, bodyOnBehalfOf string) (actor, string) {
	var who actor
	switch {
	case !verifiedService(p):
		return actor{}, "changes are recorded only by a verified service"
	case tenant.IsServiceActor(p.UserID):
		who = actor{userID: p.UserID, kind: domain.ActorService}
		if bodyOnBehalfOf != "" {
			return actor{}, "onBehalfOf is accepted only from a verified service carrying the user it acts for; this service token carries no user"
		}
	default:
		kind := domain.ActorService
		if p.Service == agentIssuer {
			kind = domain.ActorAgent
		}
		who = actor{userID: tenant.ServiceActor(p.Service), kind: kind, onBehalfOf: p.UserID}
		if bodyOnBehalfOf != "" && bodyOnBehalfOf != p.UserID {
			return actor{}, "onBehalfOf must be the user the verified service token carries"
		}
	}
	if bodyKind != "" && bodyKind != who.kind {
		return actor{}, "actorKind must be " + who.kind + ": it is taken from the verified caller of the request"
	}
	return who, ""
}
