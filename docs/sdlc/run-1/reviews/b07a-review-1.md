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

---

# Re-review of the fix commit 59409863 (on top of d7c7723d)

## Verdict: CHANGES REQUIRED

Eleven of the twelve findings are fixed correctly, and #2 is resolved as a documented risk. The fix for #6 opens a new Blocker (N1): the SECURITY DEFINER function `audit_anchor_record` does not put `pg_temp` in its search_path. As a result, `depot_app` can run arbitrary SQL as the function owner and plant fabricated anchors. I reproduced both on the local database.

## Outcome per finding

1. **Forged depot.ctx: fixed (two Nits, N3 and N4).**
   - Domain separation holds. Detached signatures sign `"depot.audit-ctx.v1." || msg`; service tokens sign `"depot.service-token.v1." || body`. Neither prefix is a prefix of the other, and the key signs nothing else (the only other `ed25519.Sign` is the token path).
   - The signature covers tenant, txid and the exact body bytes, newline-separated. None of the three can contain a newline, so the encoding is unambiguous. The body also carries the tenant, and `Verify` takes the tenant from the parsed body.
   - The issuer is bound to the source database: `Verify` requires `env.Iss == source`, where `source` is the table registry's owning service, and looks up the key by that issuer. Party runs with `DEPOT_SERVICE_NAME=party`.
   - The txid check compares the low 32 bits of `txid_current()` (epoch-extended) with Debezium's 32-bit BEGIN xid. That is correct, and `txid_current()` returns the top-level xid even inside a savepoint. A signed envelope copied into another transaction therefore fails, unless the low 32 bits repeat inside the 1-hour window, which is not practical.
   - When party has no key, `Emit` writes the message unsigned and logs once. The consumer records the change as `system` with flag `unattributed-bad-signature`, and the business change still goes through.
   - When the consumer has no verifier, `Verifier` stays a nil interface and every message is rejected with `ErrNoVerifierKeys`.
   - Tests cover the forged, no-keys, wrong-issuer and other-transaction cases.
2. **Mixed-version or rollback fork: resolved as a documented risk.** README §"Rollout, and no rollback past B-07a", the runbook's "First: was it a fork…" section, and the new `fork` break kind (the row recomputes and links to one of the last 10,000 hashes) together let operators tell a fork from tampering. The §7.18 expand/contract rule is still bent, but that is now stated plainly. Accepted.
3. **Stale context on xid reuse: fixed.**
   - The join now requires `committed_at` within ±1 h of the change's source commit time. Debezium stamps the message and the changes with the same commit `ts_ms`.
   - Rows from before 0011 get `-infinity`, so they never join.
   - The consumer purges expired contexts itself, at most once an hour per tenant, inside the context's inbox transaction.
   - The DLQ replay limits are documented.
   - The reasoning about the window is sound: reusing a low-32 xid within an hour would take more than 1.2 million transactions per second.
4. **Legacy "|" ambiguity: fixed.** Such rows are counted as `knownBreaks.legacyAmbiguous`, which covers every field of the legacy payload except the time.
5. **Scope and cost of `:verify`: fixed, with a new Minor (N2).**
   - `tenantWide` requires `LegalEntityScopes(p)=="*"` (no `le` claim, the all-scope duty and the ALL header on a GET) or `dbx.TenantWide(p, "audit.verify")`. Anyone else gets 403 AUD-0009.
   - A request walks at most 100,000 rows. The response carries `complete`, `nextFromSeq` and `startPrevHash`.
6. **Planted anchors: not fixed, and a regression.** See N1.
7. **Connector on, consumer off: fixed.**
   - The connector has `state: stopped`, which Strimzi 0.46 supports.
   - The topic is declared as a KafkaTopic with 30-day retention.
   - The heartbeat query is a non-transactional logical message, and the heartbeat topic is routed into the audit topic, where the decoder skips it (`op` empty, so ignored).
   - Nit N5: I have not verified that a heartbeat whose prefix is filtered out actually advances the slot.
8. **Sealed columns: fixed.**
   - The wording is corrected.
   - The CDC role now gets column-level SELECT without `*_sealed`.
   - I checked the upgrade both ways on the local database, with a renamed role and per-schema copies. On the old-0021 path (table-wide SELECT, sealed readable) followed by 0022, the role ends with no table SELECT, `account_holder` readable, and both sealed columns unreadable. On the fresh path, the new 0021 plus 0022 gives the same result. Both migrations re-run cleanly, and REPLICA IDENTITY stays FULL.
   - Editing the merged 0021 is safe: `dbx.Migrate` keeps no checksums, and 0022 converges the old state.
