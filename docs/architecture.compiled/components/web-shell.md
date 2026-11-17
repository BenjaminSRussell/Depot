# Web Shell

**As built (v1.0):** one React 19 + TypeScript + Vite 8 shell at `apps/web` with in-tree modules under
`apps/web/src/modules`, discovered by the shell's registry. Module federation is still pending
(`apps/web/src/shell/README.md`). The shell is a public OIDC client holding tokens in memory, with no web BFF
(ADR-0009). There is no `packages/` directory and no `@depot/*` workspace package. No image or chart serves the shell
yet; it is published as a GitHub Pages demo (`.github/workflows/pages.yml`).

## Purpose & Boundaries
The application shell for every back-office user: sign-in, navigation by duty, legal-entity selection, command
palette, forms, lookups, inbox, help and telemetry, and the business modules (ledger, procurement, sales, warehouse,
shop floor, HR, ...). Employee self-service (`apps/ess`) and the shop-floor kiosk are shell modules, not separate
apps. The shell holds no business rules; services enforce.

## Services
| Directory | Kind | Notes |
|---|---|---|
| `apps/web` | TS / React 19 | shell under `apps/web/src/shell`, modules under `apps/web/src/modules` |
| `apps/ess` | README only | ESS ships as the HR shell module (`apps/web/src/modules/hr`), partly built |

## Interfaces
- Auth: public client `depot-web`, PKCE S256; the verifier is kept in `sessionStorage` for the redirect only; access and refresh tokens live in memory (`apps/web/src/shell/auth/auth.ts`); `installBearerFetch` adds the bearer token to same-origin `/api/` requests. A reload goes back through Keycloak SSO.
- API: `/api/v1/<svc>/...` on Gateway `depot-api`; the dev server proxies the same paths (`apps/web/src/shell/devProxy.ts`).
- Navigation and actions are shown or hidden by the token's duties (`apps/web/src/shell/dutyGate.ts`).
- Legal-entity selection sends `X-Depot-LegalEntity`, which Envoy strips while devgateway and `depot-server` forward it (D23).
- Telemetry: the shell emits a W3C `traceparent` (`apps/web/src/shell/telemetry.ts`) that services ignore (D8).
- Designed and superseded: 16 federation remotes, `@depot/forms` and other shared packages, `/bff/v1/me`, `/bff/v1/permissions`, `/bff/v1/stream`.

## Data Ownership
Browser only: `sessionStorage` for the PKCE step, `localStorage` for preferences. No tokens in web storage (an ADR-0009
condition, not yet tested, D7).

## Events Produced
None (UX telemetry goes to analytics over HTTP, `/v1/analytics/ux-events`).

## Events Consumed
None. Server-sent events exist only in agents-ts, fieldservice, mobile and shopfloor; there is no generic stream (D29).

## Dependencies
internal: edge-identity (Keycloak, Envoy), platform (metadata, identity, batch inbox), every service API. external:
Keycloak.

## Gotchas & Constraints
- ADR-0009 conditions before real users: strict CSP with Trusted Types and `frame-ancestors 'none'`, no tokens in web storage, ≤ 5-minute access tokens, first-party scripts only (D7). Loading a remote module from another origin is a trigger for a web BFF.
- No live-update stream: long-running work is polled per service (D29).
- WCAG 2.1 AA remains the accessibility target (frontend §7).

## Target — not built
- Image and host for `apps/web` with the CSP headers, tested in CI; a no-tokens-in-storage check (D7).
- Module federation remotes (pending); shared `@depot/*` packages (not planned as of v1.0).
- Live updates for long-running work (D29).

## Source Sections
- Architecture frontend §1 (as built note), §2 MFE topology, §3 component hierarchy, §4 shared dependencies, §6 live updates, §8 authentication and session.
- ADR-0009; delta §1.2 (`apps/web`, `apps/ess`), §2.1 (web-shell row).
