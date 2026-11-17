# Authz policy data

`data.json` seeds duties; `authz.rego` decides.

## Duty names

A duty name is the exact string the owning service passes to `RequireDuty` or `HasDuty`, and the string a role assignment, a service grant and the web app's nav gates hold.

### Convention (#210)

Every duty is dotted lower-case: `<service>.<object>.<action>`.

- `<service>` is the owning service's duty prefix: usually its directory name without a language suffix (`fieldservice`, `agents` for `agents-ts`, `mcp` for `mcp-ts`), or the prefix it already uses (`procurement` for purchasing, `receivables`, `migration` for both migration and the migration factory, `platform` for platform-health and the platform-wide duties).
- `<object>` is the business object, singular where it names one thing (`purchase-order`, `legal-hold`, `rate-card`).
- `<action>` is the verb (`read`, `write`, `view`, `maintain`, `post`, `approve`, `run`, `admin`, `record-delivery`). The `self`, `manage` and `admin` actions of `hr.*` follow the existing `hr.performance.*` split: the employee, the manager and the HR administrator.
- `<service>.<action>` (two segments) is allowed when the duty covers the whole service rather than one object: `receivables.view`, `projects.manage`, `edi.admin`, `budget.configure`.
- Each segment is `[a-z][a-z0-9]*`, with `-` between words. No capitals, spaces or underscores.

`TestDutyNamesFollowConvention` (`services/identity/data_json_test.go`) fails on a duty in `data.json`, a service grant or a service principal that does not match this shape.

Not yet three-segment: the payables duties seeded before the convention name the object first and leave out the service (`aging.read`, `payment-run.execute`, `settlement.read`, `vendor-block.manage`), as do `period.*` (close), `open-item.read`, `employee-vendor.maintain` and `pii.read`. They match the shape, so they were not renamed in #210.

The PRD (`docs/prd`, `docs/PRD-full.md`) keeps its own duty identifiers (`WMS_WORK_SUPERVISE`, `ARView`); a service README says which seeded duty implements a PRD duty when the names differ.

### Renames and role assignments

#210 renamed every PascalCase, SCREAMING_CASE and prose duty, and the underscore and four-segment ones (`party.vendor_bank.*`, `compliance.legal_hold.*`, `pricing.rebate.claim.approve`), in one change across `data.json`, the rego tests, the service constants, `platform/go/tenant` (service grants, service principals, `DutyAllScope`, now `org.switcher.all-scope`) and the web app. The old-to-new table is `services/identity/migrations/0010_unify_duty_names.sql`, which also moves every stored role assignment to the new name. There is no alias: a token or role assignment that still carries an old name grants nothing. Outside identity's role assignments, an old name can survive where it was stored or configured outside this repository, which the migration does not reach: the Keycloak duties mapper and client scopes, integration-hub `hub_api_client.scopes`, case-category access duties, and SoD rules entered by users.

`services/identity/opa_contract_test.go` scans every Go service under `services/` and `tools/migration-factory`. It fails when a duty constant or `RequireDuty`/`HasDuty` string literal is missing from `data.json`, and when a call passes an argument it cannot resolve to a constant or literal. Call sites that pass a looked-up duty (route tables, `anyDuty` loops) are listed with a reason in `dynamicDutyArguments`; an entry that no longer matches a call fails the test.

## Operator-only duties

`"operator_only": true` duties (the `ops.*` duties of the ops control plane) act on platform-wide state, not on one tenant's data. `authz.rego` grants them only when the subject's tenant is `data.operator_tenant_id`, which identity writes into the compiled bundle from `IDENTITY_OPERATOR_TENANT_ID`; without it they grant nothing. Identity also refuses to assign them in another tenant and drops any such assignment from the bundle, and the ops service serves the operator tenant only.

## Legal-entity-scoped assignments (B-03b)

