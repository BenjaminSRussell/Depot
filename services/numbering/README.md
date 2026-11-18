# numbering

Number sequence service for Depot (wave 5). Issues per-legal-entity numbers in `gap_free`, `continuous` or `non_continuous` mode. Gap-free allocation takes a row lock and is proven by a 10k-concurrent Jepsen-style test.

Run: `DATABASE_URL=postgres://... MIGRATE_ON_START=true go run ./cmd/numbering`. Test: `go vet ./... && go test ./...`; store tests need `DEPOT_TEST_DATABASE_URL` (role without BYPASSRLS). RLS proof also needs `DEPOT_TEST_APP_DATABASE_URL` as `depot_app`.

## Configuration

| Variable | Meaning |
|---|---|
| `DATABASE_URL` | required; Postgres URL, from a Secret in the cluster |
| `MIGRATE_ON_START` | `true` applies embedded migrations at start (dev only) |
| `HTTP_ADDR`, `ADMIN_ADDR` | default `:8080`, and `:9090` serving `/metrics` only |

## API

| Operation | Duty |
|---|---|
| `POST /v1/numbering/sequences` (optional `Idempotency-Key`) | `numbering.write` |
| `GET /v1/numbering/sequences/{code}` | `numbering.read` |
| `POST /v1/numbering/sequences/{code}/next` (optional `Idempotency-Key`) | `numbering.write` or `numbering.sequence.allocate` |
| `POST /v1/numbering/sequences/{code}/reserve` (optional `Idempotency-Key`, `leaseSeconds` 1 to 3600, default 300) | `numbering.write` or `numbering.sequence.allocate` |
| `POST /v1/numbering/reservations/{id}/confirm` (`documentRef`) | `numbering.write` or `numbering.sequence.allocate` |
| `POST /v1/numbering/reservations/{id}/void` (`reason`) | `numbering.write` or `numbering.sequence.allocate` |
| `GET /v1/numbering/sequences/{code}/voided` (voided-number log) | `numbering.read` |
| `GET /healthz`, `GET /readyz`, `GET /metrics` on the admin port | public |

A caller that holds a service token cannot take a number from a `gap_free` sequence with `next` (`NUM-0004`): it reserves the number, then confirms it once its own transaction commits or voids it when that transaction fails. A reservation neither confirmed nor voided within its lease is voided as `lease_expired` the next time the sequence is reserved, confirmed or read for its voided log. A voided number is never offered again and is listed in the voided-number log.

Contract: `api/openapi.yaml`. Events: `dev.depot.numbering.sequence.allocated.v1`, `dev.depot.numbering.reservation.confirmed.v1`, `dev.depot.numbering.reservation.voided.v1`. The legal-entity header is `X-Depot-Legal-Entity`.

OPA duties `numbering.sequence.view` (GET) and `numbering.sequence.maintain` (GET, POST) authorize `/v1/numbering` and its child paths.

## Error codes

| Code | Status | Meaning |
|---|---|---|
| COM-0001 | 400 | malformed body, unknown field, failed field validation |
| AUTH-0001, AUTH-0003, COM-0500 | 401, 403, 500 | missing tenant, duty missing, internal error |
| NUM-0003 | 404 | sequence not found |
| NUM-SEQ-DUPLICATE | 409 | code already used for the legal entity |
| idempotency_key_reuse | 422 | Idempotency-Key reused with a different body |
| NUM-0002 | 422 | padded format width exhausted |
| NUM-0004 | 409 | `next` refused for a service caller on a `gap_free` sequence; reserve, confirm or void |
| NUM-0005 | 409 | the lease expired before confirm; the number was voided |
| NUM-0006 | 409 | the reservation is already confirmed or voided |
| NUM-0007 | 404 | reservation not found |

Metrics: `numbering_allocated_total`, `numbering_failed_total`, `numbering_reserved_total`, `numbering_confirmed_total`, `numbering_voided_total`.

The inventory of fiscal document number allocations is `internal/store/fiscal_numbers.json`; `TestFiscalNumberInventory` fails when a new allocation site appears unclassified.
