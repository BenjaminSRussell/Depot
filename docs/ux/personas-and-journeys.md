# Personas and journeys

The 16 personas Depot is designed for, each with the five tasks that fill most of their day, the screen and interaction budget each task must meet, and the route that must serve it. Personas 1 to 12 are the ones the UX audit studied (`docs/sdlc/run-1/ux-audit.md`, "Per persona"). Personas 13 to 16 (master-data steward, approver, maintenance planner, auditor) were added after the stage 4 review because they own the approvals, master data and audit trail the owners' auditability ask depends on; the audit did not estimate their tasks, so their "today" figures come from Round 0 ([usability-programme.md](usability-programme.md)). The task ids match `apps/web/docs/usability-tasks.md` where a task already exists there; new ids are added for the personas that had none (OP, WH6 to WH9, TE, CP, SP, SR6, MD, AM, MT, AU) and for first-run journeys (OP0, CP0, SP0, NU1 to NU3; UXS-20).

**How this maps to `docs/PRD.md` section 3.** PRD section 3 groups people by job family, not by persona. Its rows map as follows:

| PRD section 3 row | Here |
|---|---|
| Platform engineer / SRE | Out of scope for UX design: works in deployment, observability and CLI tools, not in Depot's screens |
| System administrator | Tenant admin (9) |
| Master-data steward | Master-data steward (13) |
| Controller / accountant | Controller (7); auditor (16) is the outside reader of the controller's records |
| Buyer / AP clerk | Buyer (5), AP clerk (3), approver (14) |
| Production planner / supervisor | Production planner (8), shop-floor operator (1); supervisor secondary |
| Sales rep / order desk / AR clerk | Order desk (6), AR clerk (4), customer portal user (10) |
| Warehouse manager / operator | Warehouse worker (2); warehouse supervisor secondary; supplier portal user (11) on the inbound side |
| Project manager / HR / payroll admin | HR and payroll administrator secondary (task scripts HR1 to HR5 kept); project manager out of scope for this run because no audit finding touches projects; maintenance planner (15) and field technician (12) cover the service part of that wave |
| Analyst / executive | Secondary: dashboards and KPI pages are covered by the list, honest-screen and accessibility standards, but no top-five task script until a usability round asks for one |
| Implementation partner | Secondary (TA5, TA6) |

## How to read the budgets

- **S** is screens: routes in the web app or portals, or full-screen steps on a floor device. Dialogs and side panels do not count as screens.
- **I** is interactions: one click or tap, one scan, one key chord, or one field value entered (typing then Tab or Enter counts once). Reading does not count.
- **Start state:** signed in, work context set, the task's start route open, the demo company loaded. Navigation from home to the start route is not counted (the workspace home links it in one tap, UXS-14).
- **Today** is the audit's estimate from the code (not measured). **Target** is the budget the task must meet once the listed standards ship. "+n per line" means each additional document line adds n.
- **Route status:** *exists*; *exists, needs change* (the route is there but the task cannot meet the target until the named standard ships); *missing* (the route must be built).

The targets follow the thesis "Easier" axis (`docs/prd/part-00-thesis.md`): the 40 to 60 click baseline for a five-line sales order in AX or SAP becomes 20 interactions in Depot (SR1 extended to five lines: 1 customer + 3 per line + 1 confirm + up to 3 header changes). That is a third, not a tenth; a tenth needs order templates and repeat-order features that are product scope, not UX standards, and is recorded as a gap in [README.md](README.md).

**Single source.** These tables are the source of every budget until `apps/web/e2e/taskBudgets.json` exists; from then on that file is the single source and this file and [usability-programme.md](usability-programme.md) cite it.

**Enforcement.** Every task below has a scripted happy path in `apps/web/e2e/task-budgets.demo.spec.ts` (new) that counts interactions with a helper wrapping Playwright's `click`, `fill`, `press` and scan injection, and fails when a task exceeds its target. Tasks that exceed their target today are listed in `e2e/taskBudgets.json` with their current count and the rollout slice that fixes them; that file may only shrink, and a listed count may only go down. Usability sessions record observed screens and interactions as a second measure ([usability-programme.md](usability-programme.md)). `apps/web/src/shell/usabilityTasks.test.ts` (new) checks every route in this file exists in the route table or is listed as planned in `shell/plannedRoutes.json`.