9. **Client-supplied trace id: fixed.** It is documented as client input.
10. **`dlqTopic` unused: fixed.** It is wired through `AUDIT_CDC_DLQ_TOPIC`.
11. **One-partition invariant: fixed.** There is a KafkaTopic with `partitions: 1`, and test_argocd_apps checks it.
12. **No lock timeout in 0021: fixed.** 0021 now sets `lock_timeout`.

## New findings

### N1. Blocker: `audit_anchor_record` can be hijacked through pg_temp, from depot_app up to the table owner

**Where:** `services/audit/migrations/0011_cdc_review_fixes.sql:31` (`SECURITY DEFINER SET search_path FROM CURRENT`).

**Cause.**
- `FROM CURRENT` captured `search_path=<schema>` without `pg_temp`.
- When `pg_temp` is not listed, PostgreSQL searches it first for relations.
- The function refers to `audit_entry` and `audit_anchor` without a schema.
- `depot_app` holds EXECUTE on the function, and like PUBLIC it can create temp tables, views and functions.

**Reproduced on local PG16**, with audit migrations 0001–0012 applied into schema `demo` by root and the calls made as `depot_app`:
1. `create temp table audit_entry(tenant_id, seq, hash)` holding seq 424242 and hash `ffff…`, then `select demo.audit_anchor_record(today, 424242, …)`. It returned `t`, and `demo.audit_anchor` gained an anchor for a seq that does not exist, with hash `ffff…` and `verified=t`. No real `audit_entry` row exists.
2. `create function pg_temp.evil()` that runs `alter table demo.audit_anchor disable trigger audit_anchor_no_mutation; delete from demo.audit_anchor; …`, plus a temp view `audit_entry` that calls it. Calling the function printed `NOTICE: evil runs as root` (the owner), deleted the earlier anchor, and inserted a new fabricated one.

**Impact in production.** The owner is the migration role that owns `audit_entry`. Any code or credential running as `depot_app` can therefore disable the append-only triggers on the chain and rewrite or delete chain rows and anchors. That is exactly the owner-level tampering the chain and the anchors exist to detect, and it is far worse than the original Minor #6.

**Fix.**
- Pin an explicit path with `pg_temp` last. For example, after CREATE: `EXECUTE format('ALTER FUNCTION audit_anchor_record(date,bigint,bigint,boolean,bigint,bigint,bigint) SET search_path = %I, pg_temp', current_schema());`. Also schema-qualify both tables in the body.
- Add a test in `TestAnchorsCannotBePlanted`: as depot_app, create a temp `audit_entry` and a temp `audit_anchor`, call the function, and assert that it read the real head (or raised) and that no fabricated row exists.
- Optionally revoke TEMP on the database from depot_app.

### N2. Minor: `:verify` paging does not check the link across a page boundary

**Where:** `services/audit/internal/store/chain.go:124`.

**Scenario.**
- A page stops at `last` and has already fetched the next row (`next`). The next request starts at `fromSeq=next`, trusts that row's prevHash, and drops anchors after `last`.
- Suppose rows between `last` and `next` were removed (owner-level, which N1 currently makes possible), together with an anchored head among them.
- Then both pages report `verified: true`, and the anchor in the gap is checked by neither.
- Only a client that compares `startPrevHash` with the previous page's `head` notices. The nightly job walks the whole chain, so it still catches this.

**Fix.** Before stopping, compare the fetched `next` row's prevHash with `walker.prev` and fail `link-mismatch` if they differ. Keep anchors with seq below `next` in the page's anchor set.

### N3. Nit: detached-signature domain check is exact match only

**Where:** `platform/go/tenant/detached.go:13`.

`detachedDomainOK` refuses only exact equality with the token context. A future domain such as `depot.service-token.v1.x.` would share a prefix with it. Refuse any domain that is a prefix of `serviceTokenContext`, or has it as a prefix. That is safe today, because only `depot.audit-ctx.v1.` is used.

### N4. Nit, residual once N1 is fixed: today's anchor can still be pre-claimed

Even after N1 is fixed, `depot_app` can pre-claim today's anchor with a real but stale seq, or seq 0. It also supplies the verdict columns (`verified`, `entries`, `breaks`), so the nightly job's real head for that day is not anchored (`Created=false`). This is bounded to one day at a time. Consider writing the verdict columns only from the CronJob's own role.

### N5. Nit: heartbeat slot advance not verified

**Where:** `deploy/kafka/connect.yaml:4287`.

The heartbeat message's prefix `depot.heartbeat` is not in `message.prefix.include.list`. Check on staging that `confirmed_flush_lsn` of `depot_audit_party` advances while `vendor_bank_account` is quiet.

## Tests run (re-review)

