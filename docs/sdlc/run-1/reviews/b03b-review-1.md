# Review: B-03b, commit c0611102

Branch `worktree-agent-a82fcea5f4a83448d`, parent 8aaf7a60. Reviewer: independent code review (correctness and security). No repository files were edited. Probes ran in the scratchpad only.

## Verdict: CHANGES REQUIRED

Most of the change is sound:
- The OPA rule fails closed on every malformed `le` I tried.
- The bundle query is correct.
- The shop floor restrictive and EXISTS policies do not leak.
- All suites pass.

However, turning on scoped assignments in OPA without limiting *which* duties may be scoped opens a tenant-wide escalation through SCIM (finding 1). The new `:assign-legal-entity` route can also permanently orphan a worker with no audit trail (findings 2 and 3).

## Findings

### 1. Blocker: a legal-entity-scoped assignment now grants tenant-level duties, including tenant-wide SCIM provisioning

- **Where:**
  - `policies/authz/authz.rego:27-37` (second `held_duties` rule)
  - `services/identity/scim_api.go:55`, `:123-127`
  - `services/identity/role_assignments_api.go:107`, `:133`
- **What:** `held_duties` grants *any* duty a scoped assignment carries once `le` matches. It does not consider whether the duty acts on per-legal-entity data at all.

  Several person-assignable duties act on tenant-level resources:
  - `identity.user.provision` (`/scim/v2/**`)
  - `identity.user.maintain`
  - `identity.security-role.maintain`
  - `identity.legal-entity.maintain`
  - `identity.feature.maintain`
  - `identity.setup.maintain`
  - `identity.tenant.maintain`

  Before this commit, scoped assignments were left out of the bundle, so they granted nothing. Now they grant those tenant-level routes.
- **Failure scenario (verified at the policy level, and by code reading for the handler):**
  1. Identity lets a caller with an A-scoped `identity.security-role.maintain` create scoped assignments. Only *tenant-wide* ones need `dbx.TenantWide` (role_assignments_api.go:133). Only `tenant.administer` is refused a legal entity (:107). So a company-A security admin can give user X `identity.user.provision` scoped to A.
  2. X presents a realm token with `le` = A to `/scim/v2/...`.
  3. `RequireOPAForRequest` is the only gate on SCIM, and the SCIM principal has no duties. `opa eval` against the head policy with `scoped_assignments = {t: {X: {A: ["identity.user.provision"]}}}` returns:
     - `DELETE /scim/v2/Users/{id}`, `POST /scim/v2/Users`, `PATCH /scim/v2/Groups/g1` with `le=A`: `{"allow":true}`
     - the same requests without `le`: `{"allow":false}`
  4. `SCIMAPI.inTx` then runs with `depot.legal_entity_scopes = '*'`. X can create, deprovision or regroup every user of the tenant, ending their role assignments, including the tenant administrators except the last one.

  That turns a role scoped to one company into control of the whole tenant. The new test `test_scoped_role_grants_only_its_own_duties` (authz_test.rego:144) even asserts that a scoped `identity.tenant.maintain` grants `POST /v1/tenants`. That duty is limited to the operator tenant, so it is mitigated in identity, but it shows the rule has no notion of tenant-level duties.
- **Suggested fix:** Mark tenant-level duties in the catalogue, for example `"tenant_level": true` in `data.json`, or an allow-list of legal-entity-scopable duties. Then:
  - Make the scoped `held_duties` rule skip them: `not data.duties[duty].tenant_level`.
  - Have identity refuse to create a scoped assignment of such a duty, as it already does for `tenant.administer`.
  - Have the bundle compiler drop any existing ones.

  Add tests for scoped `identity.user.provision` on `/scim/v2/Users` and scoped `identity.user.maintain`.

### 2. Major: `:assign-legal-entity` accepts any UUID, and the freeze trigger then makes a typo permanent

