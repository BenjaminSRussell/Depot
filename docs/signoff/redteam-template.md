# Red-team session sign-off record

Required for every release and for every provider, model family or endpoint change before it is promoted (DEPOT-308,
section 11.6). The session is manual: 2 hours, 3 engineers including the security lead, scoped to the leakage controls
the CI gate measures (`evals/README.md`, "Leakage and red-team gate"). The CI gate does not replace it: the gate runs a
fake and a replay provider, and only this session exercises a live model.

Copy this file to `docs/signoff/redteam-<release or change id>.md`, fill it in, and link it from the go-live checklist
row for AI (`docs/ops/go-live-checklist.md`, item 15).

## Session

| Field | Value |
|---|---|
| Release or change id | |
| Date and duration | |
| Provider, model family and endpoint under test | |
| Deployment profile (standard or cui) | |
| Engineers (3, security lead named) | |
| Gate run in CI for this commit (link to the `redteam-report` artifact) | |
| Dataset versions from the report | |

## Scope covered

Mark each as attempted live against the provider under test.

| Dataset | Paths attempted (ask, route, act, briefing) | Result |
|---|---|---|
| adv-cui | | |
| adv-fin | | |
| adv-xuser | | |
| adv-inj-exfil | | |

## Findings

One row per finding. Every successful break is also appended, with synthetic data only, to the matching dataset in
`evals/datasets/` in the same change that closes it.

| # | Finding | Control that failed | Disposition (fixed or accepted by the security lead) | Fix or acceptance reference | Added to dataset (file and case id) |
|---|---|---|---|---|---|
| | | | | | |

## Sign-off

| Role | Name | Decision (approved, approved with accepted findings, rejected) | Date |
|---|---|---|---|
| Security lead | | | |
| Engineer 2 | | | |
| Engineer 3 | | | |

No finding may stay open. A promotion with an unfixed finding needs the security lead's written acceptance in the
disposition column.
