# ADR-0006: Gateway-asserted identity with duties in the token

**Status:** Accepted — 2026-11-17 (records a decision already made in code; SDLC run 1, stage 2, deviations D1 and D23)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` §3 rows 3–4, §4 D1, D23; `docs/sdlc/run-1/architecture-review-response.md` R-02, R-03, R-06
**Amended:** 2026-11-17 after the independent stage-2 review: as-built issuer and key (Context), agents-ts exception (rule 1), legal-entity model (rule 4, Proposed — pending owner confirmation), decision logging (Consequences)

## Context

The architecture (`docs/architecture.md` §7.1–7.4, v0.x) designed authorization as policy outside the token: the JWT
carries identity and tenant only, Envoy calls an OPA-Envoy instance through ext_authz for a coarse route check, every
service re-verifies the JWT and asks its own OPA sidecar for `data.depot.authz.decision`, and a revoked duty stops
working within 10 seconds.

The code does something else, consistently, across every service:

- Keycloak realm `depot` puts the user's duties in the access token: protocol mapper `duties`
  (`deploy/keycloak/realm-depot.json`), access-token life 300 s (`accessTokenLifespan`).
- Envoy Gateway is the authenticator. `ClientTrafficPolicy/depot-api-strip-identity-headers` removes every
  client-supplied `X-Depot-*` header in `earlyRequestHeaders`; `SecurityPolicy/depot-api-jwt` validates issuer,
  audience `depot-api` and signature, and maps `tid`, `le`, `sub`, `duties`, `tier` and `auth_time` to `X-Depot-Tenant`,
  `X-Depot-Legal-Entity`, `X-Depot-User`, `X-Depot-Duties`, `X-Depot-Tier` and `X-Depot-Auth-Time`
  (`deploy/gateway/README.md`, "Request path"). There is no ext_authz filter in `deploy/gateway/`.
- **The issuer is not wired.** The SecurityPolicy's provider names issuer `https://idp.dev.depot.dev/realms/dev` and
  validates against `localJWKS` from ConfigMap `depot-dev-jwks`, a static placeholder public key with no private key
  ("nothing can mint accepted tokens until #80", `deploy/gateway/README.md`). The realm is `depot`
  (`realm-depot.json`). agent-runtime (agents-ts) is configured for `realms/dev` (dev) and `realms/stage` (stage); the
  portal BFFs for `realms/depot`. So the chain this ADR accepts runs end to end only on the single-host gateways, which
  use identity's verifier; in Kubernetes it is configured but not connected (D32).
- **Envoy routes every prefix the web shell calls** (83 generated HTTPRoutes to 60 Services, B-02a); charts whose API
  the shell does not call are not reachable through it (D31).
