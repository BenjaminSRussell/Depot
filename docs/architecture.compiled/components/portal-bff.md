# portal-bff

**As built (v1.0):** two cookie-session BFFs, `services/customer-portal-ts` and `services/supplier-portal-ts`, instead of
one designed `portal-bff-ts`. They follow the token-handler pattern the design gave all three BFFs: the browser holds
only an `HttpOnly` session cookie, never an OAuth token (ADR-0009). They are the "edge" group of architecture §3.2.

## Purpose & Boundaries
Sign-in, session and CSRF handling for the customer and supplier portals, and a narrow portal API that calls the
owning services (sales, billing, receivables; purchasing, receiving, payables, party) with Ed25519 service tokens
(ADR-0010). No business logic of their own beyond scoping to the signed-in company.

## Services
| Directory | Lang | Session store | Outbox |
|---|---|---|---|
| `services/customer-portal-ts` | TS | Valkey when `SESSION_STORE_URL` is set, else pod memory (ADR-0014) | none |
| `services/supplier-portal-ts` | TS | Valkey (ADR-0014) | none |

## Interfaces
- Hosts `customer.<env>.depot.dev` and `supplier.<env>.depot.dev` on Gateway `depot-portals` (`deploy/gateway/README.md`): only `/bff/` and `/api/v1/` are routed; every `X-Depot-*` header is removed at the edge; CORS for the portal's own origin; 1,200 requests a minute per client address.
- customer-portal-ts: `/bff/login`, `/bff/callback`, `/bff/logout`, `/bff/me`; `/api/v1/orders`, `/api/v1/invoices`, `/api/v1/open-items`, `/api/v1/payments`, `/api/v1/returns`, `/api/v1/statement`, `/api/v1/products`, `/api/v1/accounts`, `/api/v1/companies`.
- supplier-portal-ts: `/bff/login`, `/bff/callback`, `/bff/logout`, `/bff/backchannel-logout`, `/bff/me`, `/bff/registration`, `/bff/prospects`, `/bff/invites/*`; `/api/v1/purchase-orders`, `/api/v1/asns`, `/api/v1/invoices`, `/api/v1/invoice-submissions`, `/api/v1/payments`, `/api/v1/documents`, `/api/v1/requests`.
- Session cookies: `__Host-depot_customer` and `__Host-depot_supplier`, `Path=/; HttpOnly; SameSite=Lax; Secure`; CSRF token header `x-csrf-token` plus an `Origin` check. The design (pattern `bff-session-cookies`) named one cookie `__Host-depot_sid`, the header `X-Depot-CSRF` and `SameSite=Strict`: drift.
- Keycloak clients `depot-portal-customer` and `depot-portal-supplier` (confidential) in realm `depot`.

## Data Ownership
Sessions and per-session rate-limit counters only (Valkey or memory). No database and no P3 at rest.

## Events Produced
None.

## Events Consumed
None.

## Dependencies
internal: sales, billing, receivables (customer); purchasing, receiving, payables, party (supplier); edge-identity
(Keycloak, gateway); pwa-apps (`apps/customer-portal`, `apps/supplier-portal`). external: Keycloak, Valkey.

## Gotchas & Constraints
- Each TS BFF copies its own problem-details, service-token and idempotency helpers; their problem shape differs from the kernel's (D11, ADR-0008 backlog).
- The portal SPAs are not served by any image yet; the Gateway would need a second backend for static files.
- The supplier BFF limits its public routes per client address and trusts `X-Forwarded-For` only from the Envoy pod range.
- No operation in the two BFF specs carries `x-depot-duty`: they are scoped by the session and the signed-in company, not by duty. `contracts.json` shows `duty: null` for each.

## Target — not built
- Persisted-query GraphQL read model for portals (designed); not scheduled.
- Image and hosting for the portal SPAs; WAF rules (not expressible in Envoy Gateway).
- A shared TS package or conformance tests for the BFF helpers (ADR-0008 backlog).

## Source Sections
- Architecture §3.2 route table (portals row), frontend §8.2 cookie pattern; Cross-Cutting Security & Auth.
- ADR-0009, ADR-0010, ADR-0014; delta §1.1 (customer-portal-ts, supplier-portal-ts), §2.1 (portal-bff row), §2.2 (`bff-session-cookies`).
