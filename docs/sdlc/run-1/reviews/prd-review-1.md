# Stage 1 review: FR audit sample
Reviewer: independent agent; commit 4da78d9b0d070b61139d319da1efff00004d9cef; date 2026-11-17; sample seed 20261117

Method: for each row I read the full FR block in docs/prd/phase-*.md, including its sub-bullet notes. I grepped every cited Go test name: all 66 exist. I opened the cited files and line numbers and the service's NOT-IMPLEMENTED.md row for the FR. For "Not built" rows I searched services/, apps/, tools/ and platform/ for the FR id and its key nouns. I ran two cheap DB-free unit packages: `go test ./internal/domain` in services/tax (TestInclusive_RoundTrip, TestCompressIntrastatLinesMergesOnClassification) and in services/shopfloor (bundle split tests). Both pass. The repository was not modified (`git status` is clean).

Rule applied, from the brief's definitions: a status of Built or Blocked-external needs every clause of the FR text and every sub-bullet note implemented. If an in-repo clause is missing, the status is Partial. That holds even when the service's own NOT-IMPLEMENTED.md already admits the gap. Ordering used for "overstating": Built > Built-untested > Blocked-external (fully implemented, only live proof missing) > Partial > Not built.

## Result
- **Agree 47 / Disagree 13 / Evidence-wrong 0**. Agreement rate: **78.3 %** (47/60).
- Per status:

| Audited status | Sampled | Agree | Disagree | Evidence-wrong | Agreement |
|---|---|---|---|---|---|
| Built | 20 | 11 | 9 (all overstated) | 0 | 55 % |
| Partial | 20 | 20 | 0 | 0 | 100 % |
| Not built | 14 | 14 | 0 | 0 | 100 % |
| Blocked-external | 4 | 1 | 3 (all overstated) | 0 | 25 % |
| Built-untested | 2 | 1 | 1 (overstated) | 0 | 50 % |

- All 13 disagreements overstate the status. **10 are on Must FRs**: FR-13.16, FR-11.13, FR-20.9, FR-18.10, FR-25.3, FR-25.11, FR-36.12, FR-4.1, FR-50.1 and FR-5.14.
- No row understates its status. The "Not built" and "Partial" statuses held up in every sampled row.
- Rough extrapolation from the stratified sample: about 45 % of the 205 Built rows (about 90 FRs) and about 6 of the 9 Blocked-external rows are likely overstated.

