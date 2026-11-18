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
| B-05b, B-04, B-03b follow-ups | #727 |

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

- B-07b and B-07c, B-09c, B-10, B-01b, B-02b and B-02c, and the P1 waves in [execution-plan.md](execution-plan.md).

## After #726

- B-03b re-review 2 approved with R6 (identity lookup should require a service principal), R7 (registry client follows
  redirects; a 404 from a wrong URL reads as unknown) and R8 (signing key mount needs `fsGroup`). Fixing as a follow-up.
- Deploy dependency: the shop floor assign routes answer 503 SF-0202 until an environment sets `identityUrl` and
  `serviceIdentity` and identity trusts the shop floor key; the backfill runbook waits on that.
- R6–R8 fixed on the wave 3a branch (#727).

## Wave 3a (#727)

| Item | Commits | Review | Next step |
|---|---|---|---|
| B-05b: a Kafka user for every consumer (D34) | on the branch | approved on re-review, minors fixed | Merge with #727 |
| B-04: one authorization model, decision records (ADR-0017) | cecd9416, c62753bb, e6e4bbd6, R1/R2/R4 fix | [b04-review-1.md](reviews/b04-review-1.md) (3 Major), [b04-review-2.md](reviews/b04-review-2.md) (1 Major: alerts in a ConfigMap no Prometheus loads), [b04-review-3.md](reviews/b04-review-3.md): approved; R1 (docs advised copying fieldcrypt's master key into the OPA Secret), R2 (pin promtool checksum) and R4 (gate the Envoy check-error alert in dev) fixed before merge | Merge with #727 |

B-04 follow-ups: wire Secret `depot-log-pseudonym` into the service charts (fresh key, never fieldcrypt's master key);
a per-installation pseudonym key for depot-server; R3 (OPA's loopback query API can read the key; a supervisor
`FIELDCRYPT_KEYS_FILE` overrides a child's own `FIELDCRYPT_KEYS`); a live Envoy and Prometheus check (#213, including
early `X-Request-Id` removal); owner decision on machine callers at Envoy (#512).

## Wave 3c (#730)

| Item | Commits | Review | State |
|---|---|---|---|
| B-21: honest screens, real sequences and import pages, numbering list endpoint | 11c9a6b1, 6672a576, 98b4b41e | [round 1](reviews/b21-review-1.md) (4 Major), [round 2](reviews/b21-review-2.md) (1 Major: optional prop dodged the scan), [round 3](reviews/b21-review-3.md): approved | Merge with #730 |
| B-05c: event payload contracts per consumer, search receive operations and reconcile (index side) | 46ad2ed1, 78abe4d3, 70efe589 | [round 1](reviews/b05c-review-1.md) (4 Major), [round 2](reviews/b05c-review-2.md) (1 Major: not fail-closed after the first decode), [round 3](reviews/b05c-review-3.md): approved | Merge with #730 |
| B-09b: stage platform, databases and Secrets, session Valkey, numbering timeout, release pins | fe2eb629, c16d2d7e, e8330ed8 | [round 1](reviews/b09b-review-1.md) (1 Blocker: stage gateway; 4 Major), [round 2](reviews/b09b-review-2.md): approved; its 4 Minors fixed in e8330ed8 | Merge with #730 |
| CI fix: identity catalogue accepts the `{,/*}` glob; stage README wording | e74a24b1 | checked in B-21 round 3 | Merge with #730 |

Follow-ups from wave 3c:
- #731: source-count side of the search reconciliation (nine services).
- B-05c R3-m1/R3-m2: the contract analyzer does not follow module types with their own `UnmarshalJSON`, and a field's mark clears after its first followed decode.
- B-21 r3-m1/r3-m2: a route passing one harmless prop, or a wrapper in its own `.tsx`, can still slip a fake page past the scan. Planned-route target dates (2027-02-26, 2027-05-28, 2027-06-30) need owner confirmation. `at-pass-template.md` has no owner.
- B-09b: v0.2.0 not cut and no image digests recorded, so no Depot service starts in stage yet; nothing serves `idp.stage.depot.dev` for browser sign-in; a role keeps LOGIN if its Vault key is removed; stage consumers, monitoring and Temporal mTLS are off. Prod is B-09c.
- The six `deploy/multi-region/test_chart.py` schema tests fail locally with helm 3.16's error wording (also on main).
