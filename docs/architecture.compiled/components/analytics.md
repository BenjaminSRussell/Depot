# Analytics

**As built (v1.0):** `services/analytics` (Go) in place of the designed `analytics-ts` (ADR-0008), plus the `data/`
assets (CDC connector template, dbt project, Cube model). The lakehouse (Iceberg, Trino or ClickHouse) is not deployed
and no table-CDC connector exists (D3).

## Purpose & Boundaries
Semantic query API, metrics, dashboards, saved queries, schedules and exports, data-quality rules and lineage, archive
partitions, and UX telemetry (events, funnels, signals).

## Services
| Directory | Lang | Prefix | Outbox relayed |
|---|---|---|---|
| `services/analytics` | Go | `/v1/analytics`, `/v1/catalog`, `/v1/archive` | no (D2) |

## Interfaces
- `/v1/analytics/query` and `query:execute` (read-only POST, honoured for the all-entity read view), `metrics`, `dashboards`, `saved-queries`, `schedules`, `exports`, `reconciliation`, `ux-events`, `ux-funnels`, `ux-signals`.
- `/v1/catalog/dq-rules`, `/v1/catalog/lineage`; `/v1/archive/partitions`.
- Cube model in `data/cube` (tested by `data/cube/cube.test.js` in CI); dbt project in `data/dbt`; CDC template `data/cdc/connector.template.json`.

## Data Ownership
Operational tables only: dashboards, saved queries, schedules, DQ rules, lineage edges, archive state, UX telemetry.
The analytical store is not deployed.

## Events Produced
`depot.analytics.metric`, `depot.analytics.report`, `depot.catalog.dq`, `depot.archive.partition` (not relayed).

## Events Consumed
None declared. The designed lakehouse feed is table CDC, not built.

## Dependencies
internal: platform, every context (as data sources, once CDC exists), eventing-backbone. external: Cube, dbt,
Iceberg/Trino/ClickHouse (target).

## Gotchas & Constraints
- P3 data must never reach the lakehouse or exports (designed rule; enforced by `fieldcrypt` ciphertext today).
- Export auditing metric `depot_export_rows_total` is not built (pattern `audited-export`).

## Target — not built
- Lakehouse deployment, table-CDC connectors, dbt runs and Cube serving (D3).
- Relay for the analytics outbox (D2).
- Designed `analytics-ts`: superseded by ADR-0008.

## Source Sections
- Architecture §5.18 analytics endpoints (target), data §11 CQRS, read models and the lakehouse, events §10 lakehouse CDC.
- ADR-0008; delta §1.1 (`services/analytics`), §2.1 (analytics row).
