package authz

import rego.v1

default allow := false

allow if count(granted_duties) > 0

# The held duties that grant this request. decision names the first in sort order, so the allow/deny record of every
# gateway says which duty allowed a request (B-04).
granted_duties contains duty if {
	input.subject.tenant_id != ""
	input.subject.user_id != ""
	some duty in held_duties
	tenant_may_hold(duty)
	some permission in data.duties[duty].permissions
	input.action in permission.methods
	path_matches(permission.resource, input.resource)
}

# The duties the subject holds for this request (B-03b, FR-2.6, FR-2.9). A tenant-wide assignment (data.assignments)
# applies in every legal entity of the tenant and on a request with none. A legal-entity-scoped assignment
# (data.scoped_assignments[tenant][user][legal_entity]) applies only when the request acts in that legal entity:
# input.subject.legal_entity_id names the legal entities the request acts in (one UUID, or a comma list), and a scoped
# duty counts only when every one of them carries it. So a role scoped to company A grants in A, and nothing in B,
# nothing in A and B together, and nothing on a request with no legal entity (a tenant administrator's). A duty marked
# "tenant_level" in data.json acts on the tenant as a whole (identity and access administration, SCIM, the tenant's audit
# evidence, routes that run in scope '*'), so a scoped assignment never grants it, whatever its legal entity.
held_duties contains duty if {
	some duty in data.assignments[input.subject.tenant_id][input.subject.user_id]
}

held_duties contains duty if {
	entities := request_legal_entities
	count(entities) > 0
	scoped := data.scoped_assignments[input.subject.tenant_id][input.subject.user_id]
	some first in entities
	some duty in scoped[first]
	not data.duties[duty].tenant_level
	every entity in entities {
		duty in scoped[entity]
	}
}

# The legal entities the request acts in, in lower case like the compiled bundle's keys (legal_entity_id::text).
request_legal_entities := {lower(trim_space(part)) |
	some part in split(object.get(input.subject, "legal_entity_id", ""), ",")
	trim_space(part) != ""
}

# An operator_only duty acts on platform-wide state (the ops control plane): it grants nothing outside the platform
# operator's tenant, whatever a tenant's role assignments say. identity adds operator_tenant_id to the compiled bundle;
# without it no tenant may hold such a duty.
tenant_may_hold(duty) if not data.duties[duty].operator_only

tenant_may_hold(duty) if {
	data.duties[duty].operator_only == true
	input.subject.tenant_id == data.operator_tenant_id
}

decision := {"allow": true, "duty": sort(granted_duties)[0]} if allow

decision := {"allow": false, "duty_missing": missing_duty} if {
	not allow
	input.subject.tenant_id != ""
	input.subject.user_id != ""
	candidates := hint_candidates
	count(candidates) > 0
	sizes := {duty_permission_count(duty) |
		some duty in candidates
	}
	smallest_size := min(sizes)
	least_privileged := {duty |
		some duty in candidates
		duty_permission_count(duty) == smallest_size
	}
	missing_duty := sort(least_privileged)[0]
}

default decision := {"allow": false}

data_scope := {"unrestricted": true} if {
	input.subject.tenant_id != ""
	input.subject.user_id != ""
	not data.dimension_scopes[input.subject.tenant_id][input.subject.user_id]
}

data_scope := {"unrestricted": false, "dimensions": dimensions} if {
	input.subject.tenant_id != ""
	input.subject.user_id != ""
	grants := data.dimension_scopes[input.subject.tenant_id][input.subject.user_id]
	dimensions := {attribute: sort(values) |
		some attribute, values in grants
	}
}

granting_duties := {duty |
	some duty, spec in data.duties
	some permission in spec.permissions
	input.action in permission.methods
	path_matches(permission.resource, input.resource)
}

person_duties := {duty |
	some duty in granting_duties
	not data.duties[duty].service_only
}

hint_candidates := person_duties if count(person_duties) > 0

hint_candidates := granting_duties if count(person_duties) == 0

duty_permission_count(duty) := total if {
	counts := [count(permission.methods) |
		some permission in data.duties[duty].permissions
	]
	total := sum(counts)
}

path_matches(pattern, path) if glob.match(pattern, ["/", ":"], path)

# --- Gateway enforcement (B-04, ADR-0017) ---------------------------------------------------------------------------
#
# Every gateway asks this policy, with the subject from the verified token, the request method and the path the service
# receives: devgateway and depot-server through identity's RequireOPA (data.authz.decision), Envoy Gateway through the
# OPA-Envoy plugin (envoy_ext_authz_grpc, path authz/envoy) after jwt_authn has verified the token and set X-Depot-Tenant,
# X-Depot-User and X-Depot-Legal-Entity from its claims (the client's copies are removed in earlyRequestHeaders).

