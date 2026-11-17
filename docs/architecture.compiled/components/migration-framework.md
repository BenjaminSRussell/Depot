# Migration Framework

**As built (v1.0):** `services/migration` (Go, own database, API under `/v1/integration/*`) and
`tools/migration-factory` (Go, chart `depot-migration-factory`, API under `/api/v1/migration` and `/api/v1/cutover`).
Architecture §3.2 groups `services/migration` under the integration context; this component owns it because it is the
migration pipeline the design put in the integration database.

## Purpose & Boundaries
AX 2012 / D365 and SAP ECC / S/4HANA migration: projects, connection profiles, data templates, mapping specs, lookup
tables, entity bindings, cross-references (xref), migration runs, sync status and conflicts, parallel-run diffs and
configuration packages (migration); packs, opening targets, tolerances, coexistence, parallel runs and cutover plans
(migration-factory).

## Services
| Directory | Lang | Prefix | Outbox |
|---|---|---|---|
| `services/migration` | Go | `/v1/integration` | outbox table, no connector |
| `tools/migration-factory` | Go | `/api/v1/migration`, `/api/v1/cutover` | no connector |

## Interfaces
- migration: `/v1/integration/projects`, `connection-profiles` (`:test`), `data-templates`, `mapping-specs`, `lookup-tables` (`:import`), `entity-bindings` (`:preview`), `xref` (`:record`, `:resolve`), `migration-runs` (`:rerun`), `sync-status`, `sync-conflicts` (`:resolve`), `parallel-run-diffs`, `config-packages` (`:apply`, `:diff`), `outbound-mappings`.
- migration is the only service that returns a `statusUrl` for long-running work (D29).
- Cutover opening-balance imports are served by each owning service; the shared batch store and envelope checks are `platform/go/openingimport`.

## Data Ownership
Migration projects, mappings, xref, run logs and control totals (migration, `fieldcrypt` for sensitive staged fields,
ADR-0011); packs, tolerances and cutover plans (migration-factory).

## Events Produced
`depot.migration.syncrun`, `depot.migration.syncabsent` (migration); `depot.migration.pack`,
`depot.migration.parallelrun`, `depot.migration.tolerance`, `depot.migration.coexistence`, `depot.cutover.plan`,
`depot.cutover.step`, `depot.cutover.decision` (migration-factory).

## Events Consumed
None declared.

## Dependencies
internal: integration (connectors), platform, ledger (opening entries), every target context (opening imports).
external: AX and SAP source systems (extraction method undecided).

## Gotchas & Constraints
- The closed-period check must pass on migration postings too (ledger invariant).
- Migration opening entries are entry type `migration_opening` and must be excluded from reconciliation movement terms where the design says so (R4-1 open).
- Control totals must reconcile per entity (NFR-D6).

## Target — not built
- Relay for the migration outbox (D2); shared long-running-work contract (D29).
- Designed pipeline in the `integration` database (`migration` schema): superseded by the own database (ADR-0008).

## Source Sections
- Architecture data §13 data migration from AX and SAP, §5.17 integration endpoints (target).
- ADR-0008, ADR-0011; delta §1.1 (`services/migration`), §1.2 (`tools/migration-factory`), §2.1 (migration-framework row).
