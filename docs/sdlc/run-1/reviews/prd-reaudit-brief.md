# Brief: re-audit of Built / Built-untested / Blocked-external rows (stage 1, after a failed gate)

The first FR audit failed its independent review (78% agreement; every disagreement OVERSTATED status).
Read the review first: docs/sdlc/run-1/reviews/prd-review-1.md
(its "Systematic issues" section tells you exactly how the first audit went wrong).

Repository: the repository (Depot ERP). READ-ONLY on the repository. You edit only the audit file(s) named in
your assignment, in place.

## The strict rule
- **Built**: EVERY clause of the FR statement AND every indented note line under it in docs/prd/phase-*.md is
  implemented in the repo, and at least one named test covers it. If any in-repo clause/note is missing -> Partial.
- **Built-untested**: as Built but no test. Look for tests in Go, TS (vitest/playwright), Python (pytest,
  deploy/**/test_*.py), Helm/kubeconform, Tofu (*.tftest.hcl), load/tools before deciding "no test".
- **Blocked-external**: nothing remains to build in the repo; only a live system, vendor account or human sign-off
  (#213 / #512) is missing. If any in-repo work remains -> Partial (say what is in-repo vs external).
- A row next to a NOT-IMPLEMENTED.md line for that FR that says partial / open / later slice / handoff / not built
  is Partial unless you can show that line is stale with code+test evidence (cite both).
- A Built row with a real Gap is a contradiction: resolve it (Partial, or move the gap text to "live proof only").
- Downgrading is fine; upgrading is not in scope.

## Procedure, for every Built / Built-untested / Blocked-external row in your file(s)
1. Extract the FR text with ALL its note lines.
2. Check each clause against code and tests; check the service's NOT-IMPLEMENTED.md.
3. Rewrite the row in place: Status, Evidence (repo-relative paths, `path:line`, test names), Gap (what is missing,
   specific). Keep the UX column.
4. Update the Summary table counts at the top of the file to match the rows.
5. Append at the end of the file a section:
```
## Re-audit log (after gate 1 fail)
| FR | Before | After | Reason (clause missing / NI row / evidence) |
```
listing every row you changed, and a line "Rows re-checked: N; unchanged: N; downgraded: N".

Mechanical pre-check flags (gap-column / NOT-IMPLEMENTED line / #213-#512 cited) are in
(working notes, not kept)
(format: `<file letter> <FR> <status> <flags>`). They are hints; check every row, flagged or not.
Also fix these 13 rows exactly as the review says if they are in your files: FR-9.16, FR-13.16, FR-11.13,
FR-20.9, FR-18.10, FR-20.18, FR-25.3, FR-38.20, FR-25.11, FR-50.1, FR-4.1, FR-36.12, FR-5.14 (all -> Partial).
