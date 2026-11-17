# audit

Append-only field-level audit log for Depot (wave 5). Changes arrive through `POST /v1/audit/changes`, which is the in-process stand-in for a wave-4 Debezium/Kafka consumer (`internal/cdc.Consumer`). Each row is hash-chained; updates and deletes are rejected by a trigger.

Run: `DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/audit`. Test: `go vet ./... && go test ./...`; store tests need `DEPOT_TEST_DATABASE_URL`. RLS proof also needs `DEPOT_TEST_APP_DATABASE_URL` as `depot_app`.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080`, and `:9090` serving `/metrics` only |

## API

| Operation | Duty |
|---|---|
| `POST /v1/audit/changes` | `audit.write`, from a verified service token only (`engineering` holds it through its service grant) |
| `GET /v1/audit/records/{entity}/{recordId}` | `audit.read` |
| `POST /v1/audit/alert-rules` | `audit.write` |
| `GET /v1/audit/alerts` | `audit.read` |
| `POST /v1/audit/reads` | `audit.read-log.write` (service-only: `hr` holds it through its service grant; tenant-level, so the token names no legal entity and callers with ALL scope or no legal-entity claim are recorded too) |
| `GET /v1/audit/reads` | `audit.read-log.view` |
| `GET /v1/audit/reads:export?format=csv\|ndjson` | `audit.read-log.export` |
| `GET /v1/audit/reads:verify` | `audit.read-log.export` |
| `GET /v1/audit/reads:break-glass?status=pending\|reviewed\|all` | `audit.read-log.review` |
| `POST /v1/audit/reads/{id}:review` | `audit.read-log.review` |
| `POST /v1/audit/texts/expand` | authenticated |
| `GET /healthz`, `GET /readyz`, `GET /metrics` on the admin port | public |

Contract: `api/openapi.yaml`. Event: `dev.depot.audit.change.recorded.v1`.

Wave 3 entity metadata is not imported; `entity` is an opaque name. Wave 4 Kafka is not imported; a future consumer calls `cdc.Consumer.Apply`.

## Who made a change (B-06, AUD-API-1, FR-5.10)

What is guaranteed for every change recorded from this release on:

- **Only a verified service records a change.** `POST /v1/audit/changes` needs a service token (`X-Depot-Service-Token`, ADR-0010) that the audit service verified itself. A call with identity headers only (`X-Depot-User`, `X-Depot-Duties`) is refused with 403 AUD-0007: audit has no gateway route, no NetworkPolicy and no gateway secret in Kubernetes, so those headers prove nothing here. No person calls this route today; changes made by people directly reach the trail through CDC (B-07), which will record `actorKind` `person`.
- **The actor is the verified caller, never a body value.** A body that sets `userId` (any value, the caller's own included) is refused with AUD-0002, as for the read log. The service derives the actor from the verified token:

| Token | `userId` | `actorKind` | `onBehalfOf` |
|---|---|---|---|
| `act` is a service principal (`service:<name>`, accepted only from that principal's own issuer) | `service:<name>` | `service` | empty |
| `act` is an end user | `service:<calling service>` | `service` (`agent` when the issuer is `agents`) | the end user |

- **The body can only restate the actor.** `actorKind` and `onBehalfOf` are optional. `onBehalfOf` from a service acting on its own, or naming anyone but the user the token carries, is refused with AUD-0006, and so is an `actorKind` that differs from the derived one. `onBehalfOf` is the user the calling service put in its signed token: the audit service verifies which service vouches for that user, not the user's own sign-in. The History panel can therefore show "Recorded by Billing service for Ana Ruiz".
- **The database enforces the same shape** for rows with an `actorKind`: a service or agent is a `service:` id and a person never is, and only a service or agent has an `onBehalfOf`.

A change also carries `reason` (up to 1,000 characters), `changeSetId` (the field changes of one save), `eventType` and `eventId` (the business event that carried it) and `workflowInstanceId` (each up to 256 characters). Limits count characters, as OpenAPI `maxLength` and Postgres `length()` do; text must be valid UTF-8 without NUL. These are columns of `audit_entry` (migrations `0005_change_actor_context.sql`, columns and `NOT VALID` checks under a 5 s lock timeout, and `0006_change_actor_context_validate.sql`, which validates them without the exclusive lock). They are returned by `GET /v1/audit/records/{entity}/{recordId}` and published on `dev.depot.audit.change.recorded.v1`, absent when empty in both. Appends are serialised per tenant with an advisory lock, as for the read log, so concurrent changes cannot fork the chain. `occurredAt` is stored in UTC truncated to microseconds (the precision of `timestamptz`), and the hash is computed over that value, so every row's hash recomputes from the database.

**Hash encoding, for outside verifiers.** `hash` is the lowercase hex SHA-256 of one of two payloads, chosen per row:

- *v2*, when any of `actorKind`, `onBehalfOf`, `reason`, `changeSetId`, `eventType`, `eventId`, `workflowInstanceId` is non-empty (every row this API writes): the RFC 8785 (JCS) serialization of the JSON array of strings `["audit-change-v2", prevHash, entity, recordId, field, oldValue, newValue, userId, actorKind, onBehalfOf, traceId, occurredAt, reason, changeSetId, eventType, eventId, workflowInstanceId]`. In practice: no whitespace; in strings only `"` and `\` are escaped, plus `\b \t \n \f \r` and `\u00xx` (lowercase hex) for the other control characters; everything else (`<`, `>`, `&`, U+2028, U+2029, non-ASCII) is written as UTF-8. `occurredAt` is UTC, truncated to microseconds, in RFC 3339 with trailing fractional zeros dropped (`2026-11-01T09:00:00Z`, `2026-11-01T09:00:00.1234Z`). `json.dumps(values, ensure_ascii=False, separators=(",", ":"))` in Python produces the same bytes; `TestV2HashKnownAnswer` pins a vector.
- *legacy*, for rows written before AUD-API-1 (all of those fields empty): `prevHash|entity|recordId|field|oldValue|newValue|userId|traceId|occurredAt`, joined by `|`, with `occurredAt` in UTC as RFC 3339 with up to nanoseconds. Existing chains keep verifying.