All runs used fresh databases `rev_b07a_1`, `rev_b07a_2` and `rev_b07a_3` (recreated), and depot_app authenticated with `depot_app`.

| Command | Result |
|---|---|
| `services/audit`: `go vet ./... && go test -count=1 ./...` | PASS (all 6 packages) |
| `platform/go`: `go test -count=1 ./auditctx/... ./tenant/... ./httpx/... ./eventing/...` | PASS |
| `services/party`: `go test -count=1 ./...` | PASS (8 packages) |
| `tools/rlscheck`: `go test -count=1 ./...` | PASS |
| `python3 deploy/scripts/test_argocd_apps.py` | OK (49) |
| `gen_networkpolicies.py --check` | OK |
| `helm template` and `kubeconform -strict` (consumer, nightly and publicKeys set) | 5 of 5 valid; `helm lint` OK; consumer env has `AUDIT_CDC_DLQ_TOPIC` and `DEPOT_SERVICE_NAME` |
| Manual SQL: party old 0021 → 0022, and fresh 0021 + 0022 (renamed role, per-schema) | Both end with column-level SELECT and no sealed columns |
| Manual SQL: `audit_anchor_record` pg_temp hijack as depot_app (N1) | Reproduced: fabricated anchor planted, and arbitrary SQL ran as the owner |
| `opa test policies/authz` | Not rerun: the fix commit does not touch `policies/` (289/289 earlier) |

I dropped the demo schemas and the temporary role afterwards.

---

# Re-review 2 of cbd21b13 (N1–N5 only)

## Verdict: APPROVE, with one Minor follow-up (R2-1)

The N1 Blocker is closed. I re-ran my exploit and five variants, and all of them now fail. N2 and N3 are correct. N5 is accepted.

The N4 change works, but it promises more than it delivers. Under a non-superuser owner, depot_app can still anchor a stale (real, older) head by narrowing its legal-entity scope (R2-1). The anchor stays true data and verification stays sound, so this weakens a defence-in-depth claim rather than integrity. It is a one-line fix and is not blocking.

## N1, `audit_anchor_record` hijack: fixed

Migration 0013 recreates the function in four ways:
- `SET search_path = pg_catalog, <schema>, pg_temp`, computed with `format('%I', current_schema())`;
- every relation in the body schema-qualified;
- every function call written `pg_catalog.`;
- EXECUTE revoked from PUBLIC and granted to depot_app only.

The function's settings and grants (`proconfig` and `proacl`) confirm this.

Method: audit migrations 0001–0013 applied into a fresh schema (by superuser, and separately by a non-superuser owner role), then each variant run as `depot_app`.
- **Temp table `audit_entry`** with a fabricated seq 424242 and hash `ffff…`: refused with "seq 424242 is not the chain head 0". No row was written.
- **Temp view `audit_entry` calling `pg_temp.evil()`, plus a temp `audit_anchor` view**: refused. `evil()` never ran (no NOTICE).
- **Type shadowing** with `pg_temp.uuid`, `pg_temp.date` and `pg_temp.text` domains: ignored, because pg_catalog comes first for types and pg_temp last.
- **Function shadowing** with `pg_temp.current_setting(text,bool)` and `pg_temp.hashtextextended`: ignored. Functions are never looked up in pg_temp, and every call is qualified anyway. The anchor went into the real table for the tenant the real `current_setting` returned (3333…), not the shadow's 4444….
- **Shadow `pg_temp.audit_anchor` table**: ignored, because the INSERT is schema-qualified.
- **Date window**: tomorrow and today−2 are both refused with 22023.
- **Direct INSERT into `audit_anchor`**: permission denied.

Other paths I checked:
- **Shadowing pg_catalog:** impossible, because pg_catalog is listed first. depot_app has no CREATE on the service schema, on `public`, or on anything else in the path. No migration grants CREATE, and PG15+ removes CREATE on `public` from PUBLIC.
- **Operators** (`=`, `>`, `<`, `-`, `||`, `AT TIME ZONE`, `IS DISTINCT FROM`): resolved in pg_catalog with exact matches. pg_temp is never searched for operators.
- **NULLIF:** it resolves through text `=` text, which is in pg_catalog.
- **Implicit casts:** these come from `pg_cast` by OID, not from the search_path, and only a type's owner can create one.
- **Triggers fired from the definer:** `audit_anchor` has only BEFORE UPDATE/DELETE and BEFORE TRUNCATE triggers. INSERT … ON CONFLICT DO NOTHING fires neither. SELECT on `audit_entry` fires none.
- **RLS policies and column defaults** (`now()`): stored as parse trees with OIDs, so the search_path does not affect them.
- **Remaining caller control:** the caller chooses the tenant through `depot.tenant_id`. That is inherent, since depot_app is trusted per tenant everywhere.

