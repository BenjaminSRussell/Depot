# Usability programme

How Depot measures whether it is easy to use, and the release gate that stops it from getting harder without anyone noticing. This is UXS-19 ([patterns.md](patterns.md)). It keeps the method and measures of `apps/web/docs/usability-tasks.md`, `usability-baseline-report.md` and `usability-gate.md`, and changes three things:

1. **A baseline is run now**, on the current code, with the demo company, for every persona that can be tested today.
2. **Task scripts are added** for the personas that had none (shop-floor operator, field technician, customer portal user, supplier portal user, master-data steward, approver, maintenance planner, auditor), for the warehouse device, and for first-run journeys and guided flows (UXS-20); every task gets a screen and interaction budget.
3. **The gate cannot pass by default, and cannot freeze releases either.** Scope is computed from the change, a missing measurement counts as a failure, and the release-notes default line ("Not in scope") is removed. While the staged baseline is being collected, a bounded, owner-signed provisional reference lets UX fixes ship (G8).

Where this file and `apps/web/docs/usability-gate.md` disagree, this file wins until that doc is updated (rollout slice 0).

## 1. Test environments

Each round uses the same build and the same data for every participant of a persona. The environment is reset before every participant.

| Persona(s) | Environment | How it starts | Reset |
|---|---|---|---|
| AP clerk, AR clerk, buyer, order desk, controller, planner, tenant admin, warehouse (web tasks), master-data steward, approver, maintenance planner, auditor, supervisors, HR, new users (NU) | Web demo build: `cd apps/web && npm run build:demo`, served under `/Depot/`. The in-browser backend (`apps/web/src/demo/`) holds the Cedar Ridge Pumps group: a US and two euro legal entities, customers, vendors, items, warehouses, workers | Fresh browser profile; the moderator opens the task's start route | Reload the tab: demo data lives in the tab and resets on reload (`src/demo/DemoBanner.tsx`). Dates are relative to today (`src/demo/store.ts`), so records stay current |
| Field technician | Same demo build at `/technician`, on a phone (or Playwright Pixel 5 for the walkthrough) | Phone browser, airplane mode toggled for TE5 | Reload; clear the app's storage between participants |
| Shop-floor operator | Today: `/shopfloor/kiosk` in the demo build, on a touch tablet in landscape. After slice 8: `/kiosk` | Kiosk tablet mounted at standing height | Reload |
| Warehouse worker (device tasks) | `cd apps/web && DEPOT_MOBILE_STUB=1 npm run dev`, device app at `/device` (development only today), on a handheld or a phone with a Bluetooth scanner; printed location and item labels from the demo data | Device signed in as the demo worker | Restart the dev server stub |
| Customer portal user | Customer portal demo build: `cd apps/customer-portal && npx vite build --mode demo` (in-browser BFF, `src/demo.ts`, Harlan Agricultural Co-op) | Phone and desktop, half the participants each | Reload |
| Supplier portal user | **No demo mode exists.** Slice 0 adds one, modelled on the customer portal's `src/demo.ts`. Until then, a staging tenant with the same seeded data, or the tasks are "not measured" | — | — |
| Implementer (TA6) | Any environment with the org service: `/setup/demo-company` loads and resets the organisation model (`services/org/internal/seed`: legal entities, sites, warehouses, teams; no transactions) | — | Reset button on the page |

