# Sales

**As built (v1.0):** six Go services and `services/prediction-py` in place of the designed `sales` (Go) and `sales-ts`
(ADR-0008). Intercompany is the Go service `services/intercompany` (ledger component), not a saga hosted in sales; the
S4 saga is not built (D13). The customer portal is `services/customer-portal-ts` (portal-bff component).

## Purpose & Boundaries
Quotations, orders, ATP, holds, credit profiles and exposure, returns, agreements (sales); price procedures, condition
records, promotions, rebates, royalties, commissions (pricing); invoices, credit notes, subscriptions, self-billing
(billing); AR open items, payments, cash application, lockbox, dunning, disputes, deductions, statements (receivables);
revenue contracts, allocations and recognition runs (revrec); release, waves, packing slips, shipments, drop-ships,
manifests, packaging (fulfillment); payment-date prediction for AR (prediction-py).

## Services
| Directory | Lang | Prefix | Outbox relayed |
|---|---|---|---|
| `services/sales` | Go | `/v1/sales` | yes |
| `services/pricing` | Go | `/v1/pricing` | no (D2) |
| `services/billing` | Go | `/v1/billing` | yes |
| `services/receivables` | Go | `/v1/receivables` | yes |
| `services/revrec` | Go | `/v1/revrec` | no |
| `services/fulfillment` | Go | `/v1/fulfillment` | yes |
| `services/prediction-py` | Python | `/api/v1` (D22); own database | no |

## Interfaces
- Per-service OpenAPI in `contracts.json`; pricing exposes `resolve` and `simulate` (the designed `prices:resolve`), sales exposes `atp` and `credit-exposure` (designed `atp:check`, `credit-checks`).
- Customer self-service routes (`customer-portal` resources in sales, billing and receivables) are called by `services/customer-portal-ts` with service tokens.
- receivables also takes the payment provider's webhook (`/webhooks/v1/stripe`, signed with `Stripe-Signature`) through its own Gateway `depot-payments-webhook` (`deploy/gateway/payments-webhook`), with no JWT filter.
- Posting: billing and receivables post invoices, credit notes and cash through `ledgerclient`.

## Data Ownership
Orders and lines, quotations, holds, credit profiles (sales); condition records and procedures (pricing); invoices and
credit notes with gap-free numbers (billing); open items, payments, disputes (receivables); sales seals sensitive fields with
`fieldcrypt` (ADR-0011); revenue schedules (revrec); shipments, packing slips, SSCC register (fulfillment);
prediction features and scores (prediction-py, the only stateful Python service). *Target:* `shipment.cogs_journal_entry_id`,
`gl_posting_status` and `shipment_line.cogs_amount` set from `inventory.transaction.posted` (blocked by D2, D6).

## Events Produced
`depot.sales.*`, `depot.pricing.*` (not relayed), `depot.billing.invoice`, `depot.receivables.*`, `depot.revrec.*`
(not relayed), `depot.fulfillment.*` (including `depot.fulfillment.packingslip`, the COGS trigger for inventory).
prediction-py names one type in prose but declares no channel. *Target (D6):* `sales.shipment.posted.v1`,
`sales.invoice.posted.v1`, `sales.salesorder.confirmed.v1`, `sales.payment.applied.v1` with the designed payloads;
`sales.shipment.reversed.v1` is an open review item (R4-3).

## Events Consumed
fulfillment receives `depot.sales.order` and `depot.sales.orderhold`; revrec receives `depot.billing.invoice`.
billing, receivables and pricing have consumer code (`deploy/kafka/billing-consumer.yaml`,
`deploy/kafka/receivables-consumer.yaml`). prediction-py has a `KafkaUser` (`deploy/kafka/prediction-consumer.yaml`).
*Target:* `sales-shipment-cogs` from `inventory.transaction.posted` (D2, D6).

## Dependencies
internal: ledger (`ledgerclient`, tax), inventory (ATP, COGS), party (customers), product, warehouse (shipping),
platform (numbering, workflow-ts), portal-bff. external: payment provider (webhook), tax providers through tax.

## Gotchas & Constraints
- COGS is posted by inventory from the packing slip; sales never writes inventory and inventory never writes the shipment.
- prediction-py uses `/api/v1` and its own problem shape (D22, D11).
- HPA only on pricing among these charts (D18).

## Target — not built
- Relays for pricing, revrec and prediction-py (D2); designed payloads (D6); COGS link-back.
- S4 intercompany mirror-document saga (D13).
- Designed `sales-ts`: superseded by ADR-0008.

## Source Sections
- Architecture §3.2 (sales row), §3.4 packing slip and invoice rows, §5.12 sales endpoints (target); data §9 sales; events §3.8, §9.
- ADR-0008, ADR-0011; delta §1.1 (sales services), §2.1 (sales row), §2.2 (sales payloads).
