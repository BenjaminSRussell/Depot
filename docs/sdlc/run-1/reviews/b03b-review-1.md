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

---

# Re-review: fix commit 358c3682 (on top of c0611102)

## Verdict: CHANGES REQUIRED

- **The Blocker is fixed.** I re-ran every SCIM and identity exploit from finding 1 against the head policy, and all of them are now denied (see below).
- **Finding 2 is only partly fixed**, and the new bulk route makes the remaining gap larger:
  - The correction rule protects only workers that have no time rows yet.
  - The workers the backfill exists for are legacy workers, and those almost always have history.
  - So for them, a mistyped or wrong legal entity UUID is still permanent.
  - One `allUnassigned` call can freeze every such worker of the tenant in one go (new finding R1, Major).
- **The other findings are resolved**, apart from two Minor gaps:
  - a defence-in-depth regression in the new `pay_export_line` WITH CHECK (R2);
  - an effective-permissions report that disagrees with OPA (R3).

## Outcome per original finding

| # | Original | Outcome | Evidence |
|---|---|---|---|
| 1 | Blocker: scoped assignments grant tenant-level duties (SCIM) | **Resolved** | See "Blocker exploits after the fix" below. |
| 2 | Major: assign accepts any UUID, and the freeze makes it permanent | **Partly resolved, still Major** | See R1. |
| 3 | Major: no audit or event | **Resolved** | See below. |
| 4 | Minor: deploy hides every legacy worker; one call per worker | **Resolved** | See below. Two sub-points: (a) the runbook has no step to verify the legal entity UUID before the call (R1); (b) `allUnassigned` has no upper bound (R4). |
| 5 | Minor: widening keyed on a forgeable user id | **Resolved** | See "Is the `WithSystemScope` path reachable by a person?" below. |
| 6 | Minor: array `le` accepted | **Resolved** | `legalEntityClaim` accepts a JSON string only. `TestRequireOPASendsTheVerifiedLegalEntityClaim` now expects `""` for an array. |
| 7 | Minor: vacuous child policy passes rlscheck | **Resolved** (Nit left) | See below. |
| 8 | Minor: interim exemptions untracked | **Resolved** (one point unverified) | The reasons and the D17 row cite #725 and now say "partial". I could not confirm that #725 exists: this session has no GitHub access to the repository (unverified). |
| 9 | Minor: policy performance | **Resolved enough** | See below. |
| 10 | Minor: inactive legal entity still compiles | **Resolved** | See below. |
| 11 | Nit: `RequireOPA` preferred the principal's legal entity | **Resolved** | `opa.go:147` always uses the verified claim. New test `TestRequireOPAIgnoresALegalEntityPutOnThePrincipal`. |
| 12 | Nits | **Resolved** | See below. |

### Blocker exploits after the fix (finding 1)

- **Where the flag is enforced:**
  - `authz.rego:35` adds `not data.duties[duty].tenant_level`.
  - Identity refuses to create such an assignment: `role_assignments_api.go:130`, returning 422 `IDN-ROLE-TENANT-LEVEL`.
  - The compiler drops stored ones: `policy_bundle.go:189`.
- **Probe:** `opa eval` against the head `authz.rego` and `data.json`. User X holds, scoped to A:
  - `identity.user.provision`, `identity.user.maintain`, `identity.security-role.maintain`
  - `org.legal-entity.write`
  - `party.vendor.maintain`, `shopfloor.setup.maintain`

  With `le` = A, every request on a tenant-level duty is **denied**:
  - `DELETE /scim/v2/Users/u1`, `POST /scim/v2/Users`, `PATCH /scim/v2/Groups/g1`
  - `GET /v1/identity/users`
  - `POST /v1/authz/assignments`
  - `POST /v1/org/legal-entities`

  Requests on the per-legal-entity duties are still allowed: `PUT /v1/vendors/v1`, `PUT /v1/shopfloor/pay-agreements/p1` and `POST /v1/shopfloor/workers:assign-legal-entity`. The last one is then refused by the shop floor's `dbx.TenantWide` check.
- **Tests:** `test_scoped_tenant_level_duties_grant_nothing` and `TestCompiledBundleDropsScopedTenantLevelAndInactiveEntityAssignments` pin the fix. `TestRoleAssignmentGuards` also covers the creation refusal.

