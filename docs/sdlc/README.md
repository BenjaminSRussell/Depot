# Depot SDLC record

This folder is the audit trail for each pass of the Depot software development lifecycle. A pass takes the product
from requirements to shipped code in fixed stages. Every stage leaves an artifact, the evidence it rests on, an
independent review, and a gate decision. A reviewer, auditor or new engineer can follow a requirement from the PRD
to the code and tests that satisfy it, and see who decided what and when.

## Stages

| # | Stage | Artifact (source of truth) | Evidence | Gate |
|---|---|---|---|---|
| 1 | PRD | `docs/PRD.md`, `docs/prd/*.md` | `docs/sdlc/run-<n>/fr-audit.md`: each functional requirement's status with code and test evidence | The PRD review passes, and every FR has a status and evidence |
| 2 | Architecture | `docs/architecture.md`, `docs/adr/` | `docs/sdlc/run-<n>/architecture-delta.md`: as-built against as-designed | The architecture review passes, and every deviation has an ADR or a backlog item |
| 3 | Architecture compile | `docs/architecture.compiled/` (manifest, spine, contracts, components) | The manifest's source hash equals `sha256(docs/architecture.md)` | The compile check passes, and contracts match the OpenAPI and AsyncAPI files |
| 4 | UX design | `docs/ux/` | `docs/sdlc/run-<n>/ux-audit.md`: findings per persona and task, against `apps/web/docs/usability-gate.md` | The UX review passes, and each finding is in the backlog or waived with a reason |
| 5 | Backlog | GitHub issues labelled `sdlc-run-<n>`, `roadmap/` | `docs/sdlc/run-<n>/backlog.md`: issue to FR, ADR and UX finding | Every gap from stages 1 to 4 is traced to an issue or a waiver |
| 6 | Execute | Pull requests | Each PR names its issues and FRs, runs CI, and gets an independent review | CI is green and review findings are fixed or answered |
| 7 | Close | `CHANGELOG.md`, `docs/sdlc/run-<n>/close.md` | FR audit rerun on the merged code | Coverage delta recorded, and open items moved to the next run |

## Rules every stage follows

- **Evidence over assertion.** A status needs a file path and a test name, or the place that was searched. When in
  doubt, record the lower status.
- **Independent review.** The agent or person who produced an artifact does not review it. Reviews live next to
  the artifact as `<stage>-review.md`, with each finding's severity and outcome.
- **Gate log.** `docs/sdlc/run-<n>/gates.md` gets one row per gate: date, commit, decision (pass, pass with
  conditions, fail), reviewer, and links. Rows are only ever appended.
- **End-user first.** Every stage records friction for the people who use Depot: shop-floor operators, clerks,
  buyers, controllers, admins. Examples are jargon, raw ids, dead ends, slow paths and missing states. UX findings
  carry the same weight as functional gaps in the backlog.
- **Plain language.** Artifacts are written for the people who sign them off, not only for engineers.
- **Externally blocked work** (live clusters, vendor accounts, human sign-off) is tracked in #213 and #512 and
  marked `Blocked-external`, never `Built`.

## Runs

| Run | Started | Scope | Record |
|---|---|---|---|
| 1 | 2026-11-17 | Re-baseline after the Passage to Depot rename (ADR-0005): PRD v1.0, architecture as built, recompile, UX design, gap backlog, execution | [run-1/](run-1/) |
