# web-bff

**Not built (ADR-0009).** There is no web BFF and no service directory for one. The web shell (`apps/web`) is a public
OIDC client (`depot-web`, authorization code with PKCE S256) that keeps its access and refresh tokens in JavaScript
memory and sends them as bearer tokens to the same-origin API gateway, which validates them (ADR-0006). The component
stays in the manifest so the decision, its conditions and its triggers have a home.

## Purpose & Boundaries
Designed (v0.x): a cookie-session token-handler BFF for the shell, renamed from `bff-ts`, at `/bff/...`, keeping OAuth
tokens out of the browser, plus server-side composition (`/bff/v1/me`, `/bff/v1/permissions`, `/bff/v1/stream`,
`/bff/v1/search`) and a read-only GraphQL gateway. As built none of this exists; the shell talks to Envoy directly.

## Services
None. The designed directory, services/web-bff-ts, does not exist. The pattern lives on in the two portal BFFs
(portal-bff component).

## Interfaces
- As built: no `/bff/...` routes for the shell. The shell calls `/api/v1/<svc>/...` on Gateway `depot-api` with `Authorization: Bearer` added by `installBearerFetch` (`apps/web/src/shell/auth/auth.ts`); Envoy rewrites to `/v1/<svc>/...`.
- Navigation is hidden by the user's duties in the shell (`apps/web/src/shell/dutyGate.ts`), not by `/bff/v1/permissions`; services still enforce.
- Designed and superseded: cookie `__Host-depot_sid` (`HttpOnly; Secure; SameSite=Strict`), header `X-Depot-CSRF`, `/bff/login`, `/bff/callback`, server-sent events at `/bff/v1/stream`, GraphQL read gateway.

## Data Ownership
None. (Designed: sessions in the platform database.)

## Events Produced
None.

## Events Consumed
None. (Designed: the stream fan-out of domain events to the shell.)

## Dependencies
Would depend on edge-identity (Keycloak confidential client) and platform. Today the shell depends on edge-identity
directly.

## Gotchas & Constraints
- ADR-0009 is accepted with conditions that must hold before `apps/web` serves real users: a strict CSP (`script-src 'self'`, `connect-src` limited to the API origin and the Keycloak issuer, `frame-ancestors 'none'`, Trusted Types), no tokens in web storage (the PKCE verifier excepted), access tokens of 5 minutes or less with refresh rotation and reuse detection, and first-party scripts only. None is enforced yet: no image or host serves `apps/web` with these headers, and no test checks web storage (D7).
- Triggers that make a web BFF required: code the shell does not build itself (remote module federation, tenant extensions, third-party widgets); a host where a strict CSP is impossible; tokens that must outlive the page or live longer than 5 minutes; a customer, auditor or certification that forbids tokens reachable from JavaScript; or server-side composition being built anyway.
- Any script running in the shell's origin can use the in-memory token with the user's duties for the token's life (the accepted risk).

## Target — not built
- ADR-0009 conditions and their CI checks (D7).
- A live-update channel for long-running work without `/bff/v1/stream` (D29, end-user gap 3).
- Removing remaining "web BFF" references from `apps/technician/README.md` and `services/fieldservice/NOT-IMPLEMENTED.md` (ADR-0009 backlog).
- The BFF itself only when a trigger above becomes true.

## Source Sections
- Architecture §3.2 route table, frontend §2.4, §6 live updates, §8 authentication and session.
- ADR-0006, ADR-0009; delta §2.1 (web-bff row: missing), §2.2 (`bff-session-cookies`).
