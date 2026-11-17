# device-bff

**Not built (ADR-0009).** There is no device BFF and no service directory for one. Warehouse handhelds use
`services/mobile` (warehouse component) for device sessions and offline replay; shop-floor kiosk enrollment is in
`services/shopfloor` (manufacturing component); `apps/warehouse-mobile` and `apps/technician` keep IndexedDB command
queues. The component stays in the manifest to record where the designed responsibilities went.

## Purpose & Boundaries
Designed (v0.x): device enrollment, worker sessions (badge and PIN), offline packets and command sync for the
shop-floor kiosk, warehouse mobile and technician apps at `/bff/v1/sync`, `/bff/v1/packets/{kind}` and
`/bff/v1/device/*`, with an OAuth device grant. As built these are spread over mobile, shopfloor and the apps.

## Services
None. The designed directory, services/device-bff-ts, does not exist. Responsibilities moved to `services/mobile` and
`services/shopfloor`, which their own components own.

## Interfaces
- As built: mobile `/v1/mobile` (devices, sessions, menus, flows, scan, offline, events); shopfloor `/v1/shopfloor` (sessions, edge-nodes, registrations, server-sent events). Both are reached through Gateway `depot-api` with a bearer token.
- The Keycloak device grant is disabled in realm `depot`; there are no `depot-device` or per-kiosk clients (ADR-0009).
- Designed and superseded: `/bff/v1/sync`, `/bff/v1/packets/{kind}`, `/bff/v1/device/*`, `depot-kiosk` sessions.

## Data Ownership
None. Device sessions and offline logs live in mobile's database; kiosk enrollment in shopfloor's.

## Events Produced
None. (mobile publishes `depot.mobile.*`, not relayed.)

## Events Consumed
None.

## Dependencies
Would depend on edge-identity, warehouse, manufacturing and projects. As built those dependencies belong to mobile,
shopfloor and the apps.

## Gotchas & Constraints
- Token expiry during long offline periods is open (Q18); the apps replay queued commands after the user signs in again.
- The offline command shape only partly matches the designed `QueuedCommand` (3 of 6 fields: no `aggregateKey`, `actorSub`, `commandId`) (pattern `offline-queued-command`).
- mobile uses a non-standard posting key (D9) and has no legal-entity policy (D17).

## Target — not built
- Nothing is planned as a device BFF. Gaps that remain are in mobile and shopfloor (D2, D9, D17) and in the apps' offline queue shape.

## Source Sections
- Architecture §3.2 designed inventory (device-bff row) and route table, §5.20 device-bff endpoints (target), §7.1 machines and scanners row; frontend §2, §7.6.1 kiosk.
- ADR-0009; delta §2.1 (device-bff row: missing).
