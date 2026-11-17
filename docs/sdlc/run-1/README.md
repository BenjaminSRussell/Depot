# SDLC run 1: re-baseline after the rename to Depot

**Started:** 2026-11-17 · **Scope:** take the product as built, after the Passage to Depot rename (ADR-0005), through
every SDLC stage with an evidence trail. **Gate log:** [gates.md](gates.md). The conventions are in
[../README.md](../README.md).

## Outcome by stage

| # | Stage | Artifact | Evidence and reviews | Gate |
|---|---|---|---|---|
| 1 | PRD | [docs/PRD.md](../../PRD.md) v1.0 (as-built baseline) | [fr-audit/](fr-audit/): 950 FRs with status, evidence and gap; reviews [1](reviews/prd-review-1.md), [2](reviews/prd-review-2.md), [3](reviews/prd-review-3.md), [4](reviews/prd-review-4.md) with their samples | Pass at gate 10, after 4 review rounds |
| 2 | Architecture | [docs/architecture.md](../../architecture.md) v1.0, ADRs 0006 to 0015, ADR-0004 accepted | [architecture-delta.md](architecture-delta.md), [review](reviews/architecture-review.md), [response](architecture-review-response.md) | Pass at gate 7 |
| 3 | Architecture compile | [docs/architecture.compiled/](../../architecture.compiled/), rebuilt by `scripts/compile_architecture.py` | `--check` runs in CI (docs-tools job) | Pass at gate 8 |
| 4 | UX design | [docs/ux/](../../ux/): standards UXS-01 to UXS-20, 16 personas, rollout slices 0 to 20 | [ux-audit.md](ux-audit.md) (50 findings), [review and re-review](reviews/ux-design-review.md) | Pass at gate 5 |
| 5 | Backlog | GitHub issues labelled `sdlc-run-1` | [backlog.md](backlog.md), [trace.md](trace.md), [execution-plan.md](execution-plan.md) | Pending |
| 6 | Execute | Pull requests that name their issues | Each PR's CI and independent review | Pending |
| 7 | Close | `close.md` | FR audit rerun on the merged code | Pending |

## Where the product stands (950 functional requirements)

| Status | Count | Meaning |
|---|---|---|
| Built | 40 | Every clause and note of the FR is implemented in the repository, with a named test |
| Partial | 777 | The core works and is usually tested, but at least one clause or note is missing; the Gap column says which |
| Not built | 133 | No implementation |
| Blocked-external | 0 | Nothing is waiting only on a live system or a sign-off. Every such item also has work left in the repository; the external part stays in #213 and #512 |

How to read this: most waves shipped a tested core and left the joins between services for later, along with
the master-data sources, the postings and the screens. The most common gaps:

- Postings that never reach the ledger or inventory: receipts, packing slips, purchase price variance, freight,
  FX revaluation and tax.
- Inputs typed by the user where the system should read them: MRP snapshots, ATP buckets, rates and prices.
- Back-end features with no screen: close, audit, the job monitor, sequences.

The audit found five real defects, each tagged `[defect]` in its file:

- FR-14.2: ALL can close while GL is open.
- FR-26.7: smart rounding crosses its band.
- FR-23.5: dispatch ties fall back to the record id, not the order number.
- The audit service takes the actor from the request body.
- FR-8.10: product block flags are stored but nothing enforces them.

The architecture work also found that, as built, the Kubernetes request path cannot carry a request end to end.
Its gaps D31 to D36 cover gateway routes, the token issuer, legal-entity selection, Kafka users, encryption
conditions and authorization logs. Until those and D1 to D4 and D6 to D8 close, stage and prod stay closed to real
users and personal data (ADR-0006 rule 6, ADR-0011).

## How the audit was kept honest

The first pass rated 205 FRs Built. An independent reviewer sampled 60 rows with a fixed seed and agreed with
78% of them. Every disagreement overstated progress, so gate 1 failed. Four rounds followed:

| Round | Change to the audit | Review result |
|---|---|---|
| Re-audit | Every FR checked against all its clauses and notes and against the service's NOT-IMPLEMENTED.md; 158 rows downgraded | 91.5% agreement, but 2 Must rows still overstated |
| Targeted re-check | "Deviation" and "caller supplies" notes, plus schema fields; 7 rows downgraded | 1 overstated Must row, a real defect in the close order |
| Probe pass | Every conditional leg and scope clause, with probe tests; 4 rows downgraded, 2 defects found | 1 overstated Must row (sort key) |
| Sort-key check | Every remaining Built row that names an ordering; 0 rows changed | Pass |

Statuses below Built agreed 100% across every review, so the gap list behind the backlog is reliable. The residual
risk is that the last two random samples of Built rows each found 1 overstated row in 10.

## Files

- **`fr-audit/waves-NN-MM.md`:** one file per two phases. Each has the summary, a row per FR, the top gaps and
  the log of every re-audit pass. Inside these files, "fr-audit-A.md" to "fr-audit-E.md" refer to the five
  files in wave order.
- **`reviews/`:** every review, the brief each auditor worked from, and the seeded samples reviewers checked.
- **`architecture-delta.md` and `architecture-review-response.md`:** the stage 2 evidence and the response to
  its review.
- **`ux-audit.md`:** the stage 4 evidence. Screenshots stayed in working notes and were not kept.

## Decisions awaiting the owners

- **Legal-entity switching (ADR-0006 rule 4):** the recommended model lets a user switch among the legal
  entities they are entitled to. The token carries the list, and the shell's selection narrows it to one entity.
  Recorded as Proposed until the owners confirm; tracked as D33.
- **UX-GAP-1:** the UX plan reaches about a third of the AX/SAP click baseline, not the thesis's tenth. Closing the
  rest needs product features such as order templates. Needs a product-owner sign-off.