## Summary

| # | Persona | Device and place | Workspace or app | Role guide | Routes missing |
|---|---|---|---|---|---|
| 1 | Shop-floor operator | Shared wall kiosk or tablet at the work cell, gloves often | Kiosk (`/kiosk`), operator workspace | Missing | `/kiosk`, `/kiosk/jobs/:id`, `/kiosk/pi/:id` |
| 2 | Warehouse worker | Handheld scanner (Zebra TC52, Honeywell CT45) or phone; tablet at the dock | Warehouse device app, `warehouse` workspace | Missing | Directed device flows (count, move, pack, cluster) |
| 3 | AP clerk | Desktop, two screens, keyboard | `ap-clerk` | `docs/user/ap-clerk.md` | None (changes needed) |
| 4 | AR clerk | Desktop | `ar-clerk` | `docs/user/ar-clerk.md` | None |
| 5 | Buyer | Desktop, sometimes laptop | `buyer` | `docs/user/buyer.md` | None |
| 6 | Order desk / sales rep | Desktop with phone headset; laptop on the road | `sales-rep` | `docs/user/order-desk.md` | "Ship from vendor" action on the order line |
| 7 | Controller / accountant | Desktop | `controller` | Missing | `/close` |
| 8 | Production planner | Desktop, large screen | `production-planner` | Missing | None |
| 9 | Tenant admin | Desktop | `tenant-admin` | `docs/user/tenant-admin.md` | Real import behind `/admin/import` |
| 10 | Customer portal user | Phone or desktop, outside the company | Customer portal (`apps/customer-portal`) | Missing | Shipment tracking capability |
| 11 | Supplier portal user | Desktop, often non-native English, outside the company | Supplier portal (`apps/supplier-portal`) | Missing | None |
| 12 | Field technician | Phone, outdoors, offline often | Technician app (`apps/technician`) | Missing | None |
| 13 | Master-data steward | Desktop, spreadsheets beside Depot | `master-data` (new) | Missing | None (paste on the price list needed) |
| 14 | Approver / manager | Desktop and phone, between meetings | `approver` (new), alongside their own workspace | Missing | None ("Approve selected" and "Waiting on others" needed) |
| 15 | Maintenance planner / technician lead | Desktop in the maintenance office, tablet on the floor | `maintenance` (new) | Missing | None |
| 16 | Auditor (internal or external, read-only) | Desktop, time-limited access | `auditor` (new), Auditor role (UXS-13) | Missing | None (`/admin/audit` rebuild needed) |

---

## 1. Shop-floor operator

Runs a machine or an assembly station. Uses a shared kiosk for seconds at a time, between tasks, often with gloves, in noise. Does not choose a legal entity or a site and should never see those words. Success means: clocked in, on the right job, quantities and scrap reported, help called fast.

Today the kiosk is `/shopfloor/kiosk` inside the desktop shell (UX-011). After clock-in it does list the worker's jobs (`KioskJobs`) and PI sheets (`KioskPiList`), but job reporting, andon and downtime are desktop pages.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| OP1 | Clock in at the start of a shift | `/kiosk` | Missing (today `/shopfloor/kiosk` in the shell) | 1 / 2 | 1 / 2 (scan badge, Clock in) | UXS-09 |
| OP2 | Find and start my next job | `/kiosk` (My jobs list on the home screen) | Missing | 2-3 / 5+ | 1 / 2 (tap job, Start) | UXS-09, UXS-02 |
| OP3 | Report good quantity and scrap with a reason | `/kiosk/jobs/:id` | Missing (today `/shopfloor/jobs/:id`, desktop, free-text scrap reason) | 1 / 5 | 1 / 4 (Report, good qty, scrap reason button, Save) | UXS-09, UXS-12 |
| OP4 | Report a machine stopped and call for help | `/kiosk` | Missing (today `/shopfloor/andon`, `/shopfloor/downtimes`, supervisor screens) | 2 / 4+ | 1 / 3 (Machine stopped, reason, Call for help) | UXS-09 |
| OP5 | Open the instructions for the running job | `/kiosk/pi/:id`, reached from the job | Missing (today `/shopfloor/kiosk/pi/:id`, needs the id) | 1-2 / n | 2 / 1 (Instructions) | UXS-09, UXS-11 |

