# Procurement

**As built (v1.0):** three Go services, `services/purchasing`, `services/receiving` and `services/payables`, in place of
the designed `procurement` (Go) and `procurement-ts` (ADR-0008). Receiving uses the designed posting key. The supplier
portal is `services/supplier-portal-ts` (portal-bff component).

## Purpose & Boundaries
Requisition to purchase order, agreements, RFQs, catalogs, signing limits (purchasing, prefix `/v1/procurement`);
receipts, ASNs, GR/IR, invoice capture and matching, ERS self-billing, service entry sheets, settlements (receiving);
AP open items, payment proposals, payment runs and payment files, vendor blocks, aging (payables). Procurement is the
sole generator of payment files; treasury signs and transmits. Expense reimbursements are created and posted as vendor
invoices here.

## Services
| Directory | Lang | Prefix | Outbox relayed |
|---|---|---|---|
| `services/purchasing` | Go | `/v1/procurement` | yes |
| `services/receiving` | Go | `/v1/receiving` | yes |
| `services/payables` | Go | `/v1/payables` | no (D2) |

## Interfaces
- purchasing: requisitions, rfqs, agreements, purchase-orders and lines, acknowledgements, catalogs, trade-agreements, signing-limits, scheduling-agreements, budget-calls.
- receiving: receipts, asns, arrivals, grir, matching, invoices, invoice-captures, invoice-submissions, ers-self-bills, ers-settlements, service-entry-sheets, pending-postings.
- payables: open-items, payment-proposals, payment-runs, payment-files, payments, settlements, vendor-blocks, employee-vendors, aging.
- Posting: receiving posts receipts, vendor invoices, service entry sheets and settlements through `ledgerclient` with keys in the designed shape, using the service name as the context (`post:receiving:productreceipt:<id>:receipt`, `post:receiving:vendorinvoice:<id>:invoice`); the design names the context `procurement` and the receipt purpose `grir-accrual`. The payment run is a `workflow-ts` workflow.

## Data Ownership
Requisitions, RFQs, agreements, purchase orders with revisions, receipts (gap-free numbers), vendor invoices, match
results, AP open items, payment proposals and files. Bank and payee data are sealed with `fieldcrypt` in payables and
receiving (ADR-0011). Designed EN-046..052 is the target shape, including `gl_posting_status` and the valuation link
on the receipt, which is not built.

## Events Produced
purchasing: `depot.procurement.*` (purchase orders, agreements, catalogs, requisitions and more); receiving:
`depot.receiving.receipt`, `depot.receiving.ersselfbill`, `depot.receiving.serviceentrysheet`,
`depot.receiving.policy`, and `depot.payables.invoice`, `depot.payables.settlement`; payables:
`depot.payables.openitem`, `depot.payables.paymentproposal`, `depot.payables.vendor` (not relayed). *Target (D6):*
`procurement.productreceipt.posted.v1`, `procurement.vendorinvoice.posted.v1` and
`procurement.purchaseorder.confirmed.v1` with the designed payloads (productReceiptId, documentNumber, legalEntityId,
vendorId, purchaseOrderId, postingDate, lines; matchResultId, journalEntryId, apOpenItemId).

## Events Consumed
purchasing receives `depot.fulfillment.dropship` and `depot.planning.plan` (planning's outbox is not relayed, so the
second is a dead subscription). Consumer code also exists in payables and receiving
(`deploy/kafka/payables-consumer.yaml`, `deploy/kafka/receiving-consumer.yaml`). *Target:* receipt valuation status
from `inventory.transaction.posted` (blocked by D2, D6), expense invoices from `workforce.expensereport.approved`.

## Dependencies
internal: ledger (`ledgerclient`), inventory (valuation events), party (vendors), product, platform (numbering,
workflow-ts), treasury (payment transmission), workforce (reimbursements), portal-bff (supplier portal). external:
invoice capture/OCR provider (adapter only), bank file formats.

## Gotchas & Constraints
- A vendor invoice may post before inventory valuation finishes; GR/IR-clearing can be transiently debit-heavy; matching reads receipt quantities, not the GR/IR balance.
- Payment proposal approval and execution are separate duties (SoD); auth-age step-up for payment approval and vendor bank changes (ADR-0006).
- Receipt reversal creates a reversal receipt and a ledger reversal; never deletion.

## Target — not built
- Relay for payables (D2); designed payloads (D6); receipt valuation link-back from inventory (D30, D2).
- Payment-run trigger wiring in workflow-ts (D13 lists payment run among workflows with no trigger).
- Designed `procurement-ts` and `/v1/procurement/portal/*`: superseded by ADR-0008 and the supplier portal BFF.

## Source Sections
- Architecture §3.4 posting ownership, §5.10 procurement endpoints (target); data EN-046..052; events §3.6, §9.1.
- ADR-0008, ADR-0011; delta §1.1 (purchasing, receiving, payables), §2.1 (procurement row), §2.2 (procurement payloads).
