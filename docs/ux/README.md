# Depot UX design (SDLC stage 4)

This folder is the UX design of Depot: the standards every screen must meet, the personas and tasks they serve, the floor-app design, how usability is measured, and the order the work is done in. It is the stage 4 artifact of the SDLC record (`docs/sdlc/README.md`). Its evidence is the UX audit of run 1 (`docs/sdlc/run-1/ux-audit.md`, 50 findings UX-001 to UX-050, commit `cba4ebe`, 2026-11-17). The owners' brief is that Depot be "as end-user friendly as possible" and auditable.

| File | What it holds |
|---|---|
| [patterns.md](patterns.md) | The standards UXS-01 to UXS-20: rule, reason, examples from real Depot screens, components, acceptance criteria and enforcement |
| [personas-and-journeys.md](personas-and-journeys.md) | The 16 designed-for personas, their top five tasks, the screen and interaction budget for each, and the routes that must serve them (missing routes flagged); how they map to `docs/PRD.md` section 3 |
| [floor-apps.md](floor-apps.md) | Kiosk and warehouse device design: layout, sizes, states, scan grammar, sign-in, screen flows |
| [usability-programme.md](usability-programme.md) | Staged baseline with the demo company, task scripts to add, metrics, and a release gate that cannot pass by default and cannot freeze releases |
| [rollout.md](rollout.md) | The work in 21 slices (0 to 20), ordered by user impact, with a first cut of every High finding in slices 0 to 9; change management for existing users |

## Scope

In scope: every screen people use in `apps/web` (59 modules, 1,274 routes), the shop-floor kiosk (`apps/web/src/modules/shopfloor`), the warehouse device app (`apps/warehouse-mobile` over `apps/web/src/modules/mobile-flows`), the technician app (`apps/technician` over `apps/web/src/modules/technician`), the customer portal (`apps/customer-portal`) and the supplier portal (`apps/supplier-portal`). Also the parts of service APIs the screens depend on, chiefly the audit service (`services/audit/api/openapi.yaml`), stated as needs for the stage 5 backlog.

Out of scope: visual rebranding; new business capabilities that are not needed for a persona's top tasks (they belong to the functional backlog); approvals by email, Slack or Teams (thesis U8 channels) and copilot actions (U9), which the audit did not cover. The standards still apply to them when they are built (UXS-12 requires the same audit trail for an approval from any channel).

## Principles, ranked

When two principles pull in different directions, the higher one wins.

1. **Tell the truth.** A screen shows only what it really knows. No sample data outside the demo, no empty state that was not fetched, no page for a feature that does not work. Trust comes before convenience, and an audit trail is only worth something if the screens around it are honest. (UXS-01, UXS-18)
2. **Show who, when and why.** Every change can be explained by the person looking at the record: who made it, when, what changed, why, and what approved it. History cannot be edited, and it can be exported. (UXS-03, UXS-12, UXS-13)
3. **Speak the person's language.** Names, not ids. Plain words, not codes, enums or internal terms. The same word for the same thing everywhere. (UXS-02, UXS-07, UXS-16, UXS-11)
4. **Start from the work people already have.** Begin from the source document, fill in what the system knows, direct the floor worker to the next step. Every value the system can work out is one the person does not type. (UXS-05, UXS-06, UXS-09, UXS-04)
5. **Fit the hand and the place.** Keyboard for clerks, large targets and scans for the floor, a phone for people outside, assistive technology for everyone. (UXS-09, UXS-10, UXS-15, UXS-08)
6. **One way to do one thing.** One list, one form, one detail template, one history, one formatter, shared by the web app, floor apps and portals. (UXS-04, UXS-08, UXS-03, UXS-17, UXS-14)
7. **Measure it.** Every task has a budget; usability is measured against a baseline; a missing measurement is a failure, not a pass. A new person can finish a real task on day one. (UXS-19, UXS-20)

Examples of the ranking at work: a History panel shows "Change history isn't recorded for vendors yet" rather than a tidy "No changes" (1 over 6). A write-off asks for a reason even though it adds an interaction (2 over 4); an ordinary save does not.

## How this relates to `apps/web/docs`

