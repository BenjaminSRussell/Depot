# PWA Apps

**As built (v1.0):** four separate apps: `apps/warehouse-mobile` and `apps/technician` (device PWAs that re-export
flows from the web shell's modules), and `apps/customer-portal` and `apps/supplier-portal` (portal SPAs behind the
portal BFFs). The designed apps/shopfloor kiosk PWA does not exist; the kiosk is the shell module
`apps/web/src/modules/shopfloor`. None of the apps has an image yet.

## Purpose & Boundaries
Task-focused clients for people who are not at a desk: warehouse handhelds and scanners, field technicians, and
external customers and suppliers. They hold no business rules; offline work is queued and replayed against the owning
services.

## Services
| Directory | Kind | Backend |
|---|---|---|
| `apps/warehouse-mobile` | React 19 PWA; `src/App.tsx` re-exports `DeviceApp` from `apps/web/src/modules/mobile-flows` | `services/mobile` through Gateway `depot-api` |
| `apps/technician` | React 19 PWA | fieldservice and inventory truck stock through Gateway `depot-api` |
| `apps/customer-portal` | React 19 SPA | `services/customer-portal-ts` (cookie session) |
| `apps/supplier-portal` | React 19 SPA | `services/supplier-portal-ts` (cookie session) |

## Interfaces
- Offline queue (warehouse): the flow runner queues each finished flow as a command, sealed with AES-256-GCM under a non-extractable device key in IndexedDB (`apps/warehouse-mobile/README.md`), and replays it with statuses PENDING, APPLIED, CONFLICT, REJECTED (`apps/web/src/modules/mobile-flows/domain/offlineQueue.ts`). Published flows load from `GET /api/v1/mobile/menus/{id}` with SSE updates.
- The designed `QueuedCommand` (pattern `offline-queued-command`: `aggregateKey`, `actorSub`, `occurredAt`, `ifMatch`, `commandId`, `deviceId`) is 3 of 6 fields met (no `aggregateKey`, `actorSub`, `commandId`).
- Device sign-in: the designed Keycloak device flow is disabled (ADR-0009); `apps/technician/README.md` still describes a web BFF in front, which does not exist.
- Portals: `/bff/...` for session and `/api/v1/...` for data on the portal hosts (portal-bff component).

## Data Ownership
Browser only: IndexedDB command queues and device keys (warehouse-mobile, technician), service-worker app-shell cache
(`public/sw.js`), portal session cookies (set by the BFFs).

## Events Produced
None.

## Events Consumed
None (published flows arrive over SSE from mobile).

## Dependencies
internal: portal-bff, warehouse (mobile), projects (fieldservice), inventory (truck stock), web-shell (shared
modules), edge-identity. external: scanners (Zebra, Honeywell) through the browser.

## Gotchas & Constraints
- Counts are refused offline; inquiries queue nothing.
- Replay must be idempotent; without `commandId` the server dedupes by its own keys.
- Touch targets and one input per screen follow the frontend accessibility rules (§7).
- The portal SPAs have no host image; the portal Gateway would need a static backend (`deploy/gateway/README.md`).

## Target — not built
- Images and hosting for all four apps.
- The designed `QueuedCommand` fields and a device sign-in flow that survives long offline periods (Q18).
- Designed apps/shopfloor PWA: superseded by the shell module.

## Source Sections
- Architecture frontend §2 MFE topology (as built note), §5 routing, §7 accessibility, §7.6.1 kiosk, §9 implementation patterns.
- ADR-0009; delta §1.2 (apps), §2.1 (pwa-apps row), §2.2 (`offline-queued-command`).