### Audit and events (finding 3)

- The single and bulk routes take the actor from `authenticatedUser` and publish `worker.assigned` with `legalEntityId` and `assignedBy`. The other two `worker.assigned` emitters now carry `legalEntityId` too, and the AsyncAPI is updated.
- Probe: 3 outbox rows carried both the legal entity and the actor after two bulk assignments plus one correction.

### Deploy backfill (finding 4)

- The bulk route `POST /v1/shopfloor/workers:assign-legal-entity` takes either `workerIds` (up to 1,000, deduplicated and validated) or `allUnassigned`.
- It runs one transaction under `WithTenantWideAdminTx`, so only a tenant administrator can use it. A scoped caller with `A,B` got `ErrNotTenantWide` (403) in the probe.
- It never moves a worker that already has a legal entity (reported as `conflicts`) and publishes one event per worker. It also backfills export batches whose lines all belong to the target legal entity.
- In OPA the route sits under `shopfloor.setup.maintain` with POST only (rego test).
- The runbook is `docs/runbooks/delivery/shopfloor-legal-entity-backfill.md`.

### Is the `WithSystemScope` path reachable by a person? (finding 5)

No.
- `WithSystemScope` is an unexported context key, set only by:
  - the sweeper (`sweeper.go:213`);
  - the production consumer (`production_consumer.go:83`);
  - the auto clock-out route, when `p.Service != ""` and there is no legal entity (`autoclock.go:60`).
- `Service` is set only by `claims.Principal()` from a verified service token. The header path in `tenant.Middleware` never sets it, and it rejects `service:` user ids.
- The sweeper's user id no longer grants anything.

### rlscheck child-table check (finding 7)

- The check now requires `legal_entity_id ... = ANY(string_to_array(current_setting('depot.legal_entity_scopes'` in `qual`, and in `with_check` when present, and rejects `or true`.
- The `entityscope` fixture pins three negatives: the vacuous policy, the join without the legal entity, and the tenant-wide table.
- **Nit left:** it is still a text heuristic. A policy with `OR 1 = 1` or `OR current_setting(...) IS NOT NULL` would pass. Acceptable for a lint.

### Policy performance (finding 9)

- 0040 adds the expression index `worker (tenant_id, (legal_entity_id::text), id)` concurrently. The builder notes, and I agree, that the planner still prefers a hashed sequential scan at test sizes. That is harmless.
- 0041 removes the expensive `pay_line ⋈ worker` join for batches that carry a legal entity.

### Inactive legal entities in the bundle (finding 10)

- The query now joins `identity_legal_entities` and requires `status = 'active'` for scoped rows.
- **Is the 0019 compile-read policy too broad? No.** It has the same gate as the 0008 policies on `identity_users` and `identity_role_assignments`: SELECT only, `depot.policy_compile = 'on'`, and `exists (select 1 from identity_operator)`. `identity_operator` is visible only when the session's `depot.tenant_id`, or `depot.operator_context`, is the operator tenant (0004, 0009).
- The restrictive legal-entity policy still applies (the compiler sets `'*'`), and the table holds only codes, names and status. The rlsexempt entry records the reason.

### Nits (finding 12)

- The batch freeze trigger has been added.
- The `ExportPay` message now says "select one of your legal entities".
- `data.json` has one permission per line.
- The delete-and-reinsert bypass is documented in 0041. No app path deletes workers, and once any child row exists its foreign key blocks the delete.

## Classification of the 64 tenant-level duties

**The security-critical part is complete and correct.** The only route family where OPA is the *sole* authorization is identity's `RequireOPAForRequest` (SCIM; `scim_api.go:55`):
- Every other service enforces the token's `duties` claim with `authz.RequireDuty`.
- devgateway and depot-server use OPA as an additional gate in front of those services.