The docs in `apps/web/docs/` stay in force. This folder extends them and, where stated, supersedes specific clauses. Where they disagree, this folder wins until the doc is updated in the slice that changes the behaviour (table in [rollout.md](rollout.md), "Documentation alignment").

| Doc | Relationship | What changes |
|---|---|---|
| `design-system.md` | Extended | New shared components (RecordDetail, RecordHistory, LineGrid, ReasonDialog, StepUpGate, JobProgress, ContextSheet, the floor layout). **Supersedes** two "preserve" assertions in section 8 that pin fake data (admin "Audit entries" contains `user-1`; workflow "Batch runs" contains `rows-1m`). The `WorkspacePlaceholder` "Coming soon" card is no longer allowed in a workspace (UXS-01) |
| `forms.md` | Extended | Scope widened from procure-to-pay and order-to-cash to every module (UXS-08). **Supersedes** "field errors after submit only": format rules validate on blur (UXS-08). Adds defaults (UXS-06) and the line grid (UXS-05) |
| `lists.md` | Extended | Selection, bulk actions, inline edit, paste, undo and server-stored views (UXS-04). **Supersedes** the rollout checklist's order (P2P/O2C only) with [rollout.md](rollout.md) slice 8 |
| `ux-writing.md` | Extended | **Supersedes** "Identifiers a person must type or paste, such as 'Party id' or 'Run id', until a picker replaces the field": ids are allowed only on admin, integration and migration screens (UXS-02). Adds banned patterns (UUID, id prompts, dotted duty codes, problem codes outside Details, upper-snake enums) and the floor glossary |
| `usability-tasks.md` | Extended | New task scripts (OP0 to OP6, WH6 to WH9, TE, CP0 to CP5, SP0 to SP5, SR6, TA6, WS, MD, AM, MT, AU, NU), budgets per task, CT4 marked planned until slice 4, the stale #503 sentence replaced |
| `usability-gate.md` | Extended, partly superseded | Measures unchanged. Adds rules G1 to G8 ([usability-programme.md](usability-programme.md) section 6): computed scope (shell changes pull in only apps that render the shell), no reference means fail, numbers required, no default text, budgets, routes exist, narrow waivers, and a bounded owner-signed provisional reference while the staged baseline is collected |
| `usability-baseline-report.md` | Extended | Eight new personas and new-user rows; interactions, budget, SEQ, unaided success and "not measured" columns |
| `accessibility-conformance-report.md` | Input; to be corrected | Row 1.4.10 must cite the 390 px overflow (UX-012) until fixed; 2.2.1 updated when the expiry warning ships; scope widened by UXS-15; the manual assistive-technology pass reports (UXS-15) become its evidence for the "Not evaluated" rows |

## Traceability: standard to findings, thesis and enforcement

Thesis ids are from `docs/prd/part-00-thesis.md`. "Easier" is the thesis axis (clicks per task, training time). Enforcement names the check; "new" means it is added by the standard, otherwise it is an existing test that is extended. All paths are under `apps/web/` unless they start with `apps/` or `.github/`.

