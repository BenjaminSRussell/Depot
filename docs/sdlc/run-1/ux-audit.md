# UX audit — Depot run 1

Commit: cba4ebeda6844cb61843b71247247c8909e19fea; date 2026-11-17; method: static code review + demo screenshots (demo build `vite build --mode demo`, served under `/Depot/`, Playwright Chromium at 1366x900 and 390x844 touch; screenshots and scripts in `ux-scratch/shots`, `ux-scratch/scripts`).

Scope read: `docs/PRD.md` §3, `docs/prd/part-00-thesis.md` (U1–U9, A7, S4), `docs/user/*.md`, `apps/web/docs/{design-system,forms,lists,ux-writing,usability-gate,usability-tasks,usability-baseline-report,accessibility-conformance-report}.md`, `apps/web/src/shell/**`, `apps/web/src/modules/**` (59 modules, 1,274 routes, 666 nav entries), `apps/customer-portal`, `apps/supplier-portal`, `apps/technician`, `apps/warehouse-mobile` (both thin hosts over `apps/web/src/modules/{technician,mobile-flows}`), and `services/*/api/openapi.yaml` for "API but no screen".

Click/screen counts are inferred from the code paths (fields that must be touched, buttons, navigations), assuming the work context (legal entity) is already set. They are estimates, not measurements; no usability session has ever been run (see UX-013).

## Summary (counts by severity and category)

50 findings: **16 High, 30 Medium, 4 Low**.

| Category | High | Med | Low | Total | IDs |
|---|---|---|---|---|---|
| Auditability for the user (history/audit) | 2 | 1 | 0 | 3 | 001, 002, 027 |
| Capabilities with an API but no screen; stub screens that show false data | 4 | 1 | 0 | 5 | 003, 004, 005, 006, 044 |
| Raw ids instead of names | 2 | 4 | 0 | 6 | 007, 008, 021, 030, 032, 033 |
| Jargon and error codes in user copy | 1 | 5 | 3 | 9 | 009, 019, 020, 022, 035, 037, 043, 045, 047 |
| Mobile/touch on floor apps | 2 | 1 | 0 | 3 | 010, 011, 031 |
| Accessibility | 1 | 2 | 0 | 3 | 012, 041, 042 |
| Forms: validation and defaults | 1 | 7 | 0 | 8 | 014, 018, 028, 029, 034, 036, 039, 040 |
| No bulk actions | 2 | 0 | 0 | 2 | 015, 016 |
| Keyboard paths | 0 | 1 | 0 | 1 | 050 |
| Inconsistent patterns between modules | 0 | 2 | 0 | 2 | 017, 048 |
| Navigation / IA | 0 | 2 | 0 | 2 | 025, 026 |
| Onboarding, help and usability programme | 1 | 4 | 1 | 6 | 013, 023, 024, 038, 046, 049 |

Each finding is counted once, under its main category.

The headline: the P2P/O2C core that was reworked under #497–#512 (lists, forms, error catalog, lookups, guided flows, help) is in good shape. Outside it the standards have not been applied: most lists and forms bypass the shared `ListPage`/form standard, hundreds of fields still ask for UUIDs, and several screens a persona's day depends on are placeholders (audit, import, sequences, job monitor, period close). The floor apps (kiosk, warehouse device) do not yet deliver the thesis U6 promise of a touch-first, directed, one-scan-per-step experience. The usability gate cannot work as written because there is no baseline.

## Per persona: top tasks and friction

Legend: S = screens, C = estimated clicks/keystroke-groups for the happy path.

### Shop-floor operator (no workspace; uses `/shopfloor/kiosk`, `/shopfloor/jobs`)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | Clock in at the start of a shift | `/shopfloor/kiosk` | 1 / 2 (scan, Clock in) | Kiosk renders inside the desktop shell with sidebar, legal-entity and site pickers and Compact view (UX-011); top bar overflows on a phone-size screen (UX-012). |
| 2 | Find and start my next job | `/shopfloor/jobs` → `/shopfloor/jobs/:id` | 2–3 / 5+ | Kiosk says "Clock in to start jobs." but links nowhere; dispatch list is a desktop page with order/operation refs and no product name or "my jobs" filter (UX-011, UX-030). |
| 3 | Report good and scrap quantity | `/shopfloor/jobs/:id` | 1 / 5 | Scrap reason is a free-text "reason code" (UX-029); validation only on submit (UX-040). |
| 4 | Raise an andon call / report downtime | `/shopfloor/andon`, `/shopfloor/downtimes` | 2 / 4+ | Not reachable from the kiosk; shown as supervisor screens in the shell. |
| 5 | Follow a PI sheet / work instruction | `/shopfloor/kiosk/pi/:id` | 1–2 / n | Needs the id in the URL; no path from the kiosk session to "my PI sheet". No help page (UX-023), no guide (UX-024). |

### Warehouse worker (`warehouse` workspace; device app `apps/warehouse-mobile`)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | Receive a PO delivery (WH1/WH2) | `/receiving/receipts/new` → `/receiving/receipts/:id` (register) → post + confirm | 3 / ~12 for one line, +5 per extra line | Three screens; arrival form asks currency (ISO 4217), over-delivery %, batch/serial tracked per line, picks one PO line at a time; no "receive all open lines" (UX-028). |
| 2 | Pick, pack and ship (WH4) | `/fulfillment/workbench`, device `PICK`, `PACK` flows | 2–4 / many | Device prompts "Scan the pick location" without saying which location, item or quantity; cluster cart needs typed `workId,orderId,LP` lines; no sign-in (UX-010); "LP" jargon (UX-043). |
| 3 | Check on-hand (WH3) | `/inventory/onhand` | 1 / 3 | Desktop page; not in device inquiry flow ("the inquiry step has no stock to show", warehouse-mobile NOT-IMPLEMENTED). |
| 4 | Count a shelf (WH5) | `/inventory/count-sheets/:id` | 1 / 2 per line | One Save button per line, small (`size="sm"`), no scan-to-line, no save-all (UX-031). |
| 5 | Put away | device `RECEIVE_PUTAWAY` flow | 1 / 3 scans | Not directed: the location to scan is not shown (UX-010). |

