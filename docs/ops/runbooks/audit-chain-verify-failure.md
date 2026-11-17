# Runbook: audit change chain verification failed (Sev-2)

Source: the nightly CronJob `<release>-chain-nightly` (`audit chain-nightly <tenant-id>...`, B-07) failed, its log has
`audit change chain verification failed` with the tenant and the first break, or `GET /v1/audit/records:verify`
returned `"verified": false`. The change chain is the evidence of who changed what in master data; a break means a
row was changed, removed, inserted or reordered after it was written, or the chain no longer holds a daily anchor.
Severity: Sev-2 (Sev-1 when more than one tenant breaks the same night). Response: 4 hours.

## Detect

- The CronJob's Job failed (kube-state-metrics `kube_job_status_failed` for `audit-chain-nightly`).
- The job log line `audit chain anchored` with `"verified": false` names the tenant, `breaks` and the anchor of the night.
- An auditor or the History panel's export reports "not verified" (`GET /v1/audit/records:verify`).

## Contain

1. Do not delete, re-anchor or "fix" any row: the broken chain and the anchors are the evidence. `audit_entry` and
   `audit_anchor` are append-only; a break means someone with the table owner's rights bypassed the trigger.
2. Record the tenant, `firstBreak` (`seq`, `id`, `kind`), `breaks`, the last anchor that verified, and the time of the
   last clean nightly run.
3. Rotate the audit database owner and admin credentials, and freeze schema changes on `depot_audit`.

## Diagnose

Run `GET /v1/audit/records:verify` (duty `audit.verify`) for the tenant, then narrow with `fromSeq`/`toSeq`.

- `knownBreaks.legacyPrecision` or `knownBreaks.legacyFork` above zero with `verified: true` is not an incident: rows
  written before B-06 that cannot recompute for a known reason (README, "Hash encoding").
- `hash-mismatch` at a seq: a stored value of that row changed. Compare it with the `dev.depot.audit.change.recorded.v1`
  event of the same id on `depot.audit.change` (7-day retention) or the lakehouse copy, and with the source record.
- `link-mismatch` at a seq: the row before it was removed, or a row was inserted or reordered. Count the seqs around it.
- `anchor-mismatch`: the chain no longer holds an anchored head (seq and hash). The chain was rewritten (every hash
  recomputed) or cut short after that day's anchor. Compare the anchors with the `audit chain anchored` log lines,
  the copy outside the database.
- Check the Postgres audit and connection logs of `depot_audit` for sessions of the owner role around the break.

## Recover

- Do not rewrite the chain. Record the incident, the break and the evidence in the incident record, and tell the
  tenant's controller which entries are affected (by seq range).
- Restore the original rows from backup into a separate schema for comparison only; the live chain stays as found,
  and later rows keep linking to the stored hashes, so verification of later ranges still works (`fromSeq` past the
  break).

## Close

1. Verification output, the anchor history and the timeline attached to the incident.
2. Lift the schema freeze after the credentials are rotated.
3. Post-incident review within 5 working days.