| Standard | Resolves (UX-) | Thesis promise | Enforcement |
|---|---|---|---|
| UXS-01 Honest screens | 001 (interim), 003 (task route), 004, 005, 006 (interim), 044, 049 (demo tour) | A7, U4, S4 | New source scan `src/shell/honestScreens.test.ts`; `src/modules/registry.test.ts` and `src/shell/workspaces/workspaces.test.tsx` (no nav to planned routes; planned entries expire on their date); rewritten `src/modules/admin/module.test.tsx`, `src/modules/workflow/module.test.tsx`; review checklist; API coverage report (information) |
| UXS-02 Names, not ids | 007, 008, 009, 020, 021, 030, 032, 033, 001 (user column) | U2 | Behavioural rules in `src/shell/lookup/rawIdInputs.test.ts` (UUID check on plain Input; `…Id` column without render); `src/shell/plainLanguage.test.ts` with the patterns in `plainLanguageRules.ts`, duty codes matched against `policies/authz/data.json`, and positive and negative fixtures; new e2e `e2e/names-not-ids.demo.spec.ts` |
| UXS-03 Record detail with History | 002, 001 | A7, U1 | `src/shell/ui/consistency.test.ts` rule 4 with `detailPageBacklog.json`; new `src/shell/audit/RecordHistory.test.tsx`; new e2e `e2e/record-history.demo.spec.ts` (UI) and `e2e/record-history.live.spec.ts` (deployed); new A7 contract tests in `services/{party,pricing,costing,identity,receiving,ledger}`; audit actor contract test in `services/audit` (body `userId` refused) |
| UXS-04 Lists: selection, bulk, paste, undo | 015, 016, 017, 026, 031 (web), 032 (paste) | U7, U3 | `src/shell/ui/ListPage.test.tsx`; `consistency.test.ts` rule 2 widened to all modules with `listPageBacklog.json`; new hand-rolled-selection scan; `e2e/payables.demo.spec.ts` (AP4) |
| UXS-05 Keyboard line grid from a source document | 050, 014, 028, 007 | U1, Easier | New `src/shell/ui/LineGrid.test.tsx`; `e2e/p2p-keyboard.demo.spec.ts`; new `consistency.test.ts` rule with `lineGridBacklog.json`; budgets in `e2e/task-budgets.demo.spec.ts` |
| UXS-06 Context defaults | 034, 033, part of 014 and 028 | U1, Accurate | New `src/shell/defaults/useDefaults.test.ts`; new scan `src/shell/defaults/noLiteralDefaults.test.ts` |
| UXS-07 Error, status and number text | 019, 020, 022, 035, 036, 037, 045, part of 048 | U5 | `src/shell/plainLanguage.test.ts`; `src/shell/errors/bareCode.test.ts`; `src/shell/errors/catalogCoverage.test.ts` widened; new `src/shell/shared/formatting.test.ts` and `boundary.test.ts`; `apps/customer-portal/test/portal.test.tsx`, `apps/supplier-portal/test/portal.test.tsx`; `src/modules/technician/technicianApp.test.tsx` |
| UXS-08 Forms: validate early, never lose work | 018, 040, 041, 036 | U5 | `consistency.test.ts` rule 3 widened with `formStandardBacklog.json`; `src/shell/ui/forms.test.tsx`; new `src/shell/auth/SessionExpiryWarning.test.tsx` |
| UXS-09 Floor layout | 010, 011, 029, 030, 031 (device), 043 | U6 | New e2e `e2e/floor-layout.spec.ts`; `e2e/warehouse-mobile.spec.ts`, `e2e/kiosk-labor.spec.ts`, `e2e/kiosk-offline.spec.ts`; `src/modules/mobile-flows/domain/coreFlows.test.ts`, `layout.test.ts`; floor-apps.md checks F1 to F12 |
| UXS-10 Responsive shell | 012 | Easier, U6 (phones) | New e2e `e2e/reflow.demo.spec.ts` (390 and 320 px, every workspace entry route) |
| UXS-11 Help on every route | 023, 024, 046 | Easier (guided task under 30 min) | `src/shell/help/routeHelp.test.ts` widened to all modules plus persona-guide check; `src/shell/help/HelpPanel.test.tsx`; release checklist line |
| UXS-12 Approvals, reasons, step-up | 002 (why), 020, 029 | A7, U8 | New `src/shell/audit/auditedActions.test.ts` (completeness from `sensitiveFields.json`; verbs from the registry; Cancel/Delete only with a noun); new `ReasonDialog.test.tsx`, `StepUpGate.test.tsx`; service contract tests (no reason refused, tier 2 saved as pending, self-approval refused) |
| UXS-13 Auditability for end users | 001, 002, 027 | A7; PRD principle 5 ("auditable") | Rewritten `src/modules/admin/module.test.tsx`; `src/modules/workflow/inbox.test.tsx` (tabs); `e2e/record-history.demo.spec.ts` (export); usability tasks TA3, AP6, CT5, AU1, AU2 |
| UXS-14 Navigation and workspaces | 025, 011, 026 | U3, Easier | `src/shell/workspaces/workspaces.test.tsx` (persona map, task routes); `src/shell/workspaces/navAudit.test.ts` (25-entry cap) |
| UXS-15 Accessibility coverage, automated and manual | 042, keeps 012 fixed | WCAG 2.2 AA (ACR) | `e2e/a11y.spec.ts` and `e2e/flows-a11y.demo.spec.ts` on a generated route list, 2.2 tags, two viewports, from slice 2; every shared component on `/design-system` (`src/shell/pages/ComponentCatalogPage.test.tsx`); manual AT pass report per release that changes a shared component and per milestone; portals and technician app in `.github/workflows/ci.yml` (slice 20) |
| UXS-16 Task language and expert fields | 047, 043, part of 014 | U5, U1 | New term scan in `src/shell/glossary/glossary.test.tsx`; copy review checklist |
| UXS-17 Portal parity | 048, 035, 036, 039, 038 (strings) | U5, U2 | Formatting scan over portals; portal tests (`portal.test.tsx`, `writes.test.tsx`); string-extraction scan in each portal |
| UXS-18 Long-running work tracked | 006 (wired), 032 (run progress) | U4 | `src/modules/workflow/module.test.tsx` (job monitor fetch); new `JobProgress` tests; review checklist |
| UXS-19 Measured usability gate | 013, 049 | Easier (measured) | New `src/shell/usabilityTasks.test.ts`; new `e2e/task-readiness.demo.spec.ts`, `e2e/task-budgets.demo.spec.ts`; new `scripts/usability-scope.mjs`, `scripts/check-usability-gate.mjs` (G1 to G8, with fixture tests) in `.github/workflows/record-release.yml` |
| UXS-20 Guided flows and first-run journeys | 049 | Easier (guided task under 30 minutes; tasks without a super-user) | New `src/shell/documents/guidedFlows.test.tsx`; `e2e/task-readiness.demo.spec.ts`; `apps/supplier-portal/test/portal.test.tsx` (invitation and registration); usability tasks NU1 to NU3, OP0, CP0, SP0 |

