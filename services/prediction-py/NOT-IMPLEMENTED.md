# NOT-IMPLEMENTED: prediction-py (PSG-47, FR-47.15)

Built: the model, the promotion gate, scoring, the stale rule, the routes (retrain, score, read, per-tenant
settings, accuracy report), Postgres persistence under tenant RLS, the `prediction.payment.scored` outbox rows,
tenant isolation, tests, image and chart. The rows below are not built.

| Deliverable | Status | Reason |
|---|---|---|
| Pulling paid and open invoices from receivables | built (round 4) | `src/prediction/receivables.py` reads `GET /v1/receivables/prediction-invoices` with a service token (`signer.py`, grant `prediction -> receivables`, duty `receivables.prediction.read`, actor `service:prediction-sync`). Not built: the 10 minute scoring window for 1M open invoices (a pass pages 500 at a time and scores 5,000 per call) |
| Weekly retrain job and recompute on dunning, dispute or partial-payment events (BR-47.12) | built as a scheduled delta (round 4) | `python -m prediction.sync weekly` retrains on all paid invoices and rescores the open ones; `delta` (chart CronJobs, every 15 minutes) rescores the open invoices changed since the watermark (a settlement, a dunning step or a dispute moves the invoice's `updated_at`) and stamps the paid date of the ones paid. Not built: recompute on the receivables events themselves (needs the consumer and the broker); the scope is `sync.scopes` in the chart, not a tenant registry read |
| Relay of the outbox to the broker (Debezium connector and its `debezium_cdc_prediction` role) | later slice of PSG-47 | Scoring writes `dev.passage.prediction.payment.scored.v1` to the outbox in the scoring transaction; the connector that publishes it is deployment work |
| Gateway route for `GET /api/v1/ar/invoices/{id}/prediction` and the collections agent tool `passage.ar.invoice.prediction.get` | built (round 4) | Gateway routes `predictions` and `ar` (rewritten to `/v1/...`, which the service also serves), the operation carries `x-passage-tool`, and the duties are seeded for both prefixes. The tool reaches the collections agent once the MCP gateway's OpenAPI document includes this service's spec |
| Prediction accuracy screen and a scheduled accuracy job | later slice of PSG-47 | The report is served (`GET /api/v1/predictions/payment/accuracy`); outcomes are recorded when paid invoices arrive in a retrain, so the report fills only as fast as the weekly retrain runs |
| `dso_days` forecast tile and collections workspace consumption | built (round 4) | `GET /v1/predictions/payment/dso?legalEntityId=` (duty `collections.view`) and the "Forecast days to pay" tile of the AR workspace; treasury forecast's own `dso_days` is not touched |
| An AGT-0213 heuristic fallback display | handoff | The UI shows the heuristic; this service answers 422 AGT-0213 |