- Services build the principal from those headers (`platform/go/tenant/tenant.go`: "Middleware validates their shape
  but cannot authenticate their origin") and enforce the route's duty locally with `authz.RequireDuty`
  (`platform/go/authz/authz.go`, `DutyPolicy`); 56 services use it. Every OpenAPI operation declares its duty in
  `x-depot-duty` on 3,116 of 3,194 operations (97.6%); the rest have none.
- OPA (`policies/authz/authz.rego`, `package authz`, decision `allow` only) is evaluated by `services/identity`
  (`RequireOPAForRequest`, for example on SCIM), by the single-host gateways `services/identity/cmd/devgateway` and
  `tools/depot-server`, and by nothing in Kubernetes. Identity compiles and signs the bundle and serves it at
  `GET /v1/identity/policy/bundle` (`deploy/policies/opa/README.md`). Decision logs are off.
- Service-to-service calls carry either a signed Ed25519 service token (ADR-0010) or the caller's forwarded
  `X-Depot-*` headers. At least receiving, payables, party and product forward user headers to peers (identity,
  close, tax, numbering and others; see `tenant.ForwardGatewaySecret` call sites).
- On a Windows host (ADR-0004) the services listen on loopback, so `depot-server` sets a per-start
  `DEPOT_GATEWAY_SECRET` and `platform/go/tenant` rejects a header identity without it. The variable is unset in
  Kubernetes, where the code comment and ADR-0004 name NetworkPolicy as the boundary.

That boundary is not in place: only 13 of 69 service charts ship a NetworkPolicy (agents-ts, carrier-gateway-ts,
customer-portal-ts, edi, fx, identity, integration-hub-ts, mcp-ts, migration, org, receivables, supplier-portal-ts,
workflow-ts), some of those admit every pod in the namespace by default (fx), and 67 of the 69 service charts run in
the namespace `hello-ledger` in dev (all but batch and metadata). Any pod in that namespace can call a service
with forged `X-Depot-*` headers and be recorded as a genuine user.

## Decision

Keep the built model and record it as the v1.0 authorization architecture.

1. **The gateway is the only authenticator for user requests.** Services do not re-verify the user's JWT. They trust
   `X-Depot-*` headers because the network guarantees that only the gateway, and the peers listed in rule 6, can
   reach them. **Exceptions:** agents-ts verifies the bearer token itself (RS256 against the realm keys, issuer,
   audience, expiry; `services/agents-ts/src/api/auth.ts`) and refuses a request whose `X-Depot-*` headers disagree
   with the token; it must use the same issuer as Envoy (D32). The portal BFFs are their own authenticators for portal
   users (cookie sessions, ADR-0009). Identity verifies tokens for the single-host gateways.
2. **Duties travel in the access token** (`duties` claim, mapped to `X-Depot-Duties`). Each route enforces the duty
   named in its `x-depot-duty` with `authz.RequireDuty`. The `authz.Policy` interface stays the seam for a later OPA
   decision point; no service branches on role names.
3. **OPA is the policy engine where a full decision is needed**: identity's administrative and SCIM routes, and the
   built-in gateway of single-host installs (devgateway, `depot-server`). No ext_authz and no per-service OPA
   sidecar in Kubernetes for v1.0.
4. **Legal-entity scope** comes from the `le` claim (`X-Depot-Legal-Entity`), and is widened to all entities only on
   reads that send `X-Depot-LegalEntity-Scope: ALL` by a caller holding `org.switcher.all-scope` (`tenant.go`;
   architecture §7.3 rule 3a).
   - *As built:* `le` is one string (user attribute `legal_entity_id`, `multivalued: false`). The kernel lets
     `X-Depot-LegalEntity` only narrow `X-Depot-Legal-Entity`: every selected entity must be in the claim (one entity,
     or a comma list of up to 100), and a selection outside it is refused with `403 AUTH-0003`
     (`tenant.requestLegalEntity`, tests `TestMiddlewareBothLegalEntityHeaders`, `TestMiddlewareLegalEntitySelection`).
     A selection with no `le` claim is refused with `403 AUTH-0003`, except from a tenant administrator
     (`tenant.administer`, no claim; `tenant.IsTenantAdmin`), who is entitled to every entity of the tenant and may
     still pick one (B-03a, RR-01; before, the kernel took any selection as the scope when the claim was empty).
     devgateway and `depot-server` return `403` unless the selection equals the claim, tenant administrators
     included; Envoy strips the selection. Because the single claim is one entity, nothing narrows in practice, and a
     user entitled to several legal entities cannot switch company in the shell on any deployment (D33).
   - **Proposed (recommended; pending owner confirmation):** a user may work in any legal entity they are entitled
     to. The `le` claim becomes the list of entitled legal entities (a comma-joined string, like `duties`, because
     Envoy base64-encodes array claims; the kernel's parser already accepts up to 100). The shell's
     `X-Depot-LegalEntity` selects and narrows to one entity within that list; a selection outside the list, or a
     selection with no claim, is refused with `403` by the kernel and by every gateway. The one exception is a tenant
     administrator (`tenant.administer` with no claim), who is entitled to every entity of the tenant and may narrow
     to one by selection; the kernel already allows it (B-03a), and B-03c brings devgateway, `depot-server` and Envoy
     in line with the kernel, as part of this Proposed model, pending owner confirmation. Envoy forwards the selection
     instead of stripping it (it is safe to forward because the kernel checks it against the claim-derived header).
     A write in a session with more than one entitled entity must carry a selection; peer calls carry the selected
     entity in the service token (ADR-0010). The alternative considered, one entity per token with re-authentication
     on every company switch, is rejected in the recommendation because it costs a Keycloak round trip and a token
     per switch and makes cross-company reads (rule 3a) awkward. Until the owners confirm, the as-built behaviour
     stands; the acceptance test is in D33.
   - *As built (B-03b): legal-entity-scoped role assignments in OPA.* Identity's bundle compiler now carries every
     active assignment: tenant-wide ones in `data.assignments[tenant][subject]` as before, legal-entity-scoped ones
     (`identity_role_assignments.legal_entity_id` set) in the new root `data.scoped_assignments[tenant][subject][legal_entity]`
     (before, they were left out, so a per-company role granted nothing in OPA). `authz.rego` grants a scoped duty only
     when the request acts in that legal entity: `input.subject.legal_entity_id` names the entity (or comma list) and
     every listed entity must carry the duty, so a role scoped to company A grants in A and nothing in B, in A and B
     together, or on a request with no legal entity; tenant-wide assignments, tenant administrators and operator-only
     duties are unchanged (`authz_test.rego`, `test_role_scoped_to_company_a_*`, `test_tenant_admin_is_unchanged`).
     `RequireOPA` fills `input.subject.legal_entity_id` from the verified token's `le` claim (a string, or an array
     joined with commas; any other shape is no legal entity), kept out of the principal so the database scope of
     identity's own routes does not change. On the single-host gateways the selection must equal the claim, so the
     claim is the request's legal entity; under the Proposed list model (B-03c) the gateways must pass the selected
     entity instead. Envoy does not call OPA (rule 3), so in Kubernetes a scoped assignment still reaches services
     only through the token's `duties` and `le` claims.
5. **Revocation latency is the access-token life (300 s).** The designed ≤ 10 s is withdrawn for v1.0. Step-up uses
   the auth-age check on `X-Depot-Auth-Time`, not an OPA `requires_acr` obligation.
   - *Decision (B-03b): no `pv` claim for v1.0.* The PRD (FR-2.1, FR-2.6, message IAM-0405) puts the policy bundle
     version in the token as `pv`, so a sidecar can tell a token issued before the active bundle and the shell can ask
     the user to refresh. Under this ADR nothing would read it: services decide from the token's `duties`, and the OPA
     callers (identity, devgateway, `depot-server`) decide from the live bundle, whatever bundle the token was issued
     under. The bundle revision is a content hash (`.manifest` `revision`, also the endpoint's `ETag`), not an ordered
     number, so "more than 2 versions behind" has no meaning, and Keycloak would need a custom mapper to read it. A
     stale token is bounded by the 300 s access-token life instead. `pv` is not issued and FR-2.1/FR-2.6 stay Partial
     on it; revisit if per-service OPA (the upgrade path under "Alternatives rejected") is adopted, which would need an
     ordered bundle version first.
6. **What NetworkPolicy must enforce for this to be safe.** In every namespace that runs a Depot service:
   - a default-deny ingress NetworkPolicy;
   - ingress to a service's API port only from the Envoy Gateway data-plane pods, plus the named peer services that
     call it (the list is generated from the code's peer calls and service grants, not kept by hand);
   - ingress to the admin port (`/metrics`, health) only from Prometheus and the kubelet;
   - no Depot service exposed through a `LoadBalancer` or `NodePort` Service or any ingress other than Envoy Gateway;
   - the gateway keeps stripping `X-Depot-*` in `earlyRequestHeaders` (already true, tested by
     `deploy/scripts/test_gateway.py`).
   Where a namespace cannot express this (shared namespace, unlabelled pods), set `DEPOT_GATEWAY_SECRET` in
   Kubernetes as well and have Envoy add `X-Depot-Gateway-Secret`, the mechanism the Windows install already uses.
   Until every service is covered, no environment that runs untrusted workloads next to Depot is a supported
   deployment, and stage and prod are not opened to users.
   - *As built (B-01a, #660):* every service chart (70, `services/*/charts/*` and the migration factory) renders
     `<release>-ingress` from the identical template `templates/networkpolicy-ingress.yaml`: it selects every pod of the
     chart by `app.kubernetes.io/name` and admits the API port (8080) only from the Envoy pods of Gateway `depot-api`
     (namespace `envoy-gateway-system`, labels `owning-gateway-name`/`owning-gateway-namespace`) plus any other Gateway
     with an HTTPRoute to the service (`depot-portals`, `depot-region`, `depot-edge`), and from the generated peers; the
     admin port (9090) only from Prometheus (`app.kubernetes.io/name: prometheus` in `observability`). The peers and
     gateways are in each chart's `ingressPolicy` values, written by `deploy/scripts/gen_networkpolicies.py`: a peer is a
     caller whose chart configures a `*_URL` client of the service (which also covers the callers that still forward
     user headers, until part b), and every grant in the service grant table (`policies/authz/data.json`
     `service_grants`, the mirror of `serviceGrants`) must have such a client or an entry in an explicit list with its
     reason (`GRANT_ONLY_PEERS`, admitted; `GRANT_WITHOUT_CLIENT`, not admitted: transport -> carrier-gateway). carrier-gateway
     and the portal BFFs admit no `depot-api` traffic by design; iot-edge opens no port and admits nothing; hello-ledger
     also admits the cluster's Traefik for its smoke Ingress. ApplicationSet `namespace-default-deny` (dev, stage and
     prod, generated) applies a default-deny ingress policy to every namespace a service runs in (`hello-ledger`, `batch`,
     `metadata`; prod: `hello-ledger`). `make -C deploy test-argocd` runs the generator's `--check` and renders every
     dev, stage, prod and local chart to check all of the above. Kubelet probes are not filtered on the supported CNIs.
   - *Region fence:* `global.regionFenceUrl` must be read through Gateway `depot-region`
     (`https://region.<env>.depot.dev/healthz/region`), which exposes only `/healthz/region` and strips `X-Depot-*`.
     NetworkPolicy cannot limit a caller to one path, so admitting every service to ops' API port would expose the whole
     operator API (failover, break-glass, licence) to forged headers; ops admits only the gateways and its own lag
     reporter, and every chart's template fails to render when the fence URL names the ops Service (`ops`,
     `ops.<namespace>[.svc[.cluster.local]]`).
   - *Exception, metrics on the API port:* configurator, forecast, prediction and scheduler-opt (the Python services) serve
     `/metrics` on their API port 8080 and have no admin port, so Prometheus reaches their API port as well. Prometheus is
     trusted infrastructure, but this breaks the "admin port only" rule; moving their metrics to an admin port is a
     follow-up.
   - *Known weakness, label borrowing:* the policies identify peers by the pod label `app.kubernetes.io/name`, which any
     workload in the namespace can set. A pod labelled `app.kubernetes.io/name: ledger` gets every peer access ledger
     has, and can then forge `X-Depot-*` headers to those peers. Until an admission policy (Kyverno) pins each Depot
     `app.kubernetes.io/name` value to its chart's ServiceAccount and image, only trusted parties may create pods in
     these namespaces, and where that cannot be guaranteed `DEPOT_GATEWAY_SECRET` must also be set (the fallback
     above).
   - Not yet done: the integration test that sends a forged-header call from a non-peer pod (#213, k3d) and the signed
     peer calls (part b); the policies only take effect on a CNI that enforces NetworkPolicy, so `DEPOT_GATEWAY_SECRET`
     stays the fallback where one cannot be guaranteed.
7. **One enforcement model everywhere.** Envoy (Kubernetes), devgateway and `depot-server` must enforce the same
   checks for the same route. Today the two single-host gateways also call OPA and Envoy does not; that difference
   is a known gap, not a feature.

## Alternatives rejected

- **The original design (ext_authz plus a sidecar per pod).** Not built; 60-plus sidecars and a bundle carrying every
  tenant's role assignments (Q22) were the cost. Remains the upgrade path through the `authz.Policy` seam if a
  ≤ 10 s revocation or per-record obligations become a requirement.
- **Per-service JWT verification with duties in the token.** Removes the network dependency but needs JWKS and issuer
  configuration in every service; it is the fallback if NetworkPolicy cannot be enforced on a target platform.

## Consequences

- Authorization is simple and fast (a header lookup), and every route's duty is visible in its OpenAPI.
- Identity provenance in every audit row depends on network isolation. That isolation is the precondition for go-live.
- Role changes take up to five minutes to apply; users can see stale navigation and actions in that window.
- No authorization decision is logged: OPA decision logs are off, services and gateways write no allow/deny record
  with actor, duty and route, and the access log has only method, path and status (D36). Until D36 is closed, the
  system cannot answer an auditor's "who was allowed or refused what".
- Backlog: add a default-deny NetworkPolicy and gateway-only (plus generated peer allow-list) ingress to every service chart and namespace, and a CI check that fails when a chart lacks it (D1).
- Backlog: decide per environment whether `DEPOT_GATEWAY_SECRET` is also required in Kubernetes, and if so add it to the Envoy SecurityPolicy and every chart (D1).
- Backlog: move peer calls that forward user headers (receiving, payables, party, product) to signed service tokens, so peer ingress can be narrowed to the gateway only (D1).
- Backlog: make Envoy, devgateway and `depot-server` enforce one model, either all with OPA or all with duty headers only, and test the three against the same route table (D23).
- Backlog: once the owners confirm rule 4's proposed model, make `le` a list, check the selection for membership in the kernel and in both single-host gateways (403 outside the list or without a claim), forward it through Envoy, and add the end-to-end company-switch test (D33).
- Backlog: switch the SecurityPolicy to `remoteJWKS` and the `depot` realm issuer per environment, align agent-runtime's issuer, and test that a token from `realm-depot.json` passes the gateway (D32).
- Backlog: add an HTTPRoute and BackendTrafficPolicy target for every service with an API, with a CI check against the shell's prefixes and the OpenAPI prefixes (D31).
- Backlog: emit a structured allow/deny record per decision (pseudonymized subject, tenant, duty, route, outcome, request id) from `authz.Require` and from the three gateways, ship and retain it, and add `depot_authz_denied_total` with an alert (D36).
- Backlog: document the 300 s revocation window in the operator and security guides, and give tenant admins a session-revocation action that also ends Keycloak sessions (D41).
