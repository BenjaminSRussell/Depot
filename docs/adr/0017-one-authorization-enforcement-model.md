# ADR-0017: One authorization enforcement model: every gateway asks OPA, and every decision is logged

**Status:** Accepted — 2026-11-17 (SDLC run 1, B-04; closes the design part of deviations D23 and D36)
**Amends:** ADR-0006 rules 3 and 7 and its "Consequences" on decision logging
**Evidence:** `policies/authz/authz.rego` ("Gateway enforcement"), `policies/authz/testdata/route_table.json`,
`deploy/gateway/securitypolicy.yaml`, `deploy/gateway/authz-opa.yaml`, `deploy/gateway/envoyproxy.yaml`,
`services/identity/opa.go` (`RequireOPAForGateway`), `platform/go/authz/record.go`

## Context

ADR-0006 rule 7 asked for one enforcement model on the three gateways and recorded the gap: devgateway and `depot-server`
asked OPA (`identity.RequireOPAForRequest`, `data.authz.decision`) for every request, while Envoy Gateway in Kubernetes
made no authorization decision at all; it verified the JWT, mapped claims to `X-Depot-*` headers and denied the nil
tenant, and the service behind it checked the route's duty from the token's `duties` claim. A route refused on a Windows
host could be allowed in Kubernetes (D23). No gateway and no service wrote a record of a decision (D36).