Every `identity.*` duty is marked, and `test_tenant_level_duties_cover_identity_audit_and_operator_duties` pins the `identity.*`, `audit.*` and `operator_only` families. I checked the paths that run in scope `'*'`, in Go (`dbx.TenantWide`, `WithTenantWideAdminTx`, direct `set_config(... '*')`) and in the TS services, which set no all-entity scope. All of them either:
- belong to a marked duty: identity, `org.legal-entity.write`, `org.reference.write`, `party.merge`, `migration.opening-target.release`, ops;
- require a request with **no** legal entity, which a scoped assignment can never satisfy, since the scoped rule needs a non-empty `le`. This covers consolidation runs, print rules, migration outbound and the shop floor assign routes; or
- are narrow in-transaction existence checks inside a scoped operation, or service-only paths. This covers hr `withAllEntities`, the ledger trading partner check, purchasing PO-number uniqueness, the production job card lookup, the print schedule claim and `party.merge.execute`.

**The four deliberately unmarked duties:**
- **`print.admin`, `migration.run`, `migration.project.write`:** sound. Their `'*'` branch requires `dbx.TenantWide`, which means no claim, so a scoped grant cannot reach it.
- **`shopfloor.setup.maintain`:** the conclusion is acceptable, but the README's reasoning is wrong for it (Nit R5).
  - Its routes other than assign are not "a legal-entity-scoped branch". They write the tenant-wide shared setup tables (time profiles, pay agreements, absence codes), which rlsexempt records as permanent tenant-wide.
  - So an A-scoped holder can, through OPA, edit pay agreements that company B's workers use.
  - That is acceptable only under the README's *second* rule: the service checks the token's duties itself, so OPA's rule adds nothing there.
  - The README should list it under that rule.

**Coordinator's question: the B-07a audit duties.** Marking `audit.verify`, `audit.cdc.apply` and `audit.chain.anchor` as `tenant_level: true` is **right, and also required**:
- The pin test fails on any unmarked `audit.*` duty.
- For the two service flag duties the flag has no effect but does no harm: a `service_only` duty is never person-assignable, identity already refuses to assign it, and the compiler drops it (`PersonAssignable`). It can never appear in `scoped_assignments`.
- For `audit.verify`, tenant level is correct on the merits: the audit chain spans every legal entity.

## New findings

### R1. Major: a typo in the legal entity UUID is still permanent for legacy workers with history, and the bulk route can make it tenant-wide

- **Where:**
  - `services/shopfloor/internal/api/setup.go` (`assignLegalEntities`, `assignLegalEntity`: no existence check)
  - `store/worker_maintain.go` (`AssignWorkersLegalEntity`)
  - `migrations/0041_legal_entity_batch_scope_and_guards.sql` (worker and batch guards)
  - `docs/runbooks/delivery/shopfloor-legal-entity-backfill.md`
- **What:** The fix allows a correction only while a worker has no time, day, correction or cost-rate row. The workers this route exists to assign are legacy workers, and those normally already have registrations. So for them, the first assignment freezes the legal entity at once.
- **Failure scenario (verified, scratch probe `TestProbe2BulkTypoFreezesLegacyWorkersWithHistory`):**
  1. A tenant admin calls the bulk route with `{"legalEntityId": "...dead", "allUnassigned": true}`. It assigns both legacy workers, with no error.
  2. Correcting the worker that has a clock-in returns `SF-0200 (409)`. Only the worker with no time could be corrected.
  3. `backfillExportBatches` then gives the old export batches whose workers are all now in `...dead` that legal entity. The new batch guard trigger freezes them too.

  One mistyped UUID in the runbook's single-legal-entity step thus makes every legacy worker of the tenant, with all their time and pay, and the tenant's old payroll exports, permanently visible only to all-entity reads. Before this, the same mistake affected one worker per call.
- **Suggested fix (any one):**
  - Validate the legal entity: a service-token read of identity's `/v1/legal-entities/{id}` with a grant for shopfloor, or a read-only copy of the tenant's legal entities.
  - Or require a dry run, or a confirmation that echoes the legal entity's code from a source the admin cannot mistype.
  - Or allow a tenant-admin correction that moves a worker together with all its rows as an explicit, audited act.

  At minimum, add a runbook step that checks the UUID against `GET /v1/legal-entities` before the call, and state that legacy workers with history freeze on first assignment.

### R2. Minor: the 0041 `pay_export_line` WITH CHECK no longer ties the line's pay line to the caller's scope

