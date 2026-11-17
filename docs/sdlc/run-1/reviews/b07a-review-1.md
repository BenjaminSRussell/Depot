# Review of B-07a, commit d7c7723d (CDC-fed audit pipeline, depot.ctx, verify, anchor)

Reviewer: independent code review. No files in the worktree were changed.
Scope: `git show d7c7723d`, 52 files. Read against backlog B-07 (part a), architecture §7.16 and §7.18, ADR-0006 and ADR-0010.

## Verdict: CHANGES REQUIRED

The pipeline is well built and well tested. Things that work as intended:

- The actor comes from the verified principal, and `X-Depot-User: service:*` is still refused.
- The tenant match is enforced by RLS on `cdc_tx_context`. A context from another tenant, a malformed context, a context with the wrong shape for its kind, or a context written after the change is recorded as `system`/`unattributed`, and tests cover each case.
- `AppendTx` takes the per-tenant advisory lock before it reads the head, and reads the head with scope `*`, so CDC appends and HTTP appends cannot fork the chain while running the same image.
- The inbox key `<source>:<txId>:<lsn>:<table>:<id>` makes redelivery idempotent.
- The v3 hash is domain-separated from v2.
- Anchors detect a chain that has been rewritten or cut short.
- RLS on the new tables is correct, and rlscheck passes.
- The charts render and validate.

Two Major issues block approval:

- A well-formed forged `depot.ctx` is trusted (finding 1).
- The new restrictive policy makes a mixed-version or rolled-back B-06 replica fork the chain permanently (finding 2).

## Findings

### 1. Major: a well-formed forged depot.ctx is attributed to the user it names

**Where:** `services/audit/internal/cdc/handler.go:72` and `:93`, `platform/go/auditctx/auditctx.go:174` and `:193`.

**Scenario.** Any session on `depot_party` can call `select pg_logical_emit_message(true, 'depot.ctx', '{"v":1,"tenantId":"<T>","actor":"<cfo-user-id>","actorKind":"person","reason":"approved"}')` and then insert into `vendor_bank_account` in the same transaction. Sessions that can do this include:

- `depot_app`, as `TestWithAuditedTxEmitsAsTheAppRole` proves (the function needs no privilege);
- the migration owner;
- `debezium_cdc_party`;
- an ops tool;
- SQL injection in party.

The consumer checks only the JSON shape (`auditctx.Decode` and `domain.Validate`) and the tenant. The chain then records the CFO as the person who changed the bank account. The brief requires a forged context to be recorded as system/unattributed. Today only malformed forgeries are caught. A well-formed message from anyone with database access is indistinguishable from one that `auditctx.Emit` wrote.

**Fix.**

- Authenticate the message with the service's existing Ed25519 signing key (ADR-0010). `Emit` signs `tenantId || txid_current() || body` with party's key. The consumer verifies the signature against the public key registered for the table's `Source` and checks that the signed txid equals `source.txId`. A message that is unsigned or fails verification goes down the unattributed path.
- That ties forgery to the service key, not to database credentials.
- If this is deferred to B-07b, record it as an accepted residual risk in §7.16, the README and NOT-IMPLEMENTED. Also stop the docs from describing the actor as "verified" end to end.

### 2. Major: a mixed-version or rolled-back replica forks the chain permanently

**Where:** `services/audit/migrations/0009_cdc_change_feed.sql:38` (restrictive policy), with the new head read at `services/audit/internal/store/store.go:135`.

**Scenario.**

1. Migration 0009 makes rows with a non-NULL `legal_entity_id` invisible outside the caller's scope.
2. The new image writes such rows. HTTP ingest writes `tokenLegalEntity(p)` and CDC writes the row's legal entity.
3. A B-06 replica still reads the head with `select hash from audit_entry where tenant_id=$1 order by seq desc limit 1`, inside `dbx.WithTenantTx` and under the caller's legal-entity scope. Engineering's `audit.write` token is not tenant-level, so it carries one `le`.
4. If the latest row belongs to another legal entity, the old replica links to an older row. The advisory lock does not help, because the read itself is filtered.
5. The result is a permanent `link-mismatch`, because the table is append-only and cannot be repaired. From then on the nightly job fails every night, and the runbook treats the break as owner-level tampering: rotate credentials, freeze the schema.