- **Where:**
  - `services/shopfloor/internal/api/setup.go:443-451`
  - `services/shopfloor/internal/store/worker_maintain.go:40-63`
  - `services/shopfloor/migrations/0039_worker_legal_entity.sql:84-90`
- **What:** The handler checks only that the value is a non-nil UUID. Nothing checks that the legal entity exists in the tenant: there is no lookup against identity or org, and no replica table.
- **Failure scenario (verified, scratch-copy probe `TestProbeAssignUnknownLegalEntity`):**
  1. A tenant admin assigns `00000000-0000-0000-0000-00000000dead`. It succeeds (version 2, no error).
  2. Correcting it to company A returns `SF-0200 (409)`.
  3. A direct `UPDATE` is refused by `shopfloor_worker_legal_entity_guard`.

  The worker, and all its time and pay, are now visible only to `'*'` callers, with no API path to repair them.
- **Suggested fix:** Validate the legal entity against the tenant's legal entities before writing: call identity or org, or check against `httpx.OwnerLegalEntity`-style claim data if the admin selects it. Alternatively, let a tenant administrator correct a worker whose legal entity has no time or pay rows yet, rather than freezing on first write.

### 3. Major: assigning a worker's legal entity is unaudited and publishes no event

- **Where:** `services/shopfloor/internal/store/worker_maintain.go:40-63`; `AssignWorkerLegalEntity` takes no actor.
- **What:**
  - The sibling routes `AssignWorkerSetup` and `AssignWorkerResource` (setup.go:426-510) enqueue `worker.assigned.v1` with `AssignedBy`. `Update`, `Deactivate` and `Reactivate` publish `WorkerSaved` with `savedBy`.
  - The new route bumps `version`, but writes no outbox row, no actor and no audit column.
  - It is a security-relevant, irreversible decision: it chooses which company's users see a person's time and pay.
- **Failure scenario (verified):** In the probe, the tenant's outbox row count was 0 before and 0 after the assignment. An auditor cannot tell who assigned the worker, or when. Consumers of `worker.*` events also see a version jump with no event.
- **Suggested fix:**
  - Pass `p.UserID` as `assignedBy`.
  - Enqueue `WorkerAssigned`, or `WorkerSaved` with `change: "legal-entity-assigned"`, carrying `legalEntityId` and the actor.
  - Consider adding `legalEntityId` to the worker event payloads.

### 4. Minor: on deploy, every existing worker and its time and pay disappear for all scoped users, and the only remedy is one admin call per worker

- **Where:**
  - `services/shopfloor/migrations/0039_worker_legal_entity.sql:10-28`
  - CHANGELOG note
- **What:** The migration is data-safe: nullable columns only, policies and a trigger, nothing rewritten. I applied it as the table owner on a fresh database with no errors, and the policies were in place. But it is not behaviour-safe. It ships the expand step (the column) and the contract step (restrictive policies with no NULL branch) together, with no backfill.
- **Failure scenario (verified with SQL probes as `depot_app`):**
  - A NULL-legal-entity worker is invisible under scope A, and `time_registration` inserts on it fail the policy.
  - After deploy, every kiosk badge of an existing worker resolves to SF-0001, clock-ins are refused, and supervisors see empty lists.
  - Fixing it needs a tenant admin with no claim to make one POST per worker. To even find the IDs, the admin must also hold `org.switcher.all-scope` and read with the ALL scope.
  - Existing `pay_export_batch` rows have no assign route at all, so historical payroll exports stay invisible to every scoped user forever.

  Severity is Minor only because stage and prod are not open to users (ADR-0006 rule 6).
- **Suggested fix:** Add a bulk assignment, for example `POST /v1/shopfloor/workers:assign-legal-entity` with a list or a filter, plus a backfill of `pay_export_batch.legal_entity_id` from its lines' workers once the workers are assigned. Write a runbook step that gates the deploy on it. Optionally add `CHECK (legal_entity_id IS NOT NULL) NOT VALID` on `worker` and `pay_export_batch`, so new rows can never be NULL.

