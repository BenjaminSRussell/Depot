# Stage 1 review, gate attempt 2: FR re-audit sample
Reviewer: independent agent (did not produce the audit). Repo commit 224f9e3990511e7934cec06c0bb8637500ff5cd3; date 2026-11-17; sample seed 20261118 (59 rows: 30 Built, 20 Partial, 9 Not built; disjoint from the first sample).

## Method
For each row I pulled the full FR block from docs/prd/phase-*.md, including its indented note lines. I checked that the cited files, line anchors and test names exist; every cited test function was found by grep. I read the owning service's NOT-IMPLEMENTED.md row for the FR and then checked each clause in code. For Not built rows I searched services/, apps/web, tools/ and the other services' NOT-IMPLEMENTED.md files for the FR's nouns, looking for an implementation the auditor missed.

DB-free unit tests I ran, all passing:
- tax `TestCalculate_Mechanisms`
- org `TestConvert*` and `TestRuleAddressValidator_SevenCountryFixture`
- product `TestLifecycleMachine`, `TestReleasedCannotBeMoreAliveThanProduct` and `TestProcessPolicyPhaseOutAndBlockFlag`
- pricing `TestSmartRound`, `TestResolve_TwoPercentDiscountsAreSequential`, `TestResolveMultilineDiscount` and `TestResolve_PropertyDeterministic`
- scheduling `TestSchedule_ChoosesLeastFinishThenLowerCode`
- projects `TestResolve_FallbackOrder`
- planning `TestRun_PropertyPeggingExplainsSupply` and `TestRun_InputIsNotModified`
- production `./internal/domain`

`git status` is clean, so the repository was not modified.

Rule applied is the strict definition in the brief. Overstatement order: Built > Built-untested > Blocked-external > Partial > Not built.

I also confirmed that all 13 first-review fixes (FR-9.16 … FR-5.14) are now Partial in fr-audit-A..E.md. Current audit totals: 57 Built, 1 Built-untested, 0 Blocked-external, 758 Partial, 134 Not built.

## Result
- **Agree 54 / Disagree 5 / Evidence-wrong 0.** Agreement rate: **91.5 %** (54/59), up from 78.3 % at gate 1.

| Audited status | Sampled | Agree | Disagree | Evidence-wrong | Agreement |
|---|---|---|---|---|---|
| Built | 30 | 26 | 4 (all overstated) | 0 | 86.7 % |
| Built-untested | 0 | – | – | – | – |
| Blocked-external | 0 | – | – | – | – |
| Partial | 20 | 20 | 0 | 0 | 100 % |
| Not built | 9 | 8 | 1 (understated) | 0 | 88.9 % |
| **Total** | **59** | **54** | **5** | **0** | **91.5 %** |

- **Overstated: 4.** Two are **Must** (FR-21.6, FR-13.6) and two are Could (FR-10.18, FR-9.26).
- **Understated: 1.** FR-39.9 (Must) is Not built → Partial.
- Extrapolation: 4 of 30 sampled Built rows are overstated (13 %, against 45 % at gate 1). That suggests about 7-8 of the 57 remaining Built rows are still overstated, about half of them on Must FRs.

