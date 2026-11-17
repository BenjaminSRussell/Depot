package authz

import rego.v1

# B-04: the Envoy ext_authz answer (data.authz.envoy) for the shared route table (testdata/route_table.json) and the
# gateway path mapping. The single-host gateways run the same table in Go against a real OPA.

route_table := data.testdata.cases

route_tenant := data.testdata.tenant

case_user(c) := concat("", ["user-", c.name])

case_assignments(c) := {route_tenant: {case_user(c): c.duties}}

case_scoped(c) := {route_tenant: {case_user(c): object.get(c, "scoped_duties", {})}}

case_headers(c) := object.union(
	{"x-depot-tenant": route_tenant, "x-depot-user": case_user(c), "x-request-id": "req-1"},
	{"x-depot-legal-entity": c.legal_entity},
) if c.legal_entity

case_headers(c) := {"x-depot-tenant": route_tenant, "x-depot-user": case_user(c), "x-request-id": "req-1"} if {
	not c.legal_entity
}

envoy_input(method, path, headers) := {"attributes": {"request": {"http": {"method": method, "path": path, "headers": headers}}}}

envoy_answer(c) := result if {
	result := envoy with input as envoy_input(c.method, c.path, case_headers(c))
		with data.assignments as case_assignments(c)
		with data.scoped_assignments as case_scoped(c)
}

test_route_table_is_not_empty if count(route_table) >= 10

test_envoy_answers_the_route_table if {
	mismatches := {c.name |
		some c in route_table
		envoy_answer(c).allowed != c.allow
	}
	count(mismatches) == 0
}

test_envoy_records_every_route_table_decision if {
	every c in route_table {
		record := envoy_answer(c).record
		record.event == "authz.decision"
		record.source == "envoy"
		record.tenant == route_tenant
		record.subject == "[redacted]"
		record.request_id == "req-1"
		record.outcome == {true: "allow", false: "deny"}[c.allow]
		not contains(json.marshal(record), case_user(c))
	}
}

test_envoy_deny_carries_the_problem_and_missing_duty if {
	answer := envoy with input as envoy_input("POST", "/api/v1/numbering/sequences", {"x-depot-tenant": "t", "x-depot-user": "u"})
		with data.assignments as {"t": {"u": ["numbering.read"]}}
	answer.allowed == false
	answer.http_status == 403
	body := json.unmarshal(answer.body)
	body.code == "AUTH-0003"
	body.errors.duty_missing == "numbering.sequence.maintain"
	answer.record.reason == "policy_denied"
	answer.record.duty == "numbering.sequence.maintain"
	answer.record.route == "POST /v1/numbering/**"
}

test_envoy_allow_names_the_granting_duty_and_route_template if {
	answer := envoy with input as envoy_input("GET", "/api/v1/numbering/sequences/s-42?x=1", {"x-depot-tenant": "t", "x-depot-user": "u"})
		with data.assignments as {"t": {"u": ["numbering.read"]}}
	answer.allowed == true
	answer.record.duty == "numbering.read"
	answer.record.reason == "policy_allowed"
	answer.record.route == "GET /v1/numbering/**"
	not contains(answer.record.route, "s-42")
}

test_envoy_without_jwt_headers_is_unauthenticated if {
	answer := envoy with input as envoy_input("GET", "/api/v1/numbering/sequences/s1", {})
		with data.assignments as {"t": {"u": ["numbering.read"]}}
	answer.allowed == false
	answer.record.reason == "unauthenticated"
}

test_envoy_refuses_paths_that_are_not_canonical if {
	every path in [
		"/api/v1/numbering/../identity/users",
		"/api/v1/numbering//sequences",
		"/api/v1/numbering/%2e%2e/identity",
		"/api/v1/numbering%2Fsequences",
		"relative/v1/numbering",
	] {
		answer := envoy with input as envoy_input("GET", path, {"x-depot-tenant": "t", "x-depot-user": "u"})
			with data.assignments as {"t": {"u": ["numbering.sequence.maintain"]}}
		answer.allowed == false
		answer.http_status == 400
		answer.record.reason == "not_canonical"
		answer.record.route == "GET unmatched"
	}
}

test_envoy_denies_paths_outside_the_api if {
	answer := envoy with input as envoy_input("GET", "/healthz", {"x-depot-tenant": "t", "x-depot-user": "u"})
		with data.assignments as {"t": {"u": ["numbering.sequence.maintain"]}}
	answer.allowed == false
	answer.http_status == 403
}

test_gateway_resource_maps_public_paths_to_the_service_base if {
	gateway_resource("/api/v1/numbering/sequences/s1") == "/v1/numbering/sequences/s1"
	gateway_resource("/v1/numbering/sequences/s1") == "/v1/numbering/sequences/s1"
	gateway_resource("/api/v1/numbering") == "/v1/numbering"
	gateway_resource("/api/v1/production/batches:merge") == "/v1/production/batches:merge"
	gateway_resource("/api/v1/agents/a1/runs") == "/api/v1/agents/a1/runs"
	gateway_resource("/v1/agents/a1/runs") == "/api/v1/agents/a1/runs"
	gateway_resource("/api/v1/edi/partners") == "/edi/v1/partners"
	gateway_resource("/api/v1/print/jobs") == "/print/v1/jobs"
	gateway_resource("/api/v1/mcp/tools") == "/mcp/tools"
	not gateway_resource("/api/v1/numbering/../x")
	not gateway_resource("/api/v2/numbering")
	not gateway_resource("/api/v1/Numbering")
}

# Envoy decides a D22 route on the service's own base, the path the duty catalogue names.
test_envoy_decides_a_d22_route_on_the_service_base if {
	answer := envoy with input as envoy_input("POST", "/api/v1/agents/a1/runs", {"x-depot-tenant": "t", "x-depot-user": "u"})
		with data.assignments as {"t": {"u": ["agents.invoke"]}}
	answer.allowed == true
	answer.record.route == "POST /api/v1/agents/**"
}

test_decision_names_the_granting_duty if {
	result := decision with input as {"subject": {"tenant_id": "t", "user_id": "u"}, "action": "GET", "resource": "/v1/numbering/sequences/s1"}
		with data.assignments as {"t": {"u": ["numbering.sequence.maintain", "numbering.read"]}}
	result == {"allow": true, "duty": "numbering.read"}
}

test_decision_log_mask_erases_the_input if decision_log_mask == {"/input"}