### 5. Minor: `widenForSystemCaller` trusts a user id string that a header caller can assert

- **Where:**
  - `services/shopfloor/internal/store/legal_entity.go:29-30`
  - `platform/go/tenant/tenant.go:349`
- **What:** The kernel rejects an `X-Depot-User` starting with `service:`, but not `system:shopfloor-sweeper`. A header request with that user id, `shopfloor.time.autoclockout` and no `le` is widened to `'*'` by `AutoClockOut` (route `POST /v1/shopfloor/registrations:auto-clock-out`).
- **Failure scenario:** Through devgateway the user id is the verified `sub`, so a person cannot choose it there. The path is reachable only with forged headers (the ADR-0006 rule 6 threat), and a forger already has wider options. This is defence in depth, not a live hole.

  I verified that no other user path reaches the widening: only the sweeper, service tokens and the production consumer call it. A supervisor scoped to A closes only A's shifts, and a person with no claim closes nothing.
- **Suggested fix:** Mark the sweeper with an unexported context value set in `Sweeper.runTenant`, instead of matching a string. Alternatively, have the kernel reject a `system:` prefix in `X-Depot-User` as it does `service:`.

### 6. Minor: the verifier accepts an array `le` claim, but the gateways forward only a string `le`

- **Where:**
  - `services/identity/jwt.go:70-80`
  - `services/identity/cmd/devgateway/gateway.go:25`, `:149`
  - `tools/depot-server/gateway.go:29`
- **What:** `legalEntityClaim` joins an array claim, so OPA sees `A,B`. Both gateways decode `le` into a `string` and ignore the unmarshal error, so they forward `X-Depot-Legal-Entity: ""` while `duties` is still filled.
- **Failure scenario:** With a token `le: ["A"]`, OPA authorizes as company A. The downstream service sees no claim, and if the token's duties include `tenant.administer`, it treats the caller as a tenant-wide administrator. The service-side behaviour existed before this commit, but the OPA and service views of the request now disagree.

  As built, Keycloak issues a single-valued string, and ADR-0006 says the list model will be a comma string because Envoy base64-encodes arrays.
- **Suggested fix:** Accept only a JSON string, and reject any other shape with 401, or treat it as no legal entity. Make the gateways' `claimsOf` use the same parser.

### 7. Minor: the `no-legal-entity-scope` detection in rlscheck is a text match and accepts a vacuous policy

- **Where:** `tools/rlscheck/rlscheck_test.go:77-81`
- **What:** Any restrictive `ALL` policy whose `qual` merely *mentions* `depot.legal_entity_scopes` counts as "follows its parent". For example, `USING (current_setting('depot.legal_entity_scopes', true) IS NOT NULL OR true)` passes, and so does an EXISTS on a parent without the entity condition.

  A table with `legal_entity_id` but no policy is still caught by the existing `no-legal-entity-policy` check: `TestJudge` covers it, and I confirmed it in the judge logic. So the gap is only for child tables.
- **Suggested fix:** Also require the `qual` to contain `legal_entity_id` and `string_to_array(current_setting('depot.legal_entity_scopes'`, the pattern the EntityPolicy check at :71 already uses. Add a fixture with a vacuous policy to `testdata`.

### 8. Minor: the nine "interim" exemptions are not tracked, while D17 now reads as done

- **Where:**
  - `platform/go/rlsexempt/rlsexempt.go:167`, `:169`, `:172`
  - `docs/architecture.md` row D17
- **What:** The count is honest: 2 mobile, 2 integration hub and 5 agents tables are interim. But each reason says only "follow-up to B-03b". No backlog item or ID exists in `backlog-src/issues.py`, `backlog.md` or `trace.md`, and the D17 row now says "B-03b done". `agent_run_credential` and `offline_transaction` payloads stay readable across legal entities with no owner.
- **Suggested fix:** Add a backlog item (or a D-row) that names the nine tables, and reference its ID in each interim reason, so rlscheck's reason text points to something real.