# The service bases that are not /v1/<prefix> (the D22 exceptions of deploy/scripts/gen_httproutes.py): Envoy rewrites
# /api/v1/<prefix> and /v1/<prefix> to these, so the decision is taken on the path the service serves, which is the path
# the duty catalogue is written against. deploy/scripts/test_gateway.py checks this table against the generated routes.
gateway_service_bases := {
	"agents": "/api/v1/agents",
	"copilot": "/api/v1/copilot",
	"cutover": "/api/v1/cutover",
	"edi": "/edi/v1",
	"mcp": "/mcp",
	"migration": "/api/v1/migration",
	"print": "/print/v1",
}

# A path a gateway decides on: absolute, no empty, '.' or '..' segment, no backslash and no percent-encoding at all. An
# escape the service's router decodes could be decided on one path and served on another (%3A is ':', the glob's segment
# delimiter), so any '%' is refused, as the single-host gateways refuse it (400 GW-0400) before they ask; Envoy normalises
# paths, and this refuses what is left.
canonical_path(path) if {
	startswith(path, "/")
	not contains(path, "//")
	not contains(path, "\\")
	not contains(path, "%")
	not regex.match(`(^|/)\.\.?(/|$)`, path)
}

# The public prefix and the rest of a gateway path: /api/v1/<prefix>/... (the browser) or /v1/<prefix>/... (partners).
gateway_route_parts(path) := [m[1], m[2]] if {
	m := regex.find_all_string_submatch_n(`^(?:/api)?/v1/([a-z0-9][a-z0-9-]*)(/.*)?$`, path, 1)[0]
}

# The path the service receives for a public path: its service base and the rest.
gateway_resource(path) := concat("", [gateway_base(parts[0]), parts[1]]) if {
	canonical_path(path)
	parts := gateway_route_parts(path)
}

gateway_base(prefix) := object.get(gateway_service_bases, prefix, concat("", ["/v1/", prefix]))

# The route a gateway record names: the service base of the prefix and /**, never the raw path (it can carry ids). Envoy
# Gateway enables ext_authz on the routes of depot-api only, so Envoy asks about routed prefixes alone; the single-host
# gateways name an unrouted prefix "unmatched" themselves (authz.GatewayRouteOf).
gateway_route(path) := concat("", [gateway_base(parts[0]), "/**"]) if {
	canonical_path(path)
	parts := gateway_route_parts(path)
}

default gateway_route(_) := "unmatched"

envoy_http := object.get(object.get(object.get(input, "attributes", {}), "request", {}), "http", {})

envoy_headers := object.get(envoy_http, "headers", {})

envoy_method := object.get(envoy_http, "method", "")

# The plugin passes the path with its query string.
envoy_path := split(object.get(envoy_http, "path", ""), "?")[0]

envoy_subject := {
	"tenant_id": object.get(envoy_headers, "x-depot-tenant", ""),
	"user_id": object.get(envoy_headers, "x-depot-user", ""),
	"legal_entity_id": object.get(envoy_headers, "x-depot-legal-entity", ""),
}

envoy_canonical if canonical_path(envoy_path)

envoy_decision := result if {
	result := decision with input as {
		"subject": envoy_subject,
		"action": envoy_method,
		"resource": gateway_resource(envoy_path),
	}
}

# The answer to Envoy's check, which is also the logged decision: allowed, and on a deny the status and RFC 9457 body the
# single-host gateways answer with (403 AUTH-0003 with errors.duty_missing, or 400 GW-0400 for a path that is not
# canonical). record is the allow/deny record (platform/go/README.md, "Authorization decision record"); the plugin ignores
# keys it does not know.
envoy := {"allowed": true, "record": envoy_record} if envoy_allowed

envoy := {
	"allowed": false,
	"http_status": 403,
	"headers": {"content-type": "application/problem+json"},
	"body": json.marshal(object.union(
		{
			"type": "https://depot.dev/problems/AUTH-0003", "title": "Forbidden", "status": 403,
			"code": "AUTH-0003", "detail": "request is not authorized",
		},
		envoy_problem_errors,
	)),
	"record": envoy_record,
} if {
	envoy_canonical
	not envoy_allowed
}

