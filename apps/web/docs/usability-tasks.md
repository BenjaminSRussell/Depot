# Usability task scripts

Extended by docs/ux/ (UXS-19); where they differ, docs/ux wins.

The tasks a moderator gives to a participant in a usability session (`usability-baseline-report.md` records the results; `usability-gate.md` says how they gate a release). There is one workspace per persona, defined in `src/shell/workspaces/workspaces.json`. Each persona has the tasks that person does on a normal day, in the order they usually happen.

## Running a session

- **Participants** come from the target market: five per persona, none of them developers or people who built the screens. Someone who does the job today (or did within the last two years) is the best fit.
- **Environment.** The test environment for each persona, how it starts and how it is reset are in `docs/ux/usability-programme.md` section 1. Reset the data before every participant so everyone starts from the same state.
- **Moderation.** Read the scenario aloud and hand over the starting page only. Don't name menu items or buttons. Ask the participant to think aloud. Don't help; if they are stuck for 3 minutes, note the time, say "Let's move on", and score the task as failed.
- **Record** for each task: success (see the rule below), time on task in seconds, the number of errors (wrong turns that needed backing out, rejected submits, messages the participant said they didn't understand) and anything they said.
- **Success** is one of three values. *Pass:* the participant reached the stated result alone. *Pass with help:* only after a hint from the moderator. *Fail:* gave up, ran out of time, or finished with a wrong result (for example a wrong amount or the wrong record).
- **Time limit** is 5 minutes per task unless the task says otherwise.
- After the tasks, ask the participant to fill in the System Usability Scale (SUS) questionnaire (see `usability-baseline-report.md`).

## Tasks

Each row gives the scenario, where the participant starts, and what counts as done. "Starts at" is the page title as it appears in the navigation and "Route" is its address; the moderator opens that page; "Done when" is checked against the data, not against what the participant says.

### Accounts payable clerk (`ap-clerk`)

| # | Scenario | Starts at | Route | Done when |
|---|---|---|---|---|
| AP1 | A vendor invoice arrived for goods you already received. Post it so it matches the receipt. | Post vendor invoice | `/ap-invoices/post` | The invoice is posted, matched to the receipt, and the posted amount equals the invoice total. |
| AP2 | The invoice total is higher than the received value. Find out why it didn't match. | Post vendor invoice | `/ap-invoices/post` | The participant names the difference (quantity or price) and where it is shown. |
| AP3 | Find the goods received but not yet invoiced for one vendor. | GR/IR open items | `/ap-invoices/grir` | The correct list for that vendor is on screen and the participant can name its total. |
| AP4 | Pay two posted invoices for one vendor in a single payment. | Payment runs | `/payables` | One payment exists that covers both invoices. |
| AP5 | A vendor says an invoice is unpaid. Check its status. | Open items | `/ledger/open-items` | The participant states the right status and, if paid, the payment date. |
| AP6 | Find who a pending approval is waiting on. | Inbox | `/workflow/inbox` | The participant names the approver. |

### Accounts receivable clerk (`ar-clerk`)

| # | Scenario | Starts at | Route | Done when |
|---|---|---|---|---|
| AR1 | A customer paid a single invoice by bank transfer. Record the payment. | Record payment | `/receivables/payments/new` | The payment is applied and the invoice shows as paid. |
| AR2 | A customer paid less than the invoice. Record it so the rest stays open. | Record payment | `/receivables/payments/new` | The payment is applied and the remaining amount is open. |
| AR3 | Find all open items for one customer that are overdue. | Open items | `/receivables/open-items` | The correct list is on screen, and the participant names the total overdue. |
| AR4 | A customer promised to pay next Friday. Note it. | Collections worklist | `/receivables/worklist` | A payment promise with the right date exists. |
| AR5 | A customer disputes an invoice. Open a dispute. | Disputes | `/receivables/disputes` | A dispute is recorded against that invoice. |

### Buyer (`buyer`)

| # | Scenario | Starts at | Route | Done when |
|---|---|---|---|---|
| BU1 | Raise a purchase order for 100 units of a product from a known vendor. | Purchase orders | `/purchasing/purchase-orders` | The order exists with the right vendor, product, quantity and price. |
| BU2 | Send the order for approval. | Purchase orders | `/purchasing/purchase-orders` | The order is submitted and shows who has to approve it. |
| BU3 | A vendor asks for a later delivery date on a line. Change it. | Purchase orders | `/purchasing/purchase-orders` | The line shows the new date. |
| BU4 | Find which orders for one vendor are still waiting for goods. | Purchase orders | `/purchasing/purchase-orders` | The participant names the open orders and quantities. |
| BU5 | Approve a requisition and turn it into an order. | Requisition approvals | `/purchasing/approvals` | An order exists that came from the requisition. |
| BU6 | Request quotes for a product from three vendors. | Requests for quotation | `/purchasing/rfqs` | A request for quotation exists with three vendors. |

### Receiving and warehouse (`warehouse`)

| # | Scenario | Starts at | Route | Done when |
|---|---|---|---|---|
| WH1 | A delivery arrived against a purchase order. Receive it. | Record arrival | `/receiving/receipts/new` | A receipt is posted with the right quantities. |
| WH2 | Only part of the order arrived. Receive what came. | Record arrival | `/receiving/receipts/new` | The receipt shows the partial quantity and the order still shows the rest open. |
| WH3 | Check how much of a product is on hand at your warehouse. | On-hand | `/inventory/onhand` | The participant states the right quantity. |
| WH4 | Pick, pack and ship a customer order. | Picking workbench | `/fulfillment/workbench` | A shipment is created and marked shipped. |
| WH5 | Count a shelf and enter the counts. | Count sheets | `/inventory/count-sheets` | The counts are saved and any difference is visible. |

### Sales representative (`sales-rep`)

| # | Scenario | Starts at | Route | Done when |
|---|---|---|---|---|
| SR1 | Take a phone order from a customer for two products. | New order | `/sales/orders/new` | The order exists with the right customer, products and quantities. |
| SR2 | Check whether you can promise 50 units by a date. | ATP check | `/sales/atp` | The participant states yes or no and the earliest date. |
| SR3 | The order went on credit hold. Find out why. | Credit release queue | `/sales/credit-holds` | The participant names the reason and the amount over the limit. |
| SR4 | Find where a customer's order is in delivery. | Deliveries and invoices | `/sales/order-progress` | The participant states the right stage. |
| SR5 | Give a customer a quote for a product. | Quotations | `/sales/quotations` | A quotation exists with the price the price list gives. |

### Production planner (`production-planner`)

| # | Scenario | Starts at | Route | Done when |
|---|---|---|---|---|
| PP1 | Run planning for the next four weeks. | Run plan | `/planning/runs/new` | A run completes and its results are visible. |
| PP2 | Find the products the plan says to order or make this week. | Planned orders | `/planning/planned-orders` | The participant names the products and quantities. |
| PP3 | Release a planned order as a production order. | Planned orders | `/planning/planned-orders` | A production order exists from that planned order. |
| PP4 | Find which materials a production order is short of. | Material requirements | `/production/material-requirements` | The participant names the short materials. |
| PP5 | Move a job to a different day and see whether the schedule still works. | Gantt | `/scheduling` | The job is on the new day and the participant states whether the load is feasible. |

### Shop floor supervisor (`shopfloor-supervisor`)

| # | Scenario | Starts at | Route | Done when |
|---|---|---|---|---|
| SF1 | See who is clocked in right now. | Who is clocked in | `/shopfloor/clocked-in` | The participant states the right names or count. |
| SF2 | Find the machine that has been down the longest today. | Machines | `/shopfloor/machines` | The participant names the machine and the downtime. |
| SF3 | Respond to an andon call. | Andon board | `/shopfloor/andon` | The call is acknowledged and closed with a reason. |
| SF4 | Fix a worker's time entry that has the wrong end time. | Time corrections | `/shopfloor/corrections` | The correction is saved with the right time. |
| SF5 | Check how a production order is doing against plan. | Production orders | `/production` | The participant states progress and the variance. |

### Controller (`controller`)

| # | Scenario | Starts at | Route | Done when |
|---|---|---|---|---|
| CT1 | Post a manual journal that moves 1,000 between two accounts. | Journal batches | `/ledger/journal-batches` | The batch is posted and balanced. |
| CT2 | Set up a monthly recurring journal. | Recurring journals | `/ledger/recurring-journals` | The recurring journal exists with the right schedule. |
| CT3 | Check that the trial balance balances for last month. | Trial balance | `/ledger/trial-balance` | The participant states the totals and that they are equal. |
| CT4 | Close last month. | Period close (planned until B-25) | `/close` | The period shows closed, or the participant names what blocks it. |
| CT5 | Find the journal behind a number on a report. | Statements | `/finreport` | The participant reaches the journal entry. |
| CT6 | Run the depreciation for last month. | Depreciation runs | `/assets/depreciation-runs` | A run exists for the month. |

### People and payroll administrator (`hr-admin`)

| # | Scenario | Starts at | Route | Done when |
|---|---|---|---|---|
| HR1 | Hire a new employee. | Workers | `/hr/workers` | The employee exists with the right start date and position. |
| HR2 | Approve an absence request. | Absence requests | `/hr/absence/requests` | The request shows approved. |
| HR3 | Change an employee's job title. | Workers | `/hr/workers` | The title is changed with an effective date. |
| HR4 | Find certifications that expire in the next 60 days. | Expiring certifications | `/hr/certifications/expiring` | The participant names the right people. |
| HR5 | Open a position to hire for. | Requisitions | `/hr/requisitions` | A requisition exists for the position. |

### Administrator (`tenant-admin`)

| # | Scenario | Starts at | Route | Done when |
|---|---|---|---|---|
| TA1 | Add a legal entity for a new subsidiary. | Legal entities | `/org/legal-entities` | The entity exists with the right country and currency. |
| TA2 | Add a new user who can approve purchase orders but not post invoices. | Users | `/admin/security/users` | The user exists with exactly those permissions. |
| TA3 | Find who changed a setting last week. | Audit | `/admin/audit` | The participant names the person and the change. |
| TA4 | Add a warehouse to a site. | Warehouses | `/org/warehouses` | The warehouse exists under the right site. |
| TA5 | Import a list of products from a file. | Import / export | `/admin/import` | The import finishes and the participant can state how many rows loaded and how many failed. |

### Shop-floor operator (`operator`)

The kiosk starts at `/shopfloor/kiosk` today; after rollout slice 8 it moves to `/kiosk`.

| # | Scenario | Starts at | Route | Done when | Budget screens / interactions |
|---|---|---|---|---|---|
| OP0 | (First use, new badge.) Sign in on the kiosk for the first time and start your first job. | Kiosk | `/shopfloor/kiosk` | The worker is signed in and a job is started, without moderator help | 1 / 3 |
| OP1 | Your shift starts. Clock in. | Kiosk | `/shopfloor/kiosk` | A clock-in exists for the participant's worker at the session time | 1 / 2 |
| OP2 | Start the next job planned for your work cell. | Kiosk | `/shopfloor/kiosk` | The earliest planned job for the cell is started for the worker | 1 / 2 |
| OP3 | You made 12 good pieces and 2 scrap because of a material defect. Report it. | Kiosk, job running | `/shopfloor/kiosk` | Good 12 and scrap 2 with reason "Material defect" are recorded on the job | 1 / 4 |
| OP4 | Your machine stopped because it jammed. Tell the system and call for help. | Kiosk | `/shopfloor/kiosk` | A downtime with reason Jam is open on the machine, and an andon call exists | 1 / 3 |
| OP5 | Open the work instructions for the job you are running. | Kiosk, job running | `/shopfloor/kiosk/pi/:id` | The instruction for the running operation is on screen | 2 / 1 |
| OP6 | Your shift is over. Clock out so the next person can sign in. | Kiosk | `/shopfloor/kiosk` | A clock-out exists and the kiosk shows the sign-in screen | 1 / 3 |

### Warehouse worker, device tasks (`warehouse`)

The handheld device app opens at `/device` (development stub only today). WH1 to WH5 keep their routes.

| # | Scenario | Starts at | Route | Done when | Budget screens / interactions |
|---|---|---|---|---|---|
| WH6 | Pick the next order you are given. | Device menu | `/device` | Every line of the assigned pick is confirmed with the right quantity and batch | 2 scans per line, 0 taps when quantities match |
| WH7 | Put away the pallet you just received. | Device menu | `/device` | The pallet is at the location the system directed | 2 scans |
| WH8 | A delivery for PO-1042 has arrived. Receive it on the device. | Device menu | `/device` | A receipt with the delivered quantities exists | 1 document scan + 1 scan per line |
| WH9 | Count location A-01-02. | Device menu | `/device` | A count for every item at the location is saved | 3 per item |

### Additions to existing personas

WS1 starts on a planned route (`/mobile-flows/devices`, issue #703) and counts as not measurable until it ships.

| # | Scenario | Starts at | Route | Done when | Budget screens / interactions |
|---|---|---|---|---|---|
| SR6 | Ship line 2 of this order directly from the vendor. | Sales order | `/sales/orders/:id` | A drop-ship PO exists for the line with the customer's address | 0 new / 3 |
| TA6 | Load the demo company, then reset it. | Demo company | `/setup/demo-company` | The page reports records created, then removed and created again | 1 / 3 |
| WS1 | A handheld was lost. Sign its worker out of it. | Devices | `/mobile-flows/devices` | The session is ended | 1 / 3 |
| WS2 | Resolve the offline conflict from this morning. | Offline conflicts | `/mobile-flows/conflicts` | The conflict is reapplied or rejected with a reason | 1 / 3 |

### Field technician (`technician`)

| # | Scenario | Starts at | Route | Done when | Budget screens / interactions |
|---|---|---|---|---|---|
| TE1 | Tell the moderator which customer you visit first today and at what time. | My day | `/technician` | The participant names the first visit and time | 1 / 0 |
| TE2 | You are leaving for the first visit. Record that, then record that you started work on arrival. | My day | `/technician` | Travel start and work start are recorded on the visit | 1 / 2 |
| TE3 | You used one seal kit from your truck. Record it. | Visit | `/technician` | One seal kit is recorded on the visit and truck stock is one lower | 1 / 3 |
| TE4 | The work is done. Get the customer's signature and complete the visit. | Visit | `/technician` | The visit is complete with a signature | 1 / 3 |
| TE5 | You have no signal (airplane mode on). Complete TE3 again for the second visit, then turn signal back on. | Visit, offline | `/technician` | The part is recorded on the server after reconnecting, without the participant re-entering it | 0 extra |

### Customer portal user (`customer-portal`)

The customer portal is its own app (`apps/customer-portal`); its demo build answers in the browser.

| # | Scenario | Starts at | Route | Done when | Budget screens / interactions |
|---|---|---|---|---|---|
| CP0 | (First visit.) Open the invitation email, sign in and reorder 5 seal kits. | Invitation email | `/` | An order exists and the participant reached it without help | 2 / 7 |
| CP1 | Reorder 10 seal kits and 2 booster pumps. | New order | `/orders/new` | An order with those products and quantities exists | 1 / 7 |
| CP2 | Pay your two oldest open invoices by card. | Open items | `/open-items` | One payment covers both invoices | 3 / 6 |
| CP3 | Download the PDF of your last invoice. | Invoices | `/invoices` | The PDF is downloaded | 2 / 2 |
| CP4 | Find out when your last order will arrive. | Orders | `/orders` | The participant states the delivery status and date (fails until tracking is built) | 1 / 1 |
| CP5 | One pump arrived damaged. Ask to return it. | Returns | `/returns/new` | A return request for that line exists with the reason | 1 / 5 |

### Supplier portal user (`supplier-portal`)

The supplier portal demo build (`vite build --mode demo`) answers the session and the purchase orders; the other tasks stay not measurable there until their data exists.

| # | Scenario | Starts at | Route | Done when | Budget screens / interactions |
|---|---|---|---|---|---|
| SP0 | (First visit.) Accept the invitation, set up your account and confirm your first purchase order. | Invitation link | `/accept-invite` | The account is active and the PO shows acknowledged | 3 / 6 |
| SP1 | A new purchase order arrived. Confirm you will deliver it. | Purchase orders | `/purchase-orders` | The PO shows acknowledged | 1 / 2 |
| SP2 | You shipped the order today. Tell the customer what is coming. | Purchase orders | `/purchase-orders/:id` | An ASN for that PO with the shipped quantities exists | 1 / 5 |
| SP3 | Send your invoice for the shipped order. Then find out why it was held for review and what to do. | Invoices | `/invoices` | The invoice is submitted; the participant states the reason and the fix in their own words | 1 / 5 |
| SP4 | Check whether your March invoice has been paid. | Payments | `/payments` | The participant states paid or not and the date | 1 / 2 |
| SP5 | Your bank account changed. Ask the customer to update it. | Requests | `/requests` | A change request with the new details exists | 1 / 5 |

### Master-data steward (`master-data`)

| # | Scenario | Starts at | Route | Done when | Budget screens / interactions |
|---|---|---|---|---|---|
| MD1 | A colleague changed Contoso's bank account. Check it and approve it if it looks right. | Pending changes | `/party/pending-changes` | The change is approved by the participant; the participant names the old and new account (last 4) and the reason | 2 / 2 |
| MD2 | Two customer records are the same company. Merge them. | Duplicates | `/party/duplicates` | One customer remains with both records' history | 2 / 4 |
| MD3 | Update the sales prices in this spreadsheet (40 products). | Price list | `/pricing/price-list` | All 40 prices are updated with a reason; nothing else changed | 1 / 5 |
| MD4 | Create a new product and make it available to the German company. | Products | `/product/items` | The product exists and is released to Cedar Ridge Pumps Europe GmbH | 2 / 8 |
| MD5 | A BOM line should use 4 bolts, not 6. Correct it. | Bills of material | `/engineering/boms/:id` | The line shows 4 with a reason in History | 1 / 4 |

### Approver (`approver`)

| # | Scenario | Starts at | Route | Done when | Budget screens / interactions |
|---|---|---|---|---|---|
| AM1 | Approve the requisition waiting for you. | Inbox | `/workflow/inbox` | The requisition is approved | 1 / 2 |
| AM2 | A purchase order is waiting for you. Find out why it needs your approval, then approve it. | Inbox | `/workflow/inbox` | The participant names the rule (amount over limit) and the order is approved | 2 / 2 |
| AM3 | Send back the invoice waiting for you because the price is wrong. | Inbox | `/workflow/inbox` | The invoice is sent back with a reason | 1 / 3 |
| AM4 | Approve the four routine requests in your inbox at once. | Inbox | `/workflow/inbox` | All four are approved | 1 / 7 |
| AM5 | Find which of the things you sent are still waiting, and on whom. | Inbox | `/workflow/inbox` | The participant names each item and its approver | 1 / 1 |

### Maintenance planner (`maintenance`)

| # | Scenario | Starts at | Route | Done when | Budget screens / interactions |
|---|---|---|---|---|---|
| MT1 | Turn the request about the leaking pump into a work order. | Maintenance requests | `/eam/requests` | A work order exists that came from the request | 2 / 4 |
| MT2 | Set up quarterly maintenance for press 4. | Maintenance plans | `/eam/plans` | A plan with a quarterly schedule exists for the asset | 1 / 6 |
| MT3 | Send today's two unassigned visits to Jordan Avery. | Dispatch | `/service/dispatch` | Both visits are assigned to Jordan Avery | 1 / 2 |
| MT4 | Find the asset that failed most this quarter. | Maintenance KPIs | `/eam/kpis` | The participant names the asset | 1 / 3 |
| MT5 | Issue a hot-work permit for tomorrow's welding job. | Permits | `/eam/permits` | A signed permit exists for the job | 1 / 5 |

### Auditor (`auditor`, Auditor role only)

| # | Scenario | Starts at | Route | Done when | Budget screens / interactions |
|---|---|---|---|---|---|
| AU1 | Export the change history of vendor Contoso Supplies for the last year. | Vendor | `/party/:id` | A file with the vendor's changes, names and reasons is downloaded | 1 / 2 |
| AU2 | Confirm the exported history is complete and untampered. | Audit | `/admin/audit` | The participant states the verification result shown with the export | 1 / 2 |
| AU3 | List every vendor bank-account change last quarter and who approved each. | Audit | `/admin/audit` | The participant names the changes and approvers | 1 / 4 |
| AU4 | Trace the cost-of-goods figure on last month's statement to a journal entry and the person who posted it. | Statements | `/finreport` | The participant names the entry and the person | 2 / 3 |
| AU5 | Tell the moderator which record types the audit trail does not cover yet. | Audit | `/admin/audit` | The participant reads the coverage list correctly | 1 / 1 |

### New users and guided flows (`new-user`)

Participants have never used Depot. Time limit 30 minutes per task.

| # | Scenario | Starts at | Route | Done when | Budget screens / interactions |
|---|---|---|---|---|---|
| NU1 | (Warehouse.) A delivery for PO-1042 has arrived. Get it received and put away. | Home | `/` | The goods are on hand at their put-away location | 30 minutes |
| NU2 | (Order desk and planner.) This customer order needs a pump we make to order. Take it through to production. | Home | `/` | A production order exists for the order line | 30 minutes |
| NU3 | (Controller.) Get last month to a trial balance you can sign off. | Home | `/` | The participant reaches the trial balance with no open blocker, or names the blocker | 30 minutes |

Each task in `e2e/taskBudgets.json` has its budget there; `e2e/task-readiness.demo.spec.ts` lists the tasks the demo build cannot start (planned routes, other apps) in its run output.

## Keeping the scripts current

- When a screen on a task's path changes name or route, update the "Starts at" column the same week.
- Add a task when a persona gains a daily job. Don't remove a task that has a baseline; retire it by marking it "retired" in the baseline report so its trend is kept.
- Task ids (AP1, BU2, ...) never change once a baseline exists.