README §Rollout (line 88) covers the forward rollout with `strategy.type=Recreate`, but not a rollback to the B-06 image after any row with a legal entity exists. That rollback forks as soon as engineering posts under a different legal entity than the head row's. This also breaks the §7.18 rule that the previous image must run against the new schema.

**Fix (any one of):**

- (a) Expand/contract. Ship the columns and the code that reads the head with scope `*` in this release, and add the restrictive `audit_entry_legal_entity_scope` policy one release later, once no B-06 image can run.
- (b) Keep the chain head in a per-tenant `audit_chain_head` row with no legal-entity policy, updated under the lock. Even then, the old image still needs (a).
- (c) At minimum, document "no rollback past B-07a" in README, CHANGELOG and the runbook. Add to the runbook that a `link-mismatch` right after a deploy or rollback is a fork, not tampering.

### 3. Minor: a stale depot.ctx can attach to a later transaction that reuses the xid

**Where:** `services/audit/internal/store/cdc.go:31` (`tx_id = $3 and lsn <= $4 order by lsn desc limit 1`), `services/audit/cmd/audit/cdc.go:30` and `:149`.

**Scenario.**

- Debezium's `txId` for pgoutput is the 32-bit xid from BEGIN, so it wraps.
- The only defence is the 7-day purge in `chain-nightly`. That job is a separate, off-by-default CronJob, and it purges only the tenants listed in `chainNightly.tenantIds`.
- With the consumer on and the CronJob off, or a tenant left off the list, `cdc_tx_context` is never purged.
- After the xid wraps, a transaction with no message, or with a forged one from another tenant, that reuses xid X matches the old context for X, which has a lower LSN. The unattributed change is then recorded as a real user.
- Also, a dead-lettered row change replayed after 7 days loses its attribution. A dead-lettered `depot.ctx` that is replayed later cannot fix rows already appended as unattributed. Neither case is in the README or the runbook.

**Fix.**

- Bound the lookup in `FindTxContext`, for example `and recorded_at > now() - interval '1 day'`, or keep only the most recent context per source and require the same txid.
- Have the consumer purge old contexts itself instead of relying on the CronJob.
- Document the DLQ replay limits.

### 4. Minor: `:verify` passes field-shifting tampering on legacy rows

**Where:** `services/audit/internal/domain/chain.go:257` (legacy payload, `strings.Join(..., "|")`), used by `ChangeWalker.Next` at `services/audit/internal/domain/verify.go:83`.

**Scenario.**

- Take a legacy row with `oldValue="a|b"` and `newValue="c"`. Rewrite it to `oldValue="a"` and `newValue="b|c"`, or move a `|` between `userId` and `traceId`. The rewritten row has the same payload and the same hash, so `:verify` reports `verified: true`.
- The encoding predates this change, but this commit is the first to offer a verdict on these rows.
- The `legacyPrecision` recovery (999 offsets) and the legacy-fork rule are sound: neither lets a rewritten row through without also breaking the next link, as I checked.

**Fix.** Report legacy rows whose fields contain `|` as a known break of their own, such as `legacy-ambiguous`, or count all legacy rows as "integrity-limited" in `knownBreaks`. Note this in README "Hash encoding".

### 5. Minor: `chainTx` widens the scope to `*` for any principal, and `:verify` is an unbounded synchronous walk

**Where:** `services/audit/internal/store/store.go:111` and `:117`, `services/audit/internal/api/verify.go:44`.

**Scenario, scope.**

- `chainTx` sets `depot.legal_entity_scopes='*'` with no check, which bypasses the repo's `dbx.TenantWide` / `WithTenantWideAdminTx` gate.
- A caller who holds `audit.verify` but is scoped to one legal entity therefore learns things about every other legal entity: the tenant-wide entry count, the seq range, the head hash and `firstBreak.id`.
- That is small, but it reveals how much activity other legal entities have.

**Scenario, cost.**

- `GET /v1/audit/records:verify` with no range recomputes every row inside one transaction.
- Every legacy-precision row costs up to 999 extra SHA-256 hashes. Per the B-06 changelog, that covers every engineering signature.
- Repeated calls tie up audit CPU and a pool connection.

**Fix.**

- Require tenant-level authority for an open-range verify: no `le` claim, or the `dbx.TenantWide` rule.
- Or cap the range per request, for example 100k seqs, and point full walks to the CronJob.

