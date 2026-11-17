# Execution status (stage 6)

This file records where execution stands, so that work can resume after a pause. Merged PRs are the source of truth;
this file covers the work in between.

## Merged to main

| Item | PR |
|---|---|
| B-06, B-08, B-08b, backlog | #675 |
| B-03a | #718 |
| B-08c | #719 |
| B-05a | #720 |
| B-02a | #722 |
| B-01a | #724 |
| B-03b, B-07a, B-09a | #726 |

## Merged in #726 (kept for the review trail)

| Item | Commit | Review | Next step |
|---|---|---|---|
| B-03b: OPA legal-entity rule, scoped assignments in the bundle, D17 legal-entity policies (shopfloor worker and pay data) | c0611102 | [b03b-review-1.md](reviews/b03b-review-1.md): changes required (1 Blocker: a scoped assignment granted tenant-level duties such as SCIM; 2 Major: unvalidated and unaudited legal-entity assignment) ; fixed in 358c3682, re-review resolved the Blocker but found R1 (Major: legal entity not validated) | R1 fix in progress (identity existence check, dry run), then the wave 2b PR |
| B-07a: CDC audit pipeline, `depot.ctx`, `:verify`, nightly job, daily anchor | d7c7723d, fixes 59409863 | [b07a-review-1.md](reviews/b07a-review-1.md): changes required (2 Major, 6 Minor, 4 Nit); all 12 fixed in 59409863; re-review found a Blocker (SECURITY DEFINER search_path), fixed in cbd21b13; approved after re-review 2 (R2-1 fixed in 691da2ce) | In the wave 2b PR with B-03b and B-09a |

The B-07a fixes agreed after review 1:

1. Sign `depot.ctx` with the emitting service's ADR-0010 Ed25519 key. The signature covers context `depot.audit-ctx.v1.`, the tenant, `txid_current()` and the body. The consumer verifies the signature and the txid. A context that is unsigned, carries a bad signature or has a mismatched txid is recorded as unattributed.
2. Make no rollback past B-07a a documented rule. Add the fork case to the runbook, and report a fork in `:verify` if that is cheap.
3. Bound `FindTxContext` by `recorded_at`, purge old contexts from the consumer, and document the DLQ replay limits.
4. Report legacy rows that contain `|` as `legacy-ambiguous`.
5. Require tenant-wide authority for an open `:verify` range, and cap each request with a continuation.
6. Restrict `audit_anchor` inserts to a SECURITY DEFINER function or a separate role, with no future dates.
7. Add Debezium heartbeats, and enable the connector only together with the consumer.
8. Give the CDC role column-level grants that exclude `*_sealed`, and correct the "never leave the database" wording.
9. Fix the nits: unauthenticated trace id documented, the `dlqTopic` value used, a KafkaTopic CR with one partition, and `lock_timeout` in party 0021.

The B-03b follow-up is issue #725, covering the 9 tables whose D17 reason is interim: mobile `device_session` and `offline_transaction`; hub `hub_delivery` and `hub_attempt`; agents `agent_run`, `agent_action`, `approval_request`, `agent_run_state` and `agent_run_credential`.

| B-09a: owner-role migration Jobs in all 60 charts, a `migrate` command in every image, `MIGRATE_ON_START=false` (D28) | a3d87d69 | [b09a-review-1.md](reviews/b09a-review-1.md): changes required (2 Major: dev Jobs reference owner Secrets nothing creates; no retry when Postgres is briefly down); fixed in 694f0a44 and 4e718348, approved on re-review | In the wave 2b PR. The k3d proof is written but has not run (there is no Docker here) |

## Not started

- B-04, B-05b and B-05c, B-07b and B-07c, B-09b and B-09c, B-10, B-01b, B-02b and B-02c, and the P1 waves in [execution-plan.md](execution-plan.md).

## After #726

- B-03b re-review 2 approved with R6 (identity lookup should require a service principal), R7 (registry client follows
  redirects; a 404 from a wrong URL reads as unknown) and R8 (signing key mount needs `fsGroup`). Fixing as a follow-up.
- Deploy dependency: the shop floor assign routes answer 503 SF-0202 until an environment sets `identityUrl` and
  `serviceIdentity` and identity trusts the shop floor key; the backfill runbook waits on that.
- Next: B-05b (Kafka users, in progress), B-04 (gateway enforcement and decision logs, now unblocked).
