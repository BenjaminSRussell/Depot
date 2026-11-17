# Brief: PRD stage — as-built FR audit (Depot SDLC run 1)

Context: the product (formerly Passage, now **Depot**) is a manufacturing ERP monorepo at the repository
(branch claude/confident-gauss-ncr4l9). The PRD is docs/PRD.md plus docs/prd/phase-*.md (FR ids FR-<wave>.<n>,
955 of them). The code was built wave by wave. We are re-baselining the PRD to v1.0 and need an AUDITABLE status
for every FR: what is built, with evidence a reviewer can check.

You are READ-ONLY on the repository. Do not edit, commit, or run anything that writes inside the repository.
Write your output only to the file named in your assignment.

## Sources of evidence (use in this order)
1. `git grep -n "FR-<w>.<n>\b"` across services/ apps/ tools/ platform/ deploy/ — code and tests cite FR ids.
2. NOT-IMPLEMENTED.md files (79 of them, `git ls-files | grep NOT-IMPLEMENTED`) — explicit gaps.
3. Service READMEs, openapi.yaml, migrations, CHANGELOG.md, apps/web/src/modules/<module>/.
4. Open trackers: #213 (live-cluster steps) and #512 (human sign-offs) are known externally-blocked items.
Do not read whole huge files (docs/PRD-full.md is 32k lines); grep and read excerpts.

## Status vocabulary (exactly one per FR)
- **Built** — implemented and covered by at least one test you can name.
- **Built-untested** — implemented, no test found.
- **Partial** — some of the FR's notes are implemented; say which part is missing.
- **Not built** — no implementation (cite the NOT-IMPLEMENTED row if one exists).
- **Blocked-external** — needs a live system, licence, vendor account, or human sign-off (cite #213/#512 or the doc).

Be honest and conservative: when unsure between two, pick the lower one and say why. Every row needs evidence
(file paths, ideally `path:line`, and a test name) or, for Not built, where you looked.

## End-user lens (Depot must be as end-user friendly as possible)
For FRs with a user-facing surface, note friction you can see from the code: no web screen for a capability
that has an API, raw ids shown instead of names, jargon or error codes in UI copy, missing empty/error states,
flows needing many screens, no keyboard path. Keep it factual, one short phrase, in the UX column.

## Output format (markdown file)
```
# FR audit — waves A–B
Auditor: agent <letter>; repository commit: <git rev-parse HEAD>; date 2026-11-17

## Summary
| Wave | FRs | Built | Built-untested | Partial | Not built | Blocked-external |

## Wave W: <name>
| FR | Title | Priority | Status | Evidence | Gap | UX friction |
|---|---|---|---|---|---|---|
| FR-W.1 | ... | Must | Built | services/ledger/internal/api/journal.go:120; TestPostJournal_Balances | — | — |

## Top gaps (ranked by user/business impact)
1. FR-x.y — one line why it matters, suggested slice size (S/M/L).
```
List every FR of your waves, none skipped. Titles and priorities come from the phase file headings
`**FR-W.n <title>** — <Priority>`.