### 6. Minor: the app role can plant an anchor that blocks the real one and fails verification permanently

**Where:** `services/audit/migrations/0009_cdc_change_feed.sql:136` (`GRANT ... INSERT ON audit_anchor TO depot_app`), `services/audit/internal/store/chain.go:137` (`on conflict (tenant_id, anchor_date) do nothing`).

**Scenario.**

- Any code running as `depot_app`, including the audit HTTP pods, can insert `audit_anchor` rows for any date, including future ones, with any `head_seq` and `head_hash`.
- The nightly job then keeps the planted row (`Created=false`), and every walk reports `anchor-mismatch`.
- The append-only trigger means only the owner can remove the row. The result is denial of service on verification, and a false tampering alarm.

**Fix.**

- Insert anchors through a `SECURITY DEFINER` function that computes the head itself and refuses `anchor_date > current_date`.
- Or give the CronJob its own role and grant INSERT only to that role.
- Add `CHECK (anchor_date <= (now() at time zone 'utc')::date + 1)`.

### 7. Minor: the connector is always deployed while the consumer is off, with 7-day retention and no heartbeat

**Where:** `deploy/kafka/connect.yaml:4251` and `:4278`, and `deploy/envs/dev/apps/event-backbone.yaml`, which syncs `connect.yaml` and `*-consumer.yaml`.

**Scenario.**

- In dev, `audit-cdc-party` runs once party 0021 has been applied. Its consumer is off by default (`consumer.enabled: false`).
- `depot.cdc.party` keeps records for 7 days, so captured changes expire without ever reaching the chain. An audit gap is silent, and nothing alerts on it.
- The second slot `depot_audit_party` captures one low-traffic table and has no `heartbeat.interval.ms`. Debezium's docs warn that such a slot can hold WAL back while other tables in the same database are busy. I have not measured this for the Debezium and PG17 versions in use.

**Fix.**

- Give the audit source topics a longer retention, or gate the connector behind the same switch as the consumer.
- Add `heartbeat.interval.ms` and `heartbeat.action.query`, or an alert on slot lag.
- Add an alert on consumer lag for group `audit-cdc`.

### 8. Minor: sealed P3 columns do leave the database, contrary to the docs

**Where:** `deploy/kafka/connect.yaml:4270`, `services/party/migrations/0021_audit_cdc_publication.sql:12` and `:35`, and the README, §7.16 and CHANGELOG wording.

**Scenario.**

- `column.exclude.list` filters inside the Connect worker. pgoutput still sends `account_number_sealed` and `routing_number_sealed`, as ciphertext, over the replication connection to Connect.
- REPLICA IDENTITY FULL writes the full old row into the WAL on every update or delete.
- `debezium_cdc_party` gets table-wide `SELECT`, sealed columns included. Its least-privilege story so far was "reads only the outbox".
- The docs say the values "never leave the source database". Because the values are AES-GCM ciphertext, the impact is low.

**Fix.**

- Correct the wording.
- Grant column-level SELECT that leaves out the `*_sealed` columns.
- Note that a publication column list cannot drop columns while the table uses REPLICA IDENTITY FULL.

### 9. Nit: trace ids come from client headers and are not authenticated

**Where:** `platform/go/auditctx/auditctx.go:115` and `:153`.

`traceparent`, and the `X-Request-Id` that chi's `RequestID` honours, both come from the client and are hashed into the chain. A user can give their change another request's trace id. They are not authority fields, so this is low impact. Prefer the gateway- or OTel-generated span, or record that the trace id is client-supplied.

### 10. Nit: `consumer.kafka.dlqTopic` is unused

**Where:** `services/audit/charts/audit/values.yaml:28`.

The dead-letter topic is hard-coded as `cdcDeadLetter` in `cmd/audit/cdc.go`. Wire the value through or remove it.

### 11. Nit: the one-partition invariant is not declared

**Where:** `deploy/kafka/connect.yaml`.

The single partition of `depot.cdc.party`, which the message-before-changes ordering depends on, rests only on `topic.creation.default.partitions: 1`. There is no `KafkaTopic` CR, and test_argocd_apps.py does not check it. Declare a `KafkaTopic` with `partitions: 1` and assert it in the test.