Also scripted for usability: OP6, clock out and let the next person sign in (target 1 / 3: Clock out, Sign out, next badge scan; FR-25.1 "switch user in 2 taps or fewer"); OP0, first use of a kiosk with a new badge (UXS-20).

## 2. Warehouse worker

Receives, puts away, picks, packs, moves and counts. Walks all shift with a handheld; reads the screen at arm's length; scans far more than types. Success means: told where to go, what to take and how many, and done with a scan.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| WH1, WH2 | Receive a PO delivery (full or partial) at the dock | `/receiving/receipts/new?po=` (tablet) | Exists, needs change (receive all open lines in one screen) | 3 / ~12 for one line, +5 per line | 1 / 4 (choose PO, adjust partial quantities, Post) +1 per changed line | UXS-05, UXS-06, UXS-16 |
| WH8 | Receive on the device | Device Receive flow | Exists, needs change (directed by the PO or ASN lines) | 1 / scan + typed qty + confirm per line | 1 document scan, then 1 scan per line (quantity defaults to open quantity) | UXS-09 |
| WH7 | Put away a received pallet | Device Put away flow | Exists, needs change (location shown and directed) | 1 / 3 scans, location not shown | 2 scans (container, shown location) | UXS-09 |
| WH6, WH4 | Pick an order (and pack and ship it) | Device Pick and Cluster pick flows; `/fulfillment/workbench` for the lead | Exists, needs change (directed; cart from assigned work) | Many; cart typed as text | Per line: 2 scans, 0 taps when the quantity matches | UXS-09 |
| WH5, WH9 | Count a location | `/inventory/count-sheets/:id` (web); device Count flow | Exists, needs change (batch save on web; directed on device) | 1 / 2 per line (a Save per line) | Web: 1 per line + 1 Save counts. Device: 3 per item (scan location once, scan item, enter count) | UXS-04, UXS-09 |
| WH3 | Check how much of a product is on hand | `/inventory/onhand`; device Look up | Web exists; device look-up has no stock data yet | 1 / 3 | Web 1 / 2; device 1 / 1 scan | UXS-09, UXS-02 |

Missing: directed count, move, pack and cluster need mobile service calls against the warehouse work API (`apps/warehouse-mobile/NOT-IMPLEMENTED.md`); device sign-in needs the edge-identity device flow.

## 3. AP clerk

Enters and matches vendor invoices, chases differences, prepares payments. Works fast with the keyboard; is measured on invoices per hour and on errors found later. Success means: invoice posted, matched, paid on time, and a clear answer when it does not match.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| AP1 | Post a vendor invoice that matches a receipt | `/ap-invoices/post` (Start from receipt or PO) | Exists, needs change | 1 / ~8 + 6 per line cold; ~4 via GR/IR | 1 / 5 (receipt, invoice number, invoice date, total, Post), any number of lines | UXS-05, UXS-06, UXS-16 |
| AP2 | Find out why an invoice did not match | `/ap-invoices/:id` (match panel and History) | Exists, needs change | 1-2 / 3 | 1 / 2 | UXS-03, UXS-07 |
| AP3 | See goods received not yet invoiced for one vendor | `/ap-invoices/grir` | Exists | 1 / 3 | 1 / 3 | UXS-04, UXS-02 |
| AP4 | Pay two chosen invoices in one payment | `/payables/open-items` (Pay selected); task starts at `/payables` | Exists, needs change (row selection, Pay selected) | Not possible directly (proposal takes every due invoice) | 2 / 5 (open items, vendor filter, tick two, Pay selected, confirm) | UXS-04 |
| AP6 | Find who a pending approval is waiting on | Record header; `/workflow/inbox` "Waiting on others" | Exists, needs change | Not answerable | 1 / 1 | UXS-13 |

Note: the audit names `/payables/new` for AP4; the real route is `/payables/proposals/new`.

## 4. AR clerk

