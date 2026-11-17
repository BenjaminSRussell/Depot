# ADR-0016: Trace context rides in the event, and the Kafka key stays aggregate_id (D19)

**Status:** Accepted — 2026-11-17 (SDLC run 1, B-11 slice b; closes the key decision of architecture gap D19)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` D8 and D19, `docs/architecture.md` event envelope, `deploy/kafka/connect.yaml`

## Context

The event envelope must carry `traceparent`, `sequence` and `dataschema`, and the Kafka record key must be decided. The
architecture names `tenantid:subject` as the key target; the 59 outbox connectors key on `aggregate_id` (FR-4.1). Only
some outbox tables have a `headers` column, and an insert naming a missing column fails.

## Decision

1. **Trace fields live in the event JSON, not in a `headers` column.** `outbox.Enqueue` stamps `traceparent` (a child of the
   request or consumer span in its context, when there is one), `sequence` and `dataschema` into the CloudEvent it already
   stores in `outbox.payload`. With `table.expand.json.payload` that JSON is the record value, and the relay enricher copies
   the three attributes to `ce_*` headers. A caller that already set a value keeps it. No migration is needed.
2. **`sequence` is the aggregate version when the caller has it** (at least 1). A caller that sets `evt.Sequence` is kept.
   Otherwise `Enqueue` uses a process-monotonic microsecond counter: at least 1, strictly increasing within a process,
   no query and no lock. It is not a cross-pod order; a consumer rule that drops an event whose `sequence` is not above a
   stored one must wait until callers pass the aggregate version.
3. **The Kafka key stays `aggregate_id`.** The key only scopes partition and ordering. Two tenants that collide on a
   non-UUID aggregate id are ordered together, which loses no per-aggregate ordering, and tenant isolation is enforced at
   decode by the `tenantid` header check. This supersedes the architecture's `tenantid:subject` target for the key.

## Alternatives rejected

- **Writing a `headers` column.** Absent from most outbox tables; a migration sweep across every service for three attributes.
- **A per-aggregate `max(sequence)+1` query.** Most outbox tables have no index on `(aggregate_type, aggregate_id)` and the
  table is not pruned here, so the query scans.
- **`tenantid:subject` as the key.** Needs a real `partition_key` column in every outbox table, because pgoutput does not
  replicate generated columns (batch's `partition_key` is one).

## Consequences

- Consumers read `traceparent`, `sequence` and `dataschema` through `eventing.Decode` in both structured and binary mode, and
  `eventing.Consumer` continues the event's trace as a child span.
- Backlog: callers with an aggregate version set `evt.Sequence`; the OpenTelemetry SDK, Tempo and the collector replace the
  id source behind `platform/go/tracectx` (slice a of B-11).
