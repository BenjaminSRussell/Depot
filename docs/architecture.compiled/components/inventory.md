# Inventory

**As built (v1.0):** four Go services (ADR-0008). `services/inventory` consumes receipts and packing slips as designed
(§3.4) and posts valuation through `ledgerclient`, but none of the four outboxes is relayed, so
`inventory.transaction` events never reach sales or procurement (D2), and there is no valuation reservation (D30).

## Purpose & Boundaries
Inventory transactions, on-hand, reservations, journals, counting, transfers, holds and blockings, consignment and
truck stock (inventory); cost transactions, valuation ledgers, aging and cost closes (costing); costing versions,
standard-cost calculation, rolls and activation (productcost); quality orders, samples, nonconformances, CAPA, CoA,
SPC and stability studies (quality). Inventory is the valuation owner for receipt true-up, shipment COGS and the
finished-goods receipt of report-as-finished.

## Services
| Directory | Lang | Prefix | Outbox relayed |
|---|---|---|---|
| `services/inventory` | Go | `/v1/inventory` | no (D2) |
| `services/costing` | Go | `/v1/costing` | no |
| `services/productcost` | Go | `/v1/product-costing` | no |
| `services/quality` | Go | `/v1/quality` | no |

## Interfaces
- inventory: transactions (read-only), onhand, reservations, journals and journal-names, count-plans, count-sheets, count-tags, transfers and transfer-orders, holds, blockings, consignment, truck-stock, reconciliations, gl-postings, opening-batches, issue-claims; internal routes under `/internal/` are not routed by the gateway.
- costing: costing, cost-transactions, closes, settlements, aging, valuation-ledgers, valuation-categories, value-report, reconciliations, gl-postings.
- productcost: versions, calculations, rolls, activations, activation-attempts, cost-groups, cost-components, variances, estimates, postings.
- quality: quality-orders, samples, sampling-plans, nonconformances, outcomes, coa, spc, instruments, stability-studies, receipt-checks.
- Reconciliation endpoints exist in inventory and costing (designed `GET /v1/inventory/reconciliation/{check}`).
- Posting keys: the design is `post:inventory:productreceipt:<id>:valuation` and `post:inventory:shipment:<id>:cogs`. As built, inventory's event-driven postings use an `inventory-postings:` key prefix (`services/inventory/internal/api/postings_consumer.go`) and productcost uses `productcost.activation:<id>`; D9 names productcost, close, mobile and planning.

## Data Ownership
Inventory transactions (insert-only; `services/inventory/migrations/0001_inventory_ledger.sql`), on-hand projection,
reservations, journals, counting, cost layers and valuation, costing versions, quality records. Transactions are plain
tables, not monthly partitions (D10). *Target — not built (D30):* `inventory.valuation_reservation` (reserved, posted,
finalized, rejected, expired) with the per-minute sweeper that checks `ledger.posting_request` before expiring; the
designed key `post:inventory:<documentType>:<documentId>:<purpose>` for every valuation post; `journal_entry_id` and
`gl_seq` on every transaction at insert.

## Events Produced
inventory: `depot.inventory.transaction`, `depot.inventory.onhand`, `depot.inventory.reservation`,
`depot.inventory.journal`, `depot.inventory.transfer` and others; costing and productcost: `depot.costing.*`; quality:
`depot.quality.*` (none relayed, D2). *Target (D6):* `inventory.transaction.posted.v1` with `journalEntryId`, `glSeq`,
`reservationId` and `referenceType` (needed by sales COGS link-back and procurement receipt valuation),
`inventory.transaction.rejected.v1` with `reasonCode` and `retryable`, `inventory.valuationreservation.cancelled.v1`
and `.expired.v1`.

## Events Consumed
inventory receives `depot.receiving.receipt` (valuation true-up), `depot.fulfillment.packingslip` (COGS),
`depot.sales.orderprogress` and `depot.service.workorder.completed`, with a `KafkaUser` in
`deploy/kafka/inventory-consumer.yaml`. Production report-as-finished does not reach inventory as an event today.

## Dependencies
internal: ledger (`ledgerclient`), product and engineering (released products), platform (org sites and warehouses),
procurement, sales and manufacturing (events), warehouse (locations). external: none beyond the kernel.

## Gotchas & Constraints
- Negative stock is enforced at write by locking the on-hand row (NFR-D4); the hot row limits throughput.
- Without the reservation protocol a crash between `ledger.Post` and the local finalize leaves a journal entry with no inventory transaction until a retry with the same key (D30).
- FIFO layer consumption uses `FOR UPDATE SKIP LOCKED`.
- Lines with `stocked=false` (drop-ship, services) create no inventory transaction.
- Open review items: missing `shipment.reversed` event (R4-3); `:override-negative` contradicts NFR-D4 (R4-2).

## Target — not built
- Relays for all four outboxes (D2); payload fields consumers need (D6).
- Valuation reservation, sweeper and posting-request registry (D30); partitioned transactions (D10); uniform posting key in productcost (D9).

## Source Sections
- Architecture §3.4 posting ownership, §5.9 inventory endpoints (target), §6.4 caller protocol rule 5; data §9.1 EN-037..045 and EN-077, §12.4; events §3.5, §9.1–9.3.
- ADR-0008; delta §1.1 (inventory services), §2.1 (inventory row), §2.2 (valuation contracts).