**Rollout (deliberately not N/N-1 compatible with engineering).** A new audit service refuses an old engineering's `userId` (AUD-0002), and an old audit service refuses a new engineering's `onBehalfOf` and `reason` as unknown fields. Deploy audit first, then engineering. Signature records queue in engineering's `audit_outbox` meanwhile and its relay retries with backoff (at most 15 minutes between attempts), so they arrive once engineering is upgraded. Rolling audit back stalls signature delivery until audit is rolled forward again; nothing is lost.

## PHI read-access log (#606, HIPAA overlay)

HIPAA audit controls ask who looked at protected health information, not only who changed it. A service that returns a record with `phi` fields (see `services/metadata`) records the read with `POST /v1/audit/reads`: `entity`, `recordId`, `fields[]`, `purpose` (required, a reason code or free text up to 500 characters), and optionally `traceId` (default: the request id) and `occurredAt` (default: now, at most five minutes ahead). The reader is the verified caller of the request (the `act` of a service token), never the body: a body that sets `userId` is refused with AUD-0002. `sourceService` is the verified service that recorded the read. `platform/go/phiaudit` is the client and the fake for tests.

`phi_read` (migration `0003_phi_read.sql`) is per-tenant, row-level secured and append-only: a trigger rejects update, delete and truncate, and `depot_app` holds only `SELECT` and `INSERT`. Each row carries `prev_hash` and `hash` (SHA-256 over a JSON array of the previous hash and the row, so no value can shift into its neighbour); appends are serialised per tenant with an advisory lock so the chain cannot fork. An edited, removed or reordered row breaks the chain at that position.