envoy := {
	"allowed": false,
	"http_status": 400,
	"headers": {"content-type": "application/problem+json"},
	"body": json.marshal({
		"type": "https://depot.dev/problems/GW-0400", "title": "Bad Request", "status": 400,
		"code": "GW-0400", "detail": "request path is not canonical",
	}),
	"record": envoy_record,
} if not envoy_canonical

envoy_allowed if {
	envoy_canonical
	envoy_decision.allow
}

envoy_problem_errors := {"errors": {"duty_missing": envoy_decision.duty_missing}} if envoy_decision.duty_missing

default envoy_problem_errors := {}

envoy_record := {
	"event": "authz.decision",
	"schema": 1,
	"log_class": "audit.other",
	"source": "envoy",
	"tenant": envoy_subject.tenant_id,
	# The raw subject never leaves the policy: the record carries logpseudo's pseudonym of it (log_subject).
	"subject": log_subject(envoy_subject.tenant_id, envoy_subject.user_id),
	"duty": envoy_record_duty,
	"route": concat(" ", [envoy_method, gateway_route(envoy_path)]),
	"outcome": envoy_outcome,
	"reason": envoy_reason,
	"request_id": object.get(envoy_headers, "x-request-id", ""),
}

envoy_outcome := "allow" if envoy_allowed

default envoy_outcome := "deny"

envoy_record_duty := object.get(envoy_decision, "duty", "") if envoy_allowed

envoy_record_duty := object.get(envoy_decision, "duty_missing", "") if {
	envoy_canonical
	not envoy_allowed
}

default envoy_record_duty := ""

envoy_reason := "policy_allowed" if envoy_allowed

envoy_reason := "not_canonical" if not envoy_canonical

envoy_reason := "unauthenticated" if {
	envoy_canonical
	not envoy_allowed
	not envoy_authenticated
}

envoy_reason := "policy_denied" if {
	envoy_canonical
	not envoy_allowed
	envoy_authenticated
}

envoy_authenticated if {
	envoy_subject.tenant_id != ""
	envoy_subject.user_id != ""
}

# logpseudo.Subject in rego (platform/go/logpseudo, fieldcrypt.Keys.MAC): the key is HKDF-SHA256 of the 32-byte master
# key with the tenant id as salt and "passage.fieldcrypt.mac.v1" as info, the pseudonym "pn_" and the first 8 bytes, in hex,
# of HMAC-SHA256 under it of "passage.log.pseudonym.v1", a NUL byte and the trimmed, lower-cased value. HKDF-Expand needs one
# block for 32 bytes: HMAC(PRK, info || 0x01). services/identity/authztest checks it against Go on shared vectors.
log_pseudonym(key_b64, tenant, value) := concat("", ["pn_", substring(mac, 0, 16)]) if {
	ikm := base64.decode(key_b64)
	count(hex.encode(ikm)) == 64
	prk := hex.decode(crypto.hmac.sha256(ikm, tenant))
	okm := hex.decode(crypto.hmac.sha256(concat("", ["passage.fieldcrypt.mac.v1", hex.decode("01")]), prk))
	mac := crypto.hmac.sha256(concat("", ["passage.log.pseudonym.v1", hex.decode("00"), lower(trim_space(value))]), okm)
}

# The subject of a gateway record, as logpseudo.Subject writes it: the pseudonym under the dedicated key the ext_authz OPA
# is given (DEPOT_LOG_PSEUDONYM_KEY, the variable the Go services read), "[redacted]" without a key, a valid key or a
# tenant, and empty for an empty subject. Never the raw value. The key is read inside these functions only: no rule of
# the package evaluates to it, so GET /v1/data/authz (and a decision log of it) cannot show it.
log_subject(_, user) := "" if user == ""

log_subject(tenant, user) := pseudonym if {
	user != ""
	pseudonym := log_pseudonym(trim_space(object.get(opa.runtime().env, "DEPOT_LOG_PSEUDONYM_KEY", "")), tenant, user)
	tenant != ""
}

log_subject(tenant, user) := "[redacted]" if {
	user != ""
	not log_pseudonym(trim_space(object.get(opa.runtime().env, "DEPOT_LOG_PSEUDONYM_KEY", "")), tenant, user)
}

log_subject(tenant, user) := "[redacted]" if {
	user != ""
	tenant == ""
}

# The decision-log mask is not here: it ships with the ext_authz OPA's own configuration (deploy/gateway/authz-opa.yaml,
# data.system.log.mask), so a bundle that lacks or renames a rule cannot turn masking off.
