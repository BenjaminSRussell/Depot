# Eventing Backbone

**As built (v1.0):** transactional outbox tables in 58 services, relayed by 21 Debezium outbox connectors
(`deploy/kafka/connect.yaml`) to Kafka 4.0 on Strimzi, with Apicurio Registry; consumers in 17 Go services plus
`workflow-ts` and `integration-hub-ts`; Temporal with one worker, `workflow-ts`. 37 services write an outbox that no
connector relays (D2), there is no table-CDC connector (D3), and no saga is built (D13).

## Purpose & Boundaries
Carries every cross-service effect except `ledger.Post` and the synchronous peer calls: write the event with the state
change, relay it, deliver it at least once, dedupe on the consumer, dead-letter what fails. Runs the multi-step
workflows (approvals, tender, payment run, ECO, CAPA, budget plan, party merge, agent runs) in Temporal. Topics are
per service, not per context (ADR-0008).

## Services
None of its own. Infrastructure is in `deploy/kafka` (Strimzi Kafka, connectors, one `KafkaUser` per consumer
service, `deploy/kafka/registry.yaml`), `deploy/kafka-connect` (Connect image with the enricher SMT), `deploy/temporal`
and `deploy/temporal-postgres`; libraries in `platform/go/outbox`, `platform/go/events` and `platform/go/eventing`.
The Temporal worker `services/workflow-ts` is owned by the platform component.

## Interfaces
- Envelope (CloudEvents 1.0, structured JSON): `specversion, id, source, type, subject, time, datacontenttype, data` plus `tenantid, correlationid, causationid, actor, legalentityid` from `platform/go/events`. Pattern `event-envelope` also requires `dataschema`, `traceparent` and `sequence`: the Go envelope lacks all three (D19). On relayed topics the connector's enricher SMT fills `dataschema` from the template `https://schemas.depot.dev/{type}.json`, flattens an optional `headers` object into extensions and copies attributes to `ce_*` Kafka headers.
- Event type `dev.depot.<context>.<entity>.<verb>.v<n>` (pattern `event-type-pattern`, matches; the enricher rejects records that do not match).
- Topics `depot.<service>.<entity>` (pattern `topic-naming`: most match; exceptions are hyphenated names such as `depot.shopfloor.registration-corrected` and the four-segment `depot.service.workorder.completed`). The EventRouter routes `aggregate_type` to the topic and keys on `aggregate_id`, not `tenantid:subject` (D19). The full topic list with publishers and subscribers is generated into `contracts.json`.
- Dead letters: `<topic>.dlq` after 10 attempts (exponential backoff capped at 2 min) with headers `x-origin-*`, `x-dlq-reason`, `x-dlq-detail` (pattern `dlq-record-headers` designed `dlq-error-class`, `dlq-attempts`, `dlq-consumer`, one DLQ per consumer: drift).
- Replay: batch declares `/v1/events/replay` and an event catalogue under `/v1/events` in its OpenAPI; the designed `/v1/integration/event-replays` does not exist and 7-day retention limits any replay (D24).

## Data Ownership
Per-service `outbox` (`id, tenant_id, aggregate_type, aggregate_id, type, payload, created_at`) and `consumer_inbox`
tables from `platform/go/dbx`. Pattern `outbox-inbox-tables` requires `partition_key` and `headers` columns: not built
(D19). Kafka topics are created by Connect with 7-day default retention (`topic.creation.default.retention.ms`),
not generated `KafkaTopic` resources; no retention classes (D24).

## Events Produced
None of its own; it carries every service's topics. Relayed services: batch, billing, budgeting, eam, expense,
fieldservice, fulfillment, hr, intercompany, metadata, ops, party, product, production, projects, purchasing,
receivables, receiving, sales, transport, warehouse.

## Events Consumed
Consumers with a `KafkaUser`: billing, edi, intercompany, inventory, payables, prediction, receivables, receiving,
revrec, scheduling, search, tax and workflow-ts (`deploy/kafka/*-consumer.yaml`), plus consumer code in finreport,
fulfillment, pricing, production, purchasing, shopfloor and integration-hub-ts. AsyncAPI is not a complete consumer
registry. Dead subscriptions (no relayed producer): `depot.shopfloor.registration*` (production),
`depot.planning.plan` (purchasing), `depot.platform.entitydefinition` (workflow-ts; metadata publishes
`depot.metadata.entity`).

## Dependencies
internal: platform-kernel (outbox, events, eventing), every service. external: Kafka (Strimzi), Debezium 3.0.7 on
Kafka Connect, Apicurio Registry, Temporal (own Postgres).

## Gotchas & Constraints
- A service whose outbox has no connector publishes nothing, however complete its AsyncAPI (D2). Check `deploy/kafka/connect.yaml` before relying on a topic.
- The architecture's events §2.5 as-built note says there is no enricher SMT; `deploy/kafka/connect.yaml` wires `transforms: outbox,enrich` with `deploy/kafka-connect/enricher`. The compiled form follows the code; the architecture note needs a review edit.
- The enricher keeps `errors.tolerance=none`: a malformed record stops the connector task rather than publishing a broken event.
- Additive-only schema evolution; compatibility gating in Apicurio was not audited.
- Temporal namespace per tenant versus shared is still an open question in the architecture.

## Target — not built
- Connectors for the 37 unrelayed outboxes (D2); table-CDC connectors feeding audit and the lakehouse (D3).
- `traceparent`, `sequence`, `dataschema` in the Go envelope; `partition_key`, `headers` columns; `tenantid:subject` key (D19).
- Sagas S3b (production-order settlement) and S4 (intercompany mirror documents); triggers for payment-run, ECO and CAPA workflows (D13).
- 30-day retention, retention classes and an event-replay API (D24); per-consumer DLQs; tenant egress topics (Q5).

## Source Sections
- Architecture events §0 binding decisions (as built note), §2 envelope, §2.5 Debezium relay, §4 topics and transport, §4.2 topic registry, §5–§6 producer and consumer tables, §7 reliability, §9 saga design; backend §7.6 outbox and inbox.
- ADR-0008; delta §1.1 notes (consumers, dead subscriptions), §2.1 (eventing-backbone row), §2.2 (envelope, topic, DLQ, outbox contracts).