### AP clerk (`ap-clerk` workspace; guide `docs/user/ap-clerk.md`)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | Post a vendor invoice that matches a receipt (AP1) | `/ap-invoices/post` (task start) or `/ap-invoices/grir` → post | 1 / ~8 + 6 per line cold; ~4 via GR/IR | Cold start: receipt, typed receipt line number, qty, price and a "standard cost" checkbox per line, typed currency, lines as stacked fieldsets; header copy "clears GR/IR… PPV" (UX-014, UX-047). |
| 2 | Find why an invoice did not match (AP2) | `/ap-invoices/:id` | 1–2 / 3 | No history panel on the invoice (UX-002). |
| 3 | GR/IR open items for a vendor (AP3) | `/ap-invoices/grir` | 1 / 3 | Works; selection limited to this page. |
| 4 | Pay two invoices in one payment (AP4) | `/payables` → `/payables/new` | 2 / 6 | Proposal takes every due invoice; only per-vendor exclusion; cannot pick two invoices (UX-015, UX-016). |
| 5 | Who is an approval waiting on (AP6) | `/workflow/inbox` | 1 / — | Inbox shows only work waiting for you; no "waiting on others" view (UX-027). |

### AR clerk (`ar-clerk` workspace; guide `docs/user/ar-clerk.md`)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | Record a payment (AR1) | `/receivables/payments/new` → `/receivables/payments/:id` (apply) | 2 / ~10 | Two screens (record, then match/apply). Form follows the standard (error summary, unsaved guard). Currency defaults to USD (UX-034). |
| 2 | Short payment, keep remainder open (AR2) | same | 2 / ~12 | Per-row Apply amounts; OK. |
| 3 | Overdue open items for a customer (AR3) | `/receivables/open-items` | 1 / 4 | No bulk "send reminder / statement for selected" (UX-016). |
| 4 | Note a promise to pay (AR4) | `/receivables/worklist` | 1–2 / 4 | Fine. |
| 5 | Open a dispute (AR5) | `/receivables/disputes` | 1 / 5 | Write-off copy shows raw duty `receivables.write-off.approve` (UX-020). No record history on items (UX-002). |

### Buyer (`buyer` workspace; guide `docs/user/buyer.md`)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | Raise a PO for 100 units (BU1) | `/purchasing/purchase-orders/new` | 1 / ~9 | Up to 15 fields per line visible (procurement category, price unit, discount, tax amount, site/warehouse, agreement line) with defaults; OK but long; lines as fieldsets not a grid (UX-050). |
| 2 | Submit for approval (BU2) | PO detail | 1 / 2 | Fine; approver visible on PO. |
| 3 | Change a line delivery date (BU3) | PO detail | 1 / 3 | Version history exists on PO (good). |
| 4 | Open orders waiting for goods (BU4) | `/purchasing/purchase-orders`, `/purchasing/open-supply` | 1 / 4 | `open-supply` filters are raw "Warehouse id / Site id / Product id" inputs (`purchasing/OpenSupplyPage.tsx:25-27`, UX-008). |
| 5 | Request quotes from three vendors (BU6) | `/purchasing/rfqs` | 1–2 / ~8 | Fine. |

### Order desk / sales rep (`sales-rep` workspace; guide `docs/user/order-desk.md`)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | Phone order for two products (SR1) | `/sales/orders/new` → order page Confirm | 2 / ~13 | Price fills on product pick (good). Currency defaults to USD regardless of customer (UX-034); "Order type: Legal entity default / Journal", "Invoice account" unexplained (UX-047); override hint shows `sales.order.approve` (UX-020). |
| 2 | ATP check (SR2) | `/sales/atp` | 1 / 3 | Fine. |
| 3 | Why on credit hold (SR3) | `/sales/credit-holds` | 1 / 2 | Fine. |
| 4 | Where is the order (SR4) | `/sales/order-progress` | 1 / 3 | Filters are raw text "Legal entity" and "Order" inputs validated as UUIDs (`sales/OrderProgressPages.tsx:172-175`, UX-008). |
| 5 | Drop-ship a line | `/fulfillment/drop-ships` | 1 / 12 fields | Eight UUID fields (UX-007). |

### Controller / accountant (`controller` workspace; no guide)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | Post a manual journal (CT1) | `/ledger/journal-batches/new` → batch → post | 2–3 / ~10 | Copy "Enter the id of the ledger" next to a picker (UX-033). |
| 2 | Recurring journal (CT2) | `/ledger/recurring-journals/new` | 1 / ~8 | No help (UX-023). |
| 3 | Trial balance check (CT3) | `/ledger/trial-balance` | 1 / 3 | Fine. |
| 4 | Close last month (CT4) | `/close` (task script) | — | Route does not exist; the close service (28 paths) has no screen (UX-003). |
| 5 | Who changed a posting / drill to journal (CT5) | `/finreport`, `/finreport/audit-trail` | 2 / 4 | Audit trail exists for ledger events only; titles carry codes "(REPORT-0010)" (UX-019); no entity history elsewhere (UX-002). |

