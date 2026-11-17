> SDLC run 1, stage 2 evidence input: the as-built vs as-designed delta from the stage-2 audit (read-only, 2026-11-17), kept unchanged below. Claims found wrong or imprecise while writing the architecture are listed in docs/architecture.md, "Status and reading guide".

# Depot architecture delta: as-built vs as-designed (SDLC stage 2)

- Repository: `/home/user/Passage` at commit `4da78d9b` (read-only audit, 2026-11-17).
- Design inputs: `docs/architecture.md` (7,845 lines), `docs/architecture.compiled/` (manifest v2, 26 components, 59 contracts), ADR-0001 to ADR-0005, `docs/architecture-review-r4.md`.
- Method: parsed every `services/*/api/openapi.yaml` (64 specs, 2,579 paths, 3,194 operations) and `services/*/events/asyncapi.yaml` (56 specs, 358 distinct published topics); grepped migrations, `platform/go`, `deploy/`, `policies/authz` and `apps/`. Scripts and raw outputs are in `scratchpad/sdlc/work/` (`openapi.json`, `asyncapi.json`, `endpoints.json`, `inventory.md`).
- Conservative reading: "present" means the token or construct exists in code. It does not mean the code path was run. Every live-cluster claim in the repository is itself marked as pending (#213), so nothing here is verified at runtime.

## 0. Headline counts

| Area | Match | Drift | Missing | Total |
|---|---|---|---|---|
| Components in `manifest.json` (§2.1) | 0 | 24 | 2 | 26 |
| Contracts in `contracts.json` (§2.2) | 11 | 26 | 22 | 59 |
| Cross-cutting decisions (§3) | 3 | 14 | 3 | 20 |
| **All** | **14** | **64** | **27** | **105** |

Six facts frame the rest of this document.

1. **The compiled architecture is stale.** `manifest.json:3-6` records the source as `/Users/benjaminrussell/Desktop/Depot/docs/architecture.md`, sha256 `7db7f82e…`, 7,844 lines, compiled 2026-10-23. The current file has sha256 `ac2e178f…` and 7,845 lines. Recompile it before using it as a gate.
2. **The deployable topology diverged completely.** The design has 31 images in 15 contexts (`architecture.md:105`, `:304`). The code has 71 service directories (58 Go, 7 TypeScript, 6 Python), 69 service charts and 70 Argo CD service Applications. Only `ledger`, `inventory`, `treasury`, `party`, `product`, `sales`, `warehouse` and `projects` share a name with a designed deployable.
3. **The API catalogue is mostly not implemented.** 39 of the 423 concrete endpoints in `architecture.md` §5 (lines 396–1005) match a built path, about 9%. Built paths live under about 90 per-service prefixes (`/v1/hr`, `/v1/receiving`, `/v1/consol` and so on), not the 15 `/v1/<context>` prefixes in the design.
4. **The event catalogue is mostly not implemented.** 7 of the 51 topics in the registry (`architecture.md:4110-4162`) exist. Code publishes 358 other topics. 36 of the 57 services that write an `outbox` table have **no Debezium connector**, `ledger` and `inventory` among them (`deploy/kafka/connect.yaml` defines 21). Their events never reach Kafka.
5. **The authorization model changed.** Duties are in the JWT (`deploy/keycloak/realm-depot.json:164`). Envoy maps them to `X-Depot-Duties` (`deploy/gateway/README.md:10`). Services trust the headers (`platform/go/tenant/tenant.go:1-6,257-336`) and check duties locally with `authz.DutyPolicy` (`platform/go/authz/authz.go:20-25`). The design says duties are never in the JWT and every service re-verifies the token and calls an OPA sidecar (`architecture.md:1131-1144`). Only `identity` calls OPA (`RequireOPA`).
6. **Production is not deployed.** `deploy/envs/prod/apps/` holds only `hello-ledger.yaml` and `kyverno.yaml`. Stage has the services ApplicationSet but no event backbone, Temporal, Postgres, Keycloak or gateway Applications (`deploy/envs/stage/apps/`).

---

## 1. Deployable inventory

Columns: datastore comes from `migrations/` and the chart `databaseSecret`. Published and consumed topics come from AsyncAPI `operations` (`send` or `receive`), resolved to channel `address`. "Outbox relayed" means a `outbox-<service>` KafkaConnector exists in `deploy/kafka/connect.yaml`. "Chart / Argo" means a chart exists under `services/<x>/charts/` and is listed in `deploy/envs/dev/apps/services.yaml` (dev) and the generated `deploy/envs/stage/apps/services.yaml` (stage), or has its own Application. Prod has no service Applications. Component ownership is mapped by function: the manifest names none of these deployables except the eight noted in fact 2.

### 1.1 Services

| Deployable | Lang | Owning component (manifest) | Datastore | Publishes (asyncapi `send`) | Consumes (asyncapi `receive`) | Outbox relayed (Debezium connector) | Chart / Argo (dev+stage) |
|---|---|---|---|---|---|---|---|
| `services/agents-ts` (NOT-IMPLEMENTED.md) | TypeScript | agents | Postgres, own DB | 0 channels; 5 types named in prose | - | **no** (outbox table, no connector) | y (`agent-runtime`) / y |
| `services/analytics` (NOT-IMPLEMENTED.md) | Go | analytics | Postgres, own DB | 4: `depot.analytics.metric`, `depot.analytics.report`, `depot.archive.partition` ... | - | **no** (outbox table, no connector) | y (`analytics`) / y |
| `services/assets` (NOT-IMPLEMENTED.md) | Go | treasury (fixed assets) | Postgres, own DB | 4: `depot.assets.asset`, `depot.assets.capexrequest`, `depot.assets.depreciation` ... | - | **no** (outbox table, no connector) | y (`assets`) / y |
| `services/audit` (NOT-IMPLEMENTED.md) | Go | platform (audit chain) | Postgres, own DB | 1: `depot.audit.change` | - | **no** (outbox table, no connector) | y (`audit`) / y |
| `services/batch` (NOT-IMPLEMENTED.md) | Go | platform / eventing-backbone | Postgres, own DB | 4: `depot.batch.run`, `depot.notify.message`, `depot.workflow.instance` ... | - | yes | y (`batch`) / y |
| `services/billing` (NOT-IMPLEMENTED.md) | Go | sales | Postgres, own DB | 1: `depot.billing.invoice` | - | yes | y (`billing`) / y |
| `services/budgeting` (NOT-IMPLEMENTED.md) | Go | ledger | Postgres, own DB | 6: `depot.budget.check`, `depot.budget.encumbrance`, `depot.budget.plan` ... | - | yes | y (`budgeting`) / y |
| `services/carrier-gateway-ts` (NOT-IMPLEMENTED.md) | TypeScript | warehouse | none | no asyncapi | - | n/a | y (`carrier-gateway`) / y |
| `services/close` (NOT-IMPLEMENTED.md) | Go | ledger | Postgres, own DB | 7: `depot.close.accrual`, `depot.close.allocation`, `depot.close.period` ... | - | **no** (outbox table, no connector) | y (`close`) / y |
| `services/compliance` (NOT-IMPLEMENTED.md) | Go | platform (SoD, privacy) | Postgres, own DB | 6: `depot.privacy.erasure`, `depot.privacy.hold`, `depot.privacy.request` ... | - | **no** (outbox table, no connector) | y (`compliance`) / y |
| `services/configurator-py` | Python | product (`product-py`) | none (stateless) | no asyncapi | - | n/a | y (`configurator`) / y |
| `services/consolidation` (NOT-IMPLEMENTED.md) | Go | ledger | Postgres, own DB | 1: `depot.consol.run` | - | **no** (outbox table, no connector) | y (`consolidation`) / y |
| `services/costacct` (NOT-IMPLEMENTED.md) | Go | ledger (cost ledger) | Postgres, own DB | 3: `depot.controlling.allocation`, `depot.controlling.costentry`, `depot.controlling.hierarchy` | - | **no** (outbox table, no connector) | y (`costacct`) / y |
| `services/costing` (NOT-IMPLEMENTED.md) | Go | inventory | Postgres, own DB | 3: `depot.costing.agingsnapshot`, `depot.costing.cost`, `depot.costing.reconciliation` | - | **no** (outbox table, no connector) | y (`costing`) / y |
| `services/customer-portal-ts` (NOT-IMPLEMENTED.md) | TypeScript | portal-bff | Valkey/Redis sessions (`src/main.ts:6`) | no asyncapi | - | n/a | y (`customer-portal`) / y |
| `services/dispatch-py` (NOT-IMPLEMENTED.md) | Python | projects (`projects-py`) | none (stateless) | no asyncapi | - | n/a | **n / n** (no chart) |
| `services/eam` (NOT-IMPLEMENTED.md) | Go | projects (maintenance) | Postgres, own DB | 7: `depot.eam.counterreading`, `depot.eam.downtime`, `depot.eam.permitstatus` ... | - | yes | y (`eam`) / y |
| `services/edi` (NOT-IMPLEMENTED.md) | Go | integration | Postgres, own DB | 6: `depot.edi.delivery`, `depot.edi.dispute`, `depot.edi.einvoice` ... | - | **no** (outbox table, no connector) | y (`edi`) / y |
| `services/engineering` (NOT-IMPLEMENTED.md) | Go | product | Postgres, own DB | 11: `depot.engineering.bomversion`, `depot.engineering.calendar`, `depot.engineering.document` ... | - | **no** (outbox table, no connector) | y (`engineering`) / y |
| `services/expense` (NOT-IMPLEMENTED.md) | Go | workforce | Postgres, own DB | 3: `depot.expense.cashadvance`, `depot.expense.policy`, `depot.expense.report` | - | yes | y (`expense`) / y |
| `services/fieldservice` (NOT-IMPLEMENTED.md) | Go | projects (field service) | Postgres, own DB | 11: `depot.case.stage`, `depot.knowledge.article`, `depot.service.agreement` ... | - | yes | y (`fieldservice`) / y |
| `services/finreport` (NOT-IMPLEMENTED.md) | Go | ledger (statements) | Postgres, own DB | 10: `depot.reporting.balance`, `depot.reporting.definition`, `depot.reporting.distribution` ... | - | **no** (outbox table, no connector) | y (`finreport`) / y |
| `services/forecast-py` (NOT-IMPLEMENTED.md) | Python | manufacturing (`manufacturing-py`) | none | no asyncapi | - | n/a | y (`forecast`) / y |
| `services/fulfillment` (NOT-IMPLEMENTED.md) | Go | sales / warehouse | Postgres, own DB | 16: `depot.fulfillment.asn`, `depot.fulfillment.container`, `depot.fulfillment.deliverytolerance` ... | `depot.sales.order`, `depot.sales.orderhold` | yes | y (`fulfillment`) / y |
| `services/fx` (NOT-IMPLEMENTED.md) | Go | ledger | Postgres, own DB | no asyncapi | - | **no** (outbox table, no connector) | y (`fx`) / y |
| `services/hr` (NOT-IMPLEMENTED.md) | Go | workforce | Postgres, own DB | 15: `depot.hr.absence`, `depot.hr.benefitenrollment`, `depot.hr.certification` ... | - | yes | y (`hr`) / y |
| `services/identity` (NOT-IMPLEMENTED.md) | Go | edge-identity / platform | Postgres, own DB | no asyncapi | - | **no** (outbox table, no connector) | y (`identity`) / y |
| `services/integration-hub-ts` (NOT-IMPLEMENTED.md) | TypeScript | integration | Postgres, own DB | 4: `depot.hub.apiclient`, `depot.hub.catalogentry`, `depot.hub.subscription` ... | - | **no** (outbox table, no connector) | y (`integration-hub`) / y |
| `services/intercompany` (NOT-IMPLEMENTED.md) | Go | ledger (S4 saga designed in sales) | Postgres, own DB | 5: `depot.interco.charge`, `depot.interco.chargeinvoice`, `depot.interco.journal` ... | - | yes | y (`intercompany`) / y |
| `services/inventory` (NOT-IMPLEMENTED.md) | Go | inventory | Postgres, own DB | 12: `depot.inventory.blocking`, `depot.inventory.consignment`, `depot.inventory.countsheet` ... | `depot.fulfillment.packingslip`, `depot.receiving.receipt`, `depot.sales.orderprogress`, `depot.service.workorder.completed` | **no** (outbox table, no connector) | y (`inventory`) / y |
| `services/iot-edge` (NOT-IMPLEMENTED.md) | Go | manufacturing (spine: "no owner") | none (edge buffer) | no asyncapi | - | n/a | y (`iot-edge`) / y |
| `services/lean` (NOT-IMPLEMENTED.md) | Go | manufacturing | Postgres, own DB | 4: `depot.lean.flow`, `depot.lean.flowperiod`, `depot.lean.kanban` ... | - | **no** (outbox table, no connector) | y (`lean`) / y |
| `services/ledger` (NOT-IMPLEMENTED.md) | Go | ledger | Postgres, own DB | 7: `depot.ledger.balance`, `depot.ledger.batch`, `depot.ledger.journal` ... | - | **no** (outbox table, no connector) | y (`ledger`) / y |
| `services/localization` (NOT-IMPLEMENTED.md) | Go | platform (country packs) | none (packs in repo) | no asyncapi | - | n/a | y (`localization`) / y |
| `services/mcp-ts` (NOT-IMPLEMENTED.md) | TypeScript | agents (MCP gateway) | none | no asyncapi | - | n/a | y (`mcp-gateway`) / y |
| `services/metadata` (NOT-IMPLEMENTED.md) | Go | platform (metadata registry) | Postgres, own DB | 1: `depot.metadata.entity` | - | yes | y (`metadata`) / y |
| `services/migration` (NOT-IMPLEMENTED.md) | Go | migration-framework | Postgres, own DB | 2: `depot.migration.syncabsent`, `depot.migration.syncrun` | - | n/a | y (`migration`) / y |
| `services/mobile` (NOT-IMPLEMENTED.md) | Go | device-bff / warehouse | Postgres, own DB | 3: `depot.mobile.flow`, `depot.mobile.offline`, `depot.mobile.session` | - | **no** (outbox table, no connector) | y (`mobile`) / y |
| `services/numbering` (NOT-IMPLEMENTED.md) | Go | platform (number sequences) | Postgres, own DB | 1: `depot.numbering.sequence` | - | **no** (outbox table, no connector) | y (`numbering`) / y |
| `services/ops` (NOT-IMPLEMENTED.md) | Go | deployment-cicd / platform | Postgres, own DB | 4: `depot.ops.failover`, `depot.ops.license`, `depot.ops.slo` ... | - | yes | y (`ops`) / y |
| `services/org` (NOT-IMPLEMENTED.md) | Go | platform (org, legal entities) | Postgres, own DB | 11: `depot.org.address`, `depot.org.currencyrounding`, `depot.org.democompany` ... | - | **no** (outbox table, no connector) | y (`org`) / y |
| `services/party` (NOT-IMPLEMENTED.md) | Go | party (designed `party-ts`) | Postgres, own DB | 8: `depot.party.change`, `depot.party.customer`, `depot.party.merge` ... | - | yes | y (`party`) / y |
| `services/payables` (NOT-IMPLEMENTED.md) | Go | procurement | Postgres, own DB | 3: `depot.payables.openitem`, `depot.payables.paymentproposal`, `depot.payables.vendor` | - | **no** (outbox table, no connector) | y (`payables`) / y |
| `services/payroll` (NOT-IMPLEMENTED.md) | Go | workforce | Postgres, own DB | none | - | **no** (outbox table, no connector) | y (`payroll`) / y |
| `services/planning` (NOT-IMPLEMENTED.md) | Go | manufacturing | Postgres, own DB | 4: `depot.planning.forecast`, `depot.planning.plan`, `depot.planning.plannedorder` ... | - | **no** (outbox table, no connector) | y (`planning`) / y |
| `services/platform-health` (NOT-IMPLEMENTED.md) | Go | deployment-cicd (hello-ledger) | Postgres, own DB | no asyncapi | - | **no** (outbox table, no connector) | y (`hello-ledger`) / y |
| `services/prediction-py` (NOT-IMPLEMENTED.md) | Python | sales (AR) / agents | Postgres, own DB | 0 channels; 1 types named in prose | - | **no** (outbox table, no connector) | y (`prediction`) / y |
| `services/pricing` (NOT-IMPLEMENTED.md) | Go | sales | Postgres, own DB | 19: `depot.pricing.brokercontract`, `depot.pricing.campaign`, `depot.pricing.commission` ... | - | **no** (outbox table, no connector) | y (`pricing`) / y |
| `services/print` (NOT-IMPLEMENTED.md) | Go | warehouse / platform | Postgres, own DB | 1: `depot.print.job` | - | **no** (outbox table, no connector) | y (`print`) / y |
| `services/processmfg` (NOT-IMPLEMENTED.md) | Go | manufacturing | Postgres, own DB | 10: `depot.production.batch`, `depot.production.batchattribute`, `depot.production.batchorder` ... | - | **no** (outbox table, no connector) | y (`processmfg`) / y |
| `services/product` (NOT-IMPLEMENTED.md) | Go | product | Postgres, own DB | 6: `depot.product.configuration`, `depot.product.identifier`, `depot.product.product` ... | - | yes | y (`product`) / y |
| `services/productcost` (NOT-IMPLEMENTED.md) | Go | inventory (costing versions) | Postgres, own DB | 6: `depot.costing.activationattempt`, `depot.costing.costcomponent`, `depot.costing.costgroup` ... | - | **no** (outbox table, no connector) | y (`productcost`) / y |
| `services/production` (NOT-IMPLEMENTED.md) | Go | manufacturing | Postgres, own DB | 9: `depot.production.asbuilt`, `depot.production.costsnapshot`, `depot.production.journal` ... | `depot.planning.plannedorder`, `depot.shopfloor.registration`, `depot.shopfloor.registration-corrected` | yes | y (`production`) / y |
| `services/projects` (NOT-IMPLEMENTED.md) | Go | projects (designed `projects-ts`) | Postgres, own DB | 4: `depot.projects.changeorder`, `depot.projects.project`, `depot.projects.transaction` ... | - | yes | y (`projects`) / y |
| `services/purchasing` (NOT-IMPLEMENTED.md) | Go | procurement | Postgres, own DB | 22: `depot.procurement.agreement`, `depot.procurement.autocharge`, `depot.procurement.catalog` ... | `depot.fulfillment.dropship`, `depot.planning.plan` | yes | y (`purchasing`) / y |
| `services/quality` (NOT-IMPLEMENTED.md) | Go | inventory (quality) | Postgres, own DB | 9: `depot.quality.capa`, `depot.quality.coa`, `depot.quality.compliancelist` ... | - | **no** (outbox table, no connector) | y (`quality`) / y |
| `services/receivables` (NOT-IMPLEMENTED.md) | Go | sales (AR) | Postgres, own DB | 11: `depot.receivables.case`, `depot.receivables.confirmation`, `depot.receivables.creditnote` ... | - | yes | y (`receivables`) / y |
| `services/receiving` (NOT-IMPLEMENTED.md) | Go | procurement | Postgres, own DB | 6: `depot.payables.invoice`, `depot.payables.settlement`, `depot.receiving.ersselfbill` ... | - | yes | y (`receiving`) / y |
| `services/revrec` (NOT-IMPLEMENTED.md) | Go | sales (rev rec) | Postgres, own DB | 2: `depot.revrec.contract`, `depot.revrec.schedule` | `depot.billing.invoice` | **no** (outbox table, no connector) | y (`revrec`) / y |
| `services/routing-py` (NOT-IMPLEMENTED.md) | Python | warehouse (`warehouse-py`) | none (stateless) | no asyncapi | - | n/a | **n / n** (no chart) |
| `services/sales` (NOT-IMPLEMENTED.md) | Go | sales | Postgres, own DB | 18: `depot.sales.agreement`, `depot.sales.allocation`, `depot.sales.allocationquota` ... | - | yes | y (`sales`) / y |
| `services/scheduler-py` | Python | manufacturing (`manufacturing-py`) | none (stateless) | no asyncapi | - | n/a | y (`scheduler-opt`) / y |
| `services/scheduling` (NOT-IMPLEMENTED.md) | Go | manufacturing | Postgres, own DB | 14: `depot.scheduling.bottleneck`, `depot.scheduling.campaign`, `depot.scheduling.capacityblock` ... | - | **no** (outbox table, no connector) | y (`scheduling`) / y |
| `services/search` (NOT-IMPLEMENTED.md) | Go | platform (designed: no search store, §7.18) | Postgres, own DB | no asyncapi | - | n/a | y (`search`) / y |
| `services/shopfloor` (NOT-IMPLEMENTED.md) | Go | manufacturing | Postgres, own DB | 18: `depot.shopfloor.absencecode`, `depot.shopfloor.andon`, `depot.shopfloor.control-recipe` ... | `depot.production.operation` | **no** (outbox table, no connector) | y (`shopfloor`) / y |
| `services/supplier-portal-ts` (NOT-IMPLEMENTED.md) | TypeScript | portal-bff | Valkey/Redis sessions (`src/redisStore.ts:1`) | no asyncapi | - | n/a | y (`supplier-portal`) / y |
| `services/tax` (NOT-IMPLEMENTED.md) | Go | ledger | Postgres, own DB | 1: `depot.tax.provider` | - | **no** (outbox table, no connector) | y (`tax`) / y |
| `services/transport` (NOT-IMPLEMENTED.md) | Go | warehouse | Postgres, own DB | 7: `depot.tms.freightbill`, `depot.tms.gate`, `depot.tms.goodsintransit` ... | - | yes | y (`transport`) / y |
| `services/treasury` (NOT-IMPLEMENTED.md) | Go | treasury | Postgres, own DB | 4: `depot.treasury.cashdesk`, `depot.treasury.check`, `depot.treasury.reconciliation` ... | - | **no** (outbox table, no connector) | y (`treasury`) / y |
| `services/warehouse` (NOT-IMPLEMENTED.md) | Go | warehouse | Postgres, own DB | 3: `depot.wms.receipt`, `depot.wms.shipment`, `depot.wms.work` | - | yes | y (`warehouse`) / y |
| `services/workflow-ts` (NOT-IMPLEMENTED.md) | TypeScript | eventing-backbone / platform (Temporal) | Temporal (temporal-postgres) | none | `depot.platform.entitydefinition`, `depot.tms.load`, `depot.tms.tender` | n/a | y (`workflow`) / y |

Notes on the table:

- Consumer code exists (`eventing.NewConsumer` and a `deploy/kafka/*-consumer.yaml` KafkaUser) in 17 Go services: billing, edi, finreport, fulfillment, intercompany, inventory, payables, pricing, production, purchasing, receivables, receiving, revrec, scheduling, search, shopfloor and tax. Two TS services have consumers too: `workflow-ts/src/eventConsumer.ts` and `integration-hub-ts/src/consumer.ts`. Several of these consumers are not declared as `receive` operations in their AsyncAPI, for example tax, search, payables and edi. AsyncAPI is therefore not a complete consumer registry.
- **Consumers with no relayed producer** (dead subscriptions):
  - `production` reads `depot.shopfloor.registration*`, but shopfloor has no connector.
  - `purchasing` reads `depot.planning.plan`, but planning has no connector.
  - `workflow-ts` reads `depot.platform.entitydefinition`, but no AsyncAPI declares it. metadata publishes `depot.metadata.entity` (`deploy/kafka/connect.yaml:488`).
- Databases: only some service databases are provisioned in dev, on one shared Postgres server `postgres.hello-ledger.svc` (`deploy/envs/dev/apps/postgres.yaml`, the `CREATE DATABASE` scripts) plus the `deploy/postgres` releases for metadata and batch. The 22 provisioned databases are billing, budgeting, eam, expense, fieldservice, fulfillment, fx, hr, intercompany, ops, org, party, product, production, projects, purchasing, receivables, receiving, sales, transport and warehouse. No `depot_ledger` or `depot_inventory` database and no `ledger-db-app` or `inventory-db` Secret is provisioned anywhere under `deploy/`, although `services/ledger/charts/ledger/values.yaml:11` needs `ledger-db-app`.

### 1.2 Tools and apps

| Deployable | Lang | Owning component | Datastore | Topics | Chart / Argo |
|---|---|---|---|---|---|
| `tools/depot-server` | Go | none. ADR-0004 (Status: **Proposed**, `docs/adr/0004-windows-packaging.md:3`) | Postgres via `PG_DSN` | n/a (supervises services) | n / n. MSI via `deploy/windows/*.wxs` and `.github/workflows/windows-msi.yml` |
| `tools/migration-factory` | Go | migration-framework | Postgres | none | y (`depot-migration-factory`) / y |
| `tools/tenant-restore` | Go | deployment-cicd | CNPG backups | none | n / n (CLI) |
| `tools/rlscheck` | Go | testing-reconciliation | none (CI) | none | n / n (CI) |
| `tools/e2e`, `tools/signoff-packets`, `tools/connectors`, `tools/importexport` | Go | testing-reconciliation / migration-framework | none | none | n / n (CLI, CI) |
| `tools/entitygen` | doc only (`README.md` points to `services/metadata/cmd/entitygen`) | platform | none | none | n / n |
| `tools/docs` | Node | none | none | none | n / n |
| `apps/web` | TS / React 19.3 | web-shell | browser (`sessionStorage` for the PKCE step, `localStorage` for preferences) | n/a | **no image or chart**. GitHub Pages demo only (`.github/workflows/pages.yml`) |
| `apps/customer-portal`, `apps/supplier-portal` | TS / React 19.3 | pwa-apps | browser | n/a | no image. `deploy/gateway/README.md` says "no image hosts them yet" |
| `apps/warehouse-mobile`, `apps/technician` | TS / React 19.3 PWA | pwa-apps | IndexedDB offline queue | n/a | no image or chart |
| `apps/ess` | README only (shell module, "partly built (#267)") | not in design | n/a | n/a | n / n |
| `apps/developer-portal` | `NOT-IMPLEMENTED.md` only | integration ("developer portal") | n/a | n/a | n / n |
| Designed `apps/shopfloor` kiosk PWA (`architecture.md:4773`, §7.6.1) | **absent**. Implemented as `apps/web/src/modules/shopfloor` | pwa-apps | n/a | n/a | n/a |

---

## 2. Components and contracts

### 2.1 Components in `manifest.json`

| Component | Designed deployables (spine) | As-built | Verdict | Evidence |
|---|---|---|---|---|
| platform-kernel | `platform/go` (httpx, dbx, tenant, authz, events, outbox, money, ids, testkit, sequence, ledgerclient) + TS equivalent | `platform/go` has 27 packages. Present: httpx, dbx, tenant, authz, events, outbox, money, ids, testkit, ledgerclient. Added: eventing, fieldcrypt, logpseudo, phiaudit, residency, rlsexempt, cloudprofile, features, orgclient, fxclient and others. **No `sequence` package. No idempotency middleware** (DDL only). **No OTel. No TS kernel** (TS services copy helpers, for example `workflow-ts/src/serviceToken.ts`) | Drift | `platform/go/dbx/dbx.go:284-294`; `ls platform/go` |
| edge-identity | Envoy (TLS, JWT, rate limit, ext_authz to OPA-Envoy), Keycloak realm per tenant + `_internal`, OPA sidecars | Envoy SecurityPolicy JWT maps claims to `X-Depot-*` headers. **No ext_authz.** One realm `depot`. Duties in the token. OPA sidecar only on identity (`deploy/policies/opa/README.md`). Ed25519 service tokens instead of `_internal` | Drift (High) | `deploy/gateway/README.md:9-11`; `deploy/keycloak/realm-depot.json:11,164`; `platform/go/tenant/service.go:40-49` |
| platform | `platform` (Go) + `platform-ts` | Split across identity, metadata, numbering, audit, batch, org, compliance, ops, localization, search, print and platform-health (all Go). No `platform-ts`. No `/v1/platform/*` API: 0 of 104 designed platform endpoints match | Drift | openapi prefixes (`work/openapi.json`) |
| party | `party-ts` | `services/party` (Go). Merge event payload matches the contract | Drift (language, ADR-0001 conformance) | `services/party/go.mod`; `services/party/events/asyncapi.yaml` |
| product | `product`, `product-ts`, `product-py` | `product`, `engineering` (BOM, routing, ECO) and `configurator-py` | Drift | READMEs |
| ledger | `ledger` (journal, FX, tax, close, statements, budgets, consolidation) | Split into ledger, fx, tax, close, consolidation, finreport, budgeting, costacct and intercompany. **Journal not partitioned** (`services/ledger/migrations/0003_journal.sql:19,58`; EN-027/028 require monthly range partitions). **Outbox not relayed** | Drift (High) | as cited |
| inventory | `inventory` (transactions, on-hand, costing, quality, valuation owner) | inventory, costing, productcost and quality. Consumes receipts and packing slips as designed (§3.4). **No `valuation_reservation`. Outbox not relayed**, so `inventory.transaction.posted` never reaches sales | Drift (High) | `deploy/kafka/inventory-consumer.yaml`; `deploy/kafka/connect.yaml` |
| procurement | `procurement`, `procurement-ts` | purchasing, receiving and payables (Go). Receiving uses the designed posting key (`services/receiving/internal/domain/receipt.go`) | Drift | |
| manufacturing | `manufacturing`, `manufacturing-ts` (S3b settlement saga), `manufacturing-py` | production, planning, scheduling, shopfloor, lean, processmfg, iot-edge, scheduler-py and forecast-py. **No S3b settlement workflow** in `workflow-ts/src` | Drift | `ls services/workflow-ts/src` |
| sales | `sales`, `sales-ts` (hosts IC saga S4) | sales, pricing, billing, receivables, revrec and fulfillment. IC is the Go `intercompany` service. **No S4 Temporal saga** | Drift | as above |
| warehouse | `warehouse`, `warehouse-ts`, `warehouse-py` | warehouse, transport, carrier-gateway-ts, mobile, print and routing-py (no chart). Topics are `depot.wms.*`/`depot.tms.*`, not `depot.warehouse.*` | Drift | asyncapi |
| treasury | `treasury` | treasury and assets | Drift (minor) | |
| workforce | `workforce`, `workforce-ts` | hr, payroll and expense (Go). Payroll AsyncAPI has 0 channels, so `workforce.payrollrun.posted` is missing | Drift | `services/payroll/events/asyncapi.yaml` |
| projects | `projects-ts`, `projects-py` | projects, fieldservice and eam (Go), plus dispatch-py (no chart, NOT-IMPLEMENTED) | Drift | |
| integration | `integration-ts` (webhooks, EDI/Peppol, egress relay, event replays) | integration-hub-ts and edi (Go, `/edi/v1/` prefix). **No `/v1/integration/event-replays`. No tenant egress router** | Drift | grep finds no `event-replays` |
| migration-framework | pipeline in the `integration` DB | `services/migration` (own DB, API under `/v1/integration/*`) and `tools/migration-factory` | Drift (minor) | `services/migration/api/openapi.yaml` |
| analytics | `analytics-ts` + lakehouse | `services/analytics` (Go), `data/{cdc,dbt,cube}` | Drift (language) | `ls data` |
| agents | `agents-ts` (MCP at `/v1/agents/mcp`), `agents-py`, `model-serving-py` | agents-ts (`/api/v1/agents/*`) and mcp-ts (`/mcp` on `mcp.depot.<tenant>`). No agents-py or model-serving-py. prediction-py is separate | Drift | `services/agents-ts/api/openapi.yaml`; `services/mcp-ts/README.md` |
| web-bff | `services/web-bff-ts` token-handler BFF + GraphQL read gateway | **Absent.** Apps call Envoy directly with a bearer token held in memory (`apps/web/src/shell/auth/auth.ts:41,137-153`). `apps/technician/README.md:10,35` and `services/fieldservice/NOT-IMPLEMENTED.md:328` still refer to a "web BFF" | **Missing** | |
| device-bff | `services/device-bff-ts` (enrollment, sessions, offline packets, sync) | **Absent.** `services/mobile` (Go) covers warehouse device sessions and offline replay. shopfloor covers kiosk/edge enrollment | **Missing** | |
| portal-bff | one `portal-bff-ts` | two BFFs, `customer-portal-ts` and `supplier-portal-ts`: cookie session, zero dependencies, Valkey sessions | Drift (structure matches the token-handler pattern) | `services/*-portal-ts/src/session.ts:8` |
| web-shell | React 18 shell + 16 federation remotes + `@depot/*` packages | React 19.3, Vite 8, 59 in-tree modules, "**`@module-federation/vite` … still pending**". No `packages/` dir | Drift | `apps/web/package.json:19`; `apps/web/src/shell/README.md` |
| pwa-apps | shopfloor, warehouse-mobile, technician, supplier-portal, customer-portal | warehouse-mobile, technician, supplier-portal and customer-portal. Shopfloor is a web module. Adds ess and developer-portal (stubs) | Drift | `ls apps` |
| eventing-backbone | outbox + Debezium (outbox + cdc connectors per DB) + Kafka + Temporal S3b/S4 | 21 outbox connectors, **0 cdc connectors**, default topic retention 7 d (`connect.yaml:417`). Temporal only in workflow-ts (approvals, tender, payment run, CAPA, ECO, budget, agent run) | Drift (High) | as cited |
| testing-reconciliation | REC-01..19 suite, CI gates, wave-30 simulation | CI gates present (`.github/workflows/{ci,gate,ci-negative}.yml`), `tools/rlscheck`, k6 under `load/`, `evals/`. **No REC-nn catalogue found by id.** Per-service reconciliation endpoints exist (inventory, costing, treasury) | Drift (not audited in depth) | grep finds `REC-[0-9]{2}` only in unrelated expense codes |
| deployment-cicd | CNPG per context, Argo app-of-apps, digest pinning, Rollouts canary, PDBs, Vault | Argo app-of-apps with digest pinning (zero-digest placeholder). One shared `postgres:17-alpine` StatefulSet (`deploy/postgres/values.yaml:2`). CNPG only in `deploy/multi-region/chart`. **0 PDBs, no Argo Rollouts, HPA in 3 charts.** Every service is in namespace `hello-ledger`. Only 13 of 69 service charts ship a NetworkPolicy. Prod deploys nothing | Drift (High) | `deploy/envs/*/apps`; chart templates |

### 2.2 Contracts in `contracts.json`

`contracts.json` holds 59 pattern contracts and no endpoint list. To compare APIs and events, §2.3 checks each service's OpenAPI and AsyncAPI against the endpoint catalogue and topic registry in `architecture.md`.

| Contract | Verdict | As-built evidence |
|---|---|---|
| event-envelope | Drift | `platform/go/events/events.go:15-33` has no `dataschema`, `traceparent` or `sequence`, and adds `legalentityid` |
| event-type-pattern | Match | `events.go:35` regex equals the contract |
| topic-naming | Drift | Most topics fit `depot.<a>.<b>`. Exceptions: hyphenated topics (`depot.shopfloor.registration-corrected`, `control-recipe`, `edge-config`), the four-segment `depot.service.workorder.completed`, and the batch connector routing to `depot.platform.${aggregate_type}` (`connect.yaml:430`) while batch AsyncAPI declares `depot.batch.run` and others (verify) |
| endpoint-prefix-v1 | Drift | 2,353 of 2,579 paths start with `/v1/`. Exceptions: `agents-ts` `/api/v1/…`, `edi` `/edi/v1/…`, `print` `/print/v1/…`, `prediction-py` `/api/v1/…`, `mcp-ts` `/mcp` |
| problem-details-fields | Drift | Kernel `httpx.Problem` (`platform/go/httpx/httpx.go:29-40`) has no `serviceCode`. `errors` is a map, not `errors[]`. `traceId` is the chi request id (`httpx.go:147`), not the W3C trace id. Only close and ledger add `serviceCode` locally (`services/close/internal/api/api.go:96`) |
| error-codes | Drift | snake_case codes exist, but the kernel puts service codes in `code` (`AUTH-0001`, `COM-0500`: `httpx.go:131-145`, `authz.go:39`). Q15 resolution (`architecture.md:1388`) not implemented |
| auth-jwt | Match | `aud depot-api`, RS256 at the gateway (`deploy/gateway/README.md:10`) and in `identity` JWTVerifier |
| auth-duty-annotation | Match | `x-depot-duty` on 3,116 of 3,194 operations (97.6%). The gaps are the portal BFF specs and about 17 operations elsewhere |
| auth-opa-decision | Drift | Rego package is `authz` (`policies/authz/authz.rego:1`), not `depot.authz`. Decision is `allow` only, with no `legalEntityScopes` or `obligations` |
| auth-step-up | Missing | No `requires_acr`. A substitute auth-age check uses `X-Depot-Auth-Time` (`platform/go/tenant/tenant.go:111-131`) |
| auth-headers | Drift | `X-Depot-Tenant` yes. LE header is `X-Depot-Legal-Entity` (JWT) and `X-Depot-LegalEntity` (web narrowing) (`tenant.go:105,113`), not `Depot-Legal-Entity`. `X-Depot-Service-Token` replaces `X-Depot-Subject-Token` |
| tenant-guc | Match | `platform/go/dbx/dbx.go:231` |
| posting-idempotency-key | Drift | receiving and tax use `post:<ctx>:<doc>:<id>:<purpose>`. productcost uses `productcost.activation:<id>` (`services/productcost/internal/store/activation.go:524-527`). close uses document ids or UUIDv5 (`services/close/internal/api/posting.go:267-588`) |
| valuation-reservation-key | Missing | no `valuation_reservation` table or code |
| posting-request-registry | Missing | no `posting_request` table. Ledger relies on its idempotency table |
| rls-policies | Drift (low) | `FORCE ROW LEVEL SECURITY` in 55 of 59 migration sets, with 544 `AS RESTRICTIVE` policies. Names are `<table>_tenant`/`<table>_legal_entity`, not `tenant_access`/`legal_entity_scope`. Exemptions are allowlisted (`platform/go/rlsexempt/rlsexempt.go:45-127`) |
| retention-audit-classes | Missing | no `fiscal_7y`, `standard_3y` or `agent_7y` |
| retention-idempotency | Drift | 37 services create `idempotency_keys`. `locked_until` only in close, processmfg and receiving. The kernel DDL lacks state, lease and expiry (`dbx.go:286-294`) |
| retention-valuation-reservation | Missing | as above |
| audited-reveal | Missing | no `pii_payload` audit field. Reveal auditing exists only for PHI reads (`services/audit/README.md`, "PHI read-access log") |
| audited-export | Missing | no `depot_export_rows_total` metric |
| approval-required-workflow | Drift | `approval_required` exists. There is no `/v1/platform/workflow-instances`. Approvals are `workflow-ts` routes `/v1/workflows/*` |
| idempotency-key-custom-methods | Drift | `Idempotency-Key` declared on 750 of 1,718 mutating operations (44%) |
| if-match-concurrency | Drift | `If-Match` appears in 35 of 64 specs |
| payload-party.party.merged | Match | all 5 fields in `services/party/events/asyncapi.yaml` |
| payload-ledger.periodcontrol.changed | Drift | best match `close`, 6 of 8 fields (no `periodNo`, `effectiveFrom`) |
| payload-procurement.productreceipt.posted, -vendorinvoice.posted, -purchaseorder.confirmed; payload-sales.shipment.posted, -invoice.posted, -salesorder.confirmed, -payment.applied; payload-inventory.transaction.posted, -transaction.rejected; payload-ledger.journalentry.posted; payload-manufacturing.productionorder.reportedasfinished; payload-platform.workflowtask.completed; payload-workforce.payrollrun.posted (13) | Missing | no AsyncAPI message carries the designed field set. Best partial match ranges from 2 of 10 to 5 of 9 fields (`work/` script output). The designed topics do not exist (§2.3) |
| status-gl-posting | Drift | no `gl_posting_status` or `not_required` |
| status-valuation-reservation | Missing | entity absent |
| status-period-control | Match | `open`, `on_hold`, `closed`, `permanently_closed` present |
| status-production-order | Drift | no `reported_finished` state |
| status-job | Match | |
| status-agent-run | Match | agents-ts |
| status-sales-order | Drift | no `partially_shipped` |
| status-purchase-order | Match | |
| outbox-inbox-tables | Drift | `dbx.OutboxDDL` (`dbx.go:297-305`) has no `partition_key` or `headers`. The Kafka key is `aggregate_id` (`connect.yaml:427`), not `tenantid:subject` |
| dlq-record-headers | Drift | `x-origin-*`, `x-dlq-reason`, `x-dlq-detail` (`platform/go/eventing/README.md`; `consumer.go:29`). DLQ topic is `<topic>.dlq`, not `….dlq.<consumer>` |
| webhook-signature | Drift | header `Depot-Signature`, HMAC-SHA256 (`services/integration-hub-ts/src/webhook.ts:15-24`) |
| ledger-post-request | Drift | `ledgerclient` posts `POST /v1/ledger/journal-entries` (`platform/go/ledgerclient/client.go:38`). No `journal-entries:post`. Reversal is `/journal-entries/{id}/reverse` |
| log-fields | Drift (low) | the access log uses `requestId` (`httpx.go:103`). Other logs use `correlationId` (`tenant.go`) |
| health-endpoints | Match | `/healthz`, `/readyz` in every chart and spec |
| money-quantity-shape | Match | `platform/go/money` |
| bff-session-cookies | Drift | portals use `__Host-depot_customer`/`__Host-depot_supplier`. The web shell has no cookie session |
| async-job-202 | Drift | `statusUrl` only in `services/migration/internal/api/api.go:113`. No `/v1/platform/jobs` |
| mcp-gateway-path | Drift | `/mcp` (`services/mcp-ts/api/openapi.yaml`) |
| llm-request-params | Missing | no `output_config` in agents-ts |
| offline-queued-command | Drift | 3 of 6 fields (no `aggregateKey`, `actorSub`, `commandId`) |

### 2.3 APIs and events: as-built vs design catalogue

**Designed endpoints with no implementation.** Normalized comparison: path params collapsed, `/verb` treated as `:verb`, generic `{context}` rows excluded. 384 of the 423 designed endpoints have no built match.

| Designed context | Matched / designed |
|---|---|
| platform | 0 / 104 |
| manufacturing | 0 / 27 |
| warehouse | 0 / 18 |
| workforce | 0 / 13 |
| ledger | 9 / 55 |
| procurement | 5 / 40 |
| sales | 6 / 35 |
| inventory | 2 / 24 |
| integration | 4 / 20 |
| product | 3 / 18 |
| projects | 1 / 17 |
| treasury | 1 / 16 |
| party | 3 / 15 |
| agents | 2 / 15 |
| analytics | 3 / 6 |

Notable missing endpoints:
- `POST /v1/ledger/journal-entries:post` (the "only synchronous cross-context write", `architecture.md:1008-1054`)
- `/v1/ledger/posting-requests/{key}`
- `/v1/platform/jobs/{id}`, `/v1/platform/workflow-instances`, `/v1/platform/policy-bundles/current`, `/v1/platform/tenants` (as-built `/v1/tenants` in identity)
- `/v1/integration/event-replays`
- `/v1/{context}/error-codes`, `/v1/{context}/openapi.yaml`
- generated `:batch`/`:export`/`HEAD` entity endpoints
- every `/bff/*` route (`architecture.md:312`)

**Implemented APIs absent from the design and contracts** (paths per service, from `work/openapi.json`):

| Service | Paths | Service | Paths | Service | Paths |
|---|---|---|---|---|---|
| purchasing | 122 | hr | 115 | sales | 94 |
| pricing | 92 | shopfloor | 89 | inventory | 81 |
| receivables | 78 | engineering | 75 | ledger | 75 |
| fieldservice | 72 | org | 72 | party | 69 |
| fulfillment | 66 | treasury | 63 | processmfg | 58 |
| scheduling | 57 | edi | 55 | eam | 54 |
| receiving | 53 | finreport | 50 | production | 50 |
| migration | 48 | tax | 47 | planning | 45 |
| assets | 43 | transport | 42 | budgeting | 39 |

The remaining 37 specs hold 31 paths or fewer each. Overall 2,579 paths (3,194 operations). About 2,470 normalized paths have no counterpart in `architecture.md` §5, and none appears in `contracts.json`, which has no endpoint contracts.

Notable undesigned surfaces:
- `/v1/hr/*` (115 paths)
- `/v1/pricing/*` (rebates, royalties, commissions)
- `/v1/receivables/*` dunning, disputes and deductions
- `/v1/compliance`, `/v1/privacy`, `/v1/sod`, `/v1/trade`
- `/v1/ops/*` (failover, licensing, tenant provisioning)
- `/v1/audit/reads*` (HIPAA PHI read log)
- `/v1/search`
- `/as2/v1/*`, `/punchout`, `/cxml` (edi)
- `/webhooks/*` (receivables payment gateway)

**Events.**
- Designed topics with no implementation: 44 of 51. Only `depot.inventory.reservation`, `depot.inventory.transaction`, `depot.party.customer`, `depot.party.party`, `depot.product.product`, `depot.projects.transaction` and `depot.sales.returnorder` exist.
- Missing topics include `depot.ledger.journalentry` (built: `depot.ledger.journal`), `depot.ledger.periodcontrol`, `depot.procurement.{purchaseorder,productreceipt,vendorinvoice,payment}` (built: `depot.procurement.po`, `depot.receiving.receipt`, `depot.payables.invoice`), `depot.sales.{salesorder,shipment,invoice,payment}` (built: `depot.sales.order`, `depot.fulfillment.shipment`, `depot.billing.invoice`, `depot.receivables.payment`), `depot.manufacturing.*` (built: `depot.production.*`, `depot.shopfloor.*`), `depot.warehouse.*` (built: `depot.wms.*`), `depot.workforce.*` (built: `depot.hr.*`, `depot.expense.*`; payroll has none), all `depot.agents.*`, and all `depot.platform.*` except what batch routes.
- Built topics absent from the registry: 351. The largest publishers are sales (18 topics), pricing (19), fulfillment (16), purchasing (22), shopfloor (18), hr (15) and scheduling (14).
- Only 7 of the 56 AsyncAPI specs declare any `receive` operation (fulfillment, inventory, production, purchasing, revrec, shopfloor, workflow-ts).

---

## 3. Cross-cutting decisions

| # | Decision | Designed (section ref) | As-built (code ref) | Verdict |
|---|---|---|---|---|
| 1 | Tenancy / RLS | `SET LOCAL depot.tenant_id`, `depot.legal_entity_scopes`. Permissive `tenant_access` + restrictive `legal_entity_scope` on every tenant table. `depot_app` has no BYPASSRLS (`architecture.md:1147-1156`, §4.5 `:1550-1578`, `:1331`) | `dbx.go:231,255`. 1,945 `current_setting('depot.tenant_id')` and 986 `legal_entity_scopes` refs in migrations. FORCE RLS in 55 of 59 DBs. `tools/rlscheck` + `platform/go/rlsexempt` allowlist. `depot_app NOBYPASSRLS` (`deploy/envs/dev/apps/postgres.yaml` init) | **Match** (policy names drift) |
| 2 | Legal-entity policy | `Depot-Legal-Entity` header narrows OPA scopes (`:1150`, `:5288`). LE scope on every LE-owned table | Scope comes from the JWT `le` claim via `X-Depot-Legal-Entity`, narrowed by `X-Depot-LegalEntity`, with all-scope via `X-Depot-LegalEntity-Scope: ALL` + `org.switcher.all-scope` (`tenant.go:104-114,316-325`; `architecture.md:1152` already describes this). **No `legal_entity_id` and no LE policy** in shopfloor (time and pay data), scheduling, audit, mobile, fx, integration-hub-ts and agents-ts. Group-level exemptions for consolidation and eam (`rlsexempt.go:82-88`) | **Drift** |
| 3 | OPA authz duties | Duties only in the OPA bundle. Envoy ext_authz (coarse) + per-service sidecar `data.depot.authz.decision` (fine). Fail-secure 503. Revocation ≤ 10 s (`:1131-1145`) | Duties in the JWT (`realm-depot.json:164`) mapped to the `X-Depot-Duties` header (`deploy/gateway/README.md:10`). Services check `p.HasDuty` (`platform/go/authz/authz.go:20-25`; 56 services use `RequireDuty`). OPA (`package authz`, allow only) is called by identity `RequireOPA`, `services/identity/cmd/devgateway` and the `tools/depot-server` gateway, not in Kubernetes (`deploy/gateway/*.yaml` has no extAuth). Revocation ≈ token life (300 s, `realm-depot.json:11`). OPA decision logs off (`deploy/policies/opa/README.md:12`) | **Drift (High)** |
| 4 | Authentication / realms | Realm per tenant + `_internal`. Every service re-verifies the JWT (`:1121,1133`) | Single realm `depot`. Services trust gateway headers. The trust boundary is NetworkPolicy (ADR-0004 `:32`), yet only 13 of 69 charts define one and every service shares namespace `hello-ledger` (`deploy/envs/dev/apps/services.yaml`) | **Drift (High)** |
| 5 | Outbox / Debezium CDC | Outbox in the same tx. Two connectors per DB (`outbox` + `cdc`). Partition key `tenantid:subject`. Retention 30 d (`:1190-1199`, `:4093-4104`) | `outbox.Enqueue` in the same tx (`platform/go/outbox/outbox.go`). 21 outbox connectors for 57 outbox tables. **No `cdc` connector.** Key `aggregate_id`. Default retention 7 d (`connect.yaml:417,427`) | **Drift (High)** |
| 6 | Inbox / consumer / DLQ | `consumer_inbox`, 5 attempts (1→60 s), DLQ `….dlq.<consumer>`, ordering by `sequence` (`:1195`, `:3726`) | `consumer_inbox` (`platform/go/eventing/inbox.go:35`). 10 attempts, 2 s→2 min. DLQ `<topic>.dlq`. No `sequence` (`platform/go/eventing/README.md`; `consumer.go:20-29`) | **Drift** |
| 7 | Service tokens | Keycloak client credentials in `_internal` + mTLS. Forwarded subject token (`:1126`) | Ed25519 `ServiceSigner`, 60 s max TTL, context `depot.service-token.v1.`, replay table `service_token_replay` (`platform/go/tenant/service.go:40-49,420-540`; `dbx.ServiceTokenReplayDDL`). Duties are carried in the token | **Drift** (decision made in code; write ADR) |
| 8 | Field encryption (fieldcrypt) | P3 in `*_secure` tables, pgcrypto with per-tenant Vault transit keys. Crypto-shred via `pii_key_ref` (`:1323`, `:1541`, `:2034`) | App-level AES-GCM with HKDF per tenant from master keys in `FIELDCRYPT_KEYS`, optionally wrapped by Azure Key Vault/HSM. Blind-index MAC. Frozen `passage.fieldcrypt.v1` labels (`platform/go/fieldcrypt/fieldcrypt.go:30-33,58-78`; ADR-0005). Used by compliance, hr, metadata, migration, party, payables, receiving and sales. No per-subject key, so crypto-shred is per tenant, not per person | **Drift** (write ADR) |
| 9 | Observability | OTel SDK in every service. W3C traceparent through HTTP, Kafka and Temporal. Tempo, Loki, Prometheus (`:1295-1300`, `:7726-7736`) | **No OTel dependency** in `platform/go` or any service `go.mod`/`package.json`. `traceId` = chi request id. Prometheus RED metrics (`platform/go/httpx/metrics.go`). "Tempo and Loki are not deployed. Stage and prod have no monitoring app yet" (`deploy/observability/README.md:5`) | **Missing** (tracing) |
| 10 | Idempotency | Kernel middleware. Table with principal, method, path, state, `locked_until`, `expires_at`. Response written in the business tx (`:1182-1188`) | Kernel ships DDL only (`dbx.go:284-294`: no state, lease or expiry). Each of 52 services implements the handling itself. 37 create the table, 3 have a lease column. Header declared on 44% of mutating operations | **Drift** |
| 11 | Temporal workflows | TS workers per context. S3b settlement (`manufacturing-ts`), S4 intercompany (`sales-ts`). Accept-and-queue jobs at `/v1/platform/jobs` (`:1238-1243`, `:4433-4649`) | One worker, `workflow-ts`: approvals, party merge, ECO, CAPA, tender, payment run, budget plan, agent run. **No S3b or S4.** No mTLS or authorizer on the Temporal frontend (`services/workflow-ts/NOT-IMPLEMENTED.md`). Several workflows have no trigger (payment run, ECO, CAPA) | **Drift** |
| 12 | Multi-region | Open question Q20 (`:1393`). CNPG failover slots (`:355`, `:1368`) | Active/passive chart: CNPG replica cluster, MirrorMaker 2, fencing, burn-rate rules, barman backups (`deploy/multi-region/README.md`; `chart/templates/cnpg-cluster.yaml`). `services/ops` failover API. Not deployed in any env | **Match** (doc stale: Q20 is answered in code) |
| 13 | Windows packaging | Not in `architecture.md` | `tools/depot-server` (supervisor + built-in gateway with JWT + OPA + gateway secret), `deploy/windows/*.wxs`, `.github/workflows/windows-msi.yml`. ADR-0004 still **Proposed** | **Drift** (undocumented; ADR not accepted) |
| 14 | Audit trail | CDC into `platform.audit_entry`, per-tenant SHA-256 chain verified nightly, head anchored to object-locked storage. `depot.ctx` logical message carries actor and trace (`:1151`, `:1322`) | `services/audit` hash chain fed only by HTTP `POST /v1/audit/changes` ("in-process stand-in", `services/audit/README.md:3`). Only `engineering` calls it (`services/engineering/internal/api/audit.go`). No CDC connector. **No `depot.ctx`** (no `pg_logical_emit_message` in `platform/go`). Per-service audit tables in 11 services. Hash chains only in audit and shopfloor | **Missing** |
| 15 | Error contract | RFC 9457 with `code` (snake_case) + `serviceCode` + `errors[]` (`:375`, `:6123`) | Kernel `Problem` without `serviceCode`, map `errors`, service code in `code`. Portal BFFs send `{error:"invalid_request"}` with a problem+json content type (`services/customer-portal-ts/src/server.ts:42,390`). `routing-py` uses FastAPI default `{"detail":…}` (`services/routing-py/tms_optimizer/api.py:108`) | **Drift** |
| 16 | Browser session / BFF | Token-handler BFFs. Tokens never in the browser (`:1123`, `:4715`, `:5586-5628`) | `apps/web` is a public OIDC client. Access token in memory, bearer injected by `installBearerFetch` (`apps/web/src/shell/auth/auth.ts:41,137-163`). Portals do have cookie BFFs | **Missing** (web-bff) |
| 17 | Database platform | CNPG cluster per context, PgBouncer, Atlas (`:355`, `:1329-1338`) | Plain `postgres:17-alpine` StatefulSet, shared server in dev (`deploy/postgres/values.yaml:2`; `deploy/envs/dev/apps/postgres.yaml`). Plain SQL `dbx.Migrate` (`services/metadata/NOT-IMPLEMENTED.md`). CNPG only in the multi-region chart | **Drift** |
| 18 | Gap-free numbering | In-tx counter per context. Never across HTTP (`:1333`). Kernel `sequence` package (`:1407`) | `services/numbering` over HTTP, called by hr and party. No `platform/go/sequence` | **Drift** |
| 19 | Rate limiting | Envoy global limit. Valkey proposed (Q4, `:1377`) | `deploy/envoy-gateway/ratelimit-valkey.yaml`. Per-tenant tier limits (`deploy/gateway/README.md:11`) | **Match** (Q4 resolved in code; record it) |
| 20 | Search | No new search store. pg_trgm fan-out by `platform-ts` (`:1335`, Q23 `:1396`) | `services/search` with its own DB and Kafka consumer (`deploy/kafka/search-consumer.yaml`) | **Drift** (write ADR) |

---

## 4. Deviations needing a decision

| # | Deviation | Severity | Recommendation | Evidence |
|---|---|---|---|---|
| D1 | Duties in the JWT and services trusting `X-Depot-*` headers, with no per-service JWT verification or OPA in Kubernetes. Revocation takes up to 5 min, not ≤ 10 s | High | **Write ADR** for the gateway-asserted identity model, and **fix code**: default-deny NetworkPolicy plus gateway-only ingress for every service namespace (today 13 of 69 charts), or require a gateway secret as on Windows. Update §7.1–7.4 | §3 rows 3–4 |
| D2 | 36 services' outbox tables are never relayed (ledger, inventory, close, tax, treasury, shopfloor, planning, payables and others), so designed cross-context flows (COGS link-back, journal events to lakehouse and audit) cannot happen | High | **Fix code**: one connector per outbox DB, and a CI check that every outbox table has a connector | `deploy/kafka/connect.yaml`; `work/connectors.txt` vs `work/outbox_svcs.txt` |
| D3 | No CDC audit pipeline, no `depot.ctx`, central audit fed by one service over HTTP | High | **Fix code** (cdc connector into audit, actor and trace propagation), or **write ADR** choosing per-service audit tables plus a chain | §3 row 14 |
| D4 | Prod and stage environments lack the platform (prod: hello-ledger only; stage: no Kafka, Temporal, Postgres, Keycloak or gateway Apps). No `depot_ledger`/`depot_inventory` DB or Secret provisioned | High | **Fix code** (deploy manifests) before v1.0 | `deploy/envs/{stage,prod}/apps` |
| D5 | Topology: 71 deployables in about 60 Go services instead of 31 images (10 Go + 15 TS + 6 Py). Party, projects, workforce, procurement and others are Go where ADR-0001 put them in TS | High | **Write ADR** (service decomposition and language choice). **Update doc** §3.2 inventory and the manifest components | §1 |
| D6 | Event catalogue diverged: 44 of 51 designed topics absent, 351 undesigned, 13 of 15 payload contracts unmet | High | **Update doc** (regenerate the topic registry and payload contracts from AsyncAPI), then **fix code** where consumers need the designed fields (e.g. `inventory.transaction.posted.journalEntryId`/`glSeq` for COGS link-back) | §2.3 |
| D7 | Web shell holds OAuth tokens in browser memory, with no web-bff and no device-bff | High | **Write ADR** (public PKCE client accepted, with XSS mitigations) or **fix code** (build web-bff). Remove "web BFF" references from app READMEs | `apps/web/src/shell/auth/auth.ts:41` |
| D8 | No OpenTelemetry tracing. `traceId` is not a W3C trace id. Tempo and Loki not deployed | High | **Fix code** (kernel OTel + propagation through outbox headers). Required for NFR-O1 and auditability | §3 row 9 |
| D9 | Ledger posting API shape: `POST /v1/ledger/journal-entries` and `/{id}/reverse` vs designed `journal-entries:post`/`:reverse`. Posting key not uniform | Med | **Update doc** for the path. **Fix code** to make every caller use `post:<ctx>:<doc>:<id>:<purpose>` (productcost, close, mobile, planning) | `ledgerclient/client.go:38`; `productcost/internal/store/activation.go:524` |
| D10 | Journal tables not partitioned (EN-027/028, NFR-P1 5,000 lines/s) | Med | **Fix code**, or **write ADR** deferring partitioning with a volume threshold | `services/ledger/migrations/0003_journal.sql` |
| D11 | Problem-details contract: no `serviceCode`, map `errors`, service code in `code`. Portal and Python services non-conformant | Med | **Fix code** in `httpx.Problem` and the portals. **Update doc** if `code = LED-0001` is kept | §3 row 15 |
| D12 | Idempotency middleware missing from the kernel (52 hand-rolled implementations, 3 with leases) | Med | **Fix code** (kernel middleware matching §7.5) | `dbx.go:284` |
| D13 | Temporal sagas S3b (production settlement) and S4 (intercompany) not built. Payment run, ECO and CAPA workflows have no trigger | Med | **Fix code**, or **write ADR** if settlement and IC are synchronous in Go (`services/intercompany`) | `services/workflow-ts/NOT-IMPLEMENTED.md` |
| D14 | Service tokens are custom Ed25519, not Keycloak `_internal` | Med | **Write ADR** (decision made in code; ADR-0005 already treats the signing context as a wire identifier) | `platform/go/tenant/service.go:40-49` |
| D15 | Field encryption is app-level AES-GCM with Azure Key Vault wrapping, not pgcrypto + Vault transit. Crypto-shred is per tenant | Med | **Write ADR** (and record the frozen `passage.*` labels) | `fieldcrypt.go:30-33` |
| D16 | Single Keycloak realm instead of realm per tenant. Claim `tid` instead of `tenant_id` | Med | **Write ADR**. **Update doc** §7.1, §8.1 | `realm-depot.json` |
| D17 | LE scoping absent in shopfloor (pay data), scheduling and audit | Med | **Fix code** (add `legal_entity_id` + restrictive policy) or allowlist each with a reason in `rlsexempt` | §3 row 2 |
| D18 | Postgres platform: plain StatefulSet and a shared dev server, not CNPG per context. No PDBs, Rollouts or broad NetworkPolicies | Med | **Write ADR** (dev/stage vs prod topology) and **fix code** for prod | §3 row 17 |
| D19 | Envelope lacks `traceparent`, `sequence`, `dataschema`. Kafka key is `aggregate_id`, not `tenantid:subject`. DLQ naming and retry policy differ | Med | **Fix code** for `traceparent` and `sequence`. **Update doc** for DLQ and retry if kept | `events.go:15-33` |
| D20 | Search service with its own store | Low | **Write ADR** (closes Q23) | `services/search` |
| D21 | Gap-free numbering over HTTP (`services/numbering`) | Med | **Write ADR** or **fix code**. The design said an HTTP counter burns numbers on abort (`architecture.md:1333`) | `services/numbering/README.md` |
| D22 | Non-`/v1` prefixes (agents-ts, edi, print, prediction-py, mcp-ts) | Low | **Fix code** or **update doc**. Gateway routing in `deploy/gateway/httproutes.yaml` must agree | §2.2 |
| D23 | Windows distribution implemented while ADR-0004 is Proposed. Three gateway implementations (Envoy, devgateway, depot-server) enforce different checks (OPA only in the latter two) | Med | **Accept ADR-0004** and **update doc**. **Fix code** so one enforcement model holds everywhere | `docs/adr/0004-windows-packaging.md:3,32` |
| D24 | Kafka topic retention 7 d vs designed 30 d | Low | **Update doc** or **fix code** | `connect.yaml:417` |
| D25 | TS stack: kafkajs and zero-dependency Node vs Fastify + zod + Kysely + `@confluentinc/kafka-javascript`. React 19 vs 18. No module federation | Low | **Update doc** | `services/*-ts/package.json`; `apps/web/package.json:19` |
| D26 | Valkey adopted for rate limits and portal sessions (Q4) | Low | **Write ADR** (addendum to ADR-0001: Valkey is an in-cluster dependency) | `deploy/envoy-gateway/ratelimit-valkey.yaml` |
| D27 | Compiled architecture stale (sha, line count, Mac path) | Low | **Update doc**: recompile `docs/architecture.compiled` after the doc corrections | `manifest.json:3-6` |

---

## 5. Stale or wrong statements in `docs/architecture.md` for v1.0

`rg -i passage docs/architecture.md docs/architecture.compiled` returns nothing: the rename itself is complete. The remaining name-related work is to document the three frozen `passage.*` cryptographic labels (ADR-0005), which the architecture never mentions. The transport homonym ("depot" as the route start location) does not occur in the doc.

| # | Section / line | Statement now | What is true in code |
|---|---|---|---|
| C1 | Overview diagram `:19-58`; §1 `:105`; §3.2 `:279-304`; spine "Service Inventory" | 31 images, 15 contexts; `platform-ts`, `party-ts`, `procurement-ts`, `manufacturing-ts/-py`, `sales-ts`, `warehouse-ts/-py`, `workforce-ts`, `projects-ts/-py`, `integration-ts`, `analytics-ts`, `agents-py`, `model-serving-py`, `web-bff`, `device-bff`, `portal-bff` (none exist; e.g. `platform-ts` 23 mentions, `model-serving-py` 13, `device-bff` 18) | 71 service dirs (58 Go, 7 TS, 6 Py) listed in §1 of this delta. Contexts are split into per-capability services |
| C2 | §3.2 route table `:308-315`; §2.4 frontend `:4850-4860` | `/bff/...` routes served by web-, device- and portal-BFFs; the BFF rewrites `/api/v1` | Envoy rewrites `/api/v1/<svc>` to `/v1/<svc>` per HTTPRoute (`deploy/gateway/README.md`, "Routes and listeners"). Only the portal BFFs serve `/bff/*` |
| C3 | §7.1 `:1121` | Realm per tenant + `_internal` realm | Single realm `depot` (`deploy/keycloak/realm-depot.json`). Service identity is Ed25519 tokens (`platform/go/tenant/service.go`) |
| C4 | §7.1 `:1123`; §1 frontend `:4715`; §8.2 `:5586-5628` | Browser never holds a token; web-bff confidential client | `apps/web` public OIDC client, token in memory (`apps/web/src/shell/auth/auth.ts:41`) |
| C5 | §7.1 `:1129` | Step-up via OPA obligation `requires_acr` | Auth-age check via `X-Depot-Auth-Time` (`tenant.go:111-131`). No acr obligation |
| C6 | §7.1 `:1131`, `:1133` | Duties absent from JWT; every service re-verifies the JWT | `duties` claim mapper (`realm-depot.json:164`). Envoy validates and maps claims to headers. Services trust headers (`tenant.go:1-6`) |
| C7 | §7.2 `:1136-1145` | Bundle compiled by `platform-ts` at `/v1/platform/policy-bundles/current`; ext_authz + sidecar per service; `data.depot.authz.decision` with obligations | identity serves `GET /v1/identity/policy/bundle` (`deploy/policies/opa/README.md`). Rego `package authz`, allow only. Sidecar only on identity. No ext_authz |
| C8 | §7.3 `:1149-1150` vs `:1152` (3a) | Steps 1–2 say the service re-derives the tenant from the token and uses `Depot-Legal-Entity`; 3a already describes the as-built header model | Make §7.3 consistent with 3a. Header names are `X-Depot-Legal-Entity`, `X-Depot-LegalEntity`, `X-Depot-LegalEntity-Scope`. Also fix `:5288` (`Depot-Legal-Entity`) |
| C9 | §7.3 `:1151`; §7.16 `:1322`; `:1407` | `dbx` emits the `depot.ctx` logical message; audit derived from CDC into `platform.audit_entry` | No `depot.ctx`. Audit is `services/audit` with HTTP ingest and per-service audit tables (§3 row 14) |
| C10 | §7.5 `:1184` | Kernel `httpx` idempotency middleware with lease and expiry columns | `dbx.IdempotencyDDL` (`dbx.go:286-294`) has only `tenant_id, key, request_hash, response_status, response_body, created_at`. No middleware |
| C11 | §7.6 `:1192-1195`; Events §4.1 `:4096-4104`; `:3726` | DDL with `partition_key`; two connectors per DB; DLQ `….dlq.<consumer>`; 5 attempts; 30 d retention; KafkaTopic CRs generated from AsyncAPI | `dbx.OutboxDDL` (`dbx.go:297-305`). 21 outbox connectors, no `cdc`. `<topic>.dlq`. 10 attempts, 2 s–2 min. Default retention 7 d (`connect.yaml:417`). Topics auto-created by Connect, not generated |
| C12 | Events §4.2 `:4108-4166`; §5 producer/consumer `:4179-4250`; §6 consumer registry `:4252-4328` | 51-topic registry and consumer list | Only 7 of 51 exist. Regenerate from `services/*/events/asyncapi.yaml` (358 topics) |
| C13 | §5 API catalogue `:396-1005` | `/v1/<context>/…` endpoints, e.g. `journal-entries:post` | 39 of 423 exist. Built namespaces are per service (`/v1/receiving`, `/v1/consol`, `/v1/controlling`, `/v1/product-costing` …). Regenerate from OpenAPI |
| C14 | §7.16 `:1323`; Data `:1499`, `:1541`, `:2034` | P3 via pgcrypto + Vault transit per tenant; `pii_key_ref` crypto-shred | `platform/go/fieldcrypt` (AES-GCM, HKDF, Azure Key Vault wrap). Frozen labels `passage.fieldcrypt.v1`, `passage.fieldcrypt.mac.v1`, `passage.log.pseudonym.v1\0` (ADR-0005). Document them as the only sanctioned "passage" strings |
| C15 | §4 stack `:353-355`; §7.18 `:1329-1338`; Data §3 `:1491`; spine "Tech Stack" | CNPG per context, PgBouncer Pooler, Atlas; Fastify + zod + Kysely + confluent Kafka JS; React 18 | `postgres:17-alpine` StatefulSet (`deploy/postgres/values.yaml:2`), CNPG only in `deploy/multi-region`. Plain SQL `dbx.Migrate`. kafkajs, no Fastify. React 19.3, Vite 8 |
| C16 | §7.18 `:1335`; Q23 `:1396` | No search datastore; `platform-ts` fan-out | `services/search` (own DB, Kafka consumer) |
| C17 | §7.18 `:1333`; `:1407` | Gap-free counter never over HTTP; kernel `sequence` package | `services/numbering` HTTP service. No `platform/go/sequence` |
| C18 | §7.14 `:1297`; Observability `:7726-7730` | OTel SDK in every service; Tempo and Loki | No OTel deps. Tempo and Loki not deployed (`deploy/observability/README.md:5`) |
| C19 | §7.20 `:1347-1350` | PDBs everywhere; Argo Rollouts canary; HPA | 0 PDBs, no Rollouts, HPA in 3 charts. Prod Argo deploys only hello-ledger + kyverno |
| C20 | §9 Q4 `:1377`; Q20 `:1393`; spine "Open Questions" ("`iot-edge` deployable has no owner") | Valkey unresolved; multi-region out of scope | Valkey deployed for rate limits. Multi-region active/passive built (`deploy/multi-region`). iot-edge exists (NOT-IMPLEMENTED) |
| C21 | Repository Topology `:7709-7720` | `packages/@depot/*`; `services/<name>-ts (Fastify + Temporal workers)`; module federation remotes; `apps/<pwa>/` shopfloor | No `packages/` dir. No Fastify. Federation pending (`apps/web/src/shell/README.md`). Shopfloor is a web module. Add `apps/ess`, `apps/developer-portal` (stubs), `tools/depot-server`, `deploy/windows` |
| C22 | Whole doc | No Windows distribution | Add a section citing ADR-0004 (once accepted): `tools/depot-server` built-in gateway, gateway secret, MSI |
| C23 | §3.3 `:326` | Go engine jobs avoid a second workflow SDK | Still true (only `workflow-ts` uses Temporal). Job APIs are per service. No `/v1/platform/jobs` (`:378`) |
| C24 | §5.1 `:375-379` | `X-Depot-Api-Build` and `RateLimit-*` on every response | Not set by the kernel `httpx`. Only some services and mcp-ts set them |
| C25 | `docs/architecture.compiled/manifest.json:3-6`, `spine.md` "Compiled Provenance" | sha `7db7f82e…`, 7,844 lines, Mac path | Recompile against the corrected doc |

---

## 6. Auditability and end-user impact

### 6.1 Auditability gaps

1. **No complete immutable change trail.** The designed CDC-derived, hash-chained `platform.audit_entry` does not exist. `services/audit` hash-chains only what is POSTed to it, and only `engineering` posts (`services/engineering/internal/api/audit.go`). Eleven services keep their own audit tables. Only audit and shopfloor chain hashes. No head anchoring to object-locked storage was found. Financial documents are protected by insert-only triggers (12 services, including `ledger/migrations/0003_journal.sql` and `inventory/migrations/0001_inventory_ledger.sql`), but who-changed-what for mutable master data (vendor bank accounts, price conditions, credit limits) depends on per-service code.
2. **Actor and trace are lost across async hops.** There is no `depot.ctx` logical message and no `traceparent` or `sequence` in the envelope (`events.go:15-33`). `traceId` in problem responses is a chi request id, not a W3C trace id (`httpx.go:147`). With no OTel, there is no end-to-end trace from a UI click through the posting to the journal event.
3. **Authorization decisions are not logged.** OPA decision logs are off (`deploy/policies/opa/README.md:12`; this is also R4-13), and in Kubernetes no OPA decision is made per request. Duty checks happen in each service's `RequireDuty` with no central denial metric (`depot_authz_denied_total` is designed in Observability `:7731` but not found).
4. **Events for ledger, inventory, tax, treasury, close and payables never reach Kafka** (D2). Downstream audit, lakehouse (`data/cdc`) and reconciliation consumers therefore see no journal, valuation or tax events.
5. **Retention classes (`fiscal_7y`, `standard_3y`, `agent_7y`) are not implemented.** Kafka retention defaults to 7 d. Replaying from Kafka beyond a week is impossible, and there is no event-replay API (`/v1/integration/event-replays`).
6. **Traceability from requirement to code is mixed.** Each service has `NOT-IMPLEMENTED.md` tied to DPT tickets (good), but the architecture's endpoint and topic catalogues no longer map to code (§2.3). The compiled contracts are stale (D27), so the contract gate checks a superseded design.
7. **Identity provenance in Kubernetes rests on network isolation** that is configured for only 13 of 69 services (D1). A forged `X-Depot-User` from any pod in `hello-ledger` would be recorded as a genuine user in every audit row.

### 6.2 End-user impact

1. **Latency paths.**
   - `ledger.Post` is a synchronous HTTP call from 19 services (`ledgerclient` users: assets, billing, budgeting, close, consolidation, costacct, costing, eam, expense, intercompany, inventory, payroll, productcost, production, projects, receivables, revrec, tax, treasury).
   - Gap-free numbers come over HTTP from `numbering` for hr and party (D21). That adds a hop and a burn-on-abort risk.
   - Journals are not partitioned (D10), so posting and trial-balance latency will degrade with volume. NFR-P1 (5,000 lines/s, p99 < 200 ms) is not demonstrable on this schema.
2. **No composition layer.** No web-bff GraphQL read gateway and no `/bff/v1/me`, `/permissions` or `/stream`. Web modules call several services per page from the browser: sales calls party, pricing and sales; receiving calls procurement, receiving and warehouse; purchasing calls edi, procurement, product and punchout (`rg '/v1/' apps/web/src/modules/<m>`). Each is a separate round trip through Envoy.
3. **No generic live updates.** No U4 server-sent-events stream exists. SSE exists only in mobile and agents-ts, and there is no `/v1/platform/jobs` with `statusUrl` (only `migration` returns one). Long operations give inconsistent feedback across modules.
4. **Inconsistent error contract.**
   - The kernel `code` holds service codes (`AUTH-0003`), not the snake_case catalogue, and has no `serviceCode`.
   - Portal BFFs return `{error:"invalid_request"}` with a problem+json content type.
   - routing-py returns FastAPI `{"detail":…}`.
   - `errors` is a map, not `errors[]` with `{field, code, message}`.
   - The web shell's `ProblemMessage` catalogue (`apps/web/src/shell/errors/catalog.ts`) mitigates this for its own services, but partners and the MCP gateway see three shapes.
5. **Inconsistent client contract.** `Idempotency-Key` is documented on only 44% of mutating operations and `If-Match` in 35 of 64 specs. Clients (and the MCP tool generator in mcp-ts) cannot rely on safe retry or optimistic concurrency uniformly. Custom methods mix `:verb` (230 paths) and `/verb` (40 paths, e.g. hr, assets, projects, ledger `/journal-entries/{id}/reverse`).
6. **Permission changes are slow to apply.** Duties in a 5-minute token mean a revoked or granted duty takes up to 5 min to apply, not the designed ≤ 10 s. Users see stale navigation and actions after a role change.
7. **Kiosk and portals are not deployable as designed.** No `apps/shopfloor` PWA, and no images for the portal SPAs or `apps/web` ("no image hosts them yet", `deploy/gateway/README.md`, portal section).