`GET /v1/audit/reads` lists rows in chain order, filtered by `entity`, `recordId`, `userId`, `from` (inclusive) and `to` (exclusive), `limit` (1 to 500, default 100) and `cursor` (the `nextCursor` of the previous page). `GET /v1/audit/reads:export?format=csv|ndjson` streams the tenant's rows (same filters) for an auditor. Before streaming it verifies the whole chain from the genesis hash and reports the verdict in the headers `X-Depot-Read-Chain-Verified`, `-Entries`, `-Head` and, when broken, `-Broken-At`; `GET /v1/audit/reads:verify` returns the same verdict as JSON. `-Entries` and `-Head` describe the whole chain that was verified, not the filtered rows. The body's completeness is in the trailers `X-Depot-Read-Export-Rows` and `X-Depot-Read-Export-Complete: true`, sent only after the last row; if the export fails once rows are streaming, the connection is aborted, so a body without the completion trailer is incomplete. An auditor keeps the head hash from each export: a later chain that does not contain it was cut short (a chain alone cannot show that rows were removed from its end). CSV prefixes a cell that starts with `=`, `+`, `-`, `@`, tab or carriage return with an apostrophe so a spreadsheet does not run it as a formula; NDJSON is lossless, so recompute hashes from it. Read-log rows are not published as events: they would copy PHI access metadata onto the bus.

**Break-glass review (HIPAA emergency access).** A read recorded with the reserved reason code `BREAK-GLASS` as its purpose (`BREAK-GLASS` alone, or followed by `:` or a space and the reason) is emergency access: in `hr`, a person without `hr.worker.inquire` reads another worker's benefits under `hr.benefits.break-glass`, a duty kept for emergencies. `GET /v1/audit/reads:break-glass` lists those reads oldest first (`status=pending`, the default, is the review queue; `reviewed`; `all`; `limit`, `cursor` as above), and `POST /v1/audit/reads/{id}:review` with `{"outcome": "APPROVED" | "FLAGGED", "note": "..."}` records a second person's review, once; FLAGGED needs a note. The reviewer is the verified caller and may not be the reader (AUD-0005, as for a read that is not break-glass or is already reviewed; two racing reviews meet at the primary key and the loser gets AUD-0005). The review is a row in `phi_read_review` (migration `0004_phi_read_review.sql`): row-level secured, append-only (trigger; `depot_app` holds `SELECT` and `INSERT`), keyed by the read, so the `phi_read` chain is untouched and still verifies.

Metrics: `audit_phi_reads_recorded_total`, `audit_phi_reads_failed_total`, `audit_phi_read_exports_total`, `audit_phi_break_glass_reviews_total`. To accept `hr` service tokens set `serviceIdentity.publicKeys` in the chart (`DEPOT_SERVICE_NAME=audit`, `DEPOT_SERVICE_PUBLIC_KEYS`).

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body or missing change fields |
| AUTH-0001, AUTH-0003, COM-0500 | 401, 403, 500 | missing tenant, duty missing, internal error |
| AUD-0001 | 422 | domain rejection (invalid change, checkout rules) |
| AUD-0002 | 422 | a read-log or change request named the reader or actor (`userId`); it is always the verified caller |
| AUD-0003 | 400 | invalid read-log filter: time range, `limit`, `cursor` or export `format`, or break-glass `status` |
| AUD-0004 | 404 | a break-glass review named a read this tenant's log does not hold |
| AUD-0005 | 409 | a break-glass review that may not be made: the read is not break-glass, is already reviewed, or the reviewer is the reader |
| AUD-0006 | 422 | a change claimed an `onBehalfOf` or `actorKind` the verified caller cannot vouch for: `onBehalfOf` from a service acting on its own, or not the user the service token carries; an `actorKind` other than the caller's |
| AUD-0007 | 403 | a change was sent without a verified service token; identity headers are not accepted on `POST /v1/audit/changes` |
| COM-0001 | 400 | read-log entry failed validation (missing or over-long `purpose`, `fields`, `entity`, `recordId`; `occurredAt` in the future) |

Metrics: `audit_changes_applied_total`, `audit_changes_failed_total`.
