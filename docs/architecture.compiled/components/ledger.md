# Ledger

**As built (v1.0):** the designed single `ledger` deployable is nine Go services (ADR-0008). `ledger.Post` is
`POST /v1/ledger/journal-entries`, called by 19 services through `platform/go/ledgerclient` with an Ed25519 service
token (ADR-0010). The ledger's outbox is not relayed, so journal events never reach Kafka (D2).

## Purpose & Boundaries
Universal journal and balances (ledger), exchange rates (fx), tax determination and settlement (tax), period control,
accruals, allocations and close (close), consolidation (consolidation), financial statements and reporting
(finreport), budgets, budget checks and encumbrances (budgeting), cost accounting (costacct, routed as
`/v1/controlling`), intercompany charges and journals (intercompany). Ledger posts as a service principal; the end user
is the service token's `act`.

## Services
| Directory | Lang | Prefix | Outbox relayed |
|---|---|---|---|
| `services/ledger` | Go | `/v1/ledger` | no (D2) |
| `services/fx` | Go | `/v1/fx` | no (no AsyncAPI) |
| `services/tax` | Go | `/v1/tax` | no |
| `services/close` | Go | `/v1/close` | no |
| `services/consolidation` | Go | `/v1/consol` | no |
| `services/finreport` | Go | `/v1/reporting` | no |
| `services/budgeting` | Go | `/v1/budget` | yes |
| `services/costacct` | Go | `/v1/controlling` | no |
| `services/intercompany` | Go | `/v1/interco` | yes |

## Interfaces
- `POST /v1/ledger/journal-entries` (the posting API): balanced lines in transaction, accounting and reporting currency; idempotent on the caller's key; reversal `POST /v1/ledger/journal-entries/{id}/reverse`. Ledger resources also include journal-lines, journal-batches, journal-names, recurring-journals, balances, open-items, clearings and auto-clearing, charts, ledgers and ledger-sets.
- Caller protocol (`platform/go/ledgerclient`): key `post:<context>:<documentType>:<documentId>:<purpose>[:<revision>]`, never random; post, then finalize locally; retries reuse the key. productcost, close, mobile and planning use other keys (D9).
- Authorization: callers present a service token; the allowed duties per caller come from the grant table in `platform/go/tenant`, not OPA (ADR-0010).
- Synchronous peers: close is called for period checks, tax for determination, fx for rates.
- Designed: `journal-entries:post`, `GET /posting-requests/{key}`, `period-controls:check`, `X-Depot-Subject-Token`; the first two are target (D30), the subject token is superseded by ADR-0010.

## Data Ownership
Journal entries and lines (append-only; insert-only triggers in `services/ledger/migrations/0003_journal.sql`),
balances, ledgers, charts and accounts, posting rules; rates in fx; tax codes and transactions in tax; period control in
close; budgets and encumbrances in budgeting; cost entries and hierarchies in costacct; intercompany pairs in
intercompany. Journal tables are plain tables, not monthly range partitions (D10). Idempotency rests on the ledger's
idempotency table; there is no `posting_request` registry (D30).

## Events Produced
ledger: `depot.ledger.journal`, `depot.ledger.balance`, `depot.ledger.batch`, `depot.ledger.openitem`,
`depot.ledger.recurring`, `depot.ledger.realignment`, `depot.ledger.rules` (not relayed). Also `depot.close.*`,
`depot.tax.provider`, `depot.consol.run`, `depot.reporting.*`, `depot.controlling.*` (not relayed), `depot.budget.*`
and `depot.interco.*` (relayed). *Target (D6):* `ledger.journalentry.posted.v1` (topic `depot.ledger.journalentry`,
48 partitions, first 200 lines inline), `ledger.journalentry.reversed.v1`, `ledger.periodcontrol.changed.v1` (best
built match is close, 6 of 8 fields), `ledger.reconciliation.completed.v1`.

## Events Consumed
Consumer code exists in finreport, intercompany and tax (`deploy/kafka/intercompany-consumer.yaml`,
`deploy/kafka/tax-consumer.yaml`); none is declared as a `receive` operation in AsyncAPI. Designed consumers
(budget control, intercompany matching, close checklist, party merge) are target.

## Dependencies
internal: platform (org legal entities and calendars, numbering), platform-kernel (`ledgerclient`, `tenant`),
edge-identity. external: FX rate providers (fx), tax providers (tax; internal fallback).

## Gotchas & Constraints
- NFR-P1 (5,000 lines/s, p99 < 200 ms) cannot be shown on the unpartitioned schema (D10).
- Number allocated last in the posting transaction; a rolled-back post must not consume a number. Fiscal numbers stay in the posting service (ADR-0015).
- The closed-period check must pass on every posting path (reversal, migration, batch).
- Posted entries are immutable; corrections are reversals or new entries.
- Without a relayed ledger outbox, sales cannot link COGS back and budget control cannot react to postings (D2, D6).

## Target — not built
- Relay for the ledger, fx, tax, close, consolidation, finreport and costacct outboxes (D2).
- Monthly range partitions on journal tables and the group-commit benchmark (D10).
- `posting_request` registry with `GET /v1/ledger/posting-requests/{key}` and the inventory valuation reservation protocol (D30).
- Uniform posting key in productcost, close, mobile and planning (D9).
- Intercompany mirror-document saga S4 in Temporal (D13).

## Source Sections
- Architecture §3.4 posting ownership, §5.8 ledger endpoints (target), §6 universal-journal posting API, §7.5 idempotency; data EN-023..036, §8.6 invariants; events §3.4.
- ADR-0008, ADR-0010, ADR-0015; delta §1.1 (ledger services), §2.1 (ledger row), §2.2 (`ledger-post-request`, `posting-request-registry`).