Records customer payments, collects overdue amounts, handles disputes and deductions. Success means: cash applied the day it arrives and overdue customers contacted.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| AR1 | Record a payment against one invoice | `/receivables/payments/new` (record and apply on one screen) | Exists, needs change | 2 / ~10 | 1 / 6 (customer, amount, date, bank reference, suggested invoice ticked, Post) | UXS-05, UXS-06 |
| AR2 | Record a short payment and keep the rest open | `/receivables/payments/new` | Exists, needs change | 2 / ~12 | 1 / 7 | UXS-05 |
| AR3 | Find a customer's overdue items and send reminders | `/receivables/open-items` (Send reminders for selected) | Exists, needs change | 1 / 4 (no bulk send) | 1 / 3 to find, +2 to send to the selected | UXS-04 |
| AR4 | Note a promise to pay | `/receivables/worklist` | Exists | 1-2 / 4 | 1 / 4 | — |
| AR5 | Open a dispute on an invoice | `/receivables/disputes` | Exists, needs change (duty code in write-off copy; no history) | 1 / 5 | 1 / 4 | UXS-07, UXS-03, UXS-12 |

## 5. Buyer

Raises and follows purchase orders, works with vendors on dates and prices. Success means: the right goods ordered at the agreed price, and late orders seen early.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| BU1 | Raise a PO for 100 units from a known vendor | `/purchasing/purchase-orders/new` | Exists, needs change (line grid; long lines) | 1 / ~9 | 1 / 6 (vendor, product, quantity, Save; price and dates default) | UXS-05, UXS-06 |
| BU2 | Send the order for approval | `/purchasing/purchase-orders/:id` | Exists | 1 / 2 | 1 / 2, and the header shows who must approve | UXS-03, UXS-13 |
| BU3 | Change a line's delivery date for a vendor | `/purchasing/purchase-orders/:id` | Exists | 1 / 3 | 1 / 3 (+1 reason when the order is confirmed) | UXS-05, UXS-12 |
| BU4 | Find a vendor's orders still waiting for goods | `/purchasing/open-supply` | Exists, needs change (raw id filters) | 1 / 4 | 1 / 3 | UXS-02, UXS-04 |
| BU6 | Request quotes from three vendors | `/purchasing/rfqs` | Exists | 1-2 / ~8 | 1 / 6 (multi-select vendor lookup) | UXS-02 |

## 6. Order desk / sales rep

Takes orders by phone and email, answers "can you deliver" and "where is it". Success means: order entered while the customer is on the phone, with the right price and a promise that holds.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| SR1 | Take a phone order for two products | `/sales/orders/new` | Exists, needs change (line grid, currency from customer, expert fields under Advanced) | 2 / ~13 | 1 / 8 (customer, product and qty ×2, Confirm; +3 per further line) | UXS-05, UXS-06, UXS-16 |
| SR2 | Check whether 50 units can be promised by a date | `/sales/atp` | Exists | 1 / 3 | 1 / 3 | — |
| SR3 | Find why an order is on credit hold | `/sales/credit-holds` | Exists | 1 / 2 | 1 / 2 | UXS-07 |
| SR4 | Find where a customer's order is in delivery | `/sales/order-progress` | Exists, needs change (raw id filters) | 1 / 3 | 1 / 2 (order lookup) | UXS-02 |
| SR6 | Ship an order line directly from the vendor | `/sales/orders/:id` ("Ship from vendor" on the line) | Exists; action missing (today `/fulfillment/drop-ships`, eight UUID fields) | 1 / 12 fields | 0 new screens / 3 (line menu, vendor, confirm) | UXS-05, UXS-02 |

## 7. Controller / accountant

Owns the books: journals, reconciliations, close, statements, and the answers auditors ask for. Success means: a period closed on time with every number traceable to its source and its author.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| CT1 | Post a manual journal moving 1,000 between two accounts | `/ledger/journal-batches/new` | Exists, needs change (line grid, ledger defaulted) | 2-3 / ~10 | 1 / 6 (description, 2 lines × account and amount, Post; date and ledger default) | UXS-05, UXS-06 |
| CT2 | Set up a monthly recurring journal | `/ledger/recurring-journals` | Exists, needs change (no help) | 1 / ~8 | 1 / 6 | UXS-11, UXS-05 |
| CT3 | Check the trial balance balances for last month | `/ledger/trial-balance` | Exists | 1 / 3 | 1 / 3 | — |
| CT4 | Close last month, or name what blocks it | `/close` | **Missing**: the close service (28 paths) has no screen; planned until the close workspace ships | — | 1 / 3 to see blockers; +2 per module period closed (with reason where reopening) | UXS-01, UXS-12, UXS-18 |
| CT5 | Find the journal behind a number on a report and who posted it | `/finreport` → `/ledger/entries/:id` (History) | Exists, needs change (no History; codes in titles) | 2 / 4 | 2 / 3 | UXS-03, UXS-13, UXS-07 |

