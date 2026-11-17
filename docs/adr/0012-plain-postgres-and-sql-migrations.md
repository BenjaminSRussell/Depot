# ADR-0012: Plain Postgres and plain SQL migrations (`dbx.Migrate`) instead of CloudNativePG per context and Atlas

**Status:** Accepted for dev and stage; prod topology open — 2026-11-17 (records a decision already made in code; SDLC run 1, stage 2, deviations D18 and §3 row 17)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` §1.1 notes, §2.1 deployment-cicd, §3 row 17, §4 D18, §5 C15

## Context

ADR-0001 and the architecture (§4, §7.18, data §3) chose PostgreSQL 17 on CloudNativePG (CNPG), one cluster per
bounded context with replicas, a CNPG Pooler (PgBouncer, transaction mode), and Atlas versioned migrations generated
from entity metadata, run as Argo PreSync Jobs in stage and prod.

What is built:

- **Server.** `deploy/postgres` is a chart around a digest-pinned `postgres:17-alpine` StatefulSet (10 GiB volume),
  with an ExternalSecret for credentials and an optional CDC mode (logical WAL, a `debezium_cdc` role, Kafka Connect
  ingress). In dev one shared server, `postgres.hello-ledger.svc`, holds 21 service databases created by script
  (`deploy/envs/dev/apps/postgres.yaml`); metadata and batch have their own releases. CNPG appears only in the
  multi-region chart (`deploy/multi-region/chart/templates/cnpg-cluster.yaml`), which no environment deploys.
- **No pooler.** Services connect with `pgxpool` directly. Tenancy uses `SET LOCAL` inside each transaction
  (`dbx.WithTenantTx`), which would also be safe under transaction pooling.
- **Migrations.** Every service embeds `migrations/NNNN_name.sql` and applies them with `dbx.Migrate`
  (`platform/go/dbx/dbx.go`): files run in order and are recorded; a session advisory lock keyed on database and
  schema serializes replicas that start together, taken by polling so a `-- dbx:no-transaction` migration
  (`CREATE INDEX CONCURRENTLY`) is not deadlocked. Row-level security policies are written in the SQL files
  (with `FORCE ROW LEVEL SECURITY`; all 59 services with migrations use it) and checked by `tools/rlscheck`. An expand/contract lint
  exists (`deploy/multi-region/tools/expand_contract_lint.py`) but no CI workflow runs it.
- **Running migrations.** `MIGRATE_ON_START=true` is set only for fx, identity, org, receiving and transport (dev and
  stage values), pricing (chart default) and `hello-ledger` (dev, stage and prod). The other services with migrations
  (ledger, inventory, sales and about 50 more) have no migration path in any environment, dev included. Chart values
  say "stage and prod run them as an Argo PreSync Job", but no chart or Argo application defines such a Job.
- No Atlas and no migration generation from metadata.

## Decision

1. **Postgres is plain PostgreSQL 17**, deployed by `deploy/postgres`. In dev and stage, several service databases may
   share one server. Each service still has its own database, its own owner role, and the `depot_app` role
   (`NOBYPASSRLS`, not an owner) for traffic.
2. **Migrations are hand-written, forward-only SQL files** applied by `dbx.Migrate`, expand/contract only, so the
   previous image keeps working against the new schema. RLS policies live in those files. No Atlas.
3. **Migrations run with the owner role, separately from serving.** In dev that may be `MIGRATE_ON_START`; outside dev
   it must be a Job (Argo PreSync or equivalent) using an owner-role URL (`MIGRATE_DATABASE_URL`, read today only by identity and org), and serving pods run with
   `MIGRATE_ON_START=false` as `depot_app`. The Windows install already does this with `depot-server migrate`
   (ADR-0004).
4. **The production database topology is not decided by this ADR.** CNPG (or a managed Postgres where a customer
   allows one) with replicas, backups and failover is still the target for prod. It is recorded as a known gap, not
   accepted as built.

## Alternatives rejected

- **CNPG cluster per context in every environment** (the design). About 60 clusters in dev is not workable; the
  service count changed the arithmetic (ADR-0008).
- **Atlas.** Adds a tool and a generation step; the metadata generator that would feed it is not built.

## Consequences

- Simple, portable database setup that runs the same on Kubernetes, Windows and CI (`DEPOT_TEST_DATABASE_URL`).
- A shared dev or stage server couples the services' availability and restore; it is not a production shape.
- No pooler: connection counts grow with replicas × services; at production scale a pooler will be needed.
- Backups, replicas, failover and point-in-time restore exist only in the undeployed multi-region chart.
- Backlog: add a migration Job (Argo PreSync) to every service chart, run as the owner role, and remove the misleading "PreSync Job" comments until it exists (D28). *Done in B-09a:* `templates/migrate-job.yaml` in every chart with migrations (`deploy/scripts/gen_migrate_jobs.py`), serving pods `MIGRATE_ON_START=false`; the owner Secrets outside dev remain (D4).
- Backlog: run `expand_contract_lint.py` in CI on every migration change (D18).
- Backlog: decide and build the production Postgres topology (CNPG per service group or managed Postgres), with replicas, WAL archiving, PITR and a restore drill meeting RPO 5 min / RTO 1 h (D18, D4).
- Backlog: provision every service database and its Secret in stage and prod (for example `depot_ledger`/`ledger-db-app`, `depot_inventory`, `depot_numbering` are missing everywhere) (D4).
- Backlog: evaluate a connection pooler (PgBouncer or CNPG Pooler) before load tests at production scale (D18).
