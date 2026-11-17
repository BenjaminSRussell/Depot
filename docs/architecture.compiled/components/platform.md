# Platform

**As built (v1.0):** the designed `platform` (Go) and `platform-ts` deployables are split into twelve Go capability
services plus the one Temporal worker `workflow-ts` (ADR-0008). There is no `/v1/platform/*` API (0 of 104 designed
platform endpoints exist); each service has its own prefix.

## Purpose & Boundaries
Tenants and users, roles, duties and SoD, entity metadata, number sequences, audit, batch jobs and notifications,
organization structure (legal entities, sites, calendars), compliance and privacy, operations, country packs, search,
printing and the release probe. Period control belongs to ledger (close), calendars to org.

## Services
| Directory | Lang | Role | Outbox relayed |
|---|---|---|---|
| `services/identity` | Go | users, tenants, legal entities, roles and duty assignments (`/v1/identity`, `/v1/authz`, `/v1/tenants`), signed OPA bundle, SCIM | no |
| `services/metadata` | Go | entity metadata registry and generated data API (`/v1/metadata`, `/v1/data/{entity}`); code generator `cmd/entitygen` | yes |
| `services/numbering` | Go | number sequences allocated over HTTP (`/v1/numbering/sequences`, ADR-0015) | no |
| `services/audit` | Go | hash-chained change log fed over HTTP, PHI read log (`/v1/audit`) | no |
| `services/batch` | Go | batch jobs and runs, notifications, inbox, workflow definitions and instances, event catalogue (`/v1/batch`, `/v1/notify`, `/v1/inbox`, `/v1/workflows`, `/v1/events`) | yes |
| `services/org` | Go | legal entities, operating units, sites, warehouses, hierarchies, fiscal calendars, addresses, currencies, UoM, demo company (`/v1/org`) | no |
| `services/compliance` | Go | SoD rules, simulation and campaigns, privacy requests, holds and erasure, audit policies, security incidents, trade screening (`/v1/sod`, `/v1/privacy`, `/v1/compliance`, `/v1/security`, `/v1/trade`) | no |
| `services/ops` | Go | SLOs, licences, capacity, costs, failover, break-glass, region health `GET /healthz/region` (`/v1/ops`) | yes |
| `services/localization` | Go | country packs and statutory content from the repo (`/v1/localization`); no database | n/a |
| `services/search` | Go | global search over its own trigram index fed by events (`/v1/search`, ADR-0013) | n/a |
| `services/print` | Go | document templates, printers, routing rules (`/print/v1`, D22) | no |
| `services/platform-health` | Go | the `hello-ledger` release probe (`/v1/hello-ledger`, `/v1/platform/releases`) | no |
| `services/workflow-ts` | TS | the one Temporal worker: approvals, party merge, ECO, CAPA, tender, payment run, budget plan, agent runs; event-schema compatibility gate | n/a |

## Interfaces
- Per-service OpenAPI is authoritative (`contracts.json` lists every operation and duty). Route exceptions at the gateway: `inbox` → batch, `data` → metadata.
- Authorization data: duties and roles in identity; the bundle at `GET /v1/identity/policy/bundle` (internal listener). SoD: `/v1/sod/rules`, `/v1/sod/simulate`, `/v1/sod/evaluations` in compliance.
- Approvals: `workflow-ts` decision routes (`/v1/workflows/*`, architecture §7.2); services answer `approval_required` where wired. No `/v1/platform/workflow-instances`, `/v1/platform/tasks` or `/v1/platform/jobs`.
- Numbering: hr and party allocate numbers from `services/numbering` over HTTP; fiscal numbers stay in the posting services until gap-free reserve/confirm/void exists (ADR-0015, D21).
- Audit: `POST /v1/audit/changes` (only engineering posts), `/v1/audit/reads` (PHI read log written by hr).

## Data Ownership
Each stateful service has its own database (`depot_identity`, `depot_metadata`, `depot_numbering`, `depot_audit`,
`depot_batch`, `depot_org`, `depot_compliance`, `depot_ops`, `depot_search`, `depot_print`, ...). Audit rows are
append-only and hash-chained (trigger rejects updates and deletes). compliance seals sensitive fields with
`fieldcrypt`. Designed EN-001..012 (tenant, app_user, role/duty/privilege, entity_definition, number_sequence,
audit_entry with monthly partitions, legal_entity, fiscal_calendar) is the target shape; most of it now lives across
identity, metadata, numbering, audit and org.

## Events Produced
Topics are per service (`depot.metadata.entity`, `depot.org.*`, `depot.privacy.*`, `depot.sod.*`, `depot.batch.run`,
`depot.notify.message`, `depot.workflow.*`, `depot.ops.*`, `depot.numbering.sequence`, `depot.audit.change`,
`depot.print.job`, `depot.trade.*`); the full list is in `contracts.json`. Only batch, metadata and ops are relayed to
Kafka; identity, numbering, audit, org, compliance, print and platform-health write an outbox nobody relays (D2).
*Target (D6):* `platform.workflowtask.completed.v1` with the designed payload, `platform.entitydefinition.changed.v1`,
`platform.legalentity.created.v1`, `platform.numbersequence.changed.v1`.

## Events Consumed
`workflow-ts` consumes `depot.platform.entitydefinition` (no AsyncAPI declares it; metadata publishes
`depot.metadata.entity` — a dead subscription), `depot.tms.load` and `depot.tms.tender`. search consumes domain events
(not declared in AsyncAPI).

## Dependencies
internal: platform-kernel, edge-identity, eventing-backbone. external: Keycloak admin API (identity), Temporal
(`workflow-ts`, own Postgres in `deploy/temporal-postgres`), Kafka, object storage.

## Gotchas & Constraints
- Platform has no single API or database any more; "platform" is a grouping (ADR-0008).
- Number allocation is a synchronous peer call; an HTTP allocation can burn a gap-free number (D21).
- The audit service is not fed from CDC, so most changes are not in the chain (D3); eleven services keep their own audit tables.
- workflow-ts copies its own service-token and helper code (`services/workflow-ts/src/serviceToken.ts`); no TS kernel.
- `services/platform-health` is the only workload prod deploys (as `hello-ledger`, D4).

## Target — not built
- CDC-fed, hash-chained audit trail with `depot.ctx` actor and trace, head anchoring to object-locked storage (D3).
- Shared long-running-work contract `/v1/platform/jobs` with `statusUrl`, and a live-update stream for the shell (D29).
- Relay for every platform outbox (D2); legal-entity policies in audit (D17); gap-free numbering protocol (D21).
- Designed v0.x API under `/v1/platform/*` (tenants, users, SCIM, policy bundles, entity definitions, tasks, jobs, attachments, privacy requests), superseded by the per-service APIs above.

## Source Sections
- Architecture §3.2 inventory (platform row), §5.5 platform endpoints (target), §7.2 authorization, §7.10 long-running work, §7.16 audit and PII, §7.18 data layer; data EN-001..012; events §3.2.
- ADR-0006, ADR-0008, ADR-0013, ADR-0015; delta §1.1 (platform services), §2.1 (platform row).
