# Warehouse

**As built (v1.0):** three Go services, the TypeScript `services/carrier-gateway-ts` and the Python
`services/routing-py` in place of the designed `warehouse`, `warehouse-ts` and `warehouse-py` (ADR-0008). Topics are
`depot.wms.*`, `depot.tms.*` and `depot.mobile.*`, not `depot.warehouse.*`. Warehouse devices use `services/mobile`;
there is no device BFF (ADR-0009).

## Purpose & Boundaries
Locations, location directives, work, receipts and shipments in the warehouse, dock appointments, pallets, RFID,
automation (warehouse); loads, load plans, routes, tenders, freight bills, carrier invoices, landed cost, gate, goods
in transit (transport); device sessions, menus, flows, scans and offline replay for handhelds (mobile); carrier rates,
labels and tracking (carrier-gateway-ts); vehicle routing (routing-py).

## Services
| Directory | Lang | Datastore | Outbox relayed |
|---|---|---|---|
| `services/warehouse` | Go | Postgres | yes |
| `services/transport` | Go | Postgres | yes |
| `services/mobile` | Go | Postgres | no (D2) |
| `services/carrier-gateway-ts` | TS | none | n/a (no AsyncAPI) |
| `services/routing-py` | Python | none (stateless; no chart, no OpenAPI) | n/a |

## Interfaces
- warehouse `/v1/warehouse`: locations, location-directives, work, receipts, shipments, dock-doors, dock-appointments, pallets, rfid-reads, rfid-rules, automation, gs1-settings.
- transport `/v1/transport`: loads, load-plans, routes, tenders, rate-cards, freight-bills, carrier-invoices, landed-cost, gate, goods-in-transit, voyages, tracking-events.
- mobile `/v1/mobile`: devices, sessions, menus, flows, scan, offline, events (server-sent events).
- carrier-gateway-ts `/v1/carrier`: carriers, rates, labels, tracking.
- Label printing goes through `services/print` (platform component, `/print/v1`).

## Data Ownership
Locations, work and directives (warehouse); loads, tenders, freight (transport); device sessions and offline command
logs (mobile; no legal-entity policy, D17). Designed EN for warehouse (data §9) is the target shape.

## Events Produced
`depot.wms.receipt`, `depot.wms.shipment`, `depot.wms.work` (warehouse); `depot.tms.*` (transport);
`depot.mobile.flow`, `depot.mobile.offline`, `depot.mobile.session` (not relayed). Full list in `contracts.json`.

## Events Consumed
None declared. `workflow-ts` consumes `depot.tms.load` and `depot.tms.tender` (tender workflow).

## Dependencies
internal: inventory (on-hand, reservations), sales and fulfillment (releases, shipments), procurement (receipts),
platform (print, org warehouses, workflow-ts). external: carrier APIs (carrier-gateway-ts), OR-Tools (routing-py).

## Gotchas & Constraints
- mobile posts with a non-standard posting key (D9).
- routing-py has no chart or Argo application; it is not deployable today (ADR-0008 backlog).
- Offline replay from handhelds must be idempotent per command; the designed `QueuedCommand` shape is only partly met (3 of 6 fields).

## Target — not built
- Relay for mobile (D2); legal-entity policy in mobile (D17); uniform posting key (D9).
- Chart and Argo application for routing-py, or a "not deployable" marker (ADR-0008 backlog).
- Designed `warehouse-ts`, `warehouse-py` and the device BFF's `/bff/v1/sync`: superseded by ADR-0008 and ADR-0009.

## Source Sections
- Architecture §3.2 (warehouse row), §5.13 warehouse endpoints (target); data §9 warehouse; events §3.9; frontend §2 (devices).
- ADR-0008, ADR-0009; delta §1.1 (warehouse services), §2.1 (warehouse and device-bff rows).