## rlscheck definer check: sound for its criterion, covers every service

- `definerPathSafe` requires a non-empty `search_path` whose last element is `pg_temp`. An unset path, `FROM CURRENT` without pg_temp, an empty path (pg_temp then comes first implicitly) and pg_temp-first are all flagged. The fixture pins three violations plus one safe function.
- It scans the service schema, plus any schema the service's migrations create, for every service in `TestEveryTableEnforcesRLS`. That includes the Go-embedded `dbx.ServiceTokenReplayDDL`, which runs through migrations.
- No migration creates functions in a pre-existing schema such as `public.`.
- The suite passes with no new allowlist entries, so every SECURITY DEFINER function in the repository now ends its path in pg_temp, including agents-ts, edi, billing, close and the service_token_replay functions.
- Nit: the check does not confirm that the schemas listed before `pg_temp` are not writable by depot_app or PUBLIC. That holds today, but a future `GRANT CREATE` would silently reopen shadowing. Consider adding `has_schema_privilege('depot_app', s, 'CREATE')` for each schema in the path.

## N2, page-boundary link: fixed

- When a page stops, `Boundary(next)` fails `link-mismatch` if the fetched next row does not link to the last row walked.
- `DropAnchorsAfter(next-1)` keeps any anchor whose head lies between the two rows, so a removed anchored row is reported as missing.
- `TestVerifyPagesCheckTheirBoundary` removes the row at the boundary and passes.

## N3, prefix-related domains: fixed

Both prefix directions are refused, and so are `depot.` and the empty suffix. Tested.

## N4, head under the append lock: partly fixed, new Minor R2-1

- The retry loop is correct. A 40001 aborts the chainTx and `AnchorDay` walks again, up to 5 times.
- The head is read under the same advisory key that `AppendTx` uses. A volatile plpgsql function takes a new snapshot for each statement under READ COMMITTED, so the head read after the lock sees every append committed before it.

**R2-1, Minor:** `services/audit/migrations/0013_anchor_record_search_path.sql:38`. The head read inside the definer function is subject to the caller's `depot.legal_entity_scopes`.
- In production the function owner is a non-superuser migration role, and FORCE RLS binds it.
- So depot_app can run `set_config('depot.legal_entity_scopes', '', true)`, which leaves only NULL-entity rows visible, and anchor an older head.
- Reproduced with a non-superuser owner role. The chain had seq 2 (NULL entity) and seq 3 (entity 6666…). With scope `''`, the function called seq 2 "the chain head" and returned `t`, and the anchor stored head_seq 2 while the real head was 3.
- The stored hash is the real hash of seq 2, so `:verify` stays sound. The real head is not anchored that day, though, which contradicts the 0013 comment ("an anchor names the newest row at the moment it is written").
- The CI suite cannot catch this, because it runs migrations as a superuser, who bypasses FORCE RLS. The same scan as a superuser owner returned the true head.
- **Fix:** add `PERFORM pg_catalog.set_config('depot.legal_entity_scopes', '*', true);` before the head read. It is transaction-local, and the nightly job's `chainTx` already sets `*`. Then add a store test that runs the migrations as a non-superuser owner, or document the residual next to the existing "claim today's anchor early" note.

## N5, heartbeat slot advance: accepted

The runbook now has a staging check that `confirmed_flush_lsn` advances. That the heartbeat prefix is filtered out is stated as deliberate.

## Tests run (re-review 2)

| Command | Result |
|---|---|
| `services/audit`: `go vet ./... && go test -count=1 ./...` (`rev_b07a_2`) | PASS (all 6 packages), including the new N1 temp-view test and `TestVerifyPagesCheckTheirBoundary` |
| `platform/go`: `go test -count=1 ./tenant/... ./auditctx/...` | PASS |
| `tools/rlscheck`: `go test -count=1 -v ./...` (`rev_b07a_3`) | PASS, including `TestEveryTableEnforcesRLS` (all services) and `TestFixtureViolationsArePinned` (3 definer violations pinned) |
| Manual SQL as depot_app against 0013 (superuser-owned schema) | All 5 exploit variants refused, as listed above |
| Manual SQL as depot_app against 0013 (non-superuser owner) | R2-1 reproduced: stale head anchored under scope `''` |

I dropped the demo schemas and the temporary owner role afterwards.

Unrelated observation: applying audit 0007 as a non-superuser owner in a shared database fails with "must be owner of publication depot_outbox_audit", because another session's superuser created the publication first. This is the shared-database publication caveat; it predates B-07a and is not a finding.
