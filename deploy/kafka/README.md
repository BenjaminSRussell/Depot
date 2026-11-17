# deploy/kafka

The dev event backbone (Strimzi), synced by the Applications in `deploy/envs/dev/apps/event-backbone.yaml`:

| File | Application | What it holds |
|---|---|---|
| `kafka.yaml` | kafka-cluster | The `depot` cluster (KRaft, three dual-role nodes). Listeners: `plain` 9092 without authentication and `tls` 9093 with SCRAM-SHA-512; `simple` authorization, so an anonymous client on the plain listener can do nothing. |
| `registry.yaml` | apicurio-registry | Apicurio Registry. |
| `connect.yaml` | debezium-connect | Kafka Connect, the `debezium` KafkaUser (write ACLs for every relayed channel), the `outbox-<service>` relays (D2, B-05a) and the `audit-cdc-<service>` connectors (D3, B-07a). |
| `<user>.yaml` (`*-consumer.yaml`) | kafka-consumer-users | One KafkaUser per consumer, with the KafkaTopic of each of its dead-letter topics (D34, B-05b). |

## Consumer users

Every Kafka consumer in the code has a KafkaUser named `<service>-consumer` (production has two:
`production-consumer` for the planned order intake and `production-shopfloor-consumer` for the shop-floor job cards).
The consumer's chart names it in the `username` of its consumer block (`consumer.kafka`, `kafka`,
`shopfloorConsumer.kafka` or pricing's `cacheConsumer`), and its template mounts the Secret named by the block's `secret`
at `/var/run/depot/kafka` (keys `password` and `ca.crt`) and sets `KAFKA_USERNAME`, `KAFKA_PASSWORD_FILE` and
`KAFKA_TLS_CA_FILE`. The client signs in over the 9093 listener (`depot-kafka-bootstrap.kafka.svc:9093` from another
namespace).

The ACLs are least privilege, all literal except one:

- Read and Describe on each topic the consumer subscribes to;
- Read and Describe on each consumer group; pricing's replicas each read under `pricing-cache-<pod>`, so its user has the
  one prefix grant, `pricing-cache-`;
- Write and Describe on each dead-letter topic the consumer parks records on, which a KafkaTopic in the same file creates
  (topic auto-creation is off).

`deploy/scripts/test_kafka_consumers.py` (in `make -C deploy test-argocd`) keeps this true. It finds the consumers in
the code (every `kafka.ConfigFromEnv` and `eventing.Config` in Go, every kafkajs consumer config in TypeScript, every
kafka-python `KafkaConsumer` in Python), works out each one's groups, topics and dead-letter topic from the constants
and helpers it uses, and resolves environment variables through the chart. It then requires the user's ACLs to be exactly
those. A topic added to a consumer therefore needs its ACL here, or CI fails; so does a consumer without a user, a user
without a consumer, or a chart that does not mount the credentials.

## Dev

A consumer is on in dev only when its topics are relayed (an outbox connector writes them) and dev has what it needs to
start: brokers, credentials and its database Secret. Today that is receiving's consumer and workflow-ts's tender and
budget plan trigger. The other consumers stay off, each with its reason in `DEV_OFF` in the test; for all of them it is
the missing dev database Secret (D4).

The User Operator writes each user's Secret next to the KafkaUser, in `kafka`. A consumer that is on gets a copy in its
own namespace from `deploy/envs/dev/kafka-consumer-credentials`: a `SecretStore` (External Secrets, Kubernetes provider)
whose service account may only `get` the users of the consumers that are on and the cluster CA `depot-cluster-ca-cert`
(Role `depot-read-kafka-consumer-credentials` in `deploy/envs/dev/kafka-secrets`), and one ExternalSecret per consumer.

To turn another consumer on in dev:

1. give its service a dev database Secret (D4);
2. add its ExternalSecret to `deploy/envs/dev/kafka-consumer-credentials/credentials.yaml` and its user to that Role;
3. set its chart's consumer block in `deploy/envs/dev/values/<app>.yaml` (or the Application's values): `enabled: true`
   where the chart has it, `brokers: depot-kafka-bootstrap.kafka.svc:9093`, `secret: <user>-kafka`;
4. remove it from `DEV_OFF`.

Stage has no Kafka yet (#213): `stage_apps.py` drops the dev Kafka values, so every consumer is off in stage.

## Smoke test

`make -C deploy smoke-consumers` (`deploy/scripts/smoke_consumers.py`) checks D34's live part on the dev cluster. For
each consumer group that is on, it produces one CloudEvent of type `dev.depot.smoke.ping.v1` to one of the group's topics
as a temporary KafkaUser `consumer-smoke`, and waits for the group's committed offset to move past it. No handler takes
that type, so the consumer skips it or dead-letters it: the test shows the event was consumed, not handled. The
temporary user and pod are deleted afterwards. The test needs a dev cluster (#213), is not part of CI and has not been
run yet. `--dry-run` prints the plan without touching a cluster.