### 9. Minor: performance of the child-table policies

- **Where:** `services/shopfloor/migrations/0039_worker_legal_entity.sql:12-13` and `:33-79`
- **What:** The migration comment says the child policies look up the worker "by its primary key". In practice, with 2,000 workers and 100k registrations, the planner chose a **hashed SubPlan over a sequential scan of `worker`**, once per query. `worker` has no `(tenant_id, legal_entity_id)` index.

  The `pay_export_line` policy similarly hashes `pay_line JOIN worker`, and `pay_line` grows without bound. It is acceptable at today's sizes: the EXPLAIN ANALYZE above ran in about 93 ms on a loaded host. But the cost grows with every tenant's worker table and with `pay_line`, and `ExportPay` evaluates the `pay_export_line` policy inside its `NOT EXISTS`.
- **Suggested fix:** Add `create index concurrently ... on worker (tenant_id, legal_entity_id, id)` in a follow-up migration. Consider stamping `legal_entity_id` on `pay_export_line`, or making it follow `pay_export_batch`, whose policy is a plain column check, instead of the `pay_line` join.

### 10. Minor (latent): scoped assignments in an inactive legal entity still compile

- **Where:** `services/identity/policy_bundle.go:52-60`
- **What:** The query filters on user status, tenant status and the validity window, but does not join `identity_legal_entities.status`. Assignment *creation* refuses an inactive legal entity (role_assignments_api.go:168), so a legal entity deactivated later keeps granting. Nothing in identity deactivates a legal entity today, which is why this is latent.

  Expired and ended assignments (`valid_to <= now()`) are correctly excluded. Identity has no soft-delete column, and revocation sets `valid_to`.
- **Suggested fix:** Add `left join identity_legal_entities le on le.tenant_id = a.tenant_id and le.legal_entity_id = a.legal_entity_id` and `and (a.legal_entity_id is null or le.status = 'active')`.

### 11. Nit: `RequireOPA` prefers `principal.LegalEntityID` over the verified claim

- **Where:** `services/identity/opa.go:148-150`
- **What:** Today the verifier always replaces the principal with one that has no legal entity, so headers cannot inject `le`. I checked the paths:
  - devgateway strips `X-Depot-*` before the verifier.
  - SCIM chains `TenantVerifiers.Middleware`, which wraps `JWTVerifier.Middleware`, then `RequireOPA`.

  But a future middleware that sets `LegalEntityID` between the verifier and `RequireOPA` would silently take precedence over the token.
- **Suggested fix:** Always use `verifiedLegalEntityKey`.

### 12. Nit: other points

- **Trigger bypass by delete and reinsert:** Under scope `'*'`, `depot_app` can `DELETE` a worker that has no child rows and re-`INSERT` it with another legal entity, which bypasses `worker_legal_entity_guard` (verified with SQL). The child foreign keys block this once any time or days exist, and no app path deletes workers. Consider `REVOKE DELETE ON worker FROM depot_app`, or a `BEFORE DELETE` guard.
- **`pay_export_batch.legal_entity_id` has no freeze trigger.**
- **`ExportPay` error message:** A caller scoped to several legal entities gets COM-0001 "legalEntityId is required", but the export request has no `legalEntityId` field (export.go:78). Accept the field, or reword the message to "select one legal entity".
- **Files outside the B-03b batch:** the commit touches files outside its declared file set:
  - `services/identity/jwt.go`
  - `platform/go/rlsexempt/**`
  - `services/shopfloor/internal/**`
  - `services/shopfloor/api/openapi.yaml`

  The coordinator should check them against the ownership rules for parallel batches.
- **`data.json` formatting:** the new permission was appended on the same line as `assign-resource` (line 3842). It is harmless, but it breaks the file's one-permission-per-line layout.

### 13. OPA test timing (the coordinator's question)

