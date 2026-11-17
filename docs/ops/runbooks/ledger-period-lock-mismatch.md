# Runbook: ledger period lock disagrees with close

Rule: FR-14.1 (B-08c). Close sets the ledger's period lock inside every period transition and answers only after the
ledger confirmed it; a posting needs both the ledger's lock and the close service to say the period is open.
Severity: Sev-3 (postings are refused, never let through). Response: next business hour, sooner during a close.

## Detect
- Postings into a period that close lists as open (`GET /v1/close/periods?ledgerId=`) are refused with 422
  `LEDGER_PERIOD_CLOSED` whose message says "in the ledger's period lock (set by the close service)".
- Close logs `ledger period lock failed; period change rolled back` with `ledgerId` and `fiscalPeriodId`, or a
  transition answered 503 `CLOSE-0012`.

## Cause
The two services commit separately. If close's own commit failed after the ledger committed the lock (database
failover, connection drop), the ledger holds the newer, stricter status. It errs towards refusing postings; it never
admits one close reports closed.

## Repair
1. Confirm the period's status in close (`GET /v1/close/periods?ledgerId=`).
2. Resend it to the ledger: `POST /v1/close/periods/{fiscalPeriodId}:resync-ledger-lock` with `{"ledgerId": "..."}`,
   as a user with duty `period.maintain` in the period's legal entity. It changes nothing in close and answers the
   version the ledger now holds. Repeating the intended transition also resends the lock.
3. 503 `CLOSE-0012` again: check that the ledger is reachable from close (`LEDGER_URL`, service signing key). If the
   ledger answers 503 `LEDGER_PERIOD_LOCK_BUSY`, a ledger transaction on that ledger is holding its row for more than
   2 s (a long posting or rebuild); retry once it has finished.

## Close
Record the period, the cause and the resync answer on the incident.
