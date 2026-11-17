# ADR-0009: The web shell is a public OIDC client holding tokens in memory (no web BFF)

**Status:** Accepted with conditions — 2026-11-17 (records a decision already made in code; SDLC run 1, stage 2, deviation D7)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` §2.1 (web-bff, device-bff), §3 row 16, §4 D7, §5 C2, C4

## Context

The architecture (§3.2, §7.1, frontend §2.4 and §8.2) designed three TypeScript token-handler BFFs: `web-bff`,
`device-bff` and `portal-bff`. The browser would hold only an opaque `HttpOnly; Secure; SameSite=Strict` session
cookie and never an OAuth token; the BFF would add `Authorization: Bearer` and also serve `/bff/v1/me`,
`/bff/v1/permissions`, `/bff/v1/stream` (server-sent events) and a GraphQL read gateway.

What is built:

- **Portals** follow the design. `customer-portal-ts` and `supplier-portal-ts` are cookie-session BFFs
  (`__Host-depot_customer`, `__Host-depot_supplier`), confidential Keycloak clients, sessions in Valkey (ADR-0014),
  behind their own Gateway `depot-portals` with CSRF and origin checks (`deploy/gateway/README.md`).
- **The web shell has no BFF.** `apps/web` uses the public client `depot-web` (PKCE S256, no client secret,
  `deploy/keycloak/realm-depot.json`). The PKCE verifier is kept in `sessionStorage` for the redirect step only; the
  access and refresh tokens live in JavaScript memory (`apps/web/src/shell/auth/auth.ts`: "Tokens live in memory
  only"). `installBearerFetch` adds the bearer token to same-origin `/api/` requests, which Envoy validates
  (ADR-0006). A reload goes back through Keycloak, whose SSO cookie returns the user at once.
- **No device BFF.** `services/mobile` (Go) handles warehouse device sessions and offline replay; shop-floor kiosk
  enrollment is in `services/shopfloor`. `apps/warehouse-mobile` and `apps/technician` keep an IndexedDB command queue.
- Access tokens live 300 s; refresh tokens rotate with reuse 0 (`revokeRefreshToken: true`).
- No Content-Security-Policy is configured for `apps/web` in the repository, and no image or chart serves `apps/web`
  yet (only the GitHub Pages demo, `.github/workflows/pages.yml`).

## Decision

For v1.0 the web shell stays a public OIDC client (authorization code with PKCE) that keeps its tokens in memory only
and sends them as bearer tokens to the same-origin API gateway. The portals keep their cookie BFFs. Warehouse and
technician devices use `services/mobile` and their own offline queues, not a device BFF.

This is accepted only with these controls in place before `apps/web` is served to real users:

1. A strict Content-Security-Policy on the host that serves the shell: `script-src 'self'` with no `unsafe-inline` or
   `unsafe-eval`, `connect-src` limited to the API origin and the Keycloak issuer, `frame-ancestors 'none'`, and
   Trusted Types for DOM sinks.
2. Tokens never written to `localStorage`, `sessionStorage` or IndexedDB (the PKCE verifier excepted), enforced by a
   test or lint rule.
3. Access-token life stays at 5 minutes or less, with refresh-token rotation and reuse detection.
4. Every script is first-party and built from this repository; no third-party tag or runtime-loaded remote from
   another origin.

**The risk.** Any script that runs in the shell's origin (an XSS bug, a compromised npm dependency) can read the
in-memory tokens and call every API the user may call, with the user's duties (ADR-0006 puts them in the token), for
the life of the access token, and for as long as it can keep refreshing. A cookie BFF limits such an attacker to
acting through the open page.

**When a web BFF becomes required.** Build `web-bff` (or move the shell behind the portal BFF pattern) when any of
these becomes true:

- the shell loads code it does not build itself: module-federation remotes from another origin, tenant extensions,
  or third-party analytics or support widgets;
- the controls above cannot be met on a target host (for example a strict CSP is not possible);
- tokens must outlive the page (offline use of the shell, or access tokens longer than 5 minutes);
- a customer, auditor or certification requires that tokens are not reachable from JavaScript;
- server-side composition is built anyway (the designed `/bff/v1/me`, `/bff/v1/permissions`, `/bff/v1/stream` or
  GraphQL read gateway).

## Alternatives rejected

- **Build `web-bff` now.** It adds a stateful service and a session store on the hot path of every request before any
  of the triggers above holds. The portal BFFs show the pattern and can be reused when needed.

## Consequences

- No BFF hop, no session store for the shell, and the same bearer path for the shell, partners and agents.
- Web modules call several services per page directly through Envoy (no composition layer), and there is no generic
  live-update stream (U4) for the shell.
- Backlog: serve `apps/web` from an image and chart with the CSP, `frame-ancestors` and Trusted Types headers above, and test the headers in CI (D7).
- Backlog: add a test or lint rule that fails if the shell writes a token to web storage (D7).
- Backlog: remove the remaining "web BFF" and "device-bff" references from `apps/technician/README.md`, `services/fieldservice/NOT-IMPLEMENTED.md` and other READMEs (D7).
- Backlog: decide how the shell gets live updates for long-running work without `/bff/v1/stream` (end-user gap 3).
- Backlog: re-evaluate this ADR whenever a trigger above becomes true, at the latest before module federation from another origin ships.
