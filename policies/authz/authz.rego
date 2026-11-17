package authz

import rego.v1

default allow := false

allow if {
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

decision := {"allow": true} if allow

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
