# Sign-off packets

The human reviews tracked in #512 each get a workbook generated from fixtures already in the repository. A packet
does not replace the review. It puts the inputs, the expected results and the totals to tie out in one place, so the
reviewer can check them quickly and record the result.

Generate every packet, or one:

```sh
go run ./tools/signoff-packets -out /tmp/packets
go run ./tools/signoff-packets -out /tmp/packets -only tax
go run ./tools/signoff-packets -list
```

Run the commands from `tools/signoff-packets`, or pass `-repo <checkout>` from anywhere. `-only` also takes a
comma-separated list. `-date YYYY-MM-DD` sets the generation date printed on the cover, and `-commit` overrides the
commit. The same commit and date always give byte-identical workbooks. Do not commit the generated `.xlsx` files: they
are made from the fixtures and go stale when the fixtures change.

Every workbook starts with a **Cover** sheet. It lists what to check, the source fixtures, the git commit (marked when a
source fixture differs from it), the generation date, the sheets with their row counts, and the sign-off fields
(reviewer name, date, result, comments). The other sheets are the inputs, the expected results and, in most packets, a
**Reconciliation** sheet with the totals to tie out. Differences that must be 0.00 are labelled so.

| Packet (`-only`) | #512 row | Review | Source fixtures | Sheets after the cover (rows) |
|---|---|---|---|---|
| `statements` | 1 | #15 finance sign-off on the statement pack | `services/finreport/internal/domain/testdata/demo-fy2026.json` | Journals 78, Accounts 11, Trial balance 12, Statements 12, Reconciliation 12 |
| `costing` | 2 | #20 costing reference workbook | `services/costing/internal/domain/testdata/cost_reference.json` | Sequences 104, Transactions 839, Expected 467 (7 methods side by side), Reconciliation 7 |
| `leases` | 3 | #37 IFRS 16 lease schedule workbook | `services/assets/testdata/lease-25.json` | Leases 25, Schedule 1313, Reconciliation 25 |
| `poc` | 4 | #41 percentage-of-completion revenue workbook | `services/projects/testdata/poc-worked-example.csv`, `proj-estimates-12x4.csv` | Worked example inputs 3 and expected 3, Estimates inputs 48 and expected 48, Reconciliation 51 |
| `asc606` | 5 | #29 ASC 606 workbook for the 40 bundle scenarios (and the P2 workbook review) | `services/revrec/internal/domain/testdata/asc606-40.json` | Scenarios 40, Obligations 134, Schedules 744, Deferred by month 304, Reconciliation 40 |
| `tax` | 6 | #13 accountant review of the 500-scenario tax matrix | `services/tax/testdata/matrix500.json`, `matrix500-expected.csv` | Scenarios 500, Expected US 792, EU 378, IN 220, CA 112, Reconciliation 13 |
| `ebics` | 16 | #36 review of the 13 EBICS return codes against the EBICS 2.5 annex | `services/treasury/internal/ebics/testdata/returncodes.json` (from `returncodes.go`) | Return codes 13, Dispositions 14, Reconciliation 5 |
| `consolidation` | 18 | #40 P2 sign-off and P4 | `services/consolidation/internal/domain/testdata/consol-group-3-interim.json` | Inputs 15, Workings 6, Expected 17, Reconciliation 6 |
| `cashapp` | 19 | #30 decision on the cash-application rule (AC-30.1 at 438 of 500) | `services/receivables/testdata/cash-app-500/open_items.csv`, `answer_key.csv`, `match_outcomes.csv` | Open items 931, Answer key 500, Match outcomes 500, Not auto-applied 62, Reconciliation 8 |
| `modifications` | 29 | IFRS 15 / ASC 606 contract-modification worked examples 4 to 8 | `services/revrec/internal/domain/testdata/modification-examples.json` | Examples 5, Contract before 73, Expected changes 7, Remaining schedule 48, Reconciliation 5 |
| `ic-elimination` | 34 | #40 per-counterparty elimination examples (IFRS 10 B86(c)) | `services/consolidation/internal/domain/testdata/ic-partner-examples.json` | Accounts 8, Examples 2, Partner balances 18, Expected pairs 15, Expected entries 21, Reconciliation 6 |

Row counts are those of the fixtures at the time of writing; the cover of each workbook prints the current ones.

## Where the expected results come from

Every expected value in a packet is in a fixture that a service test checks the code against. No packet runs service
code itself.

- **Fixtures with expected values written by a generator script** (`costing`, `leases`, `poc`, `asc606`) or by hand
  (`consolidation`): the packet copies them.
- **Data that used to live only inside a Go test** is now a testdata file that both the test and the packet read:
  - `demo-fy2026.json`: the finreport demo year. `TestDemoDataset_StatementsTieToTrialBalanceAndCashFlowReconciles`
    reads it. The packet's trial balance, statements and cash flow are sums of its journals by the account ranges of
    the standard definitions, the same figures the test holds the engine to.
  - `returncodes.json`: `TestReturnCodes_ConstantsMatchTheEBICS25Annex` checks it against the constants and `Classify`.
  - `modification-examples.json`: `TestModify_WorkedExamples4To8` checks `Modify` against it.
  - `ic-partner-examples.json`: `TestDeriveRun_ParentSellingToTwoSubsidiaries_B86c` and
    `TestDeriveRun_UpstreamSaleFromPartnerBalances` derive their runs from it.
- **Results computed by a test oracle or the matcher** are written to a golden file the test compares with on every
  run, and rewritten with `-update`:
  - `matrix500-expected.csv`, by `TestMatrix500` in `services/tax/internal/domain`. Rewrite it with
    `go test ./internal/domain -run TestMatrix500 -update`.
  - `match_outcomes.csv`, by `TestCashApp500_AutoMatchesWithNoFalseMatch` in `services/receivables/internal/domain`.
    Rewrite it with `go test ./internal/domain -run TestCashApp500 -update`.

  Review the diff before you commit a rewritten file: a change there changes what the reviewer signed.

## Not covered by a packet

The other #512 rows need a recording, a live environment, an external assessor or a decision memo rather than a
workbook. They include the walkthrough recordings (rows 7 to 9), the cutover rehearsal (10), usability sessions and the
tree test (12, 13), the screen-reader pass (14, whose draft ACR is the packet), the SAF-T XSD download (17), and the
policy, legal and product decisions.