## Traceability: finding to standard and slice

For the stage 4 gate ("each finding is in the backlog or waived with a reason"). Slices are in [rollout.md](rollout.md).

| Finding | Standards | Slices | | Finding | Standards | Slices |
|---|---|---|---|---|---|---|
| UX-001 | UXS-01, 02, 03, 13 | 1, 3 | | UX-026 | UXS-04, 14 | 7, 20 |
| UX-002 | UXS-03, 12, 13 | 3, 19 | | UX-027 | UXS-13 | 3 |
| UX-003 | UXS-01 | 1, 4, 18 | | UX-028 | UXS-05, 06 | 5, 10 |
| UX-004 | UXS-01 | 1 | | UX-029 | UXS-09, 12 | 3, 8 |
| UX-005 | UXS-01 | 1, 17 | | UX-030 | UXS-02, 09 | 6, 8 |
| UX-006 | UXS-01, 18 | 1, 16 | | UX-031 | UXS-04, 09 | 7, 12 |
| UX-007 | UXS-02, 05 | 5, 6 | | UX-032 | UXS-02, 04, 18 | 6, 7, 16 |
| UX-008 | UXS-02 | 6 | | UX-033 | UXS-02, 06 | 5, 6 |
| UX-009 | UXS-02 | 2, 6 | | UX-034 | UXS-06 | 5 |
| UX-010 | UXS-09 | 9, 12 | | UX-035 | UXS-07, 17 | 15 |
| UX-011 | UXS-09, 14 | 8 | | UX-036 | UXS-07, 08, 17 | 15 |
| UX-012 | UXS-10, 15 | 2 | | UX-037 | UXS-07 | 2 |
| UX-013 | UXS-19 | 0 | | UX-038 | UXS-17 | 15 |
| UX-014 | UXS-05, 06, 16 | 2, 5, 10 | | UX-039 | UXS-17 | 15 |
| UX-015 | UXS-04 | 7 | | UX-040 | UXS-08 | 13 |
| UX-016 | UXS-04 | 7 | | UX-041 | UXS-08 | 13 |
| UX-017 | UXS-04 | 7 | | UX-042 | UXS-15 | 2, 20 |
| UX-018 | UXS-08 | 13 | | UX-043 | UXS-09, 16 | 2, 9 |
| UX-019 | UXS-07 | 2 | | UX-044 | UXS-01 | 12, 17 |
| UX-020 | UXS-02, 07, 12 | 2, 3, 19 | | UX-045 | UXS-07 | 2 |
| UX-021 | UXS-02 | 6 | | UX-046 | UXS-11 | 14 |
| UX-022 | UXS-07 | 2 | | UX-047 | UXS-16 | 2, 5 |
| UX-023 | UXS-11 | 14 | | UX-048 | UXS-07, 17 | 15 |
| UX-024 | UXS-11 | 14 | | UX-049 | UXS-01, 19, 20 | 0, 1, 11 |
| UX-025 | UXS-14 | 20 | | UX-050 | UXS-05 | 10 |

