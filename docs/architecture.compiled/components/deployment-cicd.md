# Deployment and CI/CD

**As built (v1.0):** Argo CD app-of-apps per environment (`deploy/argocd`, `deploy/envs`) with digest-pinned images
(placeholder digests until release), a services ApplicationSet, 69 service charts, Kyverno admission policies with
signed-image verification, Prometheus, Grafana and Alertmanager in dev, and a signed Windows MSI for single-host
installs. Dev runs the platform; stage has no event backbone, Temporal, Postgres, Keycloak or gateway; prod deploys
only `hello-ledger` and `kyverno` (D4).

## Purpose & Boundaries
Build, sign and ship every deployable; describe every environment in Git; keep infrastructure, secrets wiring,
observability and policy as code. Single-host Windows distribution (`tools/depot-server`, ADR-0004, proposed) is
packaged here too.

## Services
None. Everything is in `deploy/` (Argo CD, environments, charts for shared infrastructure, Kafka, Keycloak, gateway,
Postgres, Temporal, observability, policies, OpenTofu in `deploy/tofu`, k3d in `deploy/k3d`, multi-region chart in
`deploy/multi-region`, offline bundle in `deploy/offline`, Windows installer in `deploy/windows`) and in
`.github/workflows/`.

## Interfaces
- Environments: `deploy/envs/dev/apps` (event backbone, Temporal, Postgres, Keycloak, Envoy Gateway and gateway, services ApplicationSet, observability, Kyverno, payments webhook gateway), `deploy/envs/stage/apps` (services ApplicationSet and a few service Applications, Kyverno), `deploy/envs/prod/apps` (`hello-ledger.yaml`, `kyverno.yaml`).
- Images: the `image` job in `.github/workflows/ci.yml` builds and signs (cosign) the Go images on main; stage promotion and rollback through `deploy/scripts/stage_release.py`; release records in `.github/workflows/record-release.yml`.
- Windows: `.github/workflows/windows-msi.yml` builds the WiX MSI from `deploy/windows`.
- Path filters: `.github/workflows/gate.yml` decides which CI jobs run.

## Data Ownership
Deployment state in Git (manifests, values, digests). One shared `postgres:17-alpine` server per environment in dev
(`deploy/postgres`) creating 21 service databases by script, plus separate releases for metadata and batch; no
database or Secret for ledger, inventory or numbering anywhere (D4). Secrets come through External Secrets from the
store `depot` (`deploy/secret-store`).

## Events Produced
None.

## Events Consumed
None.

## Dependencies
internal: every service (charts, images), testing-reconciliation (gates). external: GitHub Actions and GHCR, Argo CD,
Helm, Kyverno, cosign, Strimzi, Keycloak, Envoy Gateway, External Secrets, kube-prometheus-stack, WiX.

## Gotchas & Constraints
- 63 of 65 dev service releases share namespace `hello-ledger` (batch and metadata have their own); only 13 of 69 charts ship a NetworkPolicy; no default-deny (D1).
- Chart values say stage and prod run migrations as an Argo PreSync Job, but no chart defines one; with `MIGRATE_ON_START=false` nothing applies migrations outside dev (D28).
- No PodDisruptionBudgets, no Argo Rollouts, HPA in three charts (inventory, pricing, production) (D18).
- `services/dispatch-py` and `services/routing-py` have no chart (ADR-0008 backlog).
- The `image` job matrix in `.github/workflows/ci.yml` lists four Go images (hello-ledger, fx, receiving, org); adding a service image means adding a leg there.
- Monitoring exists only in dev; Tempo and Loki are not deployed (D8).

## Target — not built
- Stage and prod platform: event backbone, Temporal, Postgres, Keycloak, gateway, service databases and Secrets (D4).
- Production database topology (replicas, WAL archive, PITR, failover), PDBs, Rollouts canaries, HPA (D18, ADR-0012).
- Migration Job per chart (D28); namespace-per-tier with default-deny NetworkPolicy (D1).
- Designed CloudNativePG per context and Vault dynamic credentials: superseded by ADR-0012 and External Secrets.

## Source Sections
- Architecture backend §7.20 deployment and scaling notes, §7.21 single-host Windows distribution, §7.8 configuration and secrets; Cross-Cutting Concerns (observability); data §3 database technology.
- ADR-0004, ADR-0012; delta §1.1 notes (databases), §2.1 (deployment-cicd row), §1 fact 6.