`data.assignments[tenant][subject]` holds the duties of tenant-wide role assignments; they grant in every legal entity and
on a request with none. `data.scoped_assignments[tenant][subject][legal_entity]` holds the duties of assignments scoped to
one legal entity; `held_duties` counts one only when the request acts in that legal entity: `input.subject.legal_entity_id`
is a UUID or a comma list (compared in lower case), and every listed entity must carry the duty. A request with no legal
entity (a tenant administrator's) gets only the tenant-wide duties. Identity compiles both roots into the bundle;
`data.json` seeds them empty.

### Tenant-level duties

`"tenant_level": true` marks a duty that acts on the tenant as a whole. A legal-entity-scoped assignment never grants
it, in three places:

- `authz.rego`'s scoped rule skips it;
- identity refuses to create such an assignment (`422 IDN-ROLE-TENANT-LEVEL`);
- the bundle compiler drops any that are stored.

Without this, an A-scoped `identity.user.provision` opened SCIM, which runs in scope `*` over every user of the
tenant (B-03b review).

A duty is tenant-level when any of these rules applies (`test_tenant_level_duties_cover_identity_audit_and_operator_duties`
pins the first three families):

| Rule | Duties |
|---|---|
| Identity and access administration: users, SCIM, role assignments, legal entities, features, setup, tenants, customer contacts, effective permissions | every `identity.*` duty |
| The tenant's audit and security evidence, and the chain verification | every `audit.*` duty; `compliance.security.admin`, `compliance.sod.admin` |
| Platform and operator administration | every `operator_only` (`ops.*`) duty; `platform.release.maintain`, `platform.release.view`, `platform.promotion.approve`, `platform.environment.rollback`, `platform.global.admin`, `agents.admin` |
| Flags whose meaning is tenant-wide | `tenant.administer`, `party.merge` |
| Routes that run only in scope `*` (`dbx.TenantWide` / `WithTenantWideAdminTx` is their only path) | `org.legal-entity.write`, `org.reference.write`, `party.merge`, `migration.opening-target.release` |
| Tenant-wide structure, schema and whole-tenant data movement | `org.hierarchy.write`, `org.hierarchy.publish`, `org.hierarchy.backdate`, `org.package.manage`, `org.demo-company.manage`, `metadata.entity.maintain`, `metadata.extension.apply`, `metadata.write`, `events.write`, `migration.package.apply`, `analytics.archive.admin` |
| Integration channels that carry every legal entity's events or grant API scopes | `hub.client.write`, `hub.catalog.write`, `hub.subscription.write`, `hub.delivery.read`, `hub.coexistence.operate` |

Not tenant-level:

- Duties that use `dbx.TenantWide` for one branch only, beside a legal-entity-scoped branch: `print.admin`,
  `migration.run`, `migration.project.write`. A tenant-wide branch needs a request with no legal entity, and a scoped
  assignment grants nothing to such a request anyway.
- Duties on a service's own tenant-wide reference tables, such as fx rates, scheduling, and `shopfloor.setup.maintain`
  (the shop floor's time profiles, pay agreements and absence codes, shared by every legal entity; its
  assign-legal-entity routes also need a request with no legal entity). Those services check the duty from the
  token's `duties` claim on every route, so OPA's legal-entity rule changes nothing for them.

A new duty that acts on the tenant as a whole must carry the flag.

## Resources

Each permission is a route template matched with `glob.match(pattern, ["/", ":"], path)`:

- `*` matches one path segment and never a `:verb` suffix, so `journals/*` and `journals/*:post` are distinct routes.
- Platform duties use `<prefix>{,/**}` (the prefix and everything beneath it).
- Seed one row per mounted method and route template, so a narrow duty cannot reach a verb route owned by another duty.
- Routes that dispatch a `:verb` inside one handler (production order commands, down payments) are seeded as one `*:verb` row per verb on the duty that handler enforces.

## Costing route prefixes

`costing` (Wave 20 inventory costing, dotted `costing.*` duties) owns `/v1/costing/...`. `productcost` (Wave 39 product costing, `productcost.*` duties) owns `/v1/product-costing/...`, so the two duty families never share a route template (#342). In the web dev proxy only, `/api/v1/product-costing/...` resolves to `DEPOT_SVC_PRODUCT_COSTING_URL`; `deploy/gateway/httproutes.yaml` has no productcost route yet.

## Path input

The PEP must pass the decoded `r.URL.Path` with no query string, or the route template (`/v1/vendors/*`). `*` matches an empty segment, so `/v1/vendors/` matches `/v1/vendors/*`, and an encoded `%3A` is not a `:` until it is decoded: a raw path can slip past a `*:verb` row or match a plain `*` row it should not.

## Flag duties

`"flag": true` duties (for example `ledger.journal.postdirect`, `budget.override`, `org.hierarchy.backdate`) are checked in a handler on a route another duty already opens. They carry no route permissions. `tax.code.maintain` is a flag until a tax-code read route is mounted. A duty can own routes and still be checked in handlers: `inventory.count.approve` opens `count-sheets/*:approve` and `count-sheets/*/lines/*:recount`, and the inventory handlers also read it with `HasDuty` for over-tolerance `:post`, marking open tags MISSING on `:transfer-tags`, and unmasking blind counts.

## Gateway enforcement (B-04, ADR-0017)

Every gateway asks this policy, so the three enforce one model:

- devgateway and `depot-server` query `data.authz.decision` through `identity.RequireOPAForGateway`, with the verified
  subject, the method and the request path without `/api`;
- Envoy's ext_authz OPA (`deploy/gateway/authz-opa.yaml`) queries `data.authz.envoy`.

`data.authz.envoy` takes the OPA-Envoy plugin's input. It reads the subject from the `x-depot-tenant`, `x-depot-user`
and `x-depot-legal-entity` headers, which the JWT filter set from the verified token after Envoy removed the client's
copies. It maps the public path to the path the service receives (`gateway_resource`: `/api` removed, or the service
base of a D22 prefix from `gateway_service_bases`) and calls `decision`. A path with any percent-encoding is not canonical
and is refused with 400, as on the single-host gateways. It answers with `allowed`, the deny status and problem body, and
`record`, the allow/deny record (`platform/go/README.md`).

Every routed prefix's service base needs at least one duty permission under it, or Envoy refuses that service to
everyone. `mcp.copilot.use` grants `POST /mcp`, the path mcp-ts serves and Envoy decides on. `gateway_test.rego` checks
the rewritten bases, and `deploy/scripts/test_gateway.py` checks every generated route.

`decision` names the granting duty on an allow (`{"allow": true, "duty": ...}`), so every record says which duty
allowed a request. `log_pseudonym` and `log_subject` compute the record's subject as `logpseudo.Subject` does, under
the dedicated key `DEPOT_LOG_PSEUDONYM_KEY` the OPA is given (`opa.runtime().env`); without it the subject is
`[redacted]`. The decision-log mask is not in this package: the ext_authz OPA loads it locally (`data.system.log.mask`),
so a bundle cannot turn it off.

`testdata/route_table.json` is the shared route table. Each row is method, public path, tenant-wide duties,
legal-entity-scoped duties, the token's legal entity and the expected answer (with `status` 400 for an encoded path,
and `token_duties_only` for a machine caller whose duties are only in its token). Three suites run it:

- `gateway_test.rego` runs it against `data.authz.envoy`;
- `services/identity/cmd/devgateway` and `tools/depot-server` (`route_table_test.go`, through
  `services/identity/authztest`) run it end to end against `opa run` on this directory.

A row is a browser path outside the D22 prefixes, because the single-host gateways serve neither the partner paths nor
the D22 rewrites.

## Tests

`opa test --timeout 120s policies/authz -v` (the default 5 s per-test timeout is too short for the larger duty catalogue), then `cd services/identity && go test ./... -count=1` (needs `opa` on PATH).

Route templates are not cross-checked against the services' routers: routes are built from helpers, loops and in-handler action dispatch, so they cannot be extracted reliably without running the routers.