- **Where:** `migrations/0041_legal_entity_batch_scope_and_guards.sql:22-29`
- **What:** WITH CHECK now checks only that the *batch's* legal entity is in scope. 0039 also required the pay line's worker to be in scope. Foreign key checks ignore RLS.
- **Failure scenario (verified with SQL as `depot_app`):**
  1. Scope A inserts `pay_export_line(pay_line_id = <company B's pay line>, batch_id = <A's batch>)`. It succeeds.
  2. Scope B cannot see that line, because it follows A's batch. B's next export selects the pay line again, and the insert fails on `pay_export_line_pkey`, so B's whole payroll export breaks.

  This needs direct SQL: `ExportPay` only inserts lines it selected in its own scope. No data leaks, since B's pay line stays hidden from A. It is a defence-in-depth regression.
- **The fallback in USING does not leak:**
  - A NULL batch's line is visible only when that line's own worker is in scope.
  - New lines cannot target NULL batches, because WITH CHECK has no fallback.
  - The backfill sets a batch's legal entity only when every line's worker is already in it.
- **Suggested fix:** Add to WITH CHECK the 0039 condition that the line's pay line belongs to a worker in scope, alongside the batch check.

### R3. Minor: the effective-permissions report disagrees with OPA for stored scoped tenant-level assignments and inactive legal entities

- **Where:** `services/identity/effective_api.go:82-86` and `:140-147`
- **What:** `GET /v1/identity/users/{id}/effective-permissions?legalEntityId=A` collects every active role scoped to A. It does not apply `TenantLevel`, and it does not filter on the legal entity's status.
- **Failure scenario:** An `identity.user.provision` assignment scoped to A that was stored before this fix (they are dropped from the bundle, not ended) is still reported as granting SCIM. So is an assignment in a deactivated legal entity. OPA denies both. A reviewer reading the report draws the wrong conclusion.

  I reasoned this from code; the existing `TestEffectiveReportEqualsOPAForEveryRoute` covers only tenant-wide assignments.
- **Suggested fix:**
  - Apply the same filters as the compiler: skip `TenantLevel` duties for scoped rows, and join the legal entity's status.
  - List the skipped roles in `ignoredRoles`.
  - Consider a one-off migration that ends (sets `valid_to`) existing scoped tenant-level assignments, with an audit record, so the assignment lists stop showing them as active.

### R4. Minor: `allUnassigned` has no upper bound

- **Where:** `store/worker_maintain.go` (`AssignWorkersLegalEntity`)
- **What:** With `allUnassigned`, the call locks every unassigned worker `FOR UPDATE` in one transaction. It writes one outbox row per worker, returns every id, and stores that whole response in `idempotency_keys`.
- **Failure scenario:** On a tenant with tens of thousands of legacy workers, this becomes a long transaction during the deploy window, blocking clock-ins on those workers, with a very large response and idempotency row.
- **Suggested fix:** Apply `MaxBulkLegalEntityWorkers` per call to `allUnassigned` too, returning a `remaining` count so the runbook loops. Alternatively, return counts only.

### R5. Nit: the README's reasoning for `shopfloor.setup.maintain`

- **Where:** `policies/authz/README.md`
- **What:** See the classification section above.
- **Suggested fix:** List it under "duties on a service's own tenant-wide reference tables".

## Tests run (re-review)

All runs used a fresh database `rev_b03b_<n>`, prepared like CI (`pg_trgm` and `btree_gist` installed; `depot_app` NOSUPERUSER and NOBYPASSRLS, password `depot_app`, which worked).

