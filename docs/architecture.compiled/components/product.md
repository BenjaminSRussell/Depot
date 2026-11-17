# Product

**As built (v1.0):** `services/product` (Go) keeps the designed name; `services/engineering` (Go) takes BOMs, routes,
ECR/ECO and release management that the design put in `product` and `product-ts`; `services/configurator-py` is the
stateless constraint configurator (the designed `product-py`).

## Purpose & Boundaries
Product masters and variants, released products per legal entity, identifiers and GTINs, barcodes, UoM, configuration
models (product); BOM and route versions, resources and calendars, documents, engineering change requests and orders,
release sessions and policies (engineering); CP-SAT configuration solving (configurator-py).

## Services
| Directory | Lang | Datastore | Outbox relayed |
|---|---|---|---|
| `services/product` | Go | Postgres, own database | yes |
| `services/engineering` | Go | Postgres, own database | no (D2) |
| `services/configurator-py` | Python | none (stateless solver, chart `configurator`, no OpenAPI) | n/a |

## Interfaces
- product: `/v1/product` (products, released-products, gtins, barcodes, configuration-models, saved-configurations, releases, uom, jobs, setup).
- engineering: `/v1/engineering` (boms, bom-versions, routes, route-versions, resources, resource-groups, calendars, ecrs, ecos, documents, release-sessions, release-lines, release-policies, ebom-imports, released-products, identifiers).
- configurator-py is called synchronously by product; it has no OpenAPI document.
- Designed `boms/{id}/explosion`, `where-used`, `change-orders/{id}:release` and `configurations:solve` are target route names; check `contracts.json` for the built ones.

## Data Ownership
product: product masters, variants, released products, identifiers, configurations. engineering: BOM and route
versions (immutable once active), resources, calendars, ECR/ECO, documents, release state. Designed EN-018..022 is the
target shape. engineering is the only service that posts to `services/audit` (`POST /v1/audit/changes`).

## Events Produced
product: `depot.product.product`, `depot.product.releasedproduct`, `depot.product.variant`, `depot.product.identifier`,
`depot.product.configuration`, `depot.product.relation` (relayed). engineering: `depot.engineering.bomversion`,
`depot.engineering.routeversion`, `depot.engineering.eco`, `depot.engineering.ecr`, `depot.engineering.releasedproduct`
and others (not relayed, D2). *Target (D6):* `product.product.released.v1`, `product.bom.versionactivated.v1`,
`product.changeorder.released.v1` with the designed payloads.

## Events Consumed
None declared. ECO approval runs in `workflow-ts` (ECO workflow).

## Dependencies
internal: platform (org, numbering, workflow-ts, audit), platform-kernel. external: OR-Tools CP-SAT (configurator-py),
GS1 check-digit vectors.

## Gotchas & Constraints
- BOM and route are snapshot-copied into production orders at release; later versions do not change released orders.
- Production and planning cannot see engineering events on Kafka until engineering's outbox is relayed (D2).
- CP-SAT tests use a fixed seed and one worker for exact results.

## Target — not built
- Relay for engineering's outbox (D2); designed event payloads (D6).
- Designed `product-ts` and `product-py` deployables: superseded by ADR-0008 (engineering, configurator-py).

## Source Sections
- Architecture §3.2 (product row), §5.7 product endpoints (target); data EN-018..022; events §3.3; §7.16 audit.
- ADR-0008; delta §1.1 (`services/product`, `services/engineering`, `services/configurator-py`), §2.1 (product row).
