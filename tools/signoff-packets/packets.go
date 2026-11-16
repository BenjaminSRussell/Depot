package main

// packets are the review workbooks, in the order of the #512 table. Item names the #512 row (docs/signoff/README.md).
var packets = []Packet{
	{
		Name: "statements", Title: "Statement pack on the demo fiscal year", Item: "#512 row 1: #15 finance sign-off on the statement pack",
		Sources: []string{statementsSource},
		Checks: []string{
			"The trial balance totals 0.00 at every period end (Trial balance, Total row; Reconciliation).",
			"The balance sheet balances: assets equal liabilities, equity and the year's result (Reconciliation).",
			"Net income is revenue less cost of sales and operating expenses, year to date (Statements).",
			"The indirect cash flow (net income, non-cash adjustments, working capital, investing, financing) equals the change in cash in every period (Statements, Check column).",
			"After the closing entries in period 12 the revenue and expense accounts are 0.00 and the result sits in retained earnings (Trial balance, P12).",
		},
		Build: buildStatements,
	},
	{
		Name: "costing", Title: "Inventory costing reference: 104 sequences under 7 methods", Item: "#512 row 2: #20 costing reference workbook",
		Sources: []string{costingSource},
		Checks: []string{
			"Each issue's value is right for FIFO, LIFO, LIFO by date, both weighted averages, moving average and standard cost (Expected).",
			"The stock left at the end has the right quantity and value under each method (Expected, On hand rows).",
			"Per method, receipts less issues less stock on hand is 0.00, except standard cost, where it is the purchase price variance (Reconciliation).",
			"Each sequence's note says what it exercises; the expected values agree with the note (Sequences, Expected).",
		},
		Build: buildCosting,
	},
	{
		Name: "leases", Title: "IFRS 16 lease schedules for 25 leases", Item: "#512 row 3: #37 IFRS 16 lease schedule workbook",
		Sources: []string{leasesSource},
		Checks: []string{
			"The initial liability is the present value of the payments at the incremental borrowing rate, in arrears or in advance (Leases).",
			"Each period's interest is the opening liability times the period rate, and the liability ends at 0.00 (Schedule, Reconciliation).",
			"Right-of-use depreciation is straight line over the term and adds up to the asset (Reconciliation).",
			"Payments less interest equal the initial liability (Reconciliation).",
		},
		Build: buildLeases,
	},
	{
		Name: "poc", Title: "Percentage-of-completion revenue", Item: "#512 row 4: #41 percentage-of-completion revenue workbook",
		Sources: []string{pocWorkedSource, pocEstimatesSource},
		Checks: []string{
			"Percentage of completion is cost to date over estimate at completion (cost to date plus estimate to complete) (Estimates expected).",
			"Revenue to date is the contract value times the percentage, and this period's revenue is the change from the prior period (Reconciliation).",
			"Revenue to date above billing is accrued revenue, below billing deferred revenue (Reconciliation).",
			"A loss-making contract provides for the whole expected loss at once, and the provision moves with the estimate (Loss provision columns).",
		},
		Build: buildPOC,
	},
	{
		Name: "asc606", Title: "ASC 606 allocation and schedules for 40 bundles", Item: "#512 row 5: #29 ASC 606 workbook for the 40 bundle scenarios (and the P2 workbook review)",
		Sources: []string{asc606Source},
		Checks: []string{
			"The transaction price is allocated by relative SSP, with the residual approach where an SSP is residual (Obligations).",
			"Point-in-time obligations are recognized on the satisfied date, milestones when reached, ratable ones daily or monthly over the term (Schedules).",
			"Allocations and schedules add up to the transaction price, and recognized plus deferred revenue is the price at the as-of date (Reconciliation).",
			"The two refused scenarios are refused for the right reason (Scenarios, Expected refusal).",
		},
		Build: buildASC606,
	},
	{
		Name: "tax", Title: "Tax calculation matrix: 500 scenarios", Item: "#512 row 6: #13 accountant review of the 500-scenario tax matrix",
		Sources: []string{taxMatrixSource, taxExpectedSource},
		Checks: []string{
			"The rates of each stack are the published US state, county and city, EU VAT, India GST and Canada GST, HST and PST rates (Scenarios).",
			"Tax on a price excluding tax is base times rate; a compound tax is charged on the base plus the earlier taxes (Expected sheets).",
			"Per-document rounding rounds the document's tax once and spreads it over the lines; per-line rounding rounds each line (Expected sheets).",
			"A price including tax is split so base plus tax is the gross amount (Reconciliation, last column must be 0).",
			"A reverse charge books output and input tax of the same amount and charges the buyer nothing (Legs column 2).",
		},
		Build: buildTax,
	},
	{
		Name: "ebics", Title: "EBICS 2.5 return codes and their handling", Item: "#512 row 16: #36 review of the 13 EBICS return codes against the EBICS 2.5 annex",
		Sources: []string{ebicsSource, ebicsCodeSource},
		Checks: []string{
			"Each code and annex name matches the EBICS 2.5 specification annex (Return codes; copy the annex text into Dispositions).",
			"The disposition of each code is right: carry on, treat as no data, back off and retry, or park for a person (Dispositions).",
			"Parking any code the client does not name is acceptable (Dispositions, last row).",
		},
		Build: buildEBICS,
	},
	{
		Name: "consolidation", Title: "Consolidation of group 3 (interim fixture)", Item: "#512 row 18: #40 P2 sign-off and P4",
		Sources: []string{consolidationSource},
		Checks: []string{
			"E1: intercompany sales and cost are eliminated at the average rate (Expected, Workings).",
			"E2: receivable and payable are eliminated, the exchange difference goes to profit or loss (IAS 21.45) (Expected).",
			"E3: unrealised profit in inventory is eliminated, the NCI bears its share of an upstream sale (Expected).",
			"E4: investment against equity at acquisition, goodwill and NCI, goodwill retranslated at the closing rate (IAS 21.47) (Expected).",
			"E5: the NCI share of the subsidiary's profit (Expected). Every rule balances (Reconciliation).",
		},
		Build: buildConsolidation,
	},
	{
		Name: "cashapp", Title: "Cash application on 500 payments", Item: "#512 row 19: #30 decision on the cash-application rule (AC-30.1 at 438 of 500)",
		Sources: []string{cashAppOpenItems, cashAppAnswerKey, cashAppMatchResults},
		Checks: []string{
			"No payment is auto-applied to invoices other than the answer key's (Reconciliation, Auto-applied wrongly must be 0).",
			"Read the payments left for a person and why the rule leaves each one (Not auto-applied).",
			"Decide whether 438 of 500 auto-applied is acceptable, or which category the rule should take on (for example short payments with a reason).",
		},
		Build: buildCashApp,
	},
	{
		Name: "modifications", Title: "Contract modification worked examples 4 to 8", Item: "#512 row 29: accountant review of the IFRS 15 / ASC 606 contract-modification worked examples 4 to 8",
		Sources: []string{modificationsSource},
		Checks: []string{
			"Each example is accounted for under the right paragraph: cumulative catch-up when the rest is not distinct, prospective when it is (Examples).",
			"A cut below recognized revenue takes the excess back out of revenue at the effective date (Expected changes, Revenue reversed).",
			"A removed or reduced obligation keeps what it delivered and its SSP follows what stays promised (Expected changes).",
			"The remaining schedule carries what is left of the new price (Remaining schedule, Reconciliation).",
		},
		Build: buildModifications,
	},
	{
		Name: "ic-elimination", Title: "Intercompany elimination per counterparty", Item: "#512 row 34: #40 accountant review of the per-counterparty elimination examples (IFRS 10 B86(c))",
		Sources: []string{icElimSource},
		Checks: []string{
			"Each holder-subsidiary pair is eliminated on its own figures, so a parent selling at different margins is eliminated per buyer (Expected pairs).",
			"Unrealised profit is the buyer's closing intercompany inventory times the seller's margin (Expected pairs, Reasoning).",
			"A difference between receivable and payable at the closing rate is an exchange difference in profit or loss (Expected entries, FX_GAIN_LOSS).",
			"In an upstream sale the NCI bears its share of the unrealised profit (Expected entries, NCI accounts).",
			"Every rule balances and the E3 total matches the statement line (Reconciliation).",
		},
		Build: buildICElimination,
	},
}
