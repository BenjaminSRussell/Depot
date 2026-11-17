# ADR-0006: Gateway-asserted identity with duties in the token

**Status:** Accepted — 2026-11-17 (records a decision already made in code; SDLC run 1, stage 2, deviations D1 and D23)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` §3 rows 3–4, §4 D1, D23

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
  audience `depot-api` and signature, and maps `tid`, `le`, `sub`, `duties` and `tier` to `X-Depot-Tenant`,
  `X-Depot-Legal-Entity`, `X-Depot-User`, `X-Depot-Duties` and `X-Depot-Tier` (`deploy/gateway/README.md`,
  "Request path"). There is no ext_authz filter in `deploy/gateway/`.
- Services build the principal from those headers (`platform/go/tenant/tenant.go`: "Middleware validates their shape
  but cannot authenticate their origin") and enforce the route's duty locally with `authz.RequireDuty`
  (`platform/go/authz/authz.go`, `DutyPolicy`); 56 services use it. Every OpenAPI operation declares its duty in
  `x-depot-duty` (97.6% of operations).
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
workflow-ts), some of those admit every pod in the namespace by default (fx), and 63 of the 65 dev service releases
share the namespace `hello-ledger` (batch and metadata have their own). Any pod in that namespace can call a service
with forged `X-Depot-*` headers and be recorded as a genuine user.

## Decision

Keep the built model and record it as the v1.0 authorization architecture.

1. **The gateway is the only authenticator for user requests.** Services do not re-verify the user's JWT. They trust
   `X-Depot-*` headers because the network guarantees that only the gateway, and the peers listed in rule 6, can
   reach them.
2. **Duties travel in the access token** (`duties` claim, mapped to `X-Depot-Duties`). Each route enforces the duty
   named in its `x-depot-duty` with `authz.RequireDuty`. The `authz.Policy` interface stays the seam for a later OPA
   decision point; no service branches on role names.
3. **OPA is the policy engine where a full decision is needed**: identity's administrative and SCIM routes, and the
   built-in gateway of single-host installs (devgateway, `depot-server`). No ext_authz and no per-service OPA
   sidecar in Kubernetes for v1.0.
4. **Legal-entity scope** comes from the `le` claim (`X-Depot-Legal-Entity`), can be narrowed by the web shell's
   `X-Depot-LegalEntity`, and is widened to all entities only on reads that send `X-Depot-LegalEntity-Scope: ALL`
   by a caller holding `org.switcher.all-scope` (`tenant.go`; architecture §7.3 rule 3a).
5. **Revocation latency is the access-token life (300 s).** The designed ≤ 10 s is withdrawn for v1.0. Step-up uses
   the auth-age check on `X-Depot-Auth-Time`, not an OPA `requires_acr` obligation.
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
- No per-request authorization decision is logged centrally.
- Backlog: add a default-deny NetworkPolicy and gateway-only (plus generated peer allow-list) ingress to every service chart and namespace, and a CI check that fails when a chart lacks it (D1).
- Backlog: decide per environment whether `DEPOT_GATEWAY_SECRET` is also required in Kubernetes, and if so add it to the Envoy SecurityPolicy and every chart (D1).
- Backlog: move peer calls that forward user headers (receiving, payables, party, product) to signed service tokens, so peer ingress can be narrowed to the gateway only (D1).
- Backlog: make Envoy, devgateway and `depot-server` enforce one model, either all with OPA or all with duty headers only, and test the three against the same route table (D23).
- Backlog: forward the web shell's `X-Depot-LegalEntity` selection through Envoy as devgateway and `depot-server` do (today `ClientTrafficPolicy/depot-api-strip-identity-headers` removes it), with a test that it can only narrow the token's `le` claim (D23).
- Backlog: add an authorization-denial metric (`depot_authz_denied_total`) in `authz.Require` and alert on it (auditability gap 3).
- Backlog: document the 300 s revocation window in the operator and security guides, and give tenant admins a session-revocation action that also ends Keycloak sessions.