### Production planner (`production-planner` workspace; no guide)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | Run planning (PP1) | `/planning/runs/new` | 1 / 5 | Scope field "Item ids — UUIDs separated by commas or spaces" (UX-032); the run's progress is not tracked in the job monitor, which is fake (UX-006). |
| 2 | What to order/make this week (PP2) | `/planning/planned-orders` | 1 / 3 | No help (UX-023). |
| 3 | Release a planned order (PP3) | `/planning/planned-orders` | 1 / 2 | Firming has bulk handling of its own (one of 12 hand-rolled selections, UX-016). |
| 4 | Shortages for a production order (PP4) | `/production/material-requirements` | 1 / 3 | Fine. |
| 5 | Move a job and check load (PP5) | `/scheduling` | 1 / 4 | Scheduling hints cite codes "SCHED-0012", "SCHED-0007" (UX-019). |

### Tenant admin (`tenant-admin` workspace; guide `docs/user/tenant-admin.md`)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | Add a legal entity (TA1) | `/org/legal-entities` | 1 / ~8 | Fine. |
| 2 | Add a user with exact permissions (TA2) | `/admin/security/users`, `/admin/security/assignments` | 2–3 / ~10 | Assignments list shows the user as a raw id (`RoleAssignmentsPage.tsx:172`); duty field hint asks for `identity.security-role.maintain`-style names (UX-021). |
| 3 | Who changed a setting (TA3) | `/admin/audit` | 1 / — | Always "No audit entries yet": the page never fetches (UX-001). |
| 4 | Add a warehouse to a site (TA4) | `/org/warehouses` | 1 / ~6 | Error "Enter the id of the legal entity the warehouse belongs to." (`fulfillment/WarehouseSettingsPage.tsx:70`, UX-033). |
| 5 | Import products from a file (TA5) | `/admin/import` | 1 / — | Import button disabled, "Coming soon"; only counts CSV columns (UX-004). Number sequences screen is a static empty state (UX-005). |

### Customer portal user (`apps/customer-portal`)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | Place a reorder | `/orders/new` | 1 / ~6 | Products in a native `<select>`, no search or images; unit typed free text for new products (UX-039). |
| 2 | Pay invoices | `/open-items` → `/pay` → Stripe | 3 / ~8 | Multi-select to pay exists (good). Amounts unformatted "1234.5 USD" (UX-035). |
| 3 | Download an invoice PDF | `/invoices/:id` | 2 / 2 | Fine. |
| 4 | Track an order / shipment | `/orders/:id` | 1 / — | Shipment tracking not built (customer-portal NOT-IMPLEMENTED). |
| 5 | Request a return | `/returns/new` | 1 / ~6 | Errors appear as "fieldKey: message" in one banner (UX-036). Statuses are lower-cased enums (UX-035). |

### Supplier portal user (`apps/supplier-portal`)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | Acknowledge a PO | `/purchase-orders` | 1 / 3 | English only (UX-038). |
| 2 | Send an ASN | `/shipments` | 1 / ~6 | No packages, SSCC or serials (NOT-IMPLEMENTED). |
| 3 | Submit an invoice | `/invoices`, `/invoices/received` | 1 / ~6 | Review reasons shown raw: `LOW_CONFIDENCE, TOTAL_MISMATCH, NO_VENDOR_TAX_ID` (UX-037). |
| 4 | Check payment status | `/payments` | 1 / 2 | ISO dates (UX-035). |
| 5 | Request a company details change | `/requests` | 1 / 5 | Field errors as raw keys (UX-036). |

### Field technician (`apps/technician`, also `/technician` in the demo)

| # | Task | Route(s) | S / C | Friction |
|---|---|---|---|---|
| 1 | See today's route | My day | 1 / 0 | Good: plain, offline, large targets, no overflow at 390px. Header date is ISO "2026-11-17" (UX-045). |
| 2 | Start travel / start work | visit | 1 / 1 each | Good. |
| 3 | Record parts used | visit | 1 / 3 | Good (truck stock shown). |
| 4 | Capture signature and complete | visit | 1 / 3 | Blockers explained in plain words (good). |
| 5 | Work offline and sync | header | 0 / 1 | Good; queue count shown. No guide (UX-024), no usability tasks (UX-013). |

The technician app is the best-executed floor experience in the repo and is a good model for the kiosk and the warehouse device.

## Findings

