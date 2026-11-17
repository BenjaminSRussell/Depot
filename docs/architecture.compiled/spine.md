# Architecture Spine: Depot ERP Platform (v1.0 as built)

Compiled from `docs/architecture.md` v1.0 "as built" (SDLC run 1, stage 2) and ADRs 0001–0015. Provenance (hash, line
count, compiler) is in `manifest.json`; endpoints and topics are generated from the specs into `contracts.json`. Read
the markers as the architecture does: **As built** is what the code does; *Target — not built* is design intent with a
gap id (D-nn, see the architecture's "Known gaps"); *Superseded by ADR-00NN* means the ADR is authoritative.

## Service Inventory & Boundaries
As built: 71 service directories under `services/` (58 Go, 7 TypeScript, 6 Python) grouped into the 15 designed
contexts (ADR-0008), plus 7 directories under `apps/`. The deployable is a capability service, not a context: each
stateful service owns its database (`depot_<service>`, plain PostgreSQL 17, ADR-0012), its `api/openapi.yaml`, its
`events/asyncapi.yaml`, its chart under `services/<dir>/charts/` and a `NOT-IMPLEMENTED.md`. 69 services have a chart
(not `services/dispatch-py`, `services/routing-py`). `ledger.Post` (`POST /v1/ledger/journal-entries` through
`platform/go/ledgerclient`, 19 callers) is the one designed synchronous cross-context write; as built, services also
call each other's REST APIs synchronously (close period checks, tax, numbering).

Component → owned directories (the authoritative list is `manifest.json`, built from
`scripts/architecture_components.json`; `--check` fails if a directory is unowned or owned twice):

- platform-kernel — `platform/go` library (no service directory)
- edge-identity — Envoy Gateway, Keycloak realm `depot`, OPA policies under `policies/authz`; config in `deploy/gateway`, `deploy/keycloak` (no service directory)
- platform — identity, metadata, numbering, audit, batch, org, compliance, ops, localization, search, print, platform-health (Go) and workflow-ts (the one Temporal worker)
- party — party (Go)
- product — product, engineering (Go), configurator-py
- ledger — ledger, fx, tax, close, consolidation, finreport, budgeting, costacct, intercompany (Go)
- inventory — inventory, costing, productcost, quality (Go)
- procurement — purchasing, receiving, payables (Go)
- manufacturing — production, planning, scheduling, shopfloor, lean, processmfg, iot-edge (Go), scheduler-py, forecast-py
- sales — sales, pricing, billing, receivables, revrec, fulfillment (Go), prediction-py
- warehouse — warehouse, transport, mobile (Go), carrier-gateway-ts, routing-py
- treasury — treasury, assets (Go)
- workforce — hr, payroll, expense (Go)
- projects — projects, fieldservice, eam (Go), dispatch-py
- integration — edi (Go), integration-hub-ts, `apps/developer-portal` (stub)
- migration-framework — migration (Go; grouped under the integration context in architecture §3.2), `tools/migration-factory`
- analytics — analytics (Go) and the `data/` assets (lakehouse not deployed)
- agents — agents-ts (agent runtime, copilot), mcp-ts (MCP gateway at `/mcp`)
- portal-bff — customer-portal-ts, supplier-portal-ts (two cookie-session BFFs; the "edge" group of §3.2)
- web-shell — `apps/web` (React 19 shell with in-tree modules) and `apps/ess` (a shell module, README only)
- pwa-apps — `apps/warehouse-mobile`, `apps/technician`, `apps/customer-portal`, `apps/supplier-portal`
- web-bff, device-bff — **not built** (ADR-0009): the shell is a public OIDC client with tokens in memory; devices use `services/mobile` and `services/shopfloor`
- eventing-backbone — outbox tables, 21 Debezium outbox connectors (`deploy/kafka/connect.yaml`), Kafka (Strimzi), Apicurio, `platform/go/eventing`, Temporal
- testing-reconciliation — CI gates, `tools/rlscheck`, `load/`, `evals/`, per-service reconciliation endpoints
- deployment-cicd — `deploy/` (Argo CD app-of-apps, charts, Kyverno, observability, Windows MSI), `.github/workflows/`

No service directory is named after a designed deployable that does not exist: there is no `web-bff`, `device-bff`,
`portal-bff`, `platform-ts`, `party-ts`, `procurement-ts`, `manufacturing-ts`, `sales-ts`, `warehouse-ts`,
`workforce-ts`, `projects-ts`, `integration-ts`, `analytics-ts`, `agents-py` or `model-serving-py`.

## Tech Stack Decisions
- Go 1.26 services on chi, pgx/v5, shopspring/decimal, franz-go consumers, `log/slog`; one kernel `platform/go` (ADR-0008). No OpenTelemetry SDK (D8).
- TypeScript only where its ecosystem is the reason: Node 24 on `node:http` with few dependencies; Temporal TS SDK and kafkajs in workflow-ts, kafkajs in integration-hub-ts. No shared TS kernel (ADR-0008). No Fastify, zod, Kysely or pino.
- Python only for numerical and ML work (FastAPI, OR-Tools), stateless except prediction-py.
- PostgreSQL 17 (`postgres:17-alpine`), database per stateful service, hand-written SQL migrations applied by `dbx.Migrate`; no CloudNativePG outside the undeployed `deploy/multi-region` chart, no PgBouncer, no Atlas (ADR-0012).
- Kafka 4.0 on Strimzi, Debezium outbox connectors, Apicurio Registry; Temporal with its own Postgres (`deploy/temporal-postgres`).
- Keycloak with one realm `depot` (ADR-0007); duties in the access token; Envoy Gateway validates the JWT and maps claims to trusted `X-Depot-*` headers (ADR-0006); Ed25519 service tokens between services (ADR-0010).
- Application-level field encryption `platform/go/fieldcrypt` with blind indexes (ADR-0011); search service with a trigram index (ADR-0013); Valkey for gateway rate-limit counters and portal sessions only (ADR-0014); central numbering service over HTTP (ADR-0015).
- Web: React 19 + Vite 8, in-tree modules, module federation pending; public OIDC client `depot-web` with PKCE (ADR-0009).
- Observability: Prometheus RED metrics from `platform/go/httpx`, Grafana and Alertmanager in dev (`deploy/observability`); Tempo and Loki not deployed.
- Single-host Windows install: `tools/depot-server` and a WiX MSI (ADR-0004, proposed).

## Naming Conventions
- Monorepo: `platform/go`, `services/<dir>` with `-ts` and `-py` suffixes, `apps/<app>`, `deploy/`, `tools/`, `data/`, `policies/`. Service layout: `cmd/`, `internal/`, `migrations/`, `api/openapi.yaml`, `events/asyncapi.yaml`, `charts/<chart>/`, `NOT-IMPLEMENTED.md`.
- REST, as built: each service owns `/v1/<service-prefix>/...` (`/v1/receiving`, `/v1/consol`, `/v1/controlling`, `/v1/product-costing`, ...); Envoy rewrites `/api/v1/<svc>` to `/v1/<svc>` per HTTPRoute (`deploy/gateway/README.md`). Exceptions to `/v1`: agents-ts and prediction-py (`/api/v1`), edi (`/edi/v1`), print (`/print/v1`), mcp-ts (`/mcp`) (D22). Lifecycle actions use colon custom methods or sub-resources per spec. Health `/healthz`, `/readyz`, `/metrics`. Every operation carries `x-depot-duty` (about 98% do; `contracts.json` lists the duty per operation).
- Events: CloudEvents type `dev.depot.<context>.<entity>.<verb>.v<n>` (kernel regex matches the design); topics as built are per service, `depot.<service>.<entity>` (for example `depot.receiving.receipt`, `depot.wms.work`), not per context (ADR-0008); DLQ `<topic>.dlq`; consumer groups per service with a `KafkaUser` in `deploy/kafka/`.
- DB: `tenant_id` first, primary key `(tenant_id, id)`; enums `text` + `CHECK`; money `numeric` + `currency`; RLS policies `<table>_tenant_access` (permissive) and `<table>_legal_entity_scope` (restrictive).
- Posting idempotency key `post:<context>:<documentType>:<documentId>:<purpose>[:<revision>]` (not uniform yet, D9). Error `type` `https://depot.dev/problems/<code>`.

## Cross-Cutting Non-Negotiables
- Tenancy: `dbx` begins every transaction with `SET LOCAL depot.tenant_id` and `depot.legal_entity_scopes`; RLS forced in every service's migrations; app role `depot_app` without BYPASSRLS; exemptions allowlisted in `platform/go/rlsexempt` and checked by `tools/rlscheck`. Legal-entity policy missing in some services (D17).
- Auth: Envoy validates the token (`aud depot-api`, RS256, JWKS) and maps claims to `X-Depot-*` headers; services check the route's duty with `authz.RequireDuty` and trust the headers, which is safe only with ADR-0006 rule 6 network isolation (not met, D1). OPA decides only in identity and the single-host gateways (D23). Step-up is an auth-age check on `X-Depot-Auth-Time`. Service-to-service calls carry Ed25519 service tokens whose `act` is the end user (ADR-0010).
- Event envelope: CloudEvents with `tenantid, correlationid, causationid, actor, legalentityid`; outbox row written in the business transaction; consumers dedupe with `consumer_inbox`, retry up to 10 times with backoff capped at 2 min, then dead-letter. *Target — not built:* `traceparent`, `sequence`, `dataschema`, `tenantid:subject` key (D19).
- Error envelope: RFC 9457 problem details from `platform/go/httpx`; as built `errors` is a map and the service code sits in `code` (D11).
- API rules: `Idempotency-Key` handled per service (no kernel middleware, D12); `If-Match` in about half the specs; no shared jobs contract or `statusUrl` outside migration (D29).
- Financial: posted fiscal documents are protected by insert-only triggers; corrections by reversal. *Target — not built:* range-partitioned journal (D10), valuation reservation and posting-request registry (D30), sagas S3b and S4 (D13).
- PII: sensitive fields sealed with `fieldcrypt`; erasure by pseudonymization; three frozen `passage.*` cryptographic labels (ADR-0005). No CDC-fed audit chain (D3); retention classes not built (D24).
- Observability: Prometheus metrics and `/healthz`, `/readyz` everywhere. *Target — not built:* OpenTelemetry tracing and W3C trace ids (D8).
- Deployment: Argo CD app-of-apps with digest pinning; dev runs the platform, stage lacks the backbone and identity, prod deploys only `hello-ledger` and `kyverno` (D4); no PDBs, Rollouts or migration Job (D18, D28); 63 services share namespace `hello-ledger`.
- AI: agents act for a user through `mcp-ts` and `agents-ts`; the AI design was not audited beyond these deployables.

## Open Questions
- Gaps to close are the architecture's "Known gaps" table (D1–D4, D6–D13, D17–D19, D21–D24, D28–D30); each becomes a backlog item, and each accepted ADR lists its follow-up work as `Backlog:` bullets.
- ADR-0009 conditions (strict CSP, no tokens in web storage, ≤ 5-minute access tokens, first-party scripts) must hold before `apps/web` is served to real users (D7); its triggers decide when a web BFF is needed.
- ADR-0015 limits central numbering to non-fiscal numbers until gap-free reserve/confirm/void exists (D21).
- Event egress topic naming and tenant isolation on shared topics (Q5); agent on-behalf-of delegation; Temporal namespace per tenant; external adapters (OCR, e-invoice access points, payment provider, WORM store).
- Unresolved review items R4-1..R4-13 carried from `docs/architecture-review-r4.md`.

## Compiled Provenance
See `manifest.json` (`source.sha256`, `source.lines`, `compiler`, `created`). Rebuild with
`python3 scripts/compile_architecture.py --write`; CI runs `--check` and fails on drift (`docs/architecture.compiled/README.md`).