## 8. Production planner

Runs planning, firms and releases orders, balances load. Success means: the plan is current and the floor has the right jobs in the right order.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| PP1 | Run planning for the next four weeks | `/planning/runs/new` (progress tracked) | Exists, needs change (UUID scope field; no tracked progress) | 1 / 5 | 1 / 3 | UXS-02, UXS-18 |
| PP2 | Find what the plan says to order or make this week | `/planning/planned-orders` | Exists, needs change (no help) | 1 / 3 | 1 / 3 | UXS-11, UXS-04 |
| PP3 | Release planned orders as production orders | `/planning/planned-orders` (Release selected) | Exists, needs change (hand-rolled selection) | 1 / 2 | 1 / 2 (+1 per extra order selected) | UXS-04 |
| PP4 | Find which materials a production order is short of | `/production/material-requirements` | Exists | 1 / 3 | 1 / 3 | — |
| PP5 | Move a job to another day and see if the load still works | `/scheduling` | Exists, needs change (codes in hints) | 1 / 4 | 1 / 4 | UXS-07 |

## 9. Tenant admin

Sets up the company, people and access, and answers "who changed this". Success means: new entities and users set up correctly, access exactly as intended, and every change traceable.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| TA1 | Add a legal entity for a new subsidiary | `/org/legal-entities` | Exists | 1 / ~8 | 1 / 8 | UXS-06 |
| TA2 | Add a user who may approve POs but not post invoices | `/admin/security/users` → `/admin/security/assignments` | Exists, needs change (user shown as raw id; duty codes in hints) | 2-3 / ~10 | 2 / 8 (+ second-person approval for admin duties) | UXS-02, UXS-12 |
| TA3 | Find who changed a setting last week | `/admin/audit` | Exists, needs change (never fetches) | Not possible | 1 / 3 (record type, date range, open row) | UXS-13, UXS-01 |
| TA4 | Add a warehouse to a site | `/org/warehouses` | Exists, needs change ("Enter the id of the legal entity") | 1 / ~6 | 1 / 5 | UXS-02, UXS-06 |
| TA5 | Import products from a file and see what loaded | `/admin/import` → the real import (setup checklist and Data migration, as `docs/user/tenant-admin.md` describes) | Exists, needs change (stub: disabled "Coming soon") | Not possible | 1 / 5 (file, mapping accepted, preview, Import, read counts) | UXS-01, UXS-04 |

Also scripted: TA6, load and reset the demo company (`/setup/demo-company`, exists).

## 10. Customer portal user

A customer's buyer or accounts payable person. Uses Depot rarely, from outside, on a phone as often as a desktop. Has no training. Success means: reorder, pay and get documents without phoning.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| CP1 | Reorder products | `/orders/new` | Exists, needs change (native select, free-text unit) | 1 / ~6 | 1 / 7 (search, quantity and Add for each of two products, Place order) | UXS-17, UXS-02 |
| CP2 | Pay open invoices | `/open-items` → `/pay` → payment provider | Exists, needs change (unformatted amounts) | 3 / ~8 | 3 / 6 | UXS-07 |
| CP3 | Download an invoice PDF | `/invoices/:id` | Exists | 2 / 2 | 2 / 2 | — |
| CP4 | See where an order or shipment is | `/orders/:id` | Exists; shipment tracking not built (`apps/customer-portal/NOT-IMPLEMENTED.md`) | Not possible | 1 / 1 | Functional backlog (carrier tracking), UXS-07 |
| CP5 | Request a return | `/returns/new` | Exists, needs change (errors as `fieldKey: message`) | 1 / ~6 | 1 / 5 | UXS-08, UXS-17 |

Also scripted: CP0, the first visit from the invitation email to a first reorder (UXS-20). The customer portal has no invitation route; a `/welcome` first-run page is missing.

## 11. Supplier portal user