## Verdicts
| FR | Audited status | Verdict | Correct status | Reviewer evidence / note |
|---|---|---|---|---|
| FR-30.8 | Built | Agree | Built | services/receivables/internal/domain/interest.go (ACT_365 basis, 5.00 minimum, line 46); INTEREST open items. Both tests exist (internal/store/collections_test.go:284, internal/domain/collections_interest_test.go:87). GL 4310 posting is built under 30-2 (NOT-IMPLEMENTED.md:142), so the "GL posting" half of :41 is stale, as the auditor said. |
| FR-10.18 | Built | **Disagree (marginal, overstated, Could)** | Partial | Templates, apply-before-mapping and `default_from_template` lineage are built and tested. The clause "let a **project** attach per-object default-value templates" is not built. `migration.data_template` has no project link (migrations/0004_data_template.sql), and a run names a template and a project separately (0007_run_project.sql). The service's own row (services/migration/NOT-IMPLEMENTED.md:88) says "Templates are per entity and name, not per project … and not per module". Its reason, "there is no migration_project yet", is stale since 0006_project.sql, but the gap itself is real. |
| FR-16.14 | Built | Agree | Built | tools/migration-factory onhand pack, plus `POST /migration/runs/{id}/opening-load` into inventory OPENING_BALANCE batches (H-S9-1 built, tools/migration-factory/NOT-IMPLEMENTED.md:140). Both tests exist. Note: services/inventory/NOT-IMPLEMENTED.md:575 still says "handoff — wave 10" (stale), and the load runs through the wave-49 factory, not services/migration. |
| FR-6.2 | Built | Agree | Built | services/org/migrations/0006_operating_unit.sql: four types, UNIQUE (tenant, type, code), tenant-scoped, optional default LE, and `manager_party_id` CHECK limited to cost_center. Tests exist. |
| FR-4.11 | Built | Agree | Built | services/metadata/internal/registry/postgres.go:22 `EntityPublished` written to the outbox; postgres_test.go:92. The live relay is #213 only. |
| FR-3.11 | Built | Agree | Built | 70 golden files (10 entities × 7); golden_test.go:20 compares bytes. Citation nit: ci.yml:205 is the **lint** matrix. The tests run in the go-test shards (ci.yml:73-128 via deploy/ci/go-test-shard.sh, which finds every go.mod). |
| FR-11.8 | Built | Agree | Built | ledger api.go:137; reversal_of_line_id (migrations/0003_journal.sql:73); once-only, date ≥ original and reinstatement (NOT-IMPLEMENTED.md:84). All 3 tests exist. |
| FR-22.6 | Built | Agree | Built | `pegging` table (planning migrations/0001_init.sql:70); reqLess sorts by date, then priority (engine.go:404-412); explain tree at `/planned-orders/{id}/pegging`. Tests pass. |
| FR-16.3 | Built | Agree | Built | All OnHand columns (0001_inventory_ledger.sql:177-187); computed physicalInvent/availPhysical/availOrdered (api.go:191-193, 499-504); update in the same transaction; `asOf` and any-subset tests. p95 < 50 ms is live/perf proof (NI:600), consistent with the gate-1 handling of FR-1.10. |
| FR-26.5 | Built | Agree | Built | resolve.go:563 "(current − amount) × (1−p1)(1−p2)" (amount before percentage, sequential); MULTILINE_DISC and TOTAL_DISC built (pricing NOT-IMPLEMENTED.md:12). Tests pass. |
| FR-23.5 | Built | Agree | Built | scheduling schedule.go:460-472 sorts by due, priority, order; Schedule() picks least finish, then lower load, then code. Tests pass. |
| FR-8.5 | Built | Agree | Built | product migrations/0001_init.sql:21-40 has all six costing methods, negative/post flags, stocked, and quarantine/registration/picking flags. Test exists. |
| FR-45.8 | Built | Agree | Built | services/fieldservice/NOT-IMPLEMENTED.md (FR-45.8 row) lists every FR element as built. The only exclusions are push (not FR text) and real devices (live). apps/technician exists; serviceWorker.test.ts and technicianApp.test.tsx exist. |
| FR-1.8 | Built | Agree | Built | cmd/hello-ledger/main.go:54 runs the migration on start; probes, REST and metrics tests exist; charts/hello-ledger/templates/ingress.yaml:10 configures TLS. A live cluster deploy is #213. |
| FR-32.3 | Built | Agree | Built | Server and client parsers; ]C1/]d2/]Q3; vectors suite (gs1.vectors.test.ts with the GS1 suite fixture); routing to step fields in apps/web/src/modules/mobile-flows/domain/scanRouting.ts (not cited, but present and tested). |
| FR-6.1 | Built | Agree | Built | MaxLegalEntitiesPerTenant = 500 (store/legalentity.go:21); no DELETE route (org api.go:177-182); the switcher filters out inactive entities (legalEntityContext.tsx:96). The NI "partial" covers only the BR-6.11 guard, which is not FR text. |
| FR-21.6 | Built | **Disagree (overstated, Must)** | Partial | Route card and job card journals, hours at 2 decimals with a 0.01 h minimum (domain/journal.go:22-26,128), Dr WIP / Cr labor and overhead absorption, auto-start and :finish/:reopen are all built and tested. The clause "**valued at the resource's cost category rate**" is not built. Operations carry caller-supplied `laborRate`/`overheadRate` (domain/explode.go:127-128), and services/production/NOT-IMPLEMENTED.md:94 records "Unit costs and rates on the create request — deviation: … resource cost categories (wave 9) are read by the caller until the service resolves them itself". No cost-category entity or resource rate exists anywhere in services/: the engineering `resource` table (0002_resources.sql:43-61) has no rate, and grep `cost_category\|cost category` finds nothing. Minor: `autoStartOnFeedback` is a per-site parameter, not the order's. |
| FR-27.10 | Built | Agree | Built | Quantity and amount commitments (slice 36, sales NOT-IMPLEMENTED.md:1160); release under a row lock; both tests exist. NI:878 is stale on "amount", as the auditor said. |
| FR-6.9 | Built | Agree | Built | numeric(28,12) factor/offset_value (0018_uom.sql:27-28); 50-row fixture; two-hop and three-hop-unreachable domain tests pass. |
| FR-6.13 | Built | Agree | Built | AddressValidator {Normalize, Suggest} and NullAddressValidator (domain/addressvalidation.go:14-27). Tests exist. |
| FR-8.10 | Built | Agree | Built | lifecycle.go and the four blocked_* columns (0001_init.sql:88-91). Tests pass. |
| FR-26.7 | Built | Agree | Built | numeric(19,6); roundMinor = RoundBank(MinorUnits) (resolve.go:622); DefaultSmartBands (calc.go:13). Tests pass. |
| FR-26.9 | Built | Agree | Built | `reverses_journal_id` (0002_journals_rebates.sql:21); 100k atomic and reversible test measured in the Wave 26 proof (NI AC4). |
| FR-14.2 | Built | Agree | Built | close domain/period.go:119-145 ValidateTransition (closed only via reopen; GL/ALL close last; absent rows are allowed). Adjustment periods are separate fiscal-period rows, so 13-16 can stay open while 12 is closed. Test exists. |
| FR-13.6 | Built | **Disagree (overstated, Must)** | Partial | Compound by sequence (calc.go:199,247-275), reverse charge as equal output and input legs (calc.go:415-417) and the recoverable split (calc.go:404-411) are built, and TestCalculate_Mechanisms passes. The clause "**accrue use tax as a liability with the expense to the line's account**" is not built. The engine emits only a `use`-direction tax line (calc.go:418-419) with no account. services/tax/NOT-IMPLEMENTED.md:20 says "`posting_lines` in the calculate response — not built" and :22 says "Ledger posting via `ledger.Post` — not built". No other service handles `use`: receiving sums every returned amount as input tax owed to the vendor (receiving/internal/tax/tax.go:170-180), and grep finds no use-tax handling in payables, receiving, purchasing or ledger. |
| FR-26.4 | Built | Agree | Built | Purity and determinism tests (purity_test.go:107, property_test.go:17); sha256 explain ref; 64 KB cap; no time.Now in internal/domain. Cross-replica determinism is live proof. |
| FR-9.26 | Built | **Disagree (overstated, Could)** | Partial | Structure, where-used over parent documents and BOM lines, and ENG-DOC-CYCLE are built and tested. "Ordered child DIRs **with optional item text**" is not built: `document_structure` is (tenant_id, parent_id, child_id, position) only (engineering migrations/0011_orgs_docs_ebom.sql:183-191), and grep `item_text\|ItemText` finds nothing. A successor document version also does not copy the structure (store/document.go has no copy path), so "versioned with the parent" is only per-row. The route is `/where-used`, not `:where-used` (cosmetic). |
| FR-41.5 | Built | Agree | Built | projects 0003_setup_plans.sql:29-45 (five accounts; key CHECKs force the (g,*,*) and (*,*,*) fallbacks); PROJ-POST-001 (domain/errors.go:17). Tests pass. |
| FR-28.9 | Built | Agree | Built | `asn.issued` carries the containers, SSCCs and order-line quantities (fulfillment NOT-IMPLEMENTED.md:727). parent_sscc nesting since slice 12 (:617) makes part of :727 stale. Store tests (workbench_test.go, nesting_test.go) cover asn.issued as well as the cited flag test. |
| FR-22.11 | Built | Agree | Built | PATCH and DELETE on planned orders (planning api.go:103; NI:313 built); firm-on-next-run (approved_firm.go; TestRunPlan_FirmsApprovedOrders). NI:314's open part is FR-22.1 carry-forward, not FR-22.11. |
| FR-18.9 | Partial | Agree | Partial | receiving NOT-IMPLEMENTED.md:22 "Deliverable 4 … open". The bare citation `api.go:158` resolves to services/receiving/internal/api/api.go:158. |
| FR-9.17 | Partial | Agree | Partial | engineering NI:14 (ENG-J9 not built) and :36 (sales handoff). Test exists. |
| FR-36.1 | Partial | Agree | Partial | treasury NI:65-66. Test at api_test.go:483 exists. |
| FR-30.21 | Partial | Agree (gap text partly stale) | Partial | The stated gap "ledger postings on events only" is stale: down payment receipt, clearing and release journals post via `gl_journal` under 30-2 (receivables NOT-IMPLEMENTED.md:142). NI:61 is self-contradictory. The status still holds because of the real gaps: tax on down payments, `special_gl_type` on open items, and the `down_payment_reduces_exposure` effect (NI:61; grep `special_gl_type` finds nothing). |
| FR-17.13 | Partial | Agree | Partial | purchasing NI:204 (later slice). Test exists. |
| FR-5.11 | Partial | Agree | Partial | No anchor anywhere in services/audit; `:verify` exists only for reads (api.go:77). |
| FR-33.19 | Partial | Agree | Partial | quality NI:41 (deliverable 7). Test exists. |
| FR-15.3 | Partial | Agree | Partial | finreport NI:28 "later slice of DPT-15" for the currency filter, column format and ledger override. Tests exist. |
| FR-40.15 | Partial | Agree | Partial | intercompany NI:28. Test exists. |
| FR-20.4 | Partial | Agree | Partial | costing NI:33 (the daily job needs the ledger balance read; balances are caller-supplied). Bare `api.go:109` resolves to services/costing/internal/api/api.go:109. |
| FR-44.15 | Partial | Agree | Partial | eam NI:24-26 (no scheduler-driven sequences; asset-type sequences cannot generate). Tests in services/eam. |
| FR-12.7 | Partial | Agree | Partial | ledger NI:95. Tests exist. |
| FR-29.5 | Partial | Agree | Partial | billing NI:41 (no standalone credit notes). Tests exist. |
| FR-27.9 | Partial | Agree | Partial | sales NI:899 (no w26 re-pricing, re-promise NONE). Tests exist. |
| FR-26.12 | Partial | Agree | Partial | pricing NI:16, D5 partial: the consumer is missing (the NI's "invoice event does not exist" is stale, since billing publishes `invoice.posted`, as the auditor noted). |
| FR-5.18 | Partial | Agree | Partial | audit NI:8 Deliverable 9 partial. |
| FR-6.12 | Partial | Agree | Partial | org NI:86-87 (addresses and team members not in the package); no `make demo-seed`. |
| FR-22.13 | Partial | Agree | Partial | planning NI:349 (no mps scope). Tests exist. |
| FR-25.8 | Partial | Agree | Partial | shopfloor NI:135 (team bulk is a client loop; no teams) and :136. Tests exist. |
| FR-43.2 | Partial | Agree | Partial | payroll NI:26-28 (compiled-in US 2025 only). Test exists. The citation `internal/domain/ruleset.go:32` omits services/payroll/. |
| FR-48.17 | Not built | Agree | Not built | No `e_message` entity or status lifecycle anywhere; compliance NI:56 and localization NI:12,32 say later slice. Note: statutory body formats exist (services/localization/internal/domain/mtd_vat.go with ERP-0801 schema validation; services/tax/internal/domain/filing.go gb-mtd-json and de-ustva-csv). They are reusable "processing actions" but not the FR's tracked lifecycle. |
| FR-41.3 | Not built | Agree | Not built | projects NI:62 D1f; no role or booking tables (the only "booking" tables are GL bookings). |
| FR-36.22 | Not built | Agree | Not built | treasury NI:90; grep `bank_chain\|IntrmyAgt\|intermediary` finds nothing. |
| FR-30.24 | Not built | Agree | Not built | receivables NI:63; no balance-based GL or IC interest (treasury interest code is loan-only). |
| FR-39.9 | Not built | **Disagree (understated, Must)** | Partial | Part of the FR exists outside productcost. services/production `GET /v1/production/variances` (api.go:164; internal/api/inquiry.go:151-176) lists variances filtered by order, site, item group, variance type and posting-date period, with % of planned and totals. Its web page apps/web/src/modules/production/VarianceReportPage.tsx opens the order from each row. productcost `POST /variances/compute` and productcost/VariancePage.tsx decompose by component. Missing: item dimension and persisted component variances, drill to consumption and route transactions, the monthly variance report, and a single workspace (productcost NI D9). |
| FR-40.14 | Not built | Agree | Not built | intercompany NI:27; the only "onhand" grep hit is a false positive (`handler`). |
| FR-17.16 | Not built | Agree | Not built | No generic Policy/PolicyRule engine and no `/policies:evaluate`. Single-purpose policies exist (compliance `audit-policies:evaluate`, purchasing signing limits) but not the framework (purchasing NI:170,206). |
| FR-30.26 | Not built | Agree | Not built | receivables NI:65. fulfillment flags `chargeDue` on aged returnable balances (FR-28.16, `rtp_charge_days`), but nothing proposes or credits an invoice. |
| FR-42.20 | Not built | Agree | Not built | hr NI:111 "not started"; events.go:29 is a comment only. |

## Systematic issues
1. **The NI keyword filter misses "deviation" rows, and posting or valuation clauses get skipped.** Three of the four overstated Built rows sit next to a NOT-IMPLEMENTED line worded as a *deviation* or a scope note, not as partial/open/later slice:
   - FR-21.6: production NI:94, "deviation", rates on the request.
   - FR-10.18: migration NI:88, "not per project".
   - FR-13.6: tax NI:20/:22, "not built", but filed under posting lines rather than under the FR.

   In each case the missing clause names *how* a value is derived or booked: a rate source, a liability and expense posting, a scope owner. The headline calculation works, so the row was graded Built. *How to find:* for each of the 57 Built rows, grep the service's NI for `deviation|not per|caller|request body|posting_lines|ledger.Post`. Then re-check any FR whose text names a rate source, account, posting effect, scope owner ("per project", "per legal entity") or named field.
2. **Named fields are not checked against the schema.** In FR-9.26 the "optional item text" column does not exist. *How to find:* for Built rows whose FR text lists attributes in parentheses, diff the attribute list against the migration's CREATE/ALTER TABLE.
3. **Understatement through cross-service coverage.** FR-39.9 is Not built in productcost, but production's wave-21 variance report and productcost's compute endpoint cover part of it. Partial and Not built stayed reliable overall (28 of 29), but a Not built row should be checked against other services' NI files and apps/web before it is accepted. *How to find:* grep `services/*/NOT-IMPLEMENTED.md` and `apps/web/src/modules` for the FR's main nouns, not just the owning service.
4. **NOT-IMPLEMENTED files contradict themselves, and the audits inherit stale gap text.** Examples: receivables NI:61 against :142 (down-payment GL posting), inventory NI:575 against migration-factory H-S9-1, sales NI:878 against :1160, migration NI:88 ("no migration_project yet"), fulfillment NI:727 against :617, pricing NI:16 (invoice event). Statuses were mostly right, but gap text that cites a stale NI line misleads downstream stages. The fix is in the services' NI files, not the audit; flag it for stage 2.
5. **Citation precision is improved on re-audited Built rows but not on Partial rows.** Bare paths remain in Partial rows that were outside the re-audit scope: FR-18.9 `api.go:158`, FR-17.13 `api.go:275`, FR-20.4 `api.go:109` and FR-43.2 `internal/domain/ruleset.go:32`. All resolved. One Built citation points at the wrong CI job (FR-3.11, ci.yml:205 is lint, not test).
6. **Bright spots.**
   - The gate-1 failure mode (headline-only Built) is mostly fixed. Built agreement rose from 55 % to 87 %, and about 60 % of re-checked rows were downgraded.
   - All 13 gate-1 rows were corrected as instructed.
   - Partial held at 20/20.
   - No sampled row had wrong or missing cited evidence.

## Gate recommendation: **fail** (narrowly), with conditions

Agreement is **91.5 %**, which meets the 90 % threshold. The gate still fails on its second rule: **two Must FRs are overstated**, FR-21.6 (Route card and job card journals) and FR-13.6 (Compound tax, reverse charge, use tax, partial recoverability). Both are Built in the audit but miss an in-repo clause that nothing in the repository implements. Neither is a judgement call on wording. No resource cost-category rate exists anywhere, and no use-tax liability or expense posting exists anywhere.

Conditions to pass. A full re-sample is not needed; a targeted check is enough.
1. Downgrade FR-21.6 and FR-13.6 to Partial with the gaps above. Downgrade FR-10.18 and FR-9.26 to Partial. Upgrading FR-39.9 to Partial is optional, because upgrades were out of scope for the re-audit; record it in the log either way.
2. Run the systematic issue 1 and 2 checks over the remaining 57 Built rows. That means a NI grep for `deviation|not per|caller|request body|posting_lines|ledger.Post`, plus a schema check of named fields and posting/valuation clauses. Downgrade any row with an in-repo gap, and list the rows checked in the re-audit log.
3. A reviewer then checks only the rows changed by condition 2, plus a random 10 of the remaining Built Must rows. The gate passes if none of them is overstated. Agreement already exceeds 90 % and does not need to be re-measured on a full sample.
4. Non-blocking, for stage 2: fix the stale NI rows listed in issue 4, and replace the bare-path citations in the Partial rows.
