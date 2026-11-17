# Runbook: audit change chain verification failed (Sev-2)

Source: the nightly CronJob `<release>-chain-nightly` (`audit chain-nightly <tenant-id>...`, B-07) failed, its log has
`audit change chain verification failed` with the tenant and the first break, or `GET /v1/audit/records:verify`
returned `"verified": false`. The change chain is the evidence of who changed what in master data. A break means one of
these: a row was changed, removed, inserted or reordered after it was written; the chain no longer holds a daily
anchor; or two writers forked the chain (see the first section). Audit must not be rolled back past B-07a once any row
with a legal entity exists: a rollback forks the chain.

Not a break: changes flagged `unattributed-bad-signature` (`audit_cdc_contexts_rejected_total`). Those are captured
changes whose `depot.ctx` message did not verify. Investigate them as a security event in the source service: check its
signing key, the audit chart's `serviceIdentity.publicKeys`, and who emitted the message (the consumer logs the reason,
issuer and txId).
Severity: Sev-2 (Sev-1 when more than one tenant breaks the same night). Response: 4 hours.

## Detect

- The CronJob's Job failed (kube-state-metrics `kube_job_status_failed` for `audit-chain-nightly`).
- The job log line `audit chain anchored` with `"verified": false` names the tenant, `breaks` and the anchor of the night.
- An auditor or the History panel's export reports "not verified" (`GET /v1/audit/records:verify`).

## First: was it a fork from a rollout or rollback?

Before treating a break as tampering, check whether it is a fork. A fork is not tampering. It happens when an audit
replica older than B-07a runs against migration 0009. That image reads the chain head under its caller's legal-entity
scope. Once the head row belongs to another legal entity, it links its row to an older one. This happens during a mixed
rollout without `strategy.type=Recreate`, or after a rollback past B-07a once rows with a legal entity exist.

1. Look at `firstBreak.kind`. `fork` means the row recomputes but links to an earlier row than the one before it. A
   `link-mismatch` can be a fork whose predecessor lies more than 10,000 rows back.
2. Compare the row's `occurred_at` and seq with the deployment history of the audit Deployment (`kubectl rollout
   history`, Argo CD sync history). A break inside a rollout or rollback window, written by an image older than B-07a,
   is a fork.
3. If it is a fork, do not rotate credentials or freeze the schema. Make sure only B-07a or later images run, then
   roll forward; never roll back past B-07a again. Record the fork, its seq and the window in the incident. The chain
   stays as found: it is append-only. From then on `:verify` and the nightly job report that one break. Verify later
   ranges with `fromSeq` past it, and keep the incident as the explanation for auditors.
4. If no rollout or rollback overlaps the break, or `kind` is `hash-mismatch` or `anchor-mismatch`, continue below as
   possible tampering.

## Contain

1. Do not delete, re-anchor or "fix" any row: the broken chain and the anchors are the evidence. `audit_entry` and
   `audit_anchor` are append-only; a break that is not a fork means someone with the table owner's rights bypassed the
   trigger.
2. Record the tenant, `firstBreak` (`seq`, `id`, `kind`), `breaks`, the last anchor that verified, and the time of the
   last clean nightly run.
3. Rotate the audit database owner and admin credentials, and freeze schema changes on `depot_audit`.

## Diagnose

Run `GET /v1/audit/records:verify` for the tenant, then narrow with `fromSeq`/`toSeq`. You need duty `audit.verify`
and tenant-wide authority: no legal-entity claim with the all-scope duty and the ALL-scope header, or a tenant
administrator. A long chain comes back in pages of 100,000 rows; follow `nextFromSeq`.

- `knownBreaks.legacyPrecision`, `legacyFork` or `legacyAmbiguous` above zero with `verified: true` is not an incident.
  These are rows written before B-06 whose integrity is limited for a known reason (README, "Hash encoding").
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
