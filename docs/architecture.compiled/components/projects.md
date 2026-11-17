# Projects

**As built (v1.0):** three Go services, `services/projects`, `services/fieldservice` and `services/eam`, plus the
stateless `services/dispatch-py`, in place of the designed `projects-ts` and `projects-py` (ADR-0008). All three Go
outboxes are relayed. dispatch-py has no chart and is marked not implemented.

## Purpose & Boundaries
Projects and WBS, project transactions, change orders, invoice proposals, WIP and project postings (projects); service
agreements, cases, work orders, SLAs, technicians, dispatch, knowledge articles, supplier claims (fieldservice, prefix
`/v1/service` plus `/v1/cases`); enterprise asset management: functional locations, assets, counters, plans, permits,
rounds, maintenance work orders (eam); dispatch optimization (dispatch-py).

## Services
| Directory | Lang | Prefix | Outbox relayed |
|---|---|---|---|
| `services/projects` | Go | `/v1/projects` | yes |
| `services/fieldservice` | Go | `/v1/service`, `/v1/cases` | yes |
| `services/eam` | Go | `/v1/eam` | yes |
| `services/dispatch-py` | Python | none (stateless; no chart, no OpenAPI) | n/a |

## Interfaces
- projects: projects, transactions, change-orders, invoice-proposals, categories, groups, posting-profiles, gl-postings, gl-reconciliation.
- fieldservice: agreements, cases, work-orders, activities, dispatch, technicians, service-objects, sla, sla-profiles, knowledge, supplier-claims, kpis; server-sent events for technicians.
- eam: assets, functional-locations, counters, plans, requests, work-orders, permits, permit-types, rounds, fault-catalogs, kpis, gl-postings.
- The technician app (`apps/technician`) calls fieldservice and inventory truck stock; its README still assumes a web BFF in front, which does not exist (ADR-0009). It keeps an IndexedDB offline queue.

## Data Ownership
Projects, WBS, project transactions (projects); agreements, cases, work orders (fieldservice); asset register,
maintenance plans and work orders (eam). Designed EN for projects (data §9) is the target shape.

## Events Produced
`depot.projects.*`, `depot.service.*` (including the four-segment `depot.service.workorder.completed`, consumed by
inventory), `depot.case.stage`, `depot.knowledge.article`, `depot.eam.*`; all relayed.

## Events Consumed
None declared.

## Dependencies
internal: ledger (`ledgerclient` for WIP and project postings), sales (billing), workforce (time, technicians),
inventory (parts consumption), manufacturing (asset downtime), platform. external: OR-Tools (dispatch-py).

## Gotchas & Constraints
- `apps/technician/README.md` and `services/fieldservice/NOT-IMPLEMENTED.md` still mention a web or device BFF; ADR-0009 lists their cleanup as backlog.
- dispatch-py has no chart or Argo application (ADR-0008 backlog).

## Target — not built
- Chart for dispatch-py or a "not deployable" marker (ADR-0008 backlog).
- Designed `projects-ts` and `projects-py`: superseded by ADR-0008.

## Source Sections
- Architecture §3.2 (projects row), §3.4 project WIP row, §5.16 projects endpoints (target); data §9 projects; events §3.9.
- ADR-0008, ADR-0009; delta §1.1 (projects, fieldservice, eam, dispatch-py), §2.1 (projects row).