| ID | Severity | Persona(s) | Category | Where (route + file:line) | Problem | Recommendation | Effort |
|---|---|---|---|---|---|---|---|
| UX-001 | High | Tenant admin, controller, auditors | Auditability | `/admin/audit` — `apps/web/src/modules/admin/module.ts:17`, `admin/pages.tsx:59-99` | The Audit page renders `AuditViewer` with its default `items = []` and never calls the audit service, so it always says "No audit entries yet". The User column is the raw `userId`, Time is the raw ISO string. The audit service exposes `GET /v1/audit/records/{entity}/{recordId}`, `/v1/audit/reads`, `/v1/audit/alerts` (services/audit), and nothing in `apps/` calls `/v1/audit`. Usability task TA3 cannot pass. Confirmed in the demo screenshot `_admin_audit.png`. | Wire the page to the audit API with filters (entity, record, user, date range), names via `UserName`, `formatDateTime`, a link to the record. Until then hide the nav entry instead of showing a false empty state. | M |
| UX-002 | High | All clerks, controller, tenant admin | Auditability | Detail pages: `ap-invoices/InvoiceDetailPage.tsx`, `party/PartyDetailPage.tsx`, product, ledger entry and most of the 106 `*Detail*` files; only 22 mention history | There is no shared "History" panel. PO (`purchasing/PurchaseOrderPages.tsx:1370`) and sales order (`sales/OrderDetailPage.tsx:378`) have bespoke version histories; the vendor/customer record (bank accounts, credit limit), AP invoice, product and GL entry pages show no "who changed what, when, why". Thesis A7 promises field-level audit for every entity. | Build one `<RecordHistory entity id>` component on `/v1/audit/records/{entity}/{recordId}` (who, when, field, old → new, reason, workflow link) and put it in the detail-page template; add a consistency test that every `*DetailPage` renders it. | M |
| UX-003 | High | Controller | Capability with an API but no screen | Task CT4 `/close` (`apps/web/docs/usability-tasks.md`, Controller) — no such route; `services/close/api/openapi.yaml` (28 paths: periods, module status, reopen requests, checklist runs/tasks, year-end, accruals, allocations, regroupings) has zero references in `apps/web/src` | Period close, the close checklist and reopen approvals have no UI. Thesis S4 ("continuous close… checklist tasks auto-complete") is invisible to the controller; the task script's start page is a 404. | Build a Close workspace: period × module status grid, checklist run with live task state and owners, reopen request/approve, year-end run with progress. Add the route to the controller workspace. | L |
| UX-004 | High | Tenant admin, implementers | Stub screen | `/admin/import` — `admin/pages.tsx:191-240` | "Import / export" only counts CSV columns; the Import button is disabled with a "Coming soon" badge; the textarea is prefilled with `title,body / Hello,World`. The tenant-admin guide describes a different import (setup checklist + Data migration). TA5 cannot pass from its start page. | Point `/admin/import` at the real import wizard (or remove it from nav and workspaces); one import entry with preview, per-row errors and loaded/failed counts. | M |
| UX-005 | High | Tenant admin, controller | Stub screen (false empty state) | `/admin/sequences`, `/admin/alerts`, `/admin/documents`, `/admin/texts` — `admin/pages.tsx:124-160, 244-266` | Static empty states regardless of data. Sequences says "No sequences set up yet" while `GET /api/v1/numbering/sequences` returns sequences and the setup checklist links here as the fix (`setup/checks.ts:35`). `docs/user/tenant-admin.md:54` tells admins to "see and change" sequences here. | Wire to the numbering service (list, create, next value) or hide the entries. Rule: a page never shows an empty state it did not get from a fetch. | M |
| UX-006 | High | Approvers, planners, admins | Stub screen (fake data) | `/workflow`, `/workflow/jobs`, `/workflow/designer`, `/workflow/templates` — `workflow/pages.tsx:5-108` | Hardcoded sample data ships in production builds: Job monitor shows `rows-1m … 400000 / 1000000 … 2 min`; the designer is a static three-card list; templates are three hardcoded codes. Thesis U4 (tracked long-running work with progress and ETA) is not delivered; users see invented job status. | Wire Job monitor to the batch service with links back to the originating record; remove sample data from non-demo builds; hide designer/templates until real. | M |
| UX-007 | High | Order desk, warehouse | Raw ids | `/fulfillment/drop-ships` — `fulfillment/DropShipsPage.tsx:41-52` | The create form asks for eight UUIDs (legal entity, order, order line, customer, product, vendor, PO, PO line) with "Enter the order id as a UUID." Screenshot `_fulfillment_drop-ships.png`. The array-defined labels escape `rawIdInputs.test.ts`. | Start a drop-ship from the sales order line ("Ship from vendor") with everything prefilled; any remaining field uses `EntityLookup`. | M |
| UX-008 | High | Order desk, buyer, cost accountant, planner | Raw ids | 60 files with `<Input value={…Id}>`, e.g. `sales/DeliveryTolerancesPage.tsx:261-268`, `sales/OrderProgressPages.tsx:172-175`, `sales/ChangePolicyPage.tsx:164-167`, `sales/AllocationQuotasPage.tsx:122-128`, `costing/AdjustmentsPage.tsx:288-307`, `purchasing/OpenSupplyPage.tsx:25-27` | Labels were renamed from "… id" to "Legal entity", "Product", "Customer", but the control is still a monospace text box validated as a UUID. `shell/lookup/rawIdInputs.test.ts` only matches labels ending in " id", so these, array-defined labels and "Item ids" pass. | Replace with `EntityLookup`/`LookupFormField`. Change the guard to flag any `UUID.test` validator on a plain `Input`, not label text. | M |
| UX-009 | High | All | Jargon | 547 user-visible strings mention "UUID" in about 200 module files; top: "Enter the legal entity id (a UUID)." ×69, "Enter the legal entity's UUID." ×41, "Enter the item id as a UUID." ×34, "UUID of the customer party." ×7 | Business users are told to type or paste UUIDs. `ux-writing.md` allows ids only "until a picker replaces the field"; lookups exist for these entities. | Add "UUID" to `plainLanguageRules.ts`; rewrite messages to "Choose a …"; convert the fields (UX-008). | M |
| UX-010 | High | Warehouse worker | Mobile/touch (floor) | `apps/warehouse-mobile/src/main.tsx:29`; `modules/mobile-flows/domain/coreFlows.ts:16-35, 47-51`; `mobile-flows/ClusterPanel.tsx:122-128`; `apps/warehouse-mobile/NOT-IMPLEMENTED.md` | Device flows are not directed: the pick step says "Scan the pick location" without showing which location, item or quantity ("they do not read the expected work line, location or quantity", NOT-IMPLEMENTED). The cluster cart is built by typing `workId,orderId,LP` lines. No sign-in; the user defaults to `"unassigned"`. Thesis U6 "one scan completes a step" is not met. | Drive each step from the work API: big location / item / quantity, scan confirms, quantity defaults to expected. Device sign-in. Cart built from assigned work. | L |
| UX-011 | High | Shop-floor operator | Mobile/touch (floor), IA | `/shopfloor/kiosk` — `shopfloor/KioskPage.tsx:107-160`, mounted under `<Shell>` in `src/main.tsx:33-44`; screenshot `m_shopfloor_kiosk.png` | The kiosk is a page inside the desktop shell: hamburger, legal-entity and site pickers, Help and Compact view on a shared device. It only clocks in/out; "Clock in to start jobs." (line 135) has no link. Job reporting is the desktop `/shopfloor/jobs/:id` page; andon and downtime are supervisor screens. No operator workspace exists. | Full-screen kiosk route outside `Shell`: badge → my jobs → start/stop → good/scrap with reason buttons → andon, each one tap; auto sign-out. Add an operator workspace and task scripts. | L |
| UX-012 | High | Everyone on phones/tablets, floor | Accessibility, mobile | Every shell page — `shell/TopBar.tsx:54`, `shell/Shell.module.css:326` | At 390 CSS px the document is 481 px wide: the top-bar actions (site picker, Help trigger at right=480) are off-screen and need sideways scrolling, measured on `/shopfloor/kiosk`, `/receiving/receipts/new`, `/shopfloor/jobs`, `/inventory/count-sheets`, `/fulfillment/workbench`. WCAG 1.4.10 failure; the ACR lists 1.4.10 as "Partially supports" for other reasons only. | Below 640 px collapse the work context into one button/sheet; add a 390 px no-horizontal-scroll Playwright check to `e2e/a11y.spec.ts`. | S |
| UX-013 | High | All personas | Onboarding / usability programme | `apps/web/docs/usability-baseline-report.md` (template only), `usability-gate.md`, `usability-tasks.md` | No baseline round has ever been recorded, so the gate's "not lower than the reference" checks compare against nothing and pass by default. Task scripts cover 10 personas but not the shop-floor operator, technician, customer-portal or supplier-portal user; CT4's start route `/close` does not exist. | Run the baseline round on this commit; add task scripts for the missing personas; add a test that every task's route exists in the route table. | M |
| UX-014 | High | AP clerk | Forms / defaults | `/ap-invoices/post` — `ap-invoices/PostInvoicePage.tsx:62-67, 216-262` | Started cold (the AP1 start page) the clerk picks a receipt per line, types the receipt line number, quantity, price and ticks "standard cost" (the system knows the item's costing method); types the currency; lines are stacked fieldsets labelled "Line N …". The ledger error is "Enter a UUID." The description reads "clears GR/IR … to PPV for standard-cost lines". Prefill only exists when arriving from GR/IR. Thesis U1/U2. | "Start from receipt or PO" on the page that loads all open lines into a grid with quantity and price defaulted; drop the standard-cost checkbox (derive it); currency from vendor; plain description with `<Term>`. | M |
| UX-015 | High | AP clerk | No bulk actions | `/payables/new` — `payables/NewProposalPage.tsx:110-140` | A payment proposal "selects every vendor invoice that is due"; the only control is excluding whole vendors. Paying two chosen invoices (AP4) is not possible directly. | "Pay selected" from vendor open items with row selection; proposal editable line by line. | M |
| UX-016 | High | AP, AR, buyer, warehouse, planner | No bulk actions | `shell/ui/DataTable.tsx`, `shell/ui/ListPage.tsx` (no selection API); 12 modules hand-roll selection (`ap-invoices/GrirPage.tsx`, `planning/Firming.tsx`, `ledger/OpenItemsPage.tsx` …) | The shared list has no row selection, bulk action bar, multi-row inline edit, paste-from-spreadsheet or undo. Thesis U7 ("every list page supports multi-row inline edit, paste … and undo") is unmet. | Add a selection model and bulk-action slot to `ListPage`; a paste-into-grid editor with validation preview; replace the 12 hand-rolled versions. | L |
| UX-017 | Med | All | Inconsistent patterns | 674 module files render `DataTable` directly vs 85 using `ListPage` | Outside the ten converted P2P/O2C lists, lists lack the standard filter chips, saved views, column chooser, CSV export, search, no-access state and Load more; each module has its own filter bar ("Filter", "Show jobs", "Apply filters"). | Convert lists in persona-workspace order (rollout checklist in `lists.md`); add a lint/test that new list pages use `ListPage`. | L |
| UX-018 | Med | All | Forms | 713 module files contain `<form>`; 57 render `FormErrorSummary`/`useUnsavedChanges`; `shell/ui/consistency.test.ts` checks P2P/O2C `New…Page` only | Most forms (manufacturing, warehouse, ledger, HR, admin) have no error summary and no unsaved-changes guard; work is lost on navigation. | Extend the consistency test to all modules with an allowlist that shrinks; codemod the common patterns. | M |
| UX-019 | Med | Controller, planner, warehouse supervisor, transport | Jargon / error codes | `finreport/RunPage.tsx:92` "(REPORT-0010)"; `transport/TenderDetailPage.tsx:119` "(TMS-0106)"; `transport/ScreeningHoldDetailPage.tsx:105`, `ScreeningHoldsPage.tsx:36` "(TMS-0133)"; `warehouse/SetupPages.tsx:418` "(WMS-0101)"; `scheduling/SetupMatricesPage.tsx:70`, `CampaignDetailPage.tsx:433`, `ParametersPage.tsx:146`; `mobile-flows/PrintJobsPage.tsx:37`, `PrinterStatusPage.tsx:24`, `RoutingRulesPage.tsx:50,65`, `OfflineConflictsPage.tsx:33`, `format.tsx:49-50`, `DeviceApp.tsx:249`, `ClusterPanel.tsx:102` | Error codes appear in titles, page descriptions, table cells and device notices, against `ux-writing.md`. `shell/plainLanguageRules.ts` only bans FR/BR/AC ids and waves, so nothing catches them. | Extend the rule to `\b[A-Z]{2,6}-\d{3,4}\b` in user strings (allowed only under ProblemMessage Details); rewrite the copy to say what happened and what to do. | S |
| UX-020 | Med | Order desk, AR clerk, supervisors | Jargon | `sales/NewOrderPage.tsx:226`, `sales/QuotationsPage.tsx:291`, `QuotationDetailPage.tsx:241`, `QuotationMaintenance.tsx:184`, `receivables/Deductions.tsx:541`, `sales/DeliveryTolerancesPage.tsx:512`, `sales/ChangeRequestDetailPage.tsx:174`, `shopfloor/AndonSettingsPage.tsx:173`, `EdgeNodeDetailPage.tsx:123` | Raw duty codes ("Needs the sales.order.approve duty") shown to users, while production uses plain names ("the View production costs duty"). | Duty display-name map and a `dutyName()` helper used by every "needs the … duty" message; test bans dotted duty codes in user text outside admin screens. | S |
| UX-021 | Med | Tenant admin, order desk, supervisors, HR | Raw ids | 149 columns `{ key: "…Id", header: …, mono: true }` with no name render, e.g. `admin/security/RoleAssignmentsPage.tsx:172` (User), `admin/pages.tsx:71`, `shopfloor/MachinesPage.tsx:12`, `sales/AlternativeItemsPage.tsx:131,288-289`, `sales/SupplementaryRulesPage.tsx:215-216`, `sales/ProposalRulesPage.tsx:87-88`, `hr/WorkerDetailPage.tsx:32`, `projects/pages.tsx:121`, `planning/Firming.tsx:102` | Tables show UUIDs where people expect names. | Render `EntityName`/`UserName`; add a test that flags `key: "*Id"` columns without `render`. | M |
| UX-022 | Low | Supervisors, controller | Jargon (raw enums) | `production/JobsPage.tsx:38`, `production/SchedulePanel.tsx:34`, `intercompany/ReconRunsPage.tsx:120`, `intercompany/ReconciliationPage.tsx:149`, `ledger/AdvancedRulesPage.tsx:39`, `ledger/DerivationStrategyPage.tsx:215`, `productcost/VersionsPage.tsx:186` | Badges print the raw status value ("finished", "started", "draft"). | `StatusPill` + module `statusLabel`. | S |
| UX-023 | Med | Operator, warehouse, controller, planner, HR, admin | Onboarding / help | `shell/help/routeHelp.ts` (151 entries, 236 patterns: purchasing, sales, receivables, fulfillment, billing, AP, payables, receiving only); 1,274 routes; `shell/help/HelpPanel.tsx:10` | The Help panel shows "There is no help for this page yet." on manufacturing, warehouse, ledger, close, shop floor, planning, HR and admin pages. The guide link goes to a GitHub `blob/main` URL, which a customer may not be able to open. | Generate default help from route metadata (page description + primary actions) for every route, then hand-write the top-5 tasks per persona; serve guides in-app. | M |
| UX-024 | Med | Operator, warehouse, controller, planner, technician, portal users, HR | Onboarding | `docs/user/` has guides for AP clerk, AR clerk, buyer, order desk and tenant admin only | Seven personas have no role guide. | Write a guide per persona, anchored to the task scripts. | M |
| UX-025 | Med | New users, all | Navigation / IA | `/features` (`shell/pages/AllFeaturesPage.tsx`), `shell/workspaces/navAudit.json` (666 entries: 381 keep, 250 advanced, 35 merge; finance 189), `workspaces.json` | All features lists "666 features"; Finance alone has 189 entries. Workspaces are good, but there is none for the shop-floor operator or maintenance/field roles, and "Sales rep or order desk" is one workspace. The workspace choice lives in `localStorage` only. | Add operator and maintenance workspaces; cut the "keep" set per area; make home task-first per workspace; store workspace choice server-side. | M |
| UX-026 | Med | All list users | Navigation / personalization | `apps/web/docs/lists.md` (Views), `shell/preferenceStorage.ts` | Saved views live in browser storage per person: not shareable, not role-defaulted, lost on a new device. Thesis U3 promises shareable, role-defaulted, versioned views. | Implement `PreferenceStorage` over a server API (the seam exists); add Share and Role default. | M |
| UX-027 | Med | AP clerk, buyer, approvers | Auditability / workflow | `/workflow/inbox` — `workflow/InboxPage.tsx:184` ("Work waiting for you") | Only your own pending work is listed; there is no "waiting on others / sent by me" view, so AP6 ("who is this waiting on") must be answered elsewhere, if at all. | Add a "Waiting on others" tab and show the current approver on every document header. | S |
| UX-028 | Med | Warehouse worker | Forms / defaults | `/receiving/receipts/new` — `receiving/NewArrivalPage.tsx:69, 252, 276-301`; `receiving/ReceiptPage.tsx:101-113` | Receiving takes three screens (record arrival, register quantity/warehouse/location/status per line, post + confirm). Arrival asks currency ("ISO 4217, such as USD"), over-delivery percent and batch/serial tracked per line although the PO and product hold them, and picks one PO line at a time. | One receive screen from the PO: all open lines, quantity defaulted to open quantity, location from put-away rules, post in the same step; master-data flags hidden. | M |
| UX-029 | Med | Shop-floor operator | Forms | `/shopfloor/jobs/:id` — `shopfloor/JobDetailPage.tsx:403-406` | Scrap reason is a free-text input "Reason code, needed when scrap is reported." The operator has to know the codes. | Select from the downtime/scrap reason master, rendered as large buttons on the kiosk. | S |
| UX-030 | Med | Operator, supervisor | Raw ids | `/shopfloor/jobs` — `shopfloor/JobsPage.tsx:31-37, 79-101` | Dispatch list shows Order and Operation refs in monospace, no product or operation description, and no "my jobs" filter for the worker clocked in. | Add product name and operation description; default filter to the worker's resource/bundle. | S |
| UX-031 | Med | Warehouse worker | Mobile/touch | `/inventory/count-sheets/:id` — `inventory/CountSheetPage.tsx:52-61` | Counts are saved line by line with small (`size="sm"`) Save buttons; no scan-to-line and no "save all". A 50-line sheet means 50 saves on a tablet. | Count flow on the device (scan location, scan item, enter count); batch save on the web page. | M |
| UX-032 | Med | Planner | Raw ids | `/planning/runs/new` — `planning/RunPlanPage.tsx:378` | "Item ids — UUIDs separated by commas or spaces" to scope a planning run. | Multi-select product lookup with paste of item numbers. | S |
| UX-033 | Med | Controller, tenant admin | Raw ids / copy | `ledger/JournalBatchPage.tsx:235`; `fulfillment/WarehouseSettingsPage.tsx:70`; 11 files with "Enter the id of …" | "Enter the id of the ledger the batch posts to." sits next to a ledger picker (CT1); "Enter the id of the legal entity the warehouse belongs to." on TA4. | Default the ledger from the legal entity; copy "Choose a ledger"; same for the other ten. | S |
| UX-034 | Med | Order desk, AP, AR, warehouse | Forms / defaults | 80 module forms default `currency: "USD"`, e.g. `sales/NewOrderPage.tsx:107`, `receiving/NewArrivalPage.tsx:69`, `ap-invoices/PostInvoicePage.tsx:56`; free-text 3-letter code | Currency does not follow the customer, vendor, PO or legal entity; the demo company has three legal entities. Wrong-currency documents are easy to create. | Default from the party/PO/legal entity; currency `Select` or lookup; warn when it differs from the party's. | S |
| UX-035 | Med | Customer and supplier portal users | Jargon / formatting | `apps/customer-portal/src/ui.tsx:82-87`, `apps/supplier-portal/src/ui.tsx:82-87` | Status badges are lower-cased enums; dates are `iso.slice(0,10)`; money is `${amount} ${currency}` with no grouping or minor units ("1234.5 USD"). | Intl date/number formatting and status label maps, shared with the web app. | S |
| UX-036 | Med | Portal users | Forms (validation) | `apps/supplier-portal/src/ui.tsx:33-37`, `apps/customer-portal/src/ui.tsx:33` | Server field errors are joined into one notice as `camelKey: message` instead of being shown at the field. | Map keys to labels and render inline errors with a summary, as in the web form standard. | S |
| UX-037 | Med | Supplier portal user | Jargon | `apps/supplier-portal/src/pages/Invoices.tsx:30` | Review reasons render raw: `LOW_CONFIDENCE, TOTAL_MISMATCH, NO_VENDOR_TAX_ID` (enum in `services/receiving/api/openapi.yaml:1880`). | Plain sentences with what the supplier should do (for example "The total does not match the lines; check and resend"). | S |
| UX-038 | Med | Supplier portal user | Onboarding / i18n | `apps/supplier-portal/NOT-IMPLEMENTED.md` | English only (strings inline); ASN has no packages, SSCC or serial entry. Foreign suppliers are the main audience. | Extract strings and ship the planned four languages; add package/serial entry. | M |
| UX-039 | Med | Customer portal user | Forms | `/orders/new` — `apps/customer-portal/src/pages/NewOrder.tsx:77-91` | Products are chosen from a native select (no search, images or catalog); for products not ordered before the unit is typed ("such as EA"); no shipment tracking. | Searchable product combobox with images; unit select from the listing; tracking when the carrier API lands. | M |
| UX-040 | Med | All | Forms (validation) | `shell/ui` form standard (`forms.md` "Inline validation", `useSubmitAttempt`) | Field errors appear after submit only. Thesis U5 promises "errors shown at the field as you type". | Validate format rules on blur; keep server rules on submit. | M |
| UX-041 | Med | All, especially clerks with long forms | Accessibility | ACR 2.2.1 (`accessibility-conformance-report.md`) | The session can end with no warning; drafts are lost. | Warn two minutes before expiry with "Stay signed in". | S |
| UX-042 | Med | All | Accessibility | `apps/web/e2e/flowRoutes.ts` (114 routes) and `e2e/a11y.spec.ts` (shell routes, WCAG 2.1) vs 1,274 routes | Automated WCAG 2.2 scanning covers P2P/O2C only; manufacturing, warehouse, ledger, HR and admin pages are unscanned, so issues like UX-012 go unnoticed. | Scan every workspace entry route at 1366 and 390 px with the 2.2 tags. | M |
| UX-043 | Low | Warehouse worker | Jargon | `mobile-flows/domain/coreFlows.ts:16,47`; `mobile-flows/DeviceApp.tsx:115, 435` | Prompts say "license plate"/"cart position LP"; the screen header reads "menu" in lower case and flow names are derived from codes ("pick cluster"). | Human labels in flow metadata; spell out terms per the glossary. | S |
| UX-044 | Med | Warehouse supervisor | Capability with an API but no screen | `apps/warehouse-mobile/NOT-IMPLEMENTED.md` (device management row); `/mobile-flows/conflicts` | Devices, sessions, force sign-out and the stale sweep are served by `services/mobile` with no web screen; the offline conflict queue has no filters. | Device administration page; filters on the conflict queue. | M |
| UX-045 | Low | Technician | Jargon / formatting | `modules/technician/TechnicianApp.tsx:46` | Header shows "Jordan Avery, 2026-11-17" (ISO date). | `formatDate` with weekday. | S |
| UX-046 | Low | AP clerk, warehouse | Help (doc drift) | `docs/user/ap-clerk.md:21`; `docs/user/tenant-admin.md:54` | Guides tell users to fill in a "location ID" (the UI has a picker) and to change sequences on a page that is a placeholder. | Fix the guides; add a docs check in the release checklist. | S |
| UX-047 | Med | Order desk, AP clerk | Jargon | `sales/NewOrderPage.tsx` (Order type: "Legal entity default", "Journal"; "Invoice account"); `ap-invoices/PostInvoicePage.tsx:216` ("GR/IR", "PPV", "AP period"); `<Term>` used 42 times in modules | Accounting and AX-style terms appear on clerk forms without explanation. | Wrap domain terms in `<Term>`, move expert fields (order type, invoice account) under Advanced, rewrite page descriptions in task language. | S |
| UX-048 | Med | Portal users | Inconsistent patterns | `apps/customer-portal/src/ui.tsx`, `apps/supplier-portal/src/ui.tsx` | Each portal re-implements Page/Field/Badge/State with its own styles, no error catalog and no design tokens; they diverge from the web app and from each other. | Publish `shell/ui` tokens, primitives, `ProblemMessage` and formatters as a shared package used by the portals. | L |
| UX-049 | Med | New users, trainers | Onboarding | Home demo tour copy ("most screens are empty in the demo"); `usability-tasks.md` "A one-click demo company and reset are pending issue #503" | No realistic training company covers most personas, which blocks the thesis target "a new user completes a guided task in < 30 min" and the usability sessions themselves. Guided flows exist only for P2P and O2C (`shell/documents/GuidedFlow.tsx`). | Ship the #503 demo company with data for every workspace's top 5 tasks; add guided flows for receive-to-stock, make-to-order and record-to-report. | M |
| UX-050 | Med | Order desk, buyer, AP clerk, controller | Keyboard paths | `sales/LinesEditor.tsx:33` ("one fieldset per line"), `ap-invoices/PostInvoicePage.tsx:229-262`; app-wide shortcuts only in `shell/Shell.tsx:203` (Ctrl+K), `planning/Firming.tsx:162`, `org/HierarchyDraftEditor.tsx:373` | Document lines are stacked fieldsets, so a 5-line order is 30+ Tab stops; there is no grid with arrow-key movement, no "add line" or "save" shortcut. Thesis U1 promises line grids with inline edit. | A shared keyboard line grid (arrows, Enter to add a line, Ctrl+S to save, Ctrl+Enter to submit), used by order, PO, invoice and journal forms. | L |

## Cross-cutting recommendations (design system/pattern-level fixes that remove many findings at once)

1. **No false screens.** Add a rule and a test: a page that shows an empty state must have fetched; a module with no data source is hidden from nav, workspaces and the palette (use `NotAvailablePage` "isn't available yet" if it must be linked). Removes UX-001, 004, 005, 006 as user-visible lies on day one, then wire them properly. (S, then M per page.)
2. **Lookups everywhere, enforced by behaviour not by label.** Replace `rawIdInputs.test.ts` with a check for UUID validators on plain `Input`s and for `key: "*Id"` columns without a name render; add "UUID", "id (a UUID)" and dotted duty codes to `plainLanguageRules.ts`. Convert with `EntityLookup`/`EntityName`/`UserName`, which already exist. Removes UX-007, 008, 009, 021, 030, 032, 033 and part of 020. (M)
3. **One record-detail template with History.** A `DetailPage` scaffold with header (status, current approver, actions), body, and a `RecordHistory` panel on `/v1/audit/records/{entity}/{recordId}`; make the audit page a cross-entity view of the same data. Removes UX-001, 002, 027 and delivers thesis A7 to users. (M)
4. **Finish the ListPage and form-standard rollout, with selection.** Extend `ListPage` with row selection, a bulk-action slot and paste-into-grid; extend `consistency.test.ts` from P2P/O2C to every module with a shrinking allowlist. Removes UX-015, 016, 017, 018 and gives every list views/export/states. (L)
5. **A shared keyboard line grid** for document lines (orders, POs, invoices, journals, receipts) with defaults pulled from the source document ("start from PO/receipt/order"). Removes UX-014, 028, 050 and is the actual thesis U1 deliverable. (L)
6. **Context defaults service.** One hook that returns legal entity, site, ledger, currency and party defaults (vendor currency, customer currency, entity ledger) and is used by every form instead of literals like `"USD"`. Removes UX-034 and parts of 014, 028, 033. (S–M)
7. **Floor-app shell.** A chromeless, full-screen layout for kiosk and device flows (no sidebar, no work-context pickers, 48 px+ targets, auto sign-out), driven by the work API so each step shows what to do and a scan confirms it. Model it on the technician app, which already gets this right. Removes UX-010, 011, 029, 031, 043 and part of 012. (L)
8. **Responsive top bar.** Collapse work context and secondary actions below 640 px; add a 390 px no-horizontal-scroll assertion to the a11y suite, run across all workspace routes. Removes UX-012 and widens UX-042 coverage. (S)
9. **Error-code and enum hygiene in one place.** Extend the plain-language test to problem codes (`[A-Z]{2,6}-\d{3,4}`) and upper-snake enums in rendered strings; route all status text through `StatusPill`/label maps; publish the formatters and `ProblemMessage` to the portals. Removes UX-019, 022, 035, 036, 037 and part of 048. (S–M)
10. **Help and guides generated, then curated.** Generate a default help entry for every route from its page title/description and primary actions so no page says "no help yet"; host guides in-app; write guides and task scripts for the seven uncovered personas. Removes UX-023, 024, 046 and supports UX-013. (M)
11. **Make the usability gate real.** Run the baseline round now (with the #503 demo company), add task scripts for operator, technician and portal users, and add a test that every task's start route exists. Without this the gate passes vacuously and "end-user friendly" is not measurable. Addresses UX-013 and UX-049. (M)
12. **Close the API-without-screen gap for daily work first:** period close (UX-003), audit (UX-001), job monitor/batch (UX-006), number sequences (UX-005), warehouse device admin (UX-044). Track remaining service paths without a client in a generated report alongside `navAudit.json`. (L overall)
