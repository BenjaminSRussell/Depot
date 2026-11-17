# ADR-0013: Global search is its own service with a Postgres trigram index fed by events

**Status:** Accepted — 2026-11-17 (records a decision already made in code; SDLC run 1, stage 2, deviation D20; closes architecture Q23)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` §3 row 20, §4 D20, §5 C16

## Context

The architecture (§7.18, data §3 and §11.4, open question Q23) designed global search without a new datastore:
`pg_trgm` indexes in each context and a fan-out by `platform-ts` that ranks the results, revisited "only through an
ADR" if fan-out could not meet the ranking need.

`services/search` (#506) is built differently (`services/search/README.md`):

- `GET /v1/search?q=&types=&limit=` over parties, products, purchase orders, receipts, vendor invoices, sales orders,
  packing slips, customer invoices, production orders and workers.
- It owns a Postgres database with a `pg_trgm` index, kept current by a Kafka consumer (`search consume`,
  `deploy/kafka/search-consumer.yaml`) that reads the source services' outbox events through the kernel's
  `eventing.Consumer` (idempotent inbox, dead-letter topic).
- The README records the rejected alternative: an HTTP ingest route pushed by each source.

## Decision

Global search is a separate service with its own Postgres trigram index, built from the domain events the source
services already publish. Results are filtered by tenant (RLS) and by the caller's legal-entity scope and duties at
query time. Per-context typeahead (`?q=` on a service's own list endpoints) stays in each service. No search engine
(OpenSearch, Elasticsearch) is introduced.

## Alternatives rejected

- **Fan-out from a platform service** (the design). One query becomes ten or more synchronous calls and the ranking is
  done on partial results.
- **A dedicated search engine.** Another stateful product to operate and secure; not needed at the designed volumes.

## Consequences

- Search is fast and ranked in one place, and adds no load to the source databases.
- The index is eventually consistent and only as complete as the events that reach Kafka: a source whose outbox has
  no Debezium connector is never indexed (D2).
- The index holds copies of names and document numbers; erasure (pseudonymization) must reach it too.
- Backlog: verify that every indexed type's source service has an outbox connector, and add a reconciliation that compares index counts with the sources (D2, D20).
- Backlog: make privacy erasure events update or remove the affected search rows, with a test (D20).
- Backlog: declare the consumed topics as `receive` operations in `services/search/events/asyncapi.yaml` (D6).