| Command | Result |
|---|---|
| `go vet ./...` and `go test -count=1 -p 1 ./...` in `services/shopfloor` | All ok: api 15 s, store 42 s, migrations ok. |
| `go test ./...` in `tools/rlscheck`, including `TestEntityScopeFixtureViolationsArePinned` | ok |
| `go test ./tenant/... ./rlsexempt/... ./dbx/...` in `platform/go` | ok |
| `opa test --timeout 600s policies/authz` (load average about 7) | **300/300 pass**, 525 s wall |
| `go vet` and `go test -run 'Bundle\|OPA\|JWT\|Verifier\|Revision\|Policy\|LegalEntit\|RoleAssignment\|Effective\|DutyContract\|Catalogue\|DataJSON' .` in `services/identity` | ok (907 s). 45 pass, including `TestRoleAssignmentGuards`, `TestCompiledBundleDropsScopedTenantLevelAndInactiveEntityAssignments`, `TestRequireOPAIgnoresALegalEntityPutOnThePrincipal`, `TestDutyContractAgainstRealPolicy` and `TestEffectiveReportEqualsOPAForEveryRoute`. The live Keycloak test is skipped. |
| `go test ./cmd/...` in `services/identity` (devgateway, identity) | ok |
| Probe: `opa eval` with the head policy (Blocker exploits) | All tenant-level requests denied; per-legal-entity duties still granted. |
| Probe: Go test `TestProbe2BulkTypoFreezesLegacyWorkersWithHistory` in a scratch copy of the module | Confirms R1: a bulk typo assigns everything, and the worker with time cannot be corrected (SF-0200). Confirms the actor appears in the events, and that a scoped `A,B` caller is refused. |
| Probe: SQL as `depot_app` on a database with shop floor migrations 0001-0041 applied | Confirms R2: a cross-company `pay_export_line` insert under scope A is accepted, and B's later insert fails on the primary key. The 0040 index is present. |

---

# Re-review 2: fix commit f9dc81c5 (on top of 358c3682), R1-R5 only

## Verdict: APPROVE

R1 to R5 are resolved, and I found no new Blocker or Major. Two new Minors and one Nit are listed below. None of them blocks merge.

Note on R1: until an environment sets `identityUrl` and `serviceIdentity`, and identity trusts the shop floor's key, the assign routes answer 503 SF-0202. Nothing can be assigned in that state, which is closed but non-functional. The backfill runbook therefore depends on that deploy step.

## Outcome per finding

| # | Outcome | Evidence |
|---|---|---|
| R1 Major (a typo freezes legacy workers) | **Resolved** | See below. |
| R2 Minor (`pay_export_line` WITH CHECK) | **Resolved** | See "Is 0042 correct?" below. |
| R3 Minor (effective report vs OPA) | **Resolved** | `effective_api.go` now reads, per row, whether it is scoped and whether its legal entity is active. A scoped row counts only if its legal entity is active and its duty is not `TenantLevel`, which matches the compiler. A role is reported if any row grants it. `effective_api_test.go` covers it. |
| R4 Minor (`allUnassigned` unbounded) | **Resolved** | See below. |
| R5 Nit (README reasoning) | **Resolved** | `shopfloor.setup.maintain` is now listed under the tenant-wide reference tables rule. |
| Optional | Done | rlscheck rejects `OR <n> = <n>`, with fixture `child_tautology`. The pattern also matches `OR 1 = 2`, but a false positive there is harmless. |

### R1: the legal entity is now confirmed with identity before anything is written

- Both assign routes call `legalEntityUsable` before the store:
  - a caller who is not `dbx.TenantWide` gets 403;
  - an unknown or inactive legal entity gets 422 SF-0201;
  - no checker configured, or any other error, gets 503 SF-0202.
- The bulk route has `dryRun`.
- The runbook now says that legacy workers freeze on their first assignment, and requires a dry run first.

### R4: `allUnassigned` is bounded

- `allUnassigned` takes at most 1,000 workers per call (`limit $2 for update`).
- The response gives `assignedCount` and `remaining`, and lists ids only for a call that named its workers, so the stored idempotent response stays small.
- The runbook says to repeat with a new Idempotency-Key until `remaining` is 0. That matters: reusing a key would replay the first response.

## Answers to the coordinator's questions

1. **Can the identity route leak anything beyond existence and active status?** No.
   - `lookup` (`legal_entities_api.go`) reads `status` only, for `tenant_id = p.TenantID`, and returns `{legalEntityId (the canonical id from the path), active}` or 404.
   - Another tenant's legal entity returns 404 (test `TestLegalEntityLookupForServices`).
   - It reads in scope `'*'`, so a caller limited to one legal entity could learn whether a *sibling* legal entity of its own tenant exists. That is the intended answer and is not sensitive.