A vendor's order or accounts person, often working in a second language. Success means: orders acknowledged, shipments announced and invoices accepted without email back and forth.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| SP1 | Acknowledge a purchase order | `/purchase-orders/:id` | Exists | 1 / 3 | 1 / 2 | UXS-07 |
| SP2 | Send an advance ship notice | `/shipments/new` (started from the PO, lines prefilled) | Exists, needs change (packages, SSCC, serials missing: functional backlog) | 1 / ~6 | 1 / 5 | UXS-05, UXS-17 |
| SP3 | Submit an invoice and understand a review result | `/invoices`, `/invoices/received` | Exists, needs change (raw review reasons) | 1 / ~6 | 1 / 5 | UXS-07 |
| SP4 | Check a payment's status | `/payments` | Exists, needs change (ISO dates) | 1 / 2 | 1 / 2 | UXS-07 |
| SP5 | Request a change to company details | `/requests` | Exists, needs change (raw field-error keys) | 1 / 5 | 1 / 5 | UXS-08, UXS-17 |

Also scripted: SP0, accept the invitation (`/accept-invite`, exists) or register (`/register`, exists) and acknowledge a first purchase order (UXS-20).

## 12. Field technician

Visits customer sites to repair and service equipment. Works on a phone, outdoors, often without signal. Success means: today's visits done, parts and signature captured, nothing lost offline. The app has no URL routes; its screens are "My day" and a visit. In the demo build it is at `/technician`.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| TE1 | See today's visits in order | App "My day" | Exists | 1 / 0 | 1 / 0 | UXS-07 (header date) |
| TE2 | Start travel, then start work | Visit screen | Exists | 1 / 1 each | 1 / 1 each | — |
| TE3 | Record parts used from truck stock | Visit screen | Exists | 1 / 3 | 1 / 3 | — |
| TE4 | Capture the customer's signature and complete | Visit screen | Exists | 1 / 3 | 1 / 3 | — |
| TE5 | Work offline and have the work sent | Header status | Exists | 0 / 1 | 0 / 0 (automatic; 1 to force "Send now") | — |

The technician app meets its targets today. The work is the guide (UXS-11), task scripts and a baseline (UXS-19), CI for the app (it has no CI job), and the header date (UXS-07).

## 13. Master-data steward

Keeps parties, products, prices and bills of material right, and is the second person on sensitive changes (thesis A3, A7, U7). Works in bulk and from spreadsheets. Success means: one record per real-world party, prices and BOMs correct, and every sensitive change approved by a second person with a reason on file. Today figures: not estimated by the audit; Round 0 measures them.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| MD1 | Approve a vendor bank-account change a colleague requested | `/party/pending-changes` → `/party/pending-changes/:id` | Exists, needs change (pending-change banner, reason shown, self-approval blocked) | Round 0 | 2 / 2 (open the request, Approve after reading the masked diff and reason) | UXS-12, UXS-03 |
| MD2 | Merge a duplicate customer | `/party/duplicates` | Exists, needs change (reason, history on the survivor) | Round 0 | 2 / 4 (open the pair, choose the survivor, Merge, reason) | UXS-12, UXS-03 |
| MD3 | Update sales prices for 40 products from a spreadsheet | `/pricing/price-list` (Paste rows) | Exists, needs change (paste with preview, undo) | One edit per row (about 40 × 3) | 1 / 5 (Paste rows, paste, check the preview, Apply, reason) | UXS-04, UXS-12 |
| MD4 | Create a product and release it to a legal entity | `/product/items/new` → `/product/released` | Exists | Round 0 | 2 / 8 | UXS-06, UXS-02 |
| MD5 | Correct a BOM line quantity, with a reason | `/engineering/boms/:id/edit` | Exists, needs change (line grid, reason, history) | Round 0 | 1 / 4 | UXS-05, UXS-12, UXS-03 |

## 14. Approver / manager

