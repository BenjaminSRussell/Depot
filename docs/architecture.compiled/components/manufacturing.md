# Manufacturing

**As built (v1.0):** seven Go services and two Python solvers in place of the designed `manufacturing`,
`manufacturing-ts` and `manufacturing-py` (ADR-0008). Only production's outbox is relayed. The production-order
settlement saga S3b is not built (D13).

## Purpose & Boundaries
Production orders, WIP, report-as-finished, variances and WIP reconciliation (production); MRP, DDMRP, forecasts,
planned orders and scenarios (planning); finite scheduling, campaigns, bottlenecks, Gantt and dispatch lists
(scheduling); shop-floor registrations, workers, clocking, machine events, OEE, andon, pay agreements and kiosk
sessions (shopfloor); kanban and flows (lean); process manufacturing: batches, formulas, potency, catch weight,
co-products (processmfg, routed as `/v1/production` too); OPC-UA/MQTT edge buffering (iot-edge); CP-SAT scheduling
(scheduler-py) and statistical forecasting (forecast-py).

## Services
| Directory | Lang | Datastore | Outbox relayed |
|---|---|---|---|
| `services/production` | Go | Postgres | yes |
| `services/planning` | Go | Postgres | no (D2) |
| `services/scheduling` | Go | Postgres | no |
| `services/shopfloor` | Go | Postgres | no |
| `services/lean` | Go | Postgres | no |
| `services/processmfg` | Go | Postgres | no |
| `services/iot-edge` | Go | none (edge buffer, own plant-edge Gateway, no OpenAPI) | n/a |
| `services/scheduler-py` | Python | none (stateless CP-SAT solver, chart `scheduler-opt`) | n/a |
| `services/forecast-py` | Python | none (stateless) | n/a |

## Interfaces
- production `/v1/production`: orders, operations, journals, material-requirements, as-built, wip-balances, wip-reconciliation, variances, cost-snapshots, gl-postings, subcontract-operations, kpi.
- planning `/v1/planning`: plan-runs, plans, planned-orders, net-requirements, item-coverages, coverage-groups, ddmrp, forecasts, scenarios, messages.
- scheduling `/v1/scheduling`; shopfloor `/v1/shopfloor` (also kiosk enrollment, edge-nodes, server-sent events); lean `/v1/lean`; processmfg `/v1/production` (batches, formulas, batch-orders, rework-orders).
- Python solvers are called synchronously by their owning Go service and have no OpenAPI document.
- Posting: production posts WIP relief, labor and overhead through `ledgerclient`; planning uses a non-standard posting key (D9).

## Data Ownership
Production orders with BOM and route snapshots, WIP balances and variances (production); plans and planned orders
(planning); schedules and capacity (scheduling); registrations, worker days and pay data (shopfloor, which also keeps a
hash-chained audit table); kanbans (lean); batches and formulas (processmfg). shopfloor and scheduling have no
legal-entity policy (D17). Designed EN for manufacturing (data §9) is the target shape.

## Events Produced
`depot.production.*` (production and processmfg), `depot.planning.*`, `depot.scheduling.*`, `depot.shopfloor.*`
(including hyphenated `depot.shopfloor.registration-corrected` and `control-recipe`), `depot.lean.*`. Only production
is relayed. *Target (D6):* `manufacturing.productionorder.reportedasfinished.v1` with the designed payload.

## Events Consumed
production receives `depot.planning.plannedorder`, `depot.shopfloor.registration` and
`depot.shopfloor.registration-corrected` — all dead subscriptions because planning and shopfloor are not relayed (D2).
shopfloor receives `depot.production.operation`. scheduling has consumer code (`deploy/kafka/scheduling-consumer.yaml`).

## Dependencies
internal: ledger (`ledgerclient`), inventory (materials, finished goods), product and engineering (BOM, routes),
workforce (time), platform, warehouse. external: OR-Tools (scheduler-py), statsforecast (forecast-py), plant devices
via iot-edge.

## Gotchas & Constraints
- Report-as-finished splits valuation: inventory posts the finished-goods receipt, production posts WIP relief; settlement (variances) was designed as the S3b Temporal saga.
- HPA is configured only for inventory, pricing and production charts (D18).
- iot-edge has a chart and its own gateway but is otherwise a stub (`NOT-IMPLEMENTED.md`).

## Target — not built
- S3b production-order settlement saga (D13); relays for planning, scheduling, shopfloor, lean, processmfg (D2).
- Uniform posting key in planning (D9); legal-entity policies in shopfloor and scheduling (D17).
- Designed `manufacturing-ts` Temporal worker: superseded by `workflow-ts` (ADR-0008).

## Source Sections
- Architecture §3.2 (manufacturing row), §3.4 RAF row, §5.11 manufacturing endpoints (target); data §9 manufacturing; events §3.7, §9 saga S3b.
- ADR-0008; delta §1.1 (manufacturing services), §2.1 (manufacturing row).
