# Edge and Identity

**As built (v1.0):** Envoy Gateway validates the JWT and maps claims to trusted `X-Depot-*` headers (ADR-0006); one
Keycloak realm `depot` (ADR-0007); duties travel in the access token; OPA decides only in identity and the single-host
gateways; Ed25519 service tokens between services (ADR-0010); Valkey holds gateway rate-limit counters (ADR-0014).

## Purpose & Boundaries
Authenticates every caller at the edge and turns the token into headers services can trust. Business authorization
stays in the service (route duty check). Config lives in `deploy/gateway`, `deploy/envoy-gateway`, `deploy/keycloak`
(realm `deploy/keycloak/realm-depot.json`), `deploy/keycloak-fips` and the duty catalogue and Rego in `policies/authz`.
The `identity` service (platform component) serves the policy bundle, SCIM and administration.

## Services
None of its own. Edge config is in `deploy/`; the identity service is owned by the platform component
(`services/identity`), the portal BFFs by portal-bff.

## Interfaces
- Gateways (`deploy/gateway/README.md`): `depot-api` for `/api/v1/<svc>/...` (browser, rewritten to `/v1/<svc>`) and `/v1/<svc>/...` (partners, agents); `depot-portals` for the two portal hosts; separate gateways for AS2, region health, the plant edge (iot-edge) and the payments webhook; MCP at `POST /mcp` on `mcp.depot.<tenant>`.
- `ClientTrafficPolicy/depot-api-strip-identity-headers` removes client-supplied identity headers first (only `X-Depot-LegalEntity-Scope` passes, for the read-only all-entity view). `SecurityPolicy/depot-api-jwt` then validates issuer, audience `depot-api`, signature via JWKS and expiry, refuses the nil tenant, and maps `tid`, `le`, `sub`, `duties`, `tier` to `X-Depot-Tenant`, `X-Depot-Legal-Entity`, `X-Depot-User`, `X-Depot-Duties`, `X-Depot-Tier`. Envoy also strips the shell's `X-Depot-LegalEntity` selection that devgateway and `depot-server` forward (D23).
- Rate limits: `BackendTrafficPolicy/depot-api-tenant-rate-limit`, one global limit per tenant by tier (starter 600/min, professional 3,000, enterprise 12,000), counters in Valkey; per-address limits on the AS2, region and portal edges. Designed bursts, `azp` and `device` classes and per-agent caps are not configured; services do not set `RateLimit-*` headers.
- Keycloak realm `depot`: clients `depot-web` (public, PKCE S256), `depot-services`, `depot-identity-deprovision`, `depot-portal-supplier`, `depot-portal-customer`; every token carries `aud: depot-api`; access tokens 300 s, refresh rotation with reuse 0. Device grant disabled; no per-kiosk clients (ADR-0009).
- OPA: identity compiles and signs a bundle (Rego, duties, tenant-wide role assignments) and serves it at `GET /v1/identity/policy/bundle` on an internal listener; identity's `RequireOPA` returns 503 on a policy outage. Decision shape `allow` only, Rego `package authz`.

## Data Ownership
Keycloak state (realm, users, the `duties` user attribute), the duty catalogue in `policies/authz`, rate-limit counters
in Valkey (never P3, never a system of record). Identity's own tables belong to the platform component.

## Events Produced
None. Kill-switch and role changes reach services through the next token or bundle, not through events.

## Events Consumed
None.

## Dependencies
internal: platform (identity: bundle, SCIM, tenants), platform-kernel (`authz`, `tenant`). external: Envoy Gateway,
Keycloak, OPA (identity and single-host gateways), Valkey, cert-manager, External Secrets.

## Gotchas & Constraints
- Services trust `X-Depot-*` headers without re-verifying the JWT. That is safe only if nothing but the gateway can reach a service (ADR-0006 rule 6); today only 13 of 69 charts ship a NetworkPolicy, no namespace has default-deny and 63 services share namespace `hello-ledger` (D1).
- Envoy, devgateway and `depot-server` enforce different checks (OPA only in the single-host gateways) (D23).
- Duties in the token mean a role change takes effect on the next token (≤ 300 s). Step-up is an auth-age check, not an `acr` obligation.
- Valkey, never Redis; rate limiting is a soft dependency (ADR-0014).
- OPA decision logs are off and there is no central denial metric (ADR-0006 backlog).

## Target — not built
- Default-deny NetworkPolicy, namespace per tier and per-deployable egress allow-lists (D1).
- One enforcement model across Envoy, devgateway and `depot-server` (D23).
- Designed v0.x items superseded by ADR-0006/0007/0010: realm per tenant plus `_internal` realm, ext_authz to OPA-Envoy, OPA sidecars with `{allow, legalEntityScopes, obligations}`, `requires_acr: mfa` step-up, per-service JWT re-verification, `X-Depot-Subject-Token`.
- Agent on-behalf-of token exchange (open question).

## Source Sections
- Architecture §3.2 route table, §7.1 authentication, §7.2 authorization, §7.4 request path, §7.17 rate limiting; Cross-Cutting Security & Auth; frontend §8.
- ADR-0006, ADR-0007, ADR-0009, ADR-0010, ADR-0014; delta §2.1 (edge-identity row), §3.
