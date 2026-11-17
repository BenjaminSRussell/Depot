# Platform Kernel

**As built (v1.0):** the Go library `platform/go`, compiled into every Go service (58). There is no TypeScript kernel;
each TS service keeps its own helpers and the cross-language contracts are pinned by shared test vectors (ADR-0008).

## Purpose & Boundaries
Implements the cross-cutting rules once so services hold domain logic only: HTTP plumbing and problem details
(`platform/go/httpx`), database access, migrations and tenant GUCs (`platform/go/dbx`), tenant and actor context from
gateway headers and service tokens (`platform/go/tenant`), duty checks (`platform/go/authz`), the event envelope
(`platform/go/events`), the transactional outbox (`platform/go/outbox`), Kafka consumers with inbox and dead-lettering
(`platform/go/eventing`), money and ids, field encryption (`platform/go/fieldcrypt`), log pseudonyms
(`platform/go/logpseudo`), RLS exemptions (`platform/go/rlsexempt`), the ledger posting client
(`platform/go/ledgerclient`) and test helpers (`platform/go/testkit`). Further packages: cloudprofile, features,
residency, phiaudit, orgclient, fxclient, issueclaim, openingimport, reimbursement, customsdata, xlsx.

## Services
None. The kernel is a library, not a service directory. Every Go service imports it through its `go.mod`.

## Interfaces
- Kernel-standard endpoints in every service: `GET /healthz` (liveness), `GET /readyz` (own database), `GET /metrics` on the admin port (Prometheus RED metrics from `platform/go/httpx`).
- Problem details: `application/problem+json` with `type` (`https://depot.dev/problems/<code>`), `title`, `status`, `detail`, `instance`, `code`, `traceId`, `errors`. As built `errors` is a map, the service code (`AUTH-0001`, `COM-0500`) sits in `code`, there is no `serviceCode`, and `traceId` is the chi request id (D11, D8). Designed: snake_case `code`, separate `serviceCode`, `errors[]`.
- Authorization: `authz.RequireDuty` checks the route's `x-depot-duty` against `X-Depot-Duties` (56 services); `tenant` builds the tenant, legal-entity scopes and actor from `X-Depot-*` headers or an Ed25519 service token (`X-Depot-Service-Token`, ADR-0010); the shell's `X-Depot-LegalEntity` narrows, never widens. Auth age for sensitive actions comes from `X-Depot-Auth-Time` (ADR-0006).
- `ledgerclient`: `POST /v1/ledger/journal-entries` with a deterministic idempotency key (see the ledger component).
- Idempotency: `dbx.IdempotencyDDL` (`tenant_id, key, request_hash, response_status, response_body, created_at`); there is no kernel middleware, so 37 services handle `Idempotency-Key` themselves and only close, processmfg and receiving hold a lease (D12). Designed: middleware with `state`, 60 s lease, 24 h expiry, `409 idempotency_in_progress`, `422 idempotency_key_reuse`, `Idempotent-Replayed: true`.
- Concurrency: `If-Match`/ETag per service where declared (35 of 64 specs); no kernel enforcement.

## Data Ownership
Infrastructure DDL emitted into each service database: `outbox` (`id, tenant_id, aggregate_type, aggregate_id, type,
payload, created_at`; no `partition_key` or `headers`, D19), `consumer_inbox`, `idempotency_keys`, RLS policies from
`dbx.RLSPolicy` and `dbx.LegalEntityScopePolicy` (`<table>_tenant_access`, `<table>_legal_entity_scope`). No
`number_sequence_counter` (numbering is a service, ADR-0015) and no `audit_event` (D3).

## Events Produced
`outbox.Enqueue` writes the event in the caller's business transaction. The envelope (`platform/go/events`) has
`specversion, id, source, type, subject, time, datacontenttype, data` and the extensions `tenantid, correlationid,
causationid, actor, legalentityid`; the type regex matches `dev.depot.<context>.<entity>.<verb>.v<n>`. *Target — not
built (D19):* `dataschema`, `traceparent`, `sequence`; *(D3)* the `depot.ctx` logical-decoding message.

## Events Consumed
`platform/go/eventing`: consumer group per service, `consumer_inbox` dedupe in the effect transaction, up to 10
attempts with exponential backoff capped at 2 min, then `<topic>.dlq` with `x-origin-*`, `x-dlq-reason`,
`x-dlq-detail` headers. No `sequence`-gap parking.

## Dependencies
internal: none (everything depends on it). external: PostgreSQL 17 (pgx/v5), Kafka (franz-go), optional Azure Key
Vault for wrapping `fieldcrypt` master keys (ADR-0011).

## Gotchas & Constraints
- `dbx.WithTenantTx` sets `SET LOCAL depot.tenant_id` and `depot.legal_entity_scopes` per transaction; `depot_app` has no BYPASSRLS, so a missing GUC yields zero rows. Exemptions need a reason in `platform/go/rlsexempt` and pass `tools/rlscheck`.
- Migrations are hand-written SQL applied by `dbx.Migrate` under an advisory lock when `MIGRATE_ON_START=true` (dev); no Atlas, no PreSync Job (ADR-0012, D28).
- Config is environment variables: `DATABASE_URL`, `MIGRATE_ON_START`, `HTTP_ADDR`, `ADMIN_ADDR`, Kafka settings, sibling base URLs, `DEPOT_SERVICE_*` signing keys (ADR-0010), `FIELDCRYPT_*` (ADR-0011), optional `DEPOT_GATEWAY_SECRET` and `DEPOT_CLOUD_PROFILE`. `OPA_URL`, `OIDC_ISSUER_REGISTRY_URL` and `OTEL_*` are not read.
- Logging: JSON `log/slog`; access log uses `requestId`, other logs `correlationId`; keyed pseudonyms under the EU profile or `LOG_PSEUDONYMIZE=true` with the frozen domain `passage.log.pseudonym.v1` (ADR-0005).
- Frozen `fieldcrypt` labels `passage.fieldcrypt.v1` and `passage.fieldcrypt.mac.v1` must not change without a `.v2` label and re-encryption (ADR-0005).
- Money is a decimal string plus currency, never float (`platform/go/money`).
- The ownership rule still holds: waves do not edit `platform/`; kernel changes go through its owner.

## Target — not built
- Kernel idempotency middleware with leases and expiry (D12); uniform problem details with `serviceCode` and `errors[]` (D11).
- OpenTelemetry SDK, W3C trace ids in problem details and logs, `traceparent` on calls and events (D8, D19).
- Kernel `sequence` package and per-database gap-free counters: superseded by the numbering service (ADR-0015); gap-free reserve/confirm/void is D21.
- `depot.ctx` actor/trace message for CDC audit (D3); envelope `sequence` and `dataschema` (D19).
- A shared TypeScript package or conformance tests for service tokens, problem details and idempotency (ADR-0008 backlog).

## Source Sections
- Architecture §7.3 tenant propagation, §7.5 idempotency, §7.6 outbox and inbox, §7.8 configuration, §7.11 health, §7.13 logging, §7.16 audit and PII, §7.18 data layer; data §4.5 RLS; events §2 envelope.
- ADR-0005, ADR-0006, ADR-0008, ADR-0010, ADR-0011, ADR-0012, ADR-0015; delta §2.1 (platform-kernel row), §2.2.