### 12. Nit: no lock timeout on the REPLICA IDENTITY change

**Where:** `services/party/migrations/0021_audit_cdc_publication.sql:12`.

`ALTER TABLE vendor_bank_account REPLICA IDENTITY FULL` takes ACCESS EXCLUSIVE and sets no `lock_timeout`. Audit 0009 does set one. Add `SET LOCAL lock_timeout = '5s';`.

## Checked and found sound

- **Party 0021 as a non-superuser owner.** It needs the same privileges as 0019: CREATE on the database, plus ownership of the table and the publication. It re-runs cleanly.
- **Publications on a shared test database.** They are database-wide, so per-schema tables accumulate in `depot_audit_party`. Dropped schemas remove their entries, and the duplicate-object handling covers races, the same pattern as 0019. The party CDC role test uses a suffixed role, so the real-role grant in 0021 does not interfere.
- **OPA.** `audit.verify` opens only `GET /v1/audit/records:verify`. `audit-cdc` and `audit-anchor` each hold one flag duty with no route, and their issuer is `audit` only. That is least privilege.
- **rlsexempt.** The two NULL-entity entries are justified: before 0009 there was no legal-entity policy at all. Small wording point: NULL also covers legacy rows that belong to a legal entity, not only tenant-level records.
- **Kafka ACLs.**
  - Connect gets literal Write on `depot.cdc.party`.
  - The consumer has Read on that topic and the `audit-cdc` group, and Write on the DLQ only.
  - `auto.create.topics.enable` is false.
  - RegexRouter routes both `.message` and table topics into one topic.
- **Message attachment.**
  - Matching by txId and LSN is correct for pgoutput: a transactional message carries the BEGIN xid and its own record LSN, which is lower than the LSNs of the changes after it.
  - A second message at the same position is ignored.
  - A context from another tenant is invisible under RLS.
- **Verifier range edges.**
  - An open `toSeq` also checks anchors beyond the last row, so it detects truncation.
  - A bounded range checks only the anchors inside it.
  - A `fromSeq` later than the first row trusts that row's `prevHash`, as documented.
  - A reversed or non-positive range is refused with AUD-0008.
  - Minor cosmetic point: an empty range reports `head` as Genesis.

## Tests run

Environment: `PATH+=/usr/local/go/bin:/root/go/bin:/root/.local/bin`, local PG16 `main`, a fresh database per run (`rev_b07a_1`, `rev_b07a_2`, `rev_b07a_3`) with pg_trgm and btree_gist, `depot_app` present.

| Command | Result |
|---|---|
| `cd services/audit && go test -count=1 ./...` (`rev_b07a_1`) | PASS: cmd/audit, internal/api, internal/cdc, internal/domain, internal/store, migrations. Ran with `-v`: no DB test skipped. |
| `cd platform/go && go test -count=1 ./auditctx/... ./httpx/... ./eventing/...` | PASS, including `TestWithAuditedTxEmitsAsTheAppRole` against the DB |
| `cd services/party && go test -count=1 ./...` (`rev_b07a_2`) | PASS, all 8 packages, including `TestAuditCDCMigrationPublishesVendorBankAccounts` |
| `cd tools/rlscheck && go test -count=1 ./...` (`rev_b07a_3`) | PASS, including `TestEveryTableEnforcesRLS/audit` |
| `opa test --timeout 120s policies/authz` (as CI does) | PASS 289/289 |
| `opa test policies/` (literal command from the brief) | 278/289 FAIL. This is not a regression: rooted at `policies/`, OPA loads `data.json` under `data.authz.*`, so `data.duties` is undefined. CI runs `policies/authz`. |
| `python3 deploy/scripts/test_argocd_apps.py` | OK, 49 tests |
| `python3 deploy/scripts/gen_networkpolicies.py --check` | OK, so the `podNames` edit matches the generator |
| `helm template` audit chart, defaults | Renders 3 resources; `kubeconform -strict`: 3 valid |
| `helm template` with consumer and chainNightly enabled (brokers, secret, one tenant) | Renders 5 resources; `kubeconform -strict`: 5 valid, 0 invalid. `helm lint`: 0 failed. |
| `helm template` with chainNightly enabled and no tenants, and with the consumer but no brokers | Fails with the intended `fail`/`required` messages |