- **Severity:** Nit (informational). B-03b is **not** the cause of the slowdown.
- **Question:** On the integration branch (B-03b merged with B-07a), `opa test --timeout 120s policies/authz` timed out at 2m0s in `test_service_grants_allow_only_their_declared_routes` (authz_test.rego:945). Is that test noticeably slower at c0611102 than at 8aaf7a60?
- **Method:**
  - I took the policy directory at each commit from `git show <commit>:policies/authz/{authz.rego,authz_test.rego,data.json}`.
  - I ran one test at a time, sequentially, alternating the commits: `opa test --timeout 900s -r '^data\.authz\.<test>$' <dir>`.
  - I recorded wall time (dominated by parsing and compiling the 220 KB test file) and peak RSS.
  - Load average was 2.0-2.5 on 4 cores.

| Test | 8aaf7a60 (base) | c0611102 (head) |
|---|---|---|
| `test_service_grants_allow_only_their_declared_routes` | 23.7 s, 20.2 s (peak RSS 70-80 MB) | 21.7 s, 19.7 s, 20.2 s (67-73 MB) |
| `test_narrow_duties_cannot_reach_verb_routes` | 20.2 s, 20.7 s | 20.7 s, 20.2 s, 21.2 s |
| Whole CI suite, `opa test --timeout 120s`, run right after each other | 288/288 pass, 306 s (load 6.1) | 298/298 pass, 259 s (load 8.5) |
| One deny `decision` (`opa eval --metrics`, `timer_rego_query_eval_ns`, 10 runs) | 16.7-26.2 ms | 15.4-31.3 ms |

- **Conclusion:** There is no measurable difference.
  - The cost of a deny decision is in `hint_candidates`, `person_duties` and `granting_duties`. Those rules run `glob.match` over every permission of every duty and are unchanged by B-03b.
  - The new `held_duties` adds one direct lookup in `data.scoped_assignments[tenant][user]`. On the `allowed(...)` helper path, which has no `legal_entity_id` and so an empty `request_legal_entities`, the scoped rule stops at `count(entities) > 0`.
  - On a quiet host the suite passes at head with the 120 s per-test timeout. The two ERRORs in my first CI-style run (load average 15 on 4 cores, while other suites ran) were the same timeouts the coordinator saw.
  - I did not have the integration branch, so I could not time B-03b+B-07a. If the timeout recurs there off-load, look first at what B-07a added: more duties or permissions widen every `granting_duties` scan, and more assertions in that test.
- **Caveat for reproducing:** `opa test -v -r <that test>` was OOM-killed at about 13.5 GB RSS **on both commits**, because verbose mode keeps the evaluation trace. Run the timing without `-v`.
- **Suggested fix (not for B-03b):**
  - Give the two heavy tests headroom: `--timeout 300s` in CI, or split them into smaller tests.
  - Make `decision` compute the `duty_missing` hint only when the caller asks for it. Most callers ignore it.

## What I checked and found correct

- **The OPA rule fails closed.** `opa eval` against the head `authz.rego` with crafted data:

  | Input | Result |
  |---|---|
  | `le` = A, upper-case A, ` A `, `A,A`, `A,`, `,A` | allow (the kernel normalises the same way, `tenant.LegalEntityScopeList`) |
  | `""`, `" "`, `","` | deny |
  | `A,B` with only A scoped | deny |
  | `A;B`, `xA` | deny |
  | JSON `null`, number, array | deny |
  | another tenant's subject | deny |
  | empty user | deny |
  | scoped `ops.region.view` outside the operator tenant | deny |
  | user with tenant-wide and scoped assignments | union, correct |

  The bundle compiler also drops scoped operator-only duties outside the operator tenant (test passes).
- **Bundle query:** active users, active tenants, the validity window and person-assignable duties apply to both roots. The roots list includes `scoped_assignments`. The revision covers the new data.
- **No header injection of `le` into OPA:** see finding 11. devgateway and depot-server refuse a selection that differs from the claim.
- **Shop floor RLS (SQL probes as `depot_app`):**
  - Scope A sees only A's workers.
  - Upper-case and whitespace scopes see nothing, because dbx canonicalises.
  - An empty scope sees nothing; `'*'` sees everything, including NULL-legal-entity workers.
  - Scope A cannot insert a worker in B or with a NULL legal entity.
  - Setting a frozen legal entity to NULL or to B is refused by the trigger, even under `'*'`.
  - Child inserts on NULL-legal-entity or other-company workers are refused.
