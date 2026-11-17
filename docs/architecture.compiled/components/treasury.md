# Treasury

**As built (v1.0):** `services/treasury` (Go, designed name) plus `services/assets` (Go) for fixed assets and leases
(ADR-0008). Neither outbox is relayed (D2).

## Purpose & Boundaries
Bank accounts, statements and reconciliation, cash desks, cash position and forecast, checks, EBICS, facilities and
loans (treasury); fixed assets, depreciation runs, group assets, capex requests, counts, leases (assets). Treasury
signs and transmits payment files that procurement (payables) generates.

## Services
| Directory | Lang | Prefix | Outbox relayed |
|---|---|---|---|
| `services/treasury` | Go | `/v1/treasury` | no (D2) |
| `services/assets` | Go | `/v1/assets`, `/v1/leases` | no |

## Interfaces
- treasury: bank-accounts, statements, bank-reconciliations, reconciliation-rules, reconciliation-runs, reconciliation-matches, cash-position, cash-forecast, cash-desks, checks, ebics-subscribers, facilities, loans, intraday-reports, opening-balances.
- assets: assets, depreciation-runs, group-assets, capex-requests, count-sessions, gl-postings, gl-reconciliation, posting-profiles, opening-assets; leases under `/v1/leases`.
- Posting: bank charges, reconciliation adjustments, depreciation and lease postings through `ledgerclient`.
- Reconciliation endpoints exist in treasury (delta §2.1, testing-reconciliation row).

## Data Ownership
Bank accounts and statements, reconciliation state, cash forecasts, checks (treasury); asset register, books,
depreciation, leases (assets). Designed treasury EN (data §9) is the target shape.

## Events Produced
`depot.treasury.*` (cash desk, check, reconciliation, ...) and `depot.assets.*` (asset, capex request, depreciation,
...); none relayed (D2).

## Events Consumed
None declared.

## Dependencies
internal: ledger (`ledgerclient`), procurement (payment files), platform. external: banks (statement import, EBICS,
payment transmission).

## Gotchas & Constraints
- `system:treasury-signer` holds the release duty for key custody; SoD between proposal approval and execution stays in payables.
- Without a relayed outbox, bank and asset events do not reach the ledger close checklist or analytics (D2).

## Target — not built
- Relays for treasury and assets (D2).

## Source Sections
- Architecture §3.2 (treasury row), §3.4 bank/depreciation row, §5.14 treasury endpoints (target); data §9 treasury; events §3.9.
- ADR-0008; delta §1.1 (`services/treasury`, `services/assets`), §2.1 (treasury row).
