# prediction-py (wave 47, PSG-47, FR-47.15)

Payment-date prediction per tenant. Numeric work only, per the Python rules in the engineering
conventions. Pure Python: a small gradient-boosted tree implementation in the standard library
(`src/prediction/gbt.py`), no numpy or scikit-learn.

- **Features** (`domain.py`): terms days, log amount, invoice day of month, dunning count, disputes,
  due month (seasonality) and the customer's mean days late (leave-one-out while training).
- **Heads:** squared-loss regression on days late (gives `predicted_payment_date` = due date + days)
  and a logistic head for `p_late_30d` (paid more than 30 days after the due date), written as
  numeric(5,4) text.
- **Retrain** (`train`): below `min_rows` paid invoices (default 500, 100 to 10,000) there is no model,
  AGT-0213. The MAE is measured on a held-out fifth the fitting never saw, then the model is refit on
  all rows. MAE within `mae_max_days` (default 6, 1 to 30) is ACTIVE and retires the previous ACTIVE
  model; above it the model stays CANDIDATE and the previous one keeps serving (AC-47.12).
- **Scoring** (`score`): `predicted_payment_date`, `p_late_30d`, `features_hash`, `model_version`,
  `scored_at`. `is_stale`: older than 7 days is stale (BR-47.12). Deterministic: same data, same model.

## API

Identity from the edge headers `x-passage-tenant`, `x-passage-user`, `x-passage-duties` (401 when
missing, malformed or the nil tenant); the tenant is never read from a body.

| Route | Duty | Notes |
|---|---|---|
| `POST /api/v1/predictions/payment/retrain` | `agents.admin` | body `paidInvoices` (up to 50,000); 201 with the model (status, rows, maeDays); 422 AGT-0213 below min_rows |
| `POST /api/v1/predictions/payment/score` | `agents.admin` | body `openInvoices` (1 to 5,000); 422 AGT-0213 without an ACTIVE model |
| `GET /api/v1/ar/invoices/{id}/prediction` | `collections.view` | the stored prediction with `stale` |
| `GET`/`PUT /api/v1/predictions/payment/settings` | `agents.admin` | the tenant's `minRows` and `maeMaxDays` (9.47.20); a tenant without its own uses the service defaults; the next retrain uses them |
| `GET /api/v1/predictions/payment/accuracy` | `agents.admin` | accuracy report over predictions whose invoices were since paid: MAE days, share within 7 days, `p_late_30d` calibration per bucket, overall and per model version |

OpenAPI: `api/openapi.yaml`. Event: `events/asyncapi.yaml`.

## Persistence

`src/prediction/store.py` (`PgRepository`) over `migrations/0001_prediction.sql`: `prediction_model` (the trees
and customer statistics as a JSON artifact, at most one ACTIVE per tenant), `payment_prediction` (latest per
invoice; `actual_paid_date` is stamped when the invoice turns up paid in a retrain), `prediction_settings` and
`outbox`. Every table has tenant row-level security (forced); each call runs in its own transaction with
`passage.tenant_id` and `passage.legal_entity_scopes` set (migration 0003: `payment_prediction` and `prediction_sync` carry the restrictive legal-entity policy). Scoring writes one `dev.passage.prediction.payment.scored.v1` outbox row per invoice in
the same transaction. A retrain holds a per-tenant advisory lock while it swaps the ACTIVE model.

Env: `DATABASE_URL` (role `passage_app`; without it the process keeps everything in memory, for tests and local
runs), `MIGRATE_ON_START` (`true` runs the migrations as the owner role at start), `PREDICTION_MIN_ROWS`,
`PREDICTION_MAE_MAX_DAYS` (service defaults).

## Recompute on receivables events

`python -m prediction.consumer` (src/prediction/consumer.py; chart `consumer`, off by default) consumes the receivables payment,
credit settlement, dispute, dunning letter, deduction, down payment and opening-item events (CloudEvents on
`passage.receivables.*`, structured or binary mode) from Kafka in the group `prediction`, and rescores the invoices they touch.
Each event names its tenant, legal entity and the open items it changed; the recompute is the sync delta for that entity
(`prediction.sync.delta`), so the invoices the event names, and anything else changed since the entity's watermark, are scored
again. One pass serves all events of a batch for the same entity. Each event is recorded in `consumer_inbox` (migration 0004,
the table of the Go services' `eventing.PgInbox`) inside the transaction around its recompute: a redelivered event is skipped, a
recompute that fails leaves no record, nothing is committed, and the batch is read again after a backoff (1 s growing to 30 s).
Offsets are committed after the batch. A malformed message is logged and skipped. A recompute that can never succeed (receivables
answers 4xx other than 408 and 429, unreadable data, a bad id) is recorded in `consumer_inbox` as `failed` with its reason, logged and
counted (`prediction_consumer_permanent_failures_total` in the log line's `metric` field) and skipped, so one entity cannot block the
partition; a transient failure (5xx, timeout) is retried with backoff. The inbox row and the recompute are not one transaction (the
recompute writes through its own idempotent upserts), so delivery is at least once and a crash in between can repeat a recompute and its
`payment.scored` events. The 15-minute delta CronJob keeps running as the
safety net. Needs `KAFKA_BROKERS`, `RECEIVABLES_URL` and the service signing key (the same as the sync jobs); optional
`PREDICTION_KAFKA_GROUP` and `PREDICTION_KAFKA_TOPICS`; SCRAM-SHA-512 over TLS with `KAFKA_USERNAME`, `KAFKA_PASSWORD_FILE` and
`KAFKA_TLS_CA_FILE` (chart `consumer.kafka.secret`; `deploy/kafka/prediction-consumer.yaml` is the KafkaUser with read ACLs on the seven
topics and the group `prediction`). The `passage.receivables.*` topics are written by `outbox-receivables` in
`deploy/kafka/connect.yaml` (routed by event type; receivables migrations 0030 and 0031 publish the outbox to `debezium_cdc_receivables`);
until that connector runs on the cluster no event reaches the consumer (`auto.create.topics.enable` is false). Tests: `tests/test_consumer.py` with a fake source and a fake Kafka client.

## Run and test

```
cd services/prediction-py
python -m venv .venv && . .venv/bin/activate
pip install -e ".[dev]"
pytest                      # tests/test_core.py needs only the standard library
uvicorn prediction.api:app --port 8080
```

`tests/test_api.py` needs the dev extras (fastapi, httpx) and skips without them. `tests/test_store.py` runs the
repository contract against the in-memory repository and, with `PASSAGE_TEST_DATABASE_URL` (owner) and
`PASSAGE_TEST_APP_DATABASE_URL` (`passage_app`) set, against Postgres in a private schema.
`prediction.synth` generates a deterministic synthetic receivables history (fictional Cedar Ridge Pumps
customers); the AC-47.12 test trains on 2,000 of its paid invoices.