- **Pay export:** a pay export scoped to one legal entity works end to end. The store test exports B's line only, A cannot read B's batch, and the direct counts per table under scope B are correct.
- **`:assign-legal-entity` authorization:** OPA requires `shopfloor.setup.maintain` with POST only (rego test). The store requires `dbx.TenantWide` (no claim, `tenant.administer` and the duty), and a scoped caller gets AUTH-0003. Idempotent: the same value returns 200 with no version bump.
- **Shop floor callers:** the only consumer is production's. The only service grant on the shop floor is `processmfg -> shopfloor.controlrecipe.create`, a tenant-wide table. So no other service-token path silently lost visibility.

## Tests run

Postgres 16 runs locally (cluster `main`). Each run used a fresh database, `rev_b03b_<n>`, prepared like CI's `.github/postgres-test-setup.sql`:
- `pg_trgm` and `btree_gist` in the database;
- the `depot_app` role: NOSUPERUSER, NOBYPASSRLS;
- `DEPOT_TEST_DATABASE_URL` set to the root socket URL, `DEPOT_TEST_APP_DATABASE_URL` set to the `depot_app` TCP URL.

| Command | Result |
|---|---|
| `opa test policies/` (as given in the brief) | Not valid. It loads `data.json` under `data.authz.*`, so 285 of 298 fail on both commits. CI runs `opa test --timeout 120s policies/authz`. |
| `opa test --timeout 120s policies/authz` (CI command), loaded host (load average 15) | 296 of 298 pass. 2 ERROR on the 120 s timeout: `test_narrow_duties_cannot_reach_verb_routes` and `test_service_grants_allow_only_their_declared_routes`. See finding 13. |
| The same CI command at head and at base, quieter host | head 298/298 pass (259 s); base 288/288 pass (306 s) |
| Timing of the two slow tests at both commits | No difference. See finding 13. |
| `go vet ./...` and `go test -count=1 -p 1 ./...` in `services/shopfloor` | All packages ok: api 29 s, store 67 s, migrations ok. |
| `go test ./...` in `tools/rlscheck` | ok (23 s). `-v` confirms that `TestEveryTableEnforcesRLS` covers agents-ts, fx, integration-hub-ts, mobile, scheduling and shopfloor. `TestJudgeNoEntityScopeOnlyInD17Services` passes. |
| `go test ./tenant/... ./rlsexempt/... ./dbx/...` in `platform/go` | ok |
| `go vet` and `go test -run 'Bundle\|OPA\|JWT\|Verifier\|Revision\|Policy\|LegalEntit' .` in `services/identity` | ok (928 s). 30 tests pass, including `TestRequireOPASendsTheVerifiedLegalEntityClaim`, `TestCompiledBundleCarriesActiveAssignmentsBySubjectAndLegalEntity`, `TestOPAAcceptsTheSignedBundleAndRejectsTampering`, `TestDutyContractAgainstRealPolicy` and `TestEffectiveReportEqualsOPAForEveryRoute`. The Keycloak live test is skipped (no Keycloak). |
| `go test ./cmd/...` in `services/identity` (devgateway, identity) | ok |
| Probe: `opa eval` edge cases, including scoped SCIM | See finding 1 and the table above. |
| Probe: SQL as `depot_app` on a database with shop floor migrations 0001-0039 applied as owner | See above, and findings 4, 9 and 12. |
| Probe: Go test `TestProbeAssignUnknownLegalEntity` in a scratch copy of the module | Confirms findings 2 and 3, and that a tenant admin without a selection gets AUTH-0003 from `ExportPay`. |