Any manager who approves requisitions, purchase orders, invoices, absences or changes. Approving is not their job, so it must take seconds and show enough to decide well (thesis U8). Success means: decisions made the same day, with the context to make them, and a trail of who decided and why.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| AM1 | Approve a purchase requisition | `/workflow/inbox` (side panel with the document) or `/purchasing/approvals` | Exists | Round 0 | 1 / 2 (open, Approve) | UXS-13, UXS-03 |
| AM2 | Approve a purchase order over your limit, seeing why it needs you | `/workflow/inbox` → `/purchasing/purchase-orders/:id` | Exists, needs change (the header names the rule that routed it) | Round 0 | 2 / 2 | UXS-03, UXS-13 |
| AM3 | Send back an invoice with a reason | `/workflow/inbox` | Exists, needs change (ReasonDialog) | Round 0 | 1 / 3 (open, Send back, reason) | UXS-12 |
| AM4 | Approve four routine requests at once | `/workflow/inbox` (Approve selected) | Exists, needs change (selection) | Four separate approvals | 1 / 3 + 1 per item (select each, Approve selected, confirm) | UXS-04 |
| AM5 | See what you sent that is still waiting, and on whom | `/workflow/inbox` "Waiting on others" | Exists, needs change (tab missing) | Not answerable | 1 / 1 | UXS-13 |

## 15. Maintenance planner / technician lead

Plans preventive maintenance, turns requests into work orders and dispatches technicians (the field technician, persona 12, does the visits). Success means: assets kept running, work planned before it fails, and the right technician sent.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| MT1 | Turn a maintenance request into a work order | `/eam/requests` → `/eam/work-orders/new` (started from the request) | Exists, needs change (start from source) | Round 0 | 2 / 4 | UXS-05, UXS-02 |
| MT2 | Set up a preventive plan for an asset | `/eam/plans/new` | Exists | Round 0 | 1 / 6 | UXS-02, UXS-06 |
| MT3 | Dispatch today's service visits to technicians | `/service/dispatch` | Exists | Round 0 | 1 / 1 per visit (assign) | UXS-04 |
| MT4 | Find the asset that failed most this quarter | `/eam/kpis`, `/eam/assets` | Exists | Round 0 | 1 / 3 | UXS-04 |
| MT5 | Issue a work permit for a hazardous job | `/eam/permits/new` | Exists, needs change (step-up signature) | Round 0 | 1 / 5 | UXS-12 |

## 16. Auditor (read-only)

An internal auditor or an external auditor with time-limited access. Never changes a record. Success means: the change history of any record, its reasons and approvals, exported in a form that can be checked, without asking an administrator. This persona is how "auditable" is tested by the person who relies on it.

| Id | Task | Must be served by | Route status | Today S / I | Target S / I | Standards |
|---|---|---|---|---|---|---|
| AU1 | Export the change history of one vendor for the last year | `/party/:id` (Export history) | Exists, needs change (History panel, export) | Not possible | 1 / 2 | UXS-13, UXS-03 |
| AU2 | Confirm the export is complete and untampered | `/admin/audit` (verification result with the export) | Exists, needs change (never fetches) | Not possible | 1 / 2 | UXS-13 |
| AU3 | List every vendor bank-account change last quarter, with who approved each | `/admin/audit` (record type, field, date range) | Exists, needs change | Not possible | 1 / 4 | UXS-13, UXS-12 |
| AU4 | Trace a statement figure to its journal entry and who posted it | `/finreport` → `/ledger/entries/:id` | Exists, needs change (History) | 2 / 4 | 2 / 3 | UXS-03 |
| AU5 | See which record types the audit trail covers | `/admin/audit` (coverage) | Missing (needs AUD-API-5) | Not possible | 1 / 1 | UXS-03, UXS-13 |

---

## Secondary personas

These keep their task scripts in `apps/web/docs/usability-tasks.md` and their workspaces, but are not among the 16 designed-for personas above. Their tasks are in usability rounds when a release touches their paths.

| Persona | Workspace | Tasks | Notes |
|---|---|---|---|
| Shop-floor supervisor | `shopfloor-supervisor` | SF1 to SF5 | Gains station enrolment (`/shopfloor/stations`, missing) and overrides |
| Warehouse supervisor | `warehouse` | New WS1: sign out a worker from a lost device (`/mobile-flows/devices`, missing); WS2: resolve an offline conflict (`/mobile-flows/conflicts`, exists, no filters) | UX-044 |
| People and payroll administrator | `hr-admin` | HR1 to HR5 | Guide missing |
| Analyst / executive | Any, with dashboards and KPI pages | None yet | Covered by the general standards; a task script is added when a usability round asks for it |
| New user, any persona | Their workspace | NU1 to NU3: complete a guided flow (receive to stock, make to order, record to report) within 30 minutes with no Depot experience | UXS-20 |
| Implementation partner | `tenant-admin` | TA5, TA6 | Import and demo company |