## Waivers and re-routed items

| Finding | Decision | Reason |
|---|---|---|
| UX-038 (supplier portal English only) | **Partly waived**: translation into the four planned languages is not part of this design run. String extraction is kept (UXS-17, slice 13) | Depot is English only by product decision (`apps/web/docs/design-system.md` section 7; ACR 3.1.1). Translating the supplier portal needs a product decision on languages and translators, which are outside the UX design. Extraction now means translation can follow without rework. Revisit at the localization wave or when the owners name the languages |
| UX-038 (ASN without packages, SSCC, serials) | Re-routed to the functional backlog (stage 5) | A missing capability (`apps/supplier-portal/NOT-IMPLEMENTED.md`), not a UX standard. UXS-05 applies to the screen when it is built |
| UX-039 (shipment tracking; product images) | Re-routed to the functional backlog | Tracking waits on the carrier API; images need an image field on the product. The searchable combobox and unit select stay in UXS-17 |
| UX-003 (close workspace) | Not waived; first cut in slice 4 (M2), the rest in slice 18 | Listed here because the full close workspace is large; the first cut lets the controller see and work the close |
| UX-049 (training data, guided flows) | Not waived; fully assigned to UXS-19 (demo data per task) and UXS-20 (guided flows, first-run journeys), slices 0 and 11 | The first version of this spec left part of it open (review R-07) |

No other finding is waived.

## Corrections to the audit

These do not change any finding's severity, but stage 5 should use the corrected facts.

