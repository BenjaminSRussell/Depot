# Workforce

**As built (v1.0):** three Go services, `services/hr`, `services/payroll` and `services/expense`, in place of the designed
`workforce` (Go) and `workforce-ts` (ADR-0008). Payroll's AsyncAPI has no channels, so `workforce.payrollrun.posted` does
not exist (D6), and payroll's outbox is not relayed (D2).

## Purpose & Boundaries
Workers, employments, positions, jobs, absence, benefits, compensation, recruiting, onboarding checklists, courses,
certifications, reviews, employee self-service (`/v1/hr/me`) (hr); payroll calculations and runs, posting profiles,
year-to-date openings (payroll); expense reports, card transactions, cash advances, per diem, mileage, travel
requisitions (expense). Workforce does not post AP: approved expense reports become vendor invoices in procurement.

## Services
| Directory | Lang | Prefix | Outbox relayed |
|---|---|---|---|
| `services/hr` | Go | `/v1/hr` | yes |
| `services/payroll` | Go | `/v1/payroll` | no (D2) |
| `services/expense` | Go | `/v1/expense` | yes |

## Interfaces
- hr: workers, employments, positions, jobs, absence, benefits, compensation, candidates, offers, requisitions, checklists, courses, certifications, reviews, goals, skills, consents, retention-policies, `me` (self-service; see `apps/ess/README.md`).
- payroll: runs, calculations, posting-profiles, gl-reconciliation, opening-ytd.
- expense: reports, card-transactions, card-matches, cash-advances, per-diem-computations, mileage-computations, travel-requisitions, gl-postings.
- hr allocates worker numbers from `services/numbering` and writes the PHI read log to `services/audit` through its service grant.

## Data Ownership
Workers and HR records (hr; sensitive fields sealed with `fieldcrypt`, ADR-0011), payroll runs and results (payroll),
expense reports (expense). Designed EN for workforce (data §9, including `worker_secure`) is the target shape.

## Events Produced
`depot.hr.*` (absence, benefit enrollment, certification, ...), `depot.expense.cashadvance`, `depot.expense.policy`,
`depot.expense.report`. payroll declares none. *Target (D6):* `workforce.payrollrun.posted.v1` with the designed
payload; `workforce.expensereport.approved.v1` for the procurement reimbursement path.

## Events Consumed
None declared.

## Dependencies
internal: ledger (`ledgerclient` for payroll and expense postings), procurement (reimbursement invoices), party
(employee as vendor role), platform (numbering, audit, workflow-ts), manufacturing (shop-floor time). external: none in
the critical path.

## Gotchas & Constraints
- Pay data also lives in shopfloor (pay agreements, pay exports) without a legal-entity policy (D17).
- ESS ships as a shell module under `apps/web`, not a separate PWA (`apps/ess` is README only).
- Payroll self-scoped pay statements are not built (`services/hr/NOT-IMPLEMENTED.md`).

## Target — not built
- Payroll events and relay (D6, D2).
- Designed `workforce-ts`: superseded by ADR-0008.

## Source Sections
- Architecture §3.2 (workforce row), §3.4 payroll and expense rows, §5.15 workforce endpoints (target); data §9 workforce; events §3.9.
- ADR-0008, ADR-0011, ADR-0015; delta §1.1 (hr, payroll, expense), §2.1 (workforce row), §2.2 (`payload-workforce.payrollrun.posted`).
