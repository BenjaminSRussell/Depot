# Testing and Reconciliation

**As built (v1.0):** CI gates in `.github/workflows/gate.yml` and `.github/workflows/ci.yml` (Go shards against Postgres,
OPA policy tests, web, portals, agents, workflow, Python, charts, docs tools, deploy tests),
`.github/workflows/ci-negative.yml` (proves injected failures are caught), `tools/rlscheck`, k6 load scenarios in
`load/k6`, agent evals in `evals/`, and per-service reconciliation endpoints. No REC-01..19 catalogue exists by id;
this component was not audited in depth for v1.0.

## Purpose & Boundaries
The evidence that the system is correct: unit, store and property tests per service, contract checks on OpenAPI and
AsyncAPI, RLS checks, end-to-end and sign-off packets, load tests against SLOs, agent evals, and reconciliation of
subledgers to the journal. Merge gates block on all of them.

## Services
None. Tooling lives in `tools/rlscheck`, `tools/e2e`, `tools/signoff-packets`, `load/`, `evals/`, `docs/ops/tools`
and `deploy/scripts`; workflows in `.github/workflows/`.

## Interfaces
- Gate: `gate.yml` computes path filters (`changes` job) and calls `ci.yml`; one `gate` job fails unless every called job passed or was skipped.
- Reconciliation endpoints in the services (for example inventory, costing, treasury, analytics, and `gl-reconciliation` routes in assets, eam, payroll, projects, receivables, revrec); the designed uniform `GET /v1/<context>/reconciliation/{check}` and the `reconciliation-run` workflow are target.
- Docs tools: the OpenAPI documentation gate and AsyncAPI reference (`docs/ops/tools`), and the compiled-architecture drift check (`scripts/compile_architecture.py --check`) in the `docs-tools` job.
- Load: `load/k6/scenarios.js` and `load/k6/soak.js` with SLO fixtures in `load/slo`.

## Data Ownership
Test fixtures and golden files in each service (`testdata/`), eval datasets in `evals/datasets`, load fixtures in
`load/fixtures`. No production data.

## Events Produced
None.

## Events Consumed
None.

## Dependencies
internal: platform-kernel (`platform/go/testkit`), every service, deployment-cicd (CI runners, images). external:
GitHub Actions, Postgres service containers, OPA, Keycloak (FIPS realm test), k6.

## Gotchas & Constraints
- Postgres in CI runs with `--locale=C`; queries must order text with `collate "C"`.
- `tools/rlscheck` enforces forced RLS and allowlisted exemptions (`platform/go/rlsexempt`).
- The closed-period contract test must pass on every posting path; NFR-P1 cannot be shown on the unpartitioned journal (D10).
- Nothing is verified against a live cluster: every live-cluster claim in the repository is pending (#213).

## Target — not built
- REC-01..19 reconciliation catalogue, the nightly `reconcile-daily` job and the wave-30 go-live simulation.
- A uniform reconciliation API per context and a reconciliation-run workflow.
- Generating the as-built service inventory in CI (ADR-0008 backlog, D5/D6).

## Source Sections
- Architecture Testing Strategy (Testing Architecture), backend §5.20 reconciliation endpoints (target), data §8.6 ledger invariants.
- Delta §1.2 (tools), §2.1 (testing-reconciliation row).