- **AP4 route.** The audit names `/payables/new`; the real route is `/payables/proposals/new`.
- **Kiosk jobs.** "Clock in to start jobs." has no link, but after clock-in the kiosk does list the worker's jobs and PI sheets (`modules/shopfloor/KioskJobs.tsx`, `KioskPiList.tsx`). UX-011 stands: the kiosk is inside the desktop shell, and reporting, andon and downtime are not on it.
- **Demo company.** `usability-tasks.md` says the one-click demo company is pending (#503). `/setup/demo-company` now loads and resets the organisation model (`services/org/internal/seed`), and the web demo build (`apps/web/src/demo/`, Cedar Ridge Pumps) resets on reload. UX-049 stands for transactional data per persona task.
- **Route list.** The audit's route list (`ux-scratch/routes.txt`) omits routes built from template strings (for example everything under `/production/`). Route-existence checks must read the module registry (`loadModules()`), not that list.

## Known gaps in this design

- **One tenth of the clicks (UX-GAP-1).** The thesis target for a five-line sales order is one tenth of the 40 to 60 clicks of AX or SAP. The budgets here reach about a third (20 interactions). Getting to a tenth needs product features (order templates, repeat last order, customer-specific quick entry). Stage 5 opens functional backlog item UX-GAP-1 for them; the product owner accepts the gap by signing the line in the sign-off table below.
- **Auditability depends on ingestion.** The History panel is only as complete as the audit chain. Today only `services/engineering` posts changes to `services/audit` (architecture delta D3). For the A7 set (vendor bank account, credit limit, payment terms, product price and standard cost, role assignment, AP invoice, journal entry) ingestion is a blocking dependency of slice 3 and milestone M2, checked by contract tests on the real services. For every other entity the UI shows an honest "not recorded yet" state until slice 19.
- **Retention and erasure.** How personal data is kept, held and erased in an append-only trail is an architecture decision (AUD-API-9); the UI only defines how an erased value is shown.
- **Step-up on the server.** The designed `requires_acr` obligation is missing (architecture delta C5); UXS-12 needs either it or one shared step-up problem code.

## Review and sign-off

The independent review of this folder lives next to it as `docs/sdlc/run-1/ux-design-review.md`, and the gate row in `docs/sdlc/run-1/gates.md`.

| Role | Name | Date | Decision |
|---|---|---|---|
| Product owner | | | |
| UX reviewer (did not write this) | | | |
| Accessibility reviewer | | | |
| Engineering lead (web) | | | |
| Product owner: accepts UX-GAP-1 (one tenth of the clicks is functional scope) and the UX-038 translation waiver | | | |

## Review response

The independent review (`docs/sdlc/run-1/ux-design-review.md`, gate: pass with conditions, 0 Blockers, 9 Majors, 10 Minors) was answered in this revision. Every Major is resolved in the spec itself; no Major is deferred.

| Id | Severity | How it was resolved | Where |
|---|---|---|---|
| R-01 | Major | Re-sequenced so every High finding has a first cut in slices 0 to 9: read-only sequences on the numbering API and import on the working setup step in slice 1 (the setup checklist link stays alive); a read-only close workspace (status, blockers, checklist run with task completion) in slice 4, M2; directed receive, put-away and pick with device sign-in in slice 9, M3, on the existing mobile service calls; AP invoice from receipt in slice 5. A table of first cuts per High finding heads the rollout | rollout.md (High findings table, slices 1, 4, 5, 9); patterns.md UXS-01 |
| R-02 | Major | `ID_PROMPT` now allows zero or more words and a plural (`…the (?:[a-z]+ )*ids?\b`); `DUTY_CODE` matches against the duty catalogue in `policies/authz/data.json` (748 duties) and module duties, with a shape fallback only next to "duty", "permission" or "role"; `PROBLEM_CODE` counts only known problem-code prefixes. The table now lists positive and negative fixtures, including the reviewer's counterexamples ("Enter the id of the ledger…", "the party.write duty", "tenant.administer"); all were run against the patterns while revising | patterns.md UXS-02 |
| R-03 | Major | Defined the A7 set (vendor bank account, customer credit limit, payment terms, product price and standard cost, role assignment, AP invoice, journal entry) with owning services and routes; contract tests per owning service and a live e2e; slice 3 and milestone M2 cannot be declared on the demo build alone; dependency on architecture gap D3 stated; `/admin/audit` shows audited-entity coverage | patterns.md UXS-03, UXS-13; rollout.md slice 3, Milestones |
| R-04 | Major | AUD-API-1 requires the verified principal as actor and refuses a body `userId` (as `POST /v1/audit/reads` does with AUD-0002); `onBehalfOf` only from a verified service principal with propagated user context; contract test; History labels service-recorded entries ("Recorded by Billing service for Ana Ruiz") | patterns.md UXS-03 |
| R-05 | Major | The audited-action core (registry, `sensitiveFields.json`, ReasonDialog for tier 1 on task routes, PendingChangeBanner and four eyes for the A7 fields, StepUpGate for payment release and signatures) moved into slice 3 with the History panel. Completeness is driven from `sensitiveFields.json`; the test fails when a form sends a sensitive field through a plain save; server contract tests for missing reasons, pending tier 2 writes and self-approval. Slice 19 is defined as the remaining modules | patterns.md UXS-12; rollout.md slices 3, 19 |
| R-06 | Major | Added designed-for personas 13 to 16 (master-data steward, approver/manager, maintenance planner/technician lead, auditor) with top-five tasks, budgets and routes; workspaces `master-data`, `approver`, `maintenance`, `auditor`; a read-only Auditor role with end-dated external assignments; a mapping of every `docs/PRD.md` section 3 row with a disposition; the "follows PRD section 3" sentence corrected | personas-and-journeys.md; patterns.md UXS-13, UXS-14; usability-programme.md section 4 |
| R-07 | Major | New standard UXS-20: guided flows for receive to stock, make to order and record to report; first-run journeys for the first web sign-in, the kiosk, the device, the supplier portal's `/accept-invite` and `/register`, and the customer portal (which has no invitation route; a `/welcome` page is added); demo data for every persona's top five tasks; tasks NU1 to NU3, OP0, CP0, SP0; demo tour copy fixed under UXS-01. UX-049 is now fully assigned (slices 0 and 11) | patterns.md UXS-20, UXS-01; rollout.md slice 11; usability-programme.md |
| R-08 | Major | Generated WCAG 2.2 scan at two viewports moved to slice 2 (M1); every shared component is scanned from slice 1 through the `/design-system` catalog; a manual assistive-technology pass (NVDA, JAWS, VoiceOver, keyboard only, 400% zoom, voice control) is required per release that changes a shared component, per milestone, and in "Done when" of slices 3, 7, 8, 9 and 10; one assistive-technology participant per desktop persona round where recruited. LineGrid no longer binds Insert or Ctrl+D; row actions moved to Shift+F10; a denylist test guards the keymap | patterns.md UXS-15, UXS-05; rollout.md; usability-programme.md |
| R-09 | Major | Slice 0 resized to M (about 6 weeks elapsed) plus S; the baseline is staged (A: 30 sessions before slices 1 to 4; B: 30 before slices 5 to 10; C: 25 before slices 11 to 15) with recruiting lead times; G1 shell sentinels limited to apps that render the shell (portals only via their own code or `shell/shared` after slice 15; technician only via its module); new G8: Round 0 numbers as an owner-signed provisional reference for at most 3 releases or 90 days per persona, with a re-walk on the release candidate | usability-programme.md sections 3, 6, 7; rollout.md slice 0 |
| R-10 | Minor | Budgets reconciled: five-line order 20 everywhere (1 + 3 per line + 1 + up to 3 header changes), OP4 1 / 3, CP1 1 / 7, AR1 steps listed to 6, CT1 1 / 6; `e2e/taskBudgets.json` named the single source once it exists | personas-and-journeys.md; patterns.md UXS-05; usability-programme.md |
| R-11 | Minor | Acceptance lines and tests added: UXS-19 points to G1 to G8; UXS-14 "Your tasks" checked in `home.test.tsx`; UXS-18 end-of-job notification test; UXS-17 token check on portal CSS | patterns.md |
| R-12 | Minor | Planned routes carry a slice and a date and the test fails after the date; generated help needs a purpose and at least one action; `noLiteralDefaults` covers `legalEntityId` and `siteId` | patterns.md UXS-01, UXS-11, UXS-06 |
| R-13 | Minor | Verb scan takes its verbs from the registry labels (including Correct time, Unblock, Reject, Void); Cancel, Delete and Remove count only with a record noun; fixtures for both | patterns.md UXS-12 |
| R-14 | Minor | Export columns listed; each export recorded in the trail; time-zone abbreviation on screen; `limit` and `cursor` on the per-record route; viewing sensitive history recorded (AUD-API-8); retention and erasure noted as AUD-API-9 | patterns.md UXS-03 |
| R-15 | Minor | Undo on the floor ("Undo" for 10 seconds on the kiosk, "Undo last line" on the device, then a correction with a reason); "confirm by scan" default stated per flow; reason buttons carry an icon and text | floor-apps.md sections 2, 4, 9 |
| R-16 | Minor | The technician app stays the reference implementation; it adopts the floor tokens and `QueueStatus` in slice 8, without moving its screens | patterns.md UXS-09; floor-apps.md; rollout.md slice 8 |
| R-17 | Minor | Workflow "sent by me" query added to slice 3's dependencies; F7 gives full paths and adds a kiosk queue unit test | rollout.md slice 3; floor-apps.md F7 |
| R-18 | Minor | Metrics "unaided success" (tasks without a super-user) and "time to first unaided success" (training to competence) added; the one-tenth gap became UX-GAP-1 with an owner sign-off line | usability-programme.md section 5; README Known gaps and sign-off |
| R-19 | Minor | Change management for existing users: a "What's new" entry and floor notice per slice that changes a task path, redirects for every moved route with a registry test, the guide updated in the same pull request | rollout.md "Change management for existing users" |

Correction to the review: it states that `/accept-invite` and `/register` exist in both portals. They exist only in the supplier portal (`apps/supplier-portal/src/main.tsx`); the customer portal has no invitation route, which UXS-20 now records as missing.