2. **Is it callable only by service tokens with that duty?** Partly, and not by the handler itself. See R6.
   - The duty is `service_only` and `tenant_level` (`data.json:69`).
   - Identity refuses to assign `service_only` duties to people, and the bundle compiler drops them (`PersonAssignable`). So OPA denies people on devgateway and depot-server; a rego test pins this alongside the grant.
   - The tenant kernel enforces the grant table for service tokens (`service.go:776`).
   - But the route is under `requireAnyDuty(identity.legal-entity.lookup)`, which accepts *any* principal holding the duty, including a header principal. The new test `TestLegalEntityLookupForServices` passes with a non-service principal (`caller{duties: [lookup]}`).
   - `/v1/identity` is exposed through Envoy (`deploy/gateway/httproutes.yaml:901-914`), and Envoy does not call OPA (ADR-0006 rule 3). So a person whose token `duties` claim contains the duty can call it.
   - The impact is negligible (existence and active flag in their own tenant), and identity never puts a service-only duty in a person's assignments.

3. **Is the actor binding correct, with no confused-deputy path?** Yes.
   - `registry.LegalEntities.Active` signs with `ApplyDuties(req, p, "identity", "", [lookup])`.
   - `MintDuties` restricts the token to the grant ∩ `only`. Because the lookup duty is tenant-level, it sets `le` to empty and mints only when `IsTenantAdmin(p)` (`service.go:525-527`); a scoped caller cannot mint (registry test).
   - `tid` is `p.TenantID` and `act` is `p.UserID`, the calling tenant admin. The shop floor checks `dbx.TenantWide(p, shopfloor.setup.maintain)` before calling.
   - The only question asked is a yes/no about the caller's own tenant, and the answer is bound to the id asked: a mismatched `legalEntityId` is an error. There is no parameter by which the caller could steer the call to another tenant or another duty.

4. **Is 503 fail-closed on every error path?** Yes.
   - No checker configured → 503.
   - Sign error, transport error, read error, any status other than 200 or 404, an undecodable or mismatched body → error → 503.
   - 404 → 422, and `active: false` → 422.
   - No path continues to the store on an error.
   - One wrinkle: a misrouted `IDENTITY_URL` that answers 404 (for example "no route") is reported as 422 "unknown legal entity" rather than 503. That is still closed, but misleading for an operator. Folded into R7.

5. **Is the dry run truly write-free?** Yes.
   - The API skips `replayed` for a dry run. `replayed` is read-only in any case: `LookupReplay`.
   - The store returns inside the dry-run branch before the `UPDATE`, `publishWorkerAssigned` (outbox), `backfillExportBatches` and `remember` (idempotency). The transaction commits with no writes.
   - It does take `FOR UPDATE` row locks on up to 1,000 workers for the length of the transaction: brief, and not a write.
   - The store test checks no events and no batch changes. The API test checks the worker is not assigned.

6. **Is 0042 correct?** Yes.
   - USING is unchanged from 0041.
   - WITH CHECK requires the batch's legal entity in scope **and** the pay line's worker in scope. Verified with SQL as `depot_app` on migrations 0001-0042:

     | Scope | Insert | Result |
     |---|---|---|
     | A | B's pay line into A's batch (the R2 exploit) | **refused** |
     | A | its own line into A's batch | accepted |
     | A | its own line into a NULL batch | refused |
     | A,B | B's line into A's batch | accepted (entitled to both; `ExportPay` never does this) |

     A NULL-batch line of a NULL-legal-entity worker stays invisible to A, and `'*'` sees everything.

7. **Does the chart render?** Yes. Helm v3.16, `helm template` of `services/shopfloor/charts/shopfloor`:

   | Values | Result |
   |---|---|
   | defaults | NetworkPolicy, Service, Deployment; no `IDENTITY_URL` or `service-identity` |
   | `identityUrl=http://identity:8080`, `serviceIdentity.signingKeySecret=shopfloor-svc`, `serviceIdentity.keyId=k1` | renders `IDENTITY_URL`, `DEPOT_SERVICE_NAME=shopfloor`, `DEPOT_SERVICE_SIGNING_KEY_FILE`, `DEPOT_SERVICE_KEY_ID`, the read-only mount and the secret volume (mode 0440) |
   | `identityUrl` without the secret | fails with "serviceIdentity.signingKeySecret is required with identityUrl", as intended |
   | `identityUrl` with `edgeCa.secret` | renders both volumes |

   The YAML parses. See R8 for the mount permission.

