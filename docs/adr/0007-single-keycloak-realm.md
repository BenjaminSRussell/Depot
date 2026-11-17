# ADR-0007: One Keycloak realm for all tenants, tenant as a user attribute

**Status:** Accepted — 2026-11-17 (records a decision already made in code; SDLC run 1, stage 2, deviation D16)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` §3 row 4, §4 D16, §5 C3; `docs/sdlc/run-1/architecture-review-response.md` R-02, R-08, R-15

## Context

The architecture (§7.1, frontend §8.1) designed one Keycloak realm per tenant plus an `_internal` realm for service
identities, with the tenant fixed per realm in a `tenant_id` claim and tenant realms created by
`POST /v1/platform/tenants` from a realm template.

The code ships one realm, `depot` (`deploy/keycloak/realm-depot.json`):

- Clients: `depot-web` (public, PKCE S256; see ADR-0009), `depot-services`, `depot-identity-deprovision`,
  `depot-portal-supplier` and `depot-portal-customer` (confidential, used by the portal BFFs).
- Claims from user attributes: `tid` (tenant), `le` (one legal entity today, a single-string attribute; see ADR-0006
  rule 4), `duties` and `tier` on `depot-web` and `depot-services` tokens; `tid`, `le`, realm roles and `vendor_id` /
  `customer_id` on the portal clients' tokens. Only `depot-web` and `depot-services` have the audience mapper that
  adds `aud: depot-api`; the portal clients and `depot-identity-deprovision` have none, because their tokens are not
  presented to Envoy.
- The deploy files do not yet point at this realm: Envoy's SecurityPolicy names issuer `…/realms/dev` with a static
  placeholder key, and agent-runtime uses `realms/dev` and `realms/stage`; only the portals use `realms/depot` (D32).
- Tenants are rows in `services/identity` (`POST /v1/tenants`), not realms. Envoy maps `tid` to `X-Depot-Tenant`
  and refuses the nil tenant (ADR-0006).
- There is no `_internal` realm. Service identity is an Ed25519 service token (ADR-0010).

## Decision

One realm, `depot`, for every tenant in an installation. The tenant is the `tid` user attribute, mapped into the
token; legal entities are `le`; duties are `duties` (ADR-0006). Claim names are `tid` and `le`, not `tenant_id`.
Customer identity providers are federated into the one realm (OIDC/SAML brokering), with an IdP mapper that sets
`tid` for users of that IdP. Tenant creation is an identity-service operation, not a realm import.

## Alternatives rejected

- **Realm per tenant** (the design). Gives per-tenant password, MFA and session policy and a hard isolation boundary in
  Keycloak, at the cost of a realm per tenant to provision, upgrade and back up, and an issuer registry in every
  verifier. Not built. Remains the option for a tenant that contractually needs its own IdP policy, through a
  dedicated installation.

## Consequences

- One issuer, one JWKS, one audience per installation is the decision: Envoy, devgateway, `depot-server` and
  agents-ts each verify against the `depot` realm issuer. As built, Envoy and agent-runtime are configured for other
  realm names and Envoy for a placeholder key, so this holds only on single-host installs until D32 is closed.
- Tenant isolation in identity rests on the `tid` attribute being set only by administrators and IdP mappers. A
  wrong attribute value gives a user another tenant's scope. The attribute must not be user-editable.
- Password, MFA and session lifetimes are realm-wide; tenants cannot differ.
- Backlog: add a realm-configuration test that fails if `tid`, `le` or `duties` are user-editable (user profile `edit` permission) or if `depot-web` or `depot-services` (the clients whose tokens reach Envoy) lacks the `depot-api` audience mapper (D16).
- Backlog: point Envoy (`remoteJWKS`) and agent-runtime at the `depot` realm issuer in every environment (D32).
- Backlog: document the per-tenant IdP federation procedure (IdP mapper that sets `tid`) in the operator guide, with a test realm export (D16).
- Backlog: correct the remaining `tenant_id` claim references in the PRD and runbooks to `tid` (D16).
- Backlog: if ADR-0006 rule 4's proposed model is confirmed, turn the `le` mapper into the list of entitled legal entities (D33).