**Task readiness.** Before a round, every task's starting data must exist (AP1 needs a receipt that is not yet invoiced; SR3 needs an order on credit hold; TA3 needs a setting changed last week). New e2e `apps/web/e2e/task-readiness.demo.spec.ts` opens each task's start route in the demo build and asserts its precondition record is present. A task whose data is missing is recorded as **not measurable** and counts as failed at the gate (section 6). Adding the data is the fix (UX-049: the demo backend covers many screens but not every persona's tasks; the org-service demo company has no transactional data).

## 2. Round 0: expert walkthrough (not a baseline)

Two people who did not build the screens walk every task script on the current commit, before any participant session. For each task they record: whether it can be completed at all, screens and interactions on the happy path, blockers, and wording that a participant would not understand. Output: `docs/sdlc/run-1/usability-walkthrough.md`, the first values in `e2e/taskBudgets.json`, and a list of tasks that cannot be attempted (for example TA3 and TA5 today, CT4 because `/close` does not exist). Round 0 numbers never count as a baseline; they decide which tasks are worth a participant's time.

## 3. Baseline, staged, starting now

The baseline is the reference every later release is compared against. Running it for all 16 personas at once (80 sessions, floor sessions on real devices, a supplier portal demo mode that does not exist yet) would take months and would block the very fixes it is meant to measure. So it is staged: each stage is taken on the commit just before the rollout slices that change that stage's personas land, so the improvement can still be shown.

| Stage | Personas | Taken before | Sessions | Elapsed time |
|---|---|---|---|---|
| A | Tenant admin, controller, AP clerk, master-data steward, auditor, approver, maintenance planner | Slices 1 to 4 (honest screens, history, close first cut) | 7 × 5 = 35 | 3 to 4 weeks recruiting, then about 1.5 weeks of sessions (2 moderators, up to 6 sessions a day, 60 to 75 minutes each) |
| B | Order desk, buyer, AR clerk, warehouse worker (web and device), shop-floor operator, production planner | Slices 5 to 10 | 6 × 5 = 30, floor sessions on site | Recruiting overlaps stage A; about 2 weeks of sessions, 3 days of it on a shop floor and in a warehouse |
| C | Customer portal user, supplier portal user, field technician, new users (NU1 to NU3) | Slices 11 to 15 | 4 × 5 = 20, NU participants with no Depot experience | Needs the supplier portal demo mode; about 1.5 weeks |

Recruitment: target-market participants through a recruiting panel and customers' staff, with incentives; at least one daily assistive-technology user in each desktop persona's five where the panel allows, and at least one per stage (UXS-15). Rollout slice 0 is therefore sized M (process, about 6 weeks elapsed, part time) plus S (the scripts), and the stage A report is its exit criterion, not the whole baseline.

Each stage runs the steps below.

| Step | What | Who |
|---|---|---|
| 1 | Freeze the build: record the commit, build the demo bundles, note it in the report | Release manager |
| 2 | Run task readiness (section 1); add missing demo data or mark tasks not measurable | Engineering |
| 3 | Recruit 5 participants per persona from the target market, none of them developers or people who built the screens; someone who does the job today or did within two years. Floor and warehouse participants do the tasks on real devices, standing, with gloves if their job uses them | Product owner |
| 4 | Run sessions as written in `usability-tasks.md` ("Running a session"): read the scenario, hand over the start page only, think aloud, no help, stop at 3 minutes stuck or 5 minutes total (2 minutes for floor tasks) | Moderator |
| 5 | Record per attempt: success, time on task, errors, screens and interactions (from the recording), the single ease question; SUS after the tasks | Moderator and note taker |
| 6 | Write up with the template (`usability-baseline-report.md`, extended as in section 5) as `apps/web/docs/usability-baseline-YYYY-MM-DD.md`; one issue per finding under the run's backlog label; sign-off by moderator and product owner | Moderator, product owner |

Personas whose stage has not been reached, or who cannot be recruited in the window, keep their tasks "not measured". While that is so, their Round 0 walkthrough numbers are a provisional reference under rule G8 (section 6), for a bounded time; after that, each such task fails the gate when it comes into scope, which forces the gap to close.

## 4. Task scripts to add

Same columns as `usability-tasks.md`, plus the budget from [personas-and-journeys.md](personas-and-journeys.md). Task ids never change once a baseline exists.

### Shop-floor operator (`operator`, new workspace)

| # | Scenario | Starts at | Route | Done when | Budget S / I |
|---|---|---|---|---|---|
| OP1 | Your shift starts. Clock in. | Kiosk | `/kiosk` (today `/shopfloor/kiosk`) | A clock-in exists for the participant's worker at the session time | 1 / 2 |
| OP2 | Start the next job planned for your work cell. | Kiosk | `/kiosk` | The earliest planned job for the cell is started for the worker | 1 / 2 |
| OP3 | You made 12 good pieces and 2 scrap because of a material defect. Report it. | Kiosk, job running | `/kiosk/jobs/:id` | Good 12 and scrap 2 with reason "Material defect" are recorded on the job | 1 / 4 |
| OP4 | Your machine stopped because it jammed. Tell the system and call for help. | Kiosk | `/kiosk` | A downtime with reason Jam is open on the machine, and an andon call exists | 1 / 3 |
| OP5 | Open the work instructions for the job you are running. | Kiosk, job running | `/kiosk/pi/:id` | The instruction for the running operation is on screen | 2 / 1 |
| OP6 | Your shift is over. Clock out so the next person can sign in. | Kiosk | `/kiosk` | A clock-out exists and the kiosk shows the sign-in screen | 1 / 3 |
| OP0 | (First use, new badge.) Sign in on the kiosk for the first time and start your first job. | Kiosk | `/kiosk` | The worker is signed in and a job is started, without moderator help | 1 / 3 |

### Warehouse worker, device tasks (`warehouse`)

| # | Scenario | Starts at | Route | Done when | Budget S / I |
|---|---|---|---|---|---|
| WH6 | Pick the next order you are given. | Device menu | Device Pick flow | Every line of the assigned pick is confirmed with the right quantity and batch | 2 scans per line, 0 taps when quantities match |
| WH7 | Put away the pallet you just received. | Device menu | Device Put away flow | The pallet is at the location the system directed | 2 scans |
| WH8 | A delivery for PO-1042 has arrived. Receive it on the device. | Device menu | Device Receive flow | A receipt with the delivered quantities exists | 1 document scan + 1 scan per line |
| WH9 | Count location A-01-02. | Device menu | Device Count flow | A count for every item at the location is saved | 3 per item |

The existing WH1 to WH5 keep their routes and gain budgets.

### Field technician (`apps/technician`)

| # | Scenario | Starts at | Route | Done when | Budget S / I |
|---|---|---|---|---|---|
| TE1 | Tell the moderator which customer you visit first today and at what time. | My day | App home | The participant names the first visit and time | 1 / 0 |
| TE2 | You are leaving for the first visit. Record that, then record that you started work on arrival. | My day | Visit | Travel start and work start are recorded on the visit | 1 / 2 |
| TE3 | You used one seal kit from your truck. Record it. | Visit | Visit | One seal kit is recorded on the visit and truck stock is one lower | 1 / 3 |
| TE4 | The work is done. Get the customer's signature and complete the visit. | Visit | Visit | The visit is complete with a signature | 1 / 3 |
| TE5 | You have no signal (airplane mode on). Complete TE3 again for the second visit, then turn signal back on. | Visit, offline | Visit | The part is recorded on the server after reconnecting, without the participant re-entering it | 0 extra |

### Customer portal user (`apps/customer-portal`)

| # | Scenario | Starts at | Route | Done when | Budget S / I |
|---|---|---|---|---|---|
| CP0 | (First visit.) Open the invitation email, sign in and reorder 5 seal kits. | Invitation email | Customer portal `/` after sign-in (no invitation route; `/welcome` missing) | An order exists and the participant reached it without help | 2 / 7 |
| CP1 | Reorder 10 seal kits and 2 booster pumps. | New order | `/orders/new` | An order with those products and quantities exists | 1 / 7 |
| CP2 | Pay your two oldest open invoices by card. | Open items | `/open-items` | One payment covers both invoices | 3 / 6 |
| CP3 | Download the PDF of your last invoice. | Invoices | `/invoices` | The PDF is downloaded | 2 / 2 |
| CP4 | Find out when your last order will arrive. | Orders | `/orders` | The participant states the delivery status and date (fails until tracking is built) | 1 / 1 |
| CP5 | One pump arrived damaged. Ask to return it. | Returns | `/returns/new` | A return request for that line exists with the reason | 1 / 5 |

### Supplier portal user (`apps/supplier-portal`)

| # | Scenario | Starts at | Route | Done when | Budget S / I |
|---|---|---|---|---|---|
| SP0 | (First visit.) Accept the invitation, set up your account and confirm your first purchase order. | Invitation link | `/accept-invite` (or `/register`), then `/purchase-orders` | The account is active and the PO shows acknowledged | 3 / 6 |
| SP1 | A new purchase order arrived. Confirm you will deliver it. | Purchase orders | `/purchase-orders` | The PO shows acknowledged | 1 / 2 |
| SP2 | You shipped the order today. Tell the customer what is coming. | Purchase orders | `/purchase-orders/:id` → `/shipments/new` | An ASN for that PO with the shipped quantities exists | 1 / 5 |
| SP3 | Send your invoice for the shipped order. Then find out why it was held for review and what to do. | Invoices | `/invoices` | The invoice is submitted; the participant states the reason and the fix in their own words | 1 / 5 |
| SP4 | Check whether your March invoice has been paid. | Payments | `/payments` | The participant states paid or not and the date | 1 / 2 |
| SP5 | Your bank account changed. Ask the customer to update it. | Requests | `/requests` | A change request with the new details exists | 1 / 5 |

### Additions to existing personas

| # | Persona | Scenario | Starts at | Route | Done when | Budget S / I |
|---|---|---|---|---|---|---|
| SR6 | Sales rep | Ship line 2 of this order directly from the vendor. | Sales order | `/sales/orders/:id` | A drop-ship PO exists for the line with the customer's address | 0 new / 3 |
| TA6 | Administrator | Load the demo company, then reset it. | Demo company | `/setup/demo-company` | The page reports records created, then removed and created again | 1 / 3 |
| WS1 | Warehouse supervisor | A handheld was lost. Sign its worker out of it. | Devices | `/mobile-flows/devices` (missing) | The session is ended | 1 / 3 |
| WS2 | Warehouse supervisor | Resolve the offline conflict from this morning. | Offline conflicts | `/mobile-flows/conflicts` | The conflict is reapplied or rejected with a reason | 1 / 3 |

### Master-data steward (`master-data`, new workspace)

| # | Scenario | Starts at | Route | Done when | Budget S / I |
|---|---|---|---|---|---|
| MD1 | A colleague changed Contoso's bank account. Check it and approve it if it looks right. | Pending changes | `/party/pending-changes` | The change is approved by the participant; the participant names the old and new account (last 4) and the reason | 2 / 2 |
| MD2 | Two customer records are the same company. Merge them. | Duplicates | `/party/duplicates` | One customer remains with both records' history | 2 / 4 |
| MD3 | Update the sales prices in this spreadsheet (40 products). | Price list | `/pricing/price-list` | All 40 prices are updated with a reason; nothing else changed | 1 / 5 |
| MD4 | Create a new product and make it available to the German company. | Products | `/product/items` | The product exists and is released to Cedar Ridge Pumps Europe GmbH | 2 / 8 |
| MD5 | A BOM line should use 4 bolts, not 6. Correct it. | Bills of material | `/engineering/boms/:id` | The line shows 4 with a reason in History | 1 / 4 |

### Approver (`approver`, new workspace)

| # | Scenario | Starts at | Route | Done when | Budget S / I |
|---|---|---|---|---|---|
| AM1 | Approve the requisition waiting for you. | Inbox | `/workflow/inbox` | The requisition is approved | 1 / 2 |
| AM2 | A purchase order is waiting for you. Find out why it needs your approval, then approve it. | Inbox | `/workflow/inbox` | The participant names the rule (amount over limit) and the order is approved | 2 / 2 |
| AM3 | Send back the invoice waiting for you because the price is wrong. | Inbox | `/workflow/inbox` | The invoice is sent back with a reason | 1 / 3 |
| AM4 | Approve the four routine requests in your inbox at once. | Inbox | `/workflow/inbox` | All four are approved | 1 / 7 |
| AM5 | Find which of the things you sent are still waiting, and on whom. | Inbox | `/workflow/inbox` | The participant names each item and its approver | 1 / 1 |

### Maintenance planner (`maintenance`, new workspace)

| # | Scenario | Starts at | Route | Done when | Budget S / I |
|---|---|---|---|---|---|
| MT1 | Turn the request about the leaking pump into a work order. | Maintenance requests | `/eam/requests` | A work order exists that came from the request | 2 / 4 |
| MT2 | Set up quarterly maintenance for press 4. | Maintenance plans | `/eam/plans` | A plan with a quarterly schedule exists for the asset | 1 / 6 |
| MT3 | Send today's two unassigned visits to Jordan Avery. | Dispatch | `/service/dispatch` | Both visits are assigned to Jordan Avery | 1 / 2 |
| MT4 | Find the asset that failed most this quarter. | Maintenance KPIs | `/eam/kpis` | The participant names the asset | 1 / 3 |
| MT5 | Issue a hot-work permit for tomorrow's welding job. | Permits | `/eam/permits` | A signed permit exists for the job | 1 / 5 |

### Auditor (`auditor`, new workspace, Auditor role only)

| # | Scenario | Starts at | Route | Done when | Budget S / I |
|---|---|---|---|---|---|
| AU1 | Export the change history of vendor Contoso Supplies for the last year. | Vendor | `/party/:id` | A file with the vendor's changes, names and reasons is downloaded | 1 / 2 |
| AU2 | Confirm the exported history is complete and untampered. | Audit | `/admin/audit` | The participant states the verification result shown with the export | 1 / 2 |
| AU3 | List every vendor bank-account change last quarter and who approved each. | Audit | `/admin/audit` | The participant names the changes and approvers | 1 / 4 |
| AU4 | Trace the cost-of-goods figure on last month's statement to a journal entry and the person who posted it. | Statements | `/finreport` | The participant names the entry and the person | 2 / 3 |
| AU5 | Tell the moderator which record types the audit trail does not cover yet. | Audit | `/admin/audit` | The participant reads the coverage list correctly | 1 / 1 |

### New users and guided flows (UXS-20)

Participants have never used Depot. Time limit 30 minutes per task.

| # | Scenario | Starts at | Route | Done when | Budget |
|---|---|---|---|---|---|
| NU1 | (Warehouse.) A delivery for PO-1042 has arrived. Get it received and put away. | Home | Receive-to-stock guided flow | The goods are on hand at their put-away location | 30 minutes |
| NU2 | (Order desk and planner.) This customer order needs a pump we make to order. Take it through to production. | Home | Make-to-order guided flow | A production order exists for the order line | 30 minutes |
| NU3 | (Controller.) Get last month to a trial balance you can sign off. | Home | Record-to-report guided flow | The participant reaches the trial balance with no open blocker, or names the blocker | 30 minutes |

### Corrections to existing scripts

- **CT4** start route `/close` does not exist. Mark CT4 "planned" (the route is in `shell/plannedRoutes.json`, target rollout slice 4) until the read-only close workspace ships; it counts as failed when in scope.
- **AP4** keeps `/payables` as its start; the audit's `/payables/new` is really `/payables/proposals/new`.
- **"A one-click demo company and reset are pending issue #503"** is out of date: `/setup/demo-company` loads and resets the organisation model, and the web demo build resets on reload. Replace the sentence with section 1 of this file.

## 5. Metrics

| Measure | Definition | Reported per |
|---|---|---|
| Task success | Share of attempts scored *pass* (reached the stated result alone, checked against the data). *Pass with help* is reported separately and is not success | Task, persona |
| Time on task | Median seconds over *pass* attempts | Task |
| Errors per attempt | Mean of wrong turns that needed backing out, rejected submits, and messages the participant said they did not understand, over all attempts | Task |
| Interactions | Median interactions (definition in [personas-and-journeys.md](personas-and-journeys.md)) over *pass* attempts, from the recording; compared with the task's budget | Task |
| Single ease question (SEQ) | After each task: "Overall, how easy or difficult was this task?" 1 (very difficult) to 7 (very easy); mean | Task |
| SUS | The standard 10-question System Usability Scale, 0 to 100, scored as in `usability-baseline-report.md`; mean and lowest per persona | Persona |
| New-user guided task | Share of participants with no Depot experience who finish each guided flow (procure-to-pay, order-to-cash, and from slice 11 receive-to-stock, make-to-order, record-to-report; `shell/documents/GuidedFlow.tsx`) within 30 minutes, the thesis "Easier" target | Flow, round |
| Unaided success | Share of attempts scored *pass* rather than *pass with help*, per persona: the stand-in for the thesis measure "share of tasks completed without a super-user" | Persona, round |
| Time to first unaided success | For new users (NU tasks and first-run tasks OP0, CP0, SP0): minutes from first sign-in to the first task passed without help; the stand-in for "training hours to competence" | Round |
| Funnel drop-off (telemetry) | Per step drop-off and validation errors per field from the UX events (`shell/uxEvents.ts`, flows `procure_to_pay` and `onboarding` today; add one flow per persona's top task) | Release, as a second check only |

Targets for calling a persona "done" at the end of the rollout (not the gate, which is relative): every top-five task at or above 80% success (4 of 5), within its interaction budget, SEQ 5.5 or higher, and the persona's SUS at 68 or higher (75 for the floor and portal personas, whose users get no training).

Template changes to `usability-baseline-report.md`: add the eight new personas (operator, technician, customer portal, supplier portal, master-data steward, approver, maintenance planner, auditor) and the new-user rows to the summary table; add an "Unaided success" column; add columns "Interactions (median)", "Budget" and "SEQ" to each task table; add a "Not measured" column with the reason.

## 6. The release gate (cannot pass by default)

The measures and thresholds stay as in `usability-gate.md` (success within 20 points of the reference, median time within 25%, errors within +0.5, SUS within 5 points and not below 68 where the reference was above 68, no open blocker). These rules are added:

| Rule | What it says | How it is checked |
|---|---|---|
| G1 Scope is computed | The tasks in scope are worked out from the change, not declared. A task is in scope when the release changes a file its route renders: the module that serves the route, or a shared file that module imports. Shared changes bring in sentinel tasks (one per persona) only for apps that actually render the changed code: a change under `apps/web/src/shell/` brings in the web sentinels (AP1, AR1, BU1, WH1, SR1, PP1, CT1, TA1, MD1, AM1, MT1, AU1, and OP1 once the kiosk uses `shell/floor/`). The portals' sentinels (CP1, SP1) come in only through `apps/customer-portal/**`, `apps/supplier-portal/**` or, once rollout slice 15 makes the portals import it, `apps/web/src/shell/shared/**`. The technician sentinel (TE1) comes in only through `apps/technician/**` or `apps/web/src/modules/technician/**`. The warehouse device tasks come in through `apps/warehouse-mobile/**` and `apps/web/src/modules/mobile-flows/**` | New `apps/web/scripts/usability-scope.mjs <from>..<to>` maps changed files to routes (via each module's `module.ts` routes and the portals' `App.tsx`) and routes to task ids |
| G2 No reference means fail | A task in scope with no measured reference (never measured, marked not measurable, or planned) counts as failed. "Not in scope" is only valid when G1 finds no task | `check-usability-gate.mjs` |
| G3 "Passed" needs numbers | "Passed" must link a committed report in `apps/web/docs/` that has a row with numeric success, time, errors and interactions for every task G1 found | `check-usability-gate.mjs` parses the release notes and the report |
| G4 No default text | The release-notes template's "Usability gate" section starts empty. An empty section, or the literal default line, fails | `check-usability-gate.mjs` |
| G5 Budgets hold | For tasks with a scripted path, the interaction count may not exceed the budget, nor the count in `e2e/taskBudgets.json` for tasks not yet within budget | `e2e/task-budgets.demo.spec.ts` in the `web-demo-flows` CI job; fails the pull request, before release |
| G6 Routes exist | Every task's route exists in the route table or is in `shell/plannedRoutes.json` with an issue | `apps/web/src/shell/usabilityTasks.test.ts` in `npx vitest --run` |
| G7 Waivers are narrow | Only the product owner can waive, in writing, naming the measure, cause and fixing issue; a waiver covers one release; a blocker cannot be waived; the same task cannot be waived for "no reference" in two consecutive releases (G8 is the only repeatable path while a baseline is being collected) | `check-usability-gate.mjs` reads the previous release notes |
| G8 Provisional reference, bounded | For a task whose persona's baseline stage (section 3) has not been reached, or has no test environment yet, the Round 0 walkthrough is a provisional reference. The release passes for that task when two people who did not build the change re-walk it on the release candidate and it is still completable with no more interactions than Round 0 recorded. The product owner signs it in the release notes ("Provisional: tasks <ids>, Round 0 <link>, re-walk <link>, signed <name>"). It is allowed for at most 3 releases or 90 days per persona, whichever comes first, counted from the persona's first provisional use; after that G2 applies. Each use is listed in the run's `close.md` | `check-usability-gate.mjs` counts provisional uses per persona from previous release notes and checks the links |

`check-usability-gate.mjs` runs in `.github/workflows/record-release.yml` before a release is recorded. Its exit code is the gate: 0 passes, anything else stops the recording with the reason in plain words ("AP1 is in scope (apps/web/src/modules/ap-invoices changed) and has no reference measurement").

Consequence, stated plainly for sign-off: UX fixes can ship while the staged baseline is collected, under a signed provisional reference that runs out after 3 releases or 90 days per persona. After that, a release that touches a persona's task path without a measured reference fails, so each baseline stage has to be run on time. A stage that slips shows in the release notes as the persona whose provisional window is closing.

## 7. Cadence

| When | What |
|---|---|
| Now (slice 0) | Round 0 walkthrough for every task; baseline stage A |
| Before slices 5 and 11 | Baseline stages B and C |
| At each milestone M2 to M5 ([rollout.md](rollout.md)) | A milestone round for the personas the milestone changed, in-scope tasks only, 5 participants each, with an assistive-technology participant per desktop persona where recruited, and the manual AT pass of UXS-15 |
| Every release | The gate (section 6) |
| Every release with a portal or floor change | The axe and reflow checks of UXS-10 and UXS-15 already run in CI; the round covers what automation cannot |
| Close of each SDLC run | `docs/sdlc/run-<n>/close.md` reports per persona: success, interactions against budget, SUS, against the baseline |

## 8. Roles and sign-off

| Role | Owns |
|---|---|
| Product owner | Recruiting, the decision on waivers, signing reports |
| Moderator (not a builder of the screens) | Running sessions, writing the report |
| Release manager | Running `usability-scope.mjs`, writing the release-notes gate line |
| Engineering | Task readiness data, `task-budgets` scripts, the gate scripts |
| Accessibility reviewer | The ACR sign-off (#512), using the same rounds where participants use assistive technology |

Every persona whose tasks a shell change can bring into scope (G1) is baselined in stage A or B, so no G8 provisional window can expire before that persona's baseline exists (review R-20). The maintenance planner moved from stage C to stage A because its tasks run on the web demo build.