## New findings

### R6. Minor: the identity lookup handler does not require a service principal

- **Where:** `services/identity/legal_entities_api.go:45`
- **What:** The route is guarded by `requireAnyDuty(dutyLookupLegalEntity)`, which checks `p.HasDuty` only. A header principal whose `X-Depot-Duties` contains the duty passes (the identity test itself does this). Via Envoy that header is the token's `duties` claim, and Envoy never consults OPA. Impact: existence and active flag of legal entities in the caller's own tenant.
- **Suggested fix:** Also require `p.Service == "shopfloor"` (or `p.Service != ""`) in `lookup`, and add a test with a header principal expecting 403.

### R7. Nit: the identity client follows redirects and reads every 404 as "unknown"

- **Where:** `services/shopfloor/internal/registry/legalentity.go`
- **What:**
  - The `http.Client` follows redirects and forwards custom headers, including `X-Depot-Service-Token`, to the redirect target. The token is audience-bound and replay-checked, and `IDENTITY_URL` is operator-set.
  - A 404 from a wrong base URL becomes 422 SF-0201 instead of 503.
- **Suggested fix:** Set `CheckRedirect` to return `http.ErrUseLastResponse`. Treat a 404 whose body is not a problem with code `IDN-LEGAL-ENTITY-NOT-FOUND` as unavailable.

### R8. Minor (uncertain, needs a cluster to confirm): the service-identity secret is mounted 0440, with no pod `fsGroup`

- **Where:** `services/shopfloor/charts/shopfloor/templates/deployment.yaml` (volume `service-identity`)
- **What:** Secret volume files are owned by root, and by the root group unless the pod sets `fsGroup`. The shop floor pod has no `securityContext`.
- **Failure scenario:** If the image runs as a non-root uid, `DEPOT_SERVICE_SIGNING_KEY_FILE` is unreadable, `ServiceSignerFromEnv` fails, and `main` exits, so the pod crash-loops as soon as `identityUrl` is set. The same 0440 pattern already exists for the shop floor's `edge-ca` mount and for assets, so this may already be handled by the image's uid. I could not verify it without a cluster.
- **Suggested fix:** Set `podSecurityContext.fsGroup` (65532, as analytics and batch do), or confirm the image's uid, before the first environment sets `identityUrl`.

## Tests run (re-review 2)

Each run used a fresh database `rev_b03b_<n>` prepared like CI. The `depot_app` password worked without a reset.

| Command | Result |
|---|---|
| `go vet ./...` and `go test -count=1 -p 1 ./...` in `services/shopfloor` (includes the new registry, API and store tests) | All ok |
| `go test ./...` in `tools/rlscheck` (includes `child_tautology`) | ok |
| `go test ./tenant/...` in `platform/go` (grant table change) | ok |
| `opa test --timeout 600s policies/authz` | **300/300 pass**, 177 s |
| `go vet` and `go test -run 'Bundle\|OPA\|JWT\|Verifier\|Revision\|Policy\|LegalEntit\|RoleAssignment\|Effective\|DutyContract\|Catalogue\|DataJSON\|Admin' .` in `services/identity` | ok (606 s). 70 pass, including `TestLegalEntityLookupForServices`, `TestEffectiveReportEqualsOPAForEveryRoute`, `TestEffectivePermissionsEndpoint` and `TestRoleAssignmentGuards`. The Keycloak live test is skipped. |
| `go test ./cmd/...` in `services/identity` (devgateway, identity) | ok |
| Probe: SQL as `depot_app` on shop floor migrations 0001-0042 | See question 6: the R2 exploit is refused, a legitimate insert is accepted. |
| `helm template` (Helm v3.16), shop floor chart, defaults / with the new values / with `identityUrl` but no secret / with `edgeCa` | See question 7. |

All re-review 2 suites finished before the container restart. The coordinator reports CI on PR #726 at efa129c4 fully green; I did not check that myself.