Two models were open (the backlog's "all OPA, or all duty headers"):

- **Duty headers everywhere.** Drop the OPA call from the single-host gateways and rely on each service's
  `authz.RequireDuty`. This removes a check where it exists today (the legal-entity-scoped assignments of B-03b are
  enforced only by OPA, and the gateways would stop refusing a route no duty covers), so it loosens the single-host path.
  It also leaves the duty catalogue's method-and-path rules (`data.json` `permissions`) enforced nowhere.
- **OPA everywhere.** Add an OPA decision to Envoy. Envoy Gateway v1.4 (the version deployed) has `SecurityPolicy.extAuth`
  with a gRPC backend, `failOpen`, and `headersToExtAuth`; the OPA-Envoy plugin image answers Envoy's ext_authz gRPC
  check from a rego rule. Two obstacles, both solvable in the API as used here: Envoy Gateway's default HTTP filter order
  runs `ext_authz` before `jwt_authn`, so OPA would see no verified identity (fixed by `EnvoyProxy.spec.filterOrder`
  on the Gateway's `infrastructure.parametersRef`); and Envoy sees the public path (`/api/v1/<prefix>/...` or
  `/v1/<prefix>/...`), not the path the service serves (fixed in rego with the route table's rewrite).

## Decision

OPA everywhere, on one policy bundle.

1. **Every gateway asks `policies/authz` with the same input**: the subject from the verified token (tenant `tid`, user
   `sub`, legal entity `le`), the request method, and the path the service receives. devgateway and `depot-server` ask
   `data.authz.decision` through `identity.RequireOPAForGateway`; Envoy asks `data.authz.envoy` through ext_authz, a rule
   that builds that input from the headers the JWT filter set and the public path, and calls `data.authz.decision`.
   - The path the service receives is the public path without `/api`, except for the D22 prefixes Envoy rewrites to the
     service's own base (`gateway_service_bases` in `authz.rego`, checked against the generated HTTPRoutes by
     `deploy/scripts/test_gateway.py`). The single-host gateways do not rewrite those prefixes (they forward `/v1/<prefix>`),
     which is a routing gap, not a policy one; the shared route table leaves the D22 prefixes out and records why.
   - Envoy: `SecurityPolicy/depot-api-jwt` gains `extAuth` (gRPC to Service `depot-authz-opa:9191`, `failOpen: false`,
     only `x-depot-tenant`, `x-depot-user`, `x-depot-legal-entity` and `x-request-id` sent); `EnvoyProxy/depot-api-proxy`
     puts `ext_authz` after `jwt_authn`. The OPA (`deploy/gateway/authz-opa.yaml`, OPA 1.21.2-envoy) pulls identity's signed
     bundle exactly as the per-service sidecar of `deploy/policies/opa` would, so it carries every tenant's role
     assignments. It is one Deployment of two replicas beside the Gateway, not a sidecar per pod.
   - The deny answer is the same on every gateway: `403 AUTH-0003` with `errors.duty_missing`, and `400 GW-0400` for a
     path that is not canonical. A canonical path has no percent-encoding at all on any gateway: an escape a service's
     router decodes (`%3A` is `:`, the glob's segment delimiter) could otherwise be decided on one path and served on another.
     So **path identifiers must be unreserved ASCII** (letters, digits, `-`, `.`, `_`, `~`): a route whose `{id}` could
     hold a space, a non-ASCII letter, `@` or another reserved character would be refused at every gateway. Every path
     identifier today is a UUID or a pattern-restricted code. A free-text key goes in the query string or the body
     (`docs/engineering/CONVENTIONS.md`, Go services). A live Envoy may decode an escaped unreserved character (`%73`) while
     normalizing the path, before ext_authz, and then decide on the decoded path the service also receives (#213).
   - A policy outage differs, because Envoy Gateway v1.4 has no `statusOnError`: the single-host gateways answer
     `503 AUTH-0503` and record `policy_unavailable`, while Envoy fails closed with a bare `403` (no body, no record). Alerts
     `DepotAuthzEnvoyCheckErrors` (Envoy's `ext_authz.error` counter) and `DepotAuthzOPAUnavailable` (no ready OPA replica)
     cover it, and the OPA is ready only when it can answer (rule 4).
   - **Machine callers.** OPA grants only from identity's role assignments, keyed by the token's `sub`. A
     client-credentials token of `depot-services` (duties from attributes on its Keycloak service-account user,
     `deploy/keycloak/README.md`) is therefore refused at every gateway unless identity holds role assignments for that
     service-account subject. The single-host gateways already behaved this way; at Envoy it is new. In this repository no
     machine caller goes through Envoy today:
     - services call each other on cluster-internal `*_URL` addresses with Ed25519 service tokens (ADR-0010);
     - SCIM (`/scim/v2`) is not routed on `depot-api`;
     - the other client-credentials users (identity's Keycloak deprovisioning, ops provisioning, the supplier portal's
       provisioning, migration's and integration-hub's outbound OAuth) call Keycloak or external systems.

     A partner or machine integration that calls `/v1/<prefix>` through Envoy needs identity role assignments for its
     service-account `sub`. The route table holds such a caller (`token_duties_only`), refused everywhere. The alternative,
     granting machine callers from `service_principals`, is an owner decision (follow-up).
2. **The services keep their own duty check** (`authz.RequireDuty` from the token's `duties` claim). It is defence in
   depth, not the model: a request reaches a service only after a gateway's OPA allowed it, and the service still refuses
   it without the route's duty. No check is loosened.
3. **One shared route table** (`policies/authz/testdata/route_table.json`): (method, path, tenant-wide duties,
   legal-entity-scoped duties, legal entity, allow). devgateway and `depot-server` run it end to end against a real OPA
   loaded from `policies/authz` (`services/identity/authztest`); Envoy runs it as `gateway_test.rego` against
   `data.authz.envoy` (the answer Envoy would get) and is checked statically by `test_gateway.py` `AuthzEnforcement`:
   every HTTPRoute of `depot-api` is covered by the one gateway-level SecurityPolicy with `extAuth`, none is overridden
   by a route- or listener-level SecurityPolicy, and the filter order, headers, backend and NetworkPolicy are as above.
   No live Envoy runs in CI (#213).
4. **Every decision writes one allow/deny record**, event `authz.decision`, schema 1 (`platform/go/README.md`):
   pseudonymized subject (`logpseudo.Subject`, the tenant-keyed HMAC; `[redacted]` without a key, never the raw id),
   tenant, duty, route
   (method and route template, never the raw path), outcome, reason, request id, source and `log_class: audit.other`.
   `authz.Require`, `RequireDuty` and `RequireAnyDuty` write it in every service; `identity.RequireOPA*` writes it in
   identity and the single-host gateways, which also record their own legal-entity-selection and non-canonical-path
   denials; the Envoy OPA writes it as `result.record` of its console decision log.
   - **The Envoy decision log's mask does not come from the bundle.** It is `data.system.log.mask` (it erases the whole
     input: raw subject, token claims, client address, path), a local policy in the OPA's ConfigMap under `data.system`,
     which identity's bundle roots never claim. A bundle from an identity image older than this change, or one that lacks
     or renames a rule, cannot turn masking off. The pod is ready only when the bundle is active, the mask is loaded and
     the bundle answers `data.authz.envoy` (`data.system.health.ready`, `/health/ready`). `services/identity/authztest`
     runs OPA with the shipped configuration against a bundle that has no mask rule and finds no raw user, tenant, address
     or path in the log, and the pod not ready.
   - **The Envoy record carries the same pseudonym as Go.** Rego reproduces `logpseudo.Subject` exactly: HKDF-SHA256
     (salt the tenant, info `passage.fieldcrypt.mac.v1`, one 32-byte block) and HMAC-SHA256, through
     `crypto.hmac.sha256`, `hex.decode` and `base64.decode` (`data.authz.log_pseudonym`). `authztest` compares rego with
     Go on 24 vectors, including non-ASCII values and keys whose bytes are not UTF-8. The key is the dedicated
     pseudonymization key `DEPOT_LOG_PSEUDONYM_KEY` (Secret `depot-log-pseudonym`, optional; without it the subject is
     `[redacted]`), read with `opa.runtime().env`.
   - **The dedicated key.** `logpseudo` reads that key first (`DEPOT_LOG_PSEUDONYM_KEY` or `_FILE`, base64 of 32 bytes,
     used only for this MAC) and falls back to fieldcrypt's current master key. It goes through fieldcrypt's MAC
     derivation, so one key yields one pseudonym per user and tenant at Envoy, in the services and on the single-host
     gateways. Services without field-encryption keys (most charts) then pseudonymize instead of redacting. Wiring the
     Secret into every service chart is a follow-up.
   - **Never copy fieldcrypt's master key into this Secret.** Field-encryption keys derive from that master key, and
     `depot-log-pseudonym` is mounted in the ext_authz OPA pods, so a copy there would let anyone who reads the Secret or
     compromises an OPA pod decrypt PII fields. Generate a fresh 32-byte key instead. No chart mounts
     `depot-log-pseudonym` in the services yet: until it is wired (mounted optionally in the seven fieldcrypt-keyed
     services, compliance, hr, migration, party, payables, receiving and sales, whose `KeyFromEnv` already prefers it),
     those services pseudonymize under fieldcrypt's key and the Envoy record names the same user with a different
     pseudonym, and every other service writes `[redacted]`. That mismatch is accepted until the wiring lands.
   - The policy reads the key inside `log_subject` only; no rule of `data.authz` evaluates to it, so the OPA's data API
     cannot return it. `authztest` checks `data.authz` with the key set.
   - `depot-server` reads only the dedicated key for its own records. It passes `FIELDCRYPT_*` only to the seven services
     that load field-encryption keys (the tested allow-list `fieldcryptServices`, `docs/ops/windows-install.md`), and
     passes the pseudonym key to none, unless a service's own configuration sets it.
   - **Request ids.** A gateway's request id is its own and is forwarded, so the gateway's and the service's record of
     one request share it. The single-host gateways drop a client's `X-Request-Id`; Envoy removes it in
     `earlyRequestHeaders` and always generates one (`requestID: Generate`).
5. **Deny counters.**
   - `depot_authz_denied_total{source,duty,reason}` counts every deny in a Go process (`httpx`'s shared `/metrics`), with
     no user or tenant label, and `DepotAuthzDenySpike` alerts on a spike.
   - A request Envoy refuses never reaches a service. Envoy's own ext_authz counters
     (`envoy_cluster_ext_authz_denied` and `_error`, per route cluster, scraped by `deploy/gateway/envoy-podmonitor.yaml`)
     count it, and `DepotAuthzEnvoyDenySpike` alerts on a spike.
   - The alerts are a `PrometheusRule` from the observability chart, the only kind of rule kube-prometheus-stack loads.
     `test_argocd_apps.py` checks that the dev Prometheus selects every alert, and CI runs `promtool check rules` on the
     rendered rules. `DepotAuthzOPAUnavailable` is off where identity serves no bundle (`authz.opaUnavailableAlert` false in
     dev) and on by default.

## Alternatives rejected

- **Duty headers everywhere** (above): loosens the single-host gateways and leaves scoped assignments unenforced.
- **An OPA sidecar per service pod** (the original design, ADR-0006 "Alternatives rejected"): 60-plus sidecars each
  holding every tenant's assignments, for no gain over one decision at the edge while the services keep their duty check.
- **Envoy Gateway's own `authorization` rules (RBAC on JWT claims).** They match claims, not the duty catalogue's
  method-and-path permissions or the legal-entity-scoped assignments, so Envoy would still decide differently.
- **HTTP ext_authz to identity.** Puts identity on every request's path and needs a new endpoint; the OPA-Envoy plugin
  answers from the same bundle locally.

## Consequences

- The three gateways enforce the same policy for the same route; a role scoped to company A grants nothing in company B
  through Envoy too, which was not true before (ADR-0006 rule 4's "Envoy does not call OPA").
- Envoy's path now depends on identity's bundle: until identity serves it (chart value `bundle.enabled`, off in dev) and
  Secret `depot-authz-opa-bundle` exists, the ext_authz OPA is not ready and every API request through Envoy is refused.
  The Kubernetes path did not work end to end before either (the issuer is not wired, D32), so nothing that worked breaks.
- Revocation through Envoy follows the bundle's polling (10 to 20 s) for role assignments, and the token's life (300 s)
  for the services' duty check; ADR-0006 rule 5 stands.
- Records are written to stdout as structured JSON; shipping them to the log store and keeping them by `log_class` is
  #213 (no log shipper is deployed). OPA decision logs from identity's own sidecars stay off (identity does not run one in
  Kubernetes); turning them on needs the same local `system.log` mask policy.
- The Envoy metric names and the plugin's handling of the answer are checked against Envoy's documented stats and the
  CRDs, not a live scrape (#213).
- Follow-ups:
  - wire Secret `depot-log-pseudonym` into the service charts and give `depot-server` a per-installation key;
  - decide whether machine callers may be granted from `service_principals`;
  - run Envoy live (#213).
- Every service request now writes one more log line (the allow record). That is the cost of answering "who was allowed
  what, when".