## Verdicts
| FR | Audited status | Verdict | Correct status | Reviewer evidence / note |
|---|---|---|---|---|
| FR-9.16 | Built | Disagree | Partial | `:accept`/`:reject` exist (services/engineering/internal/api/api.go:199-200; store/release.go:511 copies and activates versions; auto-accept exists). The FR clause "adjust site defaults and BOM/route site" before accepting is not built: the accept body takes only `reason` (api/release.go:285-287). TestAcceptReleaseLineDuty (orgs_docs_ebom_test.go:234) checks only the duty and the returned status, not creation, update or activation. |
| FR-3.4 | Built | Agree | Built | services/metadata/internal/gen/migration.go:27,54-61 emits legal_entity_id, version, created_by, updated_by and deleted_at. Golden tests exist. |
| FR-13.16 | Built | Disagree | Partial | Journal, validate, compress, correct and CSV export exist (services/tax/internal/api/intrastat.go:31-40). Missing: there is no partner VAT id anywhere. `IntrastatLine` (tax/internal/domain/intrastat.go:135-151) has no such field, yet the FR text names it ("EU 2022 requirement") and so does the compression key. tax NOT-IMPLEMENTED.md:17 also lists as not built: the invoice value (slip cost is used instead), vendor-invoice and sales-invoice sources, country file formats, thresholds, and reversal on receipt correction. |
| FR-11.13 | Built | Disagree | Partial | tools/migration-factory/internal/domain/glopening.go:50-104 posts per-currency journals that must self-balance, at a caller-supplied `postingDate`. Neither "period 0 of the first fiscal year" nor a "conversion-suspense account that must be zero after load" exists (grep `suspense` / period 0 finds nothing in migration-factory or ledger). The auditor called ledger NOT-IMPLEMENTED.md:92 stale, but its suspense-account half is still true. |
| FR-13.5 | Built | Agree | Built | services/tax/internal/domain/calc.go:247-270 backs out parallel and compound factors. The tests pass (run by reviewer). |
| FR-20.9 | Built | Disagree | Partial | Accept, reject, propose and buyer decide all exist (services/purchasing/internal/api/po_ack.go:68-74). The sub-bullet "SLA tracking: `ack_due_days` (default 2); overdue listed on the buyer workspace" is not built. `ListAwaitingAcknowledgement` (store/po_ack.go:410-434) has no due date or overdue logic, and `ack_due` appears nowhere in services/purchasing. |
| FR-18.10 | Built | Disagree | Partial | ERS run and consumer post `ERS-{receipt}` invoices and the tests exist. The FR clause "send the self-billing document to the vendor" is not built: services/receiving/NOT-IMPLEMENTED.md:23 says "Open: self-bill delivery to the vendor (portal, EDI or Peppol; the document is only stored and published as an event, nothing sends it)". The audited Gap column says "—". |
| FR-20.18 | Built | Disagree | Partial | Public prospect registration plus a buyer accept/reject flow exist (party migration 0015, apps/supplier-portal `/register`). Request kinds are ONBOARDING/PROFILE_CHANGE/OTHER (party/migrations/0014_supplier_collaboration.sql:28), not ADD_VENDOR/ADD_TO_CATEGORY/CHANGE_STATUS. Also missing: the 3-step wave-4 approval (a single decider is used), the name-similarity ≥ 0.9 duplicate check, the sanctions hook, bank details and the 90-day purge. |
| FR-25.3 | Built | Disagree (marginal) | Partial | The dispatch list, start/stop, bundles and auto-stop are built and tested. "Split bundled time by quantity reported (default) or equally": the equal basis is not selectable, because the basis is always quantity (services/shopfloor/NOT-IMPLEMENTED.md:137, row D2e, marked **partial** by the service itself; `domain.SplitEqual` exists but is unreachable). The audited Gap lists only the auto-stop setting. |
| FR-6.6 | Built | Agree | Built | services/org/migrations/0010_postal_address.sql has owner_type, purpose and is_primary with a unique primary per purpose. Formatting for 7 countries is in org/internal/domain/address.go:75-81, and as-of versions are tested. |
| FR-1.10 | Built | Agree | Built | deploy/scripts/stage_release.py:226 has `rollback()` and the cited test exists (test_stage_release.py:471), as does the release-row test. Notes: the command is `stage_release.py rollback --env {stage,prod}` / `make rollback-stage|prod`, not `depot rollback`; the ≤ 5 min target is live proof (#213). This is inconsistent with FR-50.5's handling (see systematic issue 4). |
| FR-38.20 | Built | Disagree | Partial | All four types are stored (budgeting/migrations/0002_reservations.sql:21). The note "RevenueForecast increases available funds when the rule sets `revenue-increases-budget`" is not built: services/budgeting/NOT-IMPLEMENTED.md:33 says the document "holds nothing". Consumption by reference inside the follow-on document's check is **partial** (line 35). Expiry scheduling is a handoff (line 36). |
| FR-21.2 | Built | Agree | Built | services/production/internal/domain/status.go:15-22 has the statuses. The commands, including `:reset-status`, are in api/api.go:80, and the tests exist. |
| FR-18.6 | Built | Agree | Built | services/receiving/internal/domain/matching.go:16-19 has the full status set and PPV postings (line 176). `matching:evaluate` is at api.go:148. Run-on-read standing in for re-run-on-receipt is acceptable. |
| FR-16.16 | Built | Agree | Built | `GET /transfers/in-transit` (inventory NOT-IMPLEMENTED.md row "In-transit stock inquiry (MB5T) built"). Value is not part of the FR text. |
| FR-25.11 | Built | Disagree | Partial | Downtime, reason codes and the three andon conditions are built. The FR's "AndonAlert (kiosk banner + notification)" is not built: services/shopfloor/NOT-IMPLEMENTED.md:143 (D4, **partial**) says "Not built: … notifications and the kiosk banner (D1)", and also the sweep scheduler. |
| FR-35.13 | Built | Agree | Built | services/edi/internal/api/punchout.go has the supplier config (SetupURL, identities, SharedSecretRef) and sessions with a return secret. Both cited tests exist. Note: services/edi/NOT-IMPLEMENTED.md:62 still says the session model is "Missing", which contradicts row 39 (stale doc). |
| FR-8.13 | Built | Agree | Built | TestSymmetricRelationAndSelf exists, and the `relation.changed.v1` event is in product/internal/events/events.go:11. |
| FR-26.3 | Built | Agree | Built | services/pricing/internal/domain/resolve.go:420 handles `SearchNext`, and the tests exist. |
| FR-38.11 | Built | Agree | Built | services/costacct/NOT-IMPLEMENTED.md:29 marks reconciliation built. The tests and ReconciliationPage.tsx exist. |
| FR-37.9 | Partial | Agree | Partial | No OCI/P&L split, as the auditor states. The reversal cap is tested. |
| FR-42.15 | Partial | Agree | Partial | The cited tests exist (services/hr/internal/store/people_safety_test.go), and the listed gaps are real. |
| FR-17.3 | Partial | Agree | Partial | purchasing api.go:186 is the consolidation route. Line split is missing (purchasing NOT-IMPLEMENTED.md:164). |
| FR-12.11 | Partial | Agree | Partial | fx NOT-IMPLEMENTED.md:8: the confirmation is UI-only. The maintenance.test.tsx case exists. |
| FR-28.6 | Partial | Agree | Partial | Sandbox carrier only. services/carrier-gateway-ts/test/contract.ts exists (the test file is contract.test.ts). |
| FR-47.3 | Partial | Agree | Partial | CopilotPanel and the stream tests exist. Summarize and explain have no handler. |
| FR-50.12 | Partial | Agree | Partial | License verify tests exist, and ci.yml:620-641 has SBOM, Trivy and cosign. Login enforcement and the grace banner are missing. |
| FR-47.6 | Partial | Agree | Partial | services/agents-ts/NOT-IMPLEMENTED.md:72 confirms. The eval file is at evals/datasets/cash-app-v1.jsonl (the citation gives no path). |
| FR-13.11 | Partial | Agree | Partial | tax api.go:97 is `settlements:run`. The ledger posting is not wired. |
| FR-22.5 | Partial | Agree | Partial | planning NOT-IMPLEMENTED.md:302-306 confirms. |
| FR-48.11 | Partial | Agree | Partial | services/compliance/migrations/0006_legal_hold.sql exists, and both tests exist. |
| FR-16.15 | Partial | Agree | Partial | inventory NOT-IMPLEMENTED.md:536-538 covers the transit parameter, the release event and the TRS/TRR documents, all of which are in the FR's sub-bullets. The `ops.go:18` citation is a path constant, which is weak but resolvable. |
| FR-50.9 | Partial | Agree | Partial | docs/ops/capacity-model.md and cost-model.md exist, as do `tenant_cost` (ops migration 0006) and the tests. |
| FR-18.4 | Partial | Agree | Partial | The capture tests exist. The OCR provider is unset and Peppol is open. Also EXPENSE-kind invoices without a receipt do not exist (receiving NOT-IMPLEMENTED.md:31), which is a sub-bullet the auditor did not list. |
| FR-46.17 | Partial | Agree | Partial | services/analytics/internal/domain/process.go and the 3 tests exist. No mart model or screen. |
| FR-10.11 | Partial | Agree | Partial | The apps/web/src/modules/migration pages exist. |
| FR-12.8 | Partial | Agree | Partial | services/fx/internal/domain/reval.go and its tests exist. No run or posting. |
| FR-8.12 | Partial | Agree | Partial | The cited test exists. GTIN, attribute and category filters are not shown. |
| FR-8.20 | Partial | Agree | Partial | Engineering NOT-IMPLEMENTED confirms two DIR models. |
| FR-9.15 | Partial | Agree | Partial | engineering NOT-IMPLEMENTED.md:14: synchronous sessions, no Temporal, ENG-J7 not built. |
| FR-19.19 | Not built | Agree | Not built | No NOTED_ITEM/GUARANTEE or statistical transaction type anywhere in services/payables. |
| FR-19.23 | Not built | Agree | Not built | receiving stores one-time payees (payees.go), but services/payables has no one-time payee code. receiving NOT-IMPLEMENTED.md:31: "wave 19 FR-19.23 payment … need a decrypting read". |
| FR-48.28 | Not built | Agree | Not built | No works-council, CDD, IMSS, gratuity or auto-enrolment content in localization, hr or payroll. localization NOT-IMPLEMENTED.md:23. |
| FR-27.18 | Not built | Agree | Not built | The inventory consignment is vendor consignment (FR-16.17, migration 0007). sales NOT-IMPLEMENTED.md:912. |
| FR-34.17 | Not built | Agree | Not built | No freight-RFQ code. transport NOT-IMPLEMENTED.md:77. |
| FR-25.13 | Not built | Agree | Not built | shopfloor NOT-IMPLEMENTED.md:222 (cited as 223): "machine counts are not yet attributed to jobs". |
| FR-36.4 | Not built | Agree | Not built | Only rule-based single suggestions exist (FR-36.3, treasury store/reconciliation.go:155). There is no model and no top-3. |
| FR-36.18 | Not built | Agree | Not built | treasury NOT-IMPLEMENTED.md:87 "Not started". No deal tables. |
| FR-15.9 | Not built | Agree | Not built | finreport has only an OPA cache config knob (config.go:27) and no dimension filtering by user. NOT-IMPLEMENTED.md:37. |
| FR-14.12 | Not built | Agree | Not built | No duration, late-task or reopen-history report in services/close, and no close module in apps/web. |
| FR-36.24 | Not built | Agree | Not built | treasury NOT-IMPLEMENTED.md:91. |
| FR-36.23 | Not built | Agree | Not built | treasury NOT-IMPLEMENTED.md:90. |
| FR-20.22 | Not built | Agree | Not built | apps/supplier-portal/NOT-IMPLEMENTED.md:11. No consignment code in the portal. |
| FR-33.12 | Not built | Agree | Not built | quality NOT-IMPLEMENTED.md:37(d) is a handoff. No `coq_category` in code. |
| FR-50.5 | Blocked-external | Agree | Blocked-external | The executors, CLI and tests exist. DNS goes through a provider-neutral webhook because the provider is not chosen (a human decision), and live regions are #213. Caveat: pod scale-to-zero stays manual (ops NOT-IMPLEMENTED.md:15). |
| FR-50.1 | Blocked-external | Disagree | Partial | Three of the seven scenarios call routes that do not exist on main: `UNVERIFIED_PATHS` in load/k6/scenarios.js:55-60 (S4 warehouse scans, S6 inventory close and FX revaluation, S7 freshness). That is an in-repo implementation gap, not only the live campaign. |
| FR-4.1 | Blocked-external | Disagree | Partial | The manifests exist (deploy/kafka/kafka.yaml: 3 replicas, 12 partitions). The claim-check for messages over 1 MB (a sub-bullet) is not implemented anywhere (grep `claim.?check` finds nothing). The auditor noted it but still chose Blocked-external. |
| FR-36.12 | Blocked-external | Disagree | Partial | The FR asks for SFTP, **EBICS 3.0 (H005)** and **PSD2/Berlin Group XS2A**. treasury NOT-IMPLEMENTED.md:81 says "H004 only (no H005 …)" and "XS2A is not built". Two of the three adapters are missing in-repo, separate from the live sandbox. |
| FR-1.9 | Built-untested | Agree | Built-untested | Makefile:60 and deploy/Makefile deploy-local exist. Note: there are partial static tests (deploy/scripts/test_argocd_apps.py renders the local apps; tofu test `k3d_is_unchanged_and_builds_no_azure_module`), but nothing exercises the stand-up. |
| FR-5.14 | Built-untested | Disagree | Partial (and it has a test) | dashboards.yaml holds one "Depot SLOs" dashboard with Availability, p95 latency and Trace hops panels. It is not per-service and has no error-budget-burn panel. There are no Kafka lag, Temporal, Postgres or Argo dashboards, and alerts use one severity (`error`, values.yaml:16) instead of page/ticket/notify. A test does exist: test_argocd_apps.py:1055 `test_prometheus_selects_every_monitor_and_grafana_loads_the_slo_dashboard`. |

## Systematic issues (patterns that suggest other rows outside the sample are also wrong, with how to find them)

1. **Built is given when the headline sentence works, without checking the FR's other clauses and sub-bullet notes.** 9 of 20 sampled Built rows miss an explicit clause: the send step in FR-18.10, the SLA sub-bullet in FR-20.9, the partner VAT id in FR-13.16, period 0 and suspense in FR-11.13, the banner and notification in FR-25.11. The auditors appear to have read only the first line of each FR. *How to find:* re-extract each Built FR with all its indented sub-bullets (the PRD puts notes on following `  - ` lines) and check each one.
2. **The service's own NOT-IMPLEMENTED.md already says "partial", "open", "not built" or "later slice" for the FR, yet the audit says Built.** Examples: FR-25.3 (shopfloor D2e "partial"), FR-25.11 (D4 "partial"), FR-38.20 (budgeting rows 33-35), FR-18.10 (receiving row 23 "Open: self-bill delivery"), FR-13.16 (tax row 17 "Not built: …"). *How to find:* for every Built row, `grep -n "FR-W.n\|<deliverable>" services/<svc>/NOT-IMPLEMENTED.md` and flag any hit containing `partial|open|not built|later slice|handoff`. This is mechanical and should catch most of the roughly 90 likely overstated Built rows.
3. **A non-empty Gap column on a Built row is a contradiction.** 12 of the 20 sampled Built rows carry a Gap, and most of those gaps describe FR-text functionality (for example "RevenueForecast … not built" and "Site setting … not stored"). *How to find:* filter the audit for status = Built and Gap ≠ "—", then re-decide each row. A gap that is only live proof (such as FR-1.10's ≤ 5 min) can stay Built. Anything else is Partial.
4. **Blocked-external is applied inconsistently.** It is used for FRs that still have in-repo gaps (FR-36.12 missing H005 and XS2A; FR-4.1 missing claim-check; FR-50.1 missing routes). Meanwhile FRs that are equally "tested only against fakes, live proof pending" are marked Built (FR-1.10, and FR-20.9 with its #213 note). *How to find:* review all 9 Blocked-external rows and every Built row whose Gap mentions #213 or #512, under one rule. Blocked-external means everything in-repo is done and only a live system or sign-off remains. Otherwise the row is Partial with the blocked part noted.
5. **"No test" was judged only by Go `_test.go` and web tests; the Python and Helm deploy tests were missed.** FR-5.14 has a test in deploy/scripts/test_argocd_apps.py, and FR-1.9 has partial static tests. *How to find:* for the 2 Built-untested rows and any infrastructure FR (phase 0 and phase 9), grep deploy/**/test_*.py, *.tftest.hcl and load/tools/test_*.py.
6. **Citations use bare filenames** (`api.go:183`, `ops.go:18`, `intrastat.go:31`, `transfer.go:28`, `calc`, `party prospects`), which are ambiguous in a repo with about 80 services. All the sampled ones resolved, but only through the FR title. Some point to weak anchors: FR-16.15's `ops.go:18` is a path constant. *How to fix:* require repo-relative paths in the Evidence column.
7. **Auditors labelled NOT-IMPLEMENTED rows "stale" without checking all of the row.** FR-11.13 called ledger NOT-IMPLEMENTED.md:92 stale, but the conversion-suspense half is still missing. *How to find:* grep the audit for "stale" and re-verify each claim.
8. **Bright spot:** Not built (14/14) and Partial (20/20) were fully reliable. No understatement was found.

## Gate recommendation: fail

Agreement is 78.3 %, below the 90 % threshold, and 10 Must FRs are overstated: FR-13.16, FR-11.13, FR-20.9, FR-18.10, FR-25.3, FR-25.11, FR-36.12, FR-4.1, FR-50.1 and FR-5.14.

Conditions to re-submit:
1. Re-audit all 205 Built rows against the full FR block, including sub-bullets, and against the service's NOT-IMPLEMENTED.md row (issues 1-3). Downgrade every row that has an unimplemented in-repo clause to Partial.
2. Re-audit all 9 Blocked-external rows, plus every Built row that cites #213 or #512, under the single rule in issue 4.
3. Re-check the Built-untested rows for Python, Helm and Tofu tests (issue 5).
4. Correct the 13 Disagree rows above.
5. Replace bare-filename citations with repo-relative paths.
6. Draw a fresh sample with a new seed, weighted toward Built and Blocked-external, and review it again. The target is ≥ 90 % agreement with zero overstated Must FRs.
