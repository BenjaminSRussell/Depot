# Review: B-09a / D28, owner-role migration Jobs (commit a3d87d69)

Branch `worktree-agent-af314dbd0a94810c2`. Reviewed: `platform/go/dbx/migratecmd.go` and its test, every changed Go `main`
(57 of them), the agents-ts, integration-hub-ts and prediction-py migrate commands, `deploy/scripts/gen_migrate_jobs.py` and a
sample of its output (ledger, org, hello-ledger, transport, sales, receivables, migration-factory), `test_migrate_jobs.py`, the
`gen_networkpolicies.py`, `test_argocd_apps.py` and `stage_apps.py` changes, `deploy/postgres`, the dev Postgres policy, the
env and Application changes, the k3d script and the docs.

## Verdict: CHANGES REQUIRED

The core is sound. The Go command runs first in every `main` and dispatches only on an exact `argv[1] == "migrate"`. It refuses
extra arguments, exits 0 or 1, and takes the dbx advisory lock. The Job has the right hook annotations and security context, no
service account token, no port, distinct labels, and nothing selects it. The render guard works.

Two Major issues need fixing before merge, and both fixes are small:

- **(1)** The D28 / CHANGELOG claim that dev is covered is wrong. About 53 of 60 dev Jobs reference Secrets that nothing
  creates, so those Applications go from Synced to SyncFailed.
- **(2)** The migrate command does not wait for or retry the database. The hand-written Applications have no sync `retry`.
  Together, one transient Postgres outage during a sync fails that revision for good, where on-start migration used to
  recover by itself.

The other findings are Minor or Nit and can be follow-ups.

## Findings

### 1. Major: dev has no owner Secret for about 53 of the 60 Jobs, so those Applications now fail every sync, and D28 says otherwise

**Where:**
- `docs/architecture.md:113` (D28 row: "Still open: the owner Secrets and databases outside dev")
- `CHANGELOG.md:121`
- `docs/runbooks/delivery/deploy-local.md:57-61`
- Every generated `values.yaml` `migrateJob.ownerSecret: <chart>-db-owner` (`deploy/scripts/gen_migrate_jobs.py:106`)

**Verified:**
- No manifest, script or Terraform file in the repo creates any `*-db-owner` Secret. `grep -rln db-owner` finds only values,
  comments and `services/identity/charts/identity/test-networkpolicy.sh`. That includes `identity-db-owner` and
  `org-db-owner`, which the runbook says identity and org "use" in dev.
- In dev only five charts get a working Secret: eam and transport (ExternalSecrets in `postgres.yaml`), and fx, receiving and
  hello-ledger (the serving Secret, created outside the repo).
- The dev Postgres creates `depot_ledger` and most of the other databases with CREATE granted to `depot_app`
  (`postgres.yaml:1845-1851`), but there is no owner role and no Secret for them.

**Failure scenario:** the dev `services` ApplicationSet syncs ledger, sales, inventory and the others.
1. The PreSync Job pod sits in `CreateContainerConfigError` until `activeDeadlineSeconds` (600 s).
2. The Job fails and the sync fails. The ApplicationSet's `retry` (5 attempts) repeats this for roughly an hour per revision.
3. Because the PreSync phase fails, none of the chart's ordinary resources are applied for that revision: the Service, the
   B-01a `<release>-ingress` NetworkPolicy changes, the ServiceMonitor.

Before this commit those Applications synced; their pods were only unhealthy. The backlog's D28 exit ("a fresh environment
comes up with every schema applied") is not met in dev. Because the D28 row and CHANGELOG scope the remaining work to "outside
dev" / "stage and prod", parts b and c may not provision dev owner Secrets.

**Fix (either):**
- Provision dev owner Secrets in `deploy/envs/dev/apps/postgres.yaml`. In dev, `depot_app` already has CREATE on these
  databases, so a dev `<chart>-db-owner` ExternalSecret can carry that URL, or create `depot_<svc>_owner` roles.
- Or set `migrateJob.enabled: false` in dev for the charts that have no owner Secret yet, via a generated list in
  gen_migrate_jobs.

Either way, correct the D28 row, CHANGELOG and runbook to say that dev owner Secrets are still missing for every chart except
fx, receiving, transport, eam and hello-ledger.

Also add a rule for parts b and c: the owner Secret must not be created by an ordinary resource of the same Application. A
chart-level ExternalSecret syncs after PreSync, so the first install would deadlock: the Job waits for a Secret that the
failed sync never creates.

### 2. Major: the migrate command fails at once if Postgres is unreachable, and the hand-written Applications never retry a failed sync

**Where:**
- `platform/go/dbx/migratecmd.go:50-57`
- `deploy/envs/{dev,stage}/apps/{fx,hello-ledger,receiving,org,identity}.yaml`
- `deploy/envs/prod/apps/hello-ledger.yaml`
- `deploy/argocd/hello-ledger-local.yaml`

None of these has `syncPolicy.retry`; the dev and stage `services` ApplicationSets do.

**Verified:** `MIGRATE_DATABASE_URL=postgres://root@localhost:1/nope ledger migrate` exits 1 within milliseconds ("connection
refused"). `pgxpool` connects lazily and `Migrate` fails on the first `Acquire`. With `backoffLimit: 2` and the Job
controller's 10 s and 20 s back-off, the Job fails about 30 s after the database becomes unreachable.

**Failure scenario:** Postgres restarts, fails over or is still starting during a sync of `hello-ledger-prod` or `fx-stage`.
1. The Job fails and the sync fails.
2. Argo CD's automated sync does not re-attempt a failed sync of the same revision unless `retry` is set. This is based on
   documented Argo CD auto-sync behaviour, not reproduced here: there is no cluster.
3. The Application stays OutOfSync/failed until someone syncs it by hand or pushes a new commit.

Before this commit, on-start migration crash-looped and recovered by itself.

The same race probably affects `make deploy-local`: `root-local` (Postgres) and `hello-ledger-local` are applied together,
and the hello-ledger Job pod (a local image) starts long before Postgres pulls and runs initdb. This is uncertain: `root-local`
also syncs `postgres.yaml`'s ExternalSecrets, and the local cluster does not install External Secrets, so local may already be
broken for an unrelated reason.

**Fix:**
- In `RunMigrateCommand`, retry `Connect` plus a `select 1` with back-off for a bounded time, for example 2-3 minutes, well
  under `activeDeadlineSeconds`.
- Add `retry` (as in `services.yaml`) to the hand-written Applications.

### 3. Minor: the Job's image is computed separately from each Deployment's, and the test does not check that they match

**Where:**
- `deploy/scripts/gen_migrate_jobs.py:168-169`
- `deploy/scripts/test_migrate_jobs.py:137` (`job_problems`)

**Verified with a script that renders all 60 charts with no overrides, a tag, a digest and a registry mirror:**
- finreport with no digest and no tag: the Job runs `finreport:0.1.0` (AppVersion), while the Deployment and CronJobs run
  `finreport:latest` (`services/finreport/charts/finreport/templates/deployment.yaml:31`).
- assets with a digest: the Job runs `assets@sha256:…`, the Deployment `assets:0.1.0@sha256:…`. This is the same content, so
  harmless.
- Every other chart matches in all four cases. `imagePullPolicy` matches everywhere.

**Mutation (scratch copy):** I changed the generator to always use `:latest`, ignoring the digest and the tag, regenerated,
and ran `test_migrate_jobs.py`. All 10 tests passed. A regression that migrates with a different image version than the
pods serve would go unnoticed.

**Fix:**
- In `job_problems`, assert that the Job container's image (and pull policy) equals the image of at least one serving
  container of the same render.
- Align finreport's default tag. Better still, give every chart a `<chart>.image` helper and have the Job include it.

### 4. Minor: nothing stops the migrate command from running as `depot_app`

**Where:** `platform/go/dbx/migratecmd.go:47-60`, `deploy/scripts/test_migrate_jobs.py:178-182`.

**Verified:** `MIGRATE_DATABASE_URL=postgres://depot_app:…/fresh_db ledger migrate` exits 0, and all 41 ledger tables are
owned by `depot_app`.

The stage/prod render test only checks that the Job's Secret name and key differ from the serving container's. A Secret
named `ledger-db-owner` that holds the `depot_app` URL (a copy-paste mistake in parts b and c) passes every check. It would
silently give the serving role ownership of the schema: ALTER and DROP rights, and exemption from non-FORCE RLS tables such as
`service_token_replay`.

**Fix:** after connecting, refuse when `current_user = 'depot_app'`, or when the role is a member of it, unless an explicit
`MIGRATE_ALLOW_APP_ROLE=true` is set. Set that only in the dev exception values (fx, receiving, hello-ledger), where
`depot_app` really owns the schema. Add the same check to the TS and Python commands.

### 5. Minor: expand/contract and rollback consequences of PreSync are not written down

**Where:** `docs/runbooks/delivery/deploy-local.md:47-79`, `docs/architecture.md` §7.18, ADR-0012 note.

PreSync applies the new schema while the old pods keep serving. A sync that fails after PreSync, or an Argo/`rollback-stage`
image rollback, leaves the old code on the new schema permanently. That is safe only under the expand/contract rule (§7.18,
NFR-A4: nullable columns, a destructive change takes two releases), and none of the new docs say so.

**Verified:** pre-B-09a mains ignore their arguments, for example `git show a3d87d69^:services/ledger/cmd/ledger/main.go`.

**Failure scenario:** someone rolls stage back to a pre-B-09a digest (hello-ledger, fx, receiving, org and identity have
images).
1. The Job runs the old binary with `args: [migrate]`.
2. The old binary ignores the argument and starts serving, connected as the owner role, for 600 s. It gets no traffic,
   because no Service and default-deny, but it is still a process running as the owner.
3. The Job then fails on its deadline and the rollback sync fails.

**Fix:**
- Add a runbook paragraph: migrations must stay backward-compatible with the previous release, because PreSync runs before
  the pods change and is not undone by a rollback.
- Rolling back past B-09a needs `migrateJob.enabled=false`.

### 6. Minor: `deploy/postgres` `admitMigrateJobs` derives the wrong label for charts whose name is not the pod name

**Where:** `deploy/postgres/templates/networkpolicy.yaml:27-33` against `deploy/scripts/gen_migrate_jobs.py:181,195`.

The Postgres rule admits `<clientApps entry>-migrate`, where `clientApps` entries are pod names. The Job is labelled
`<Chart.Name>-migrate`.

**Verified:**
- sales is chart `depot-sales` with pods `sales`, and compliance is chart `depot-compliance` with pods `compliance`.
- `helm template deploy/postgres --set clientApps={sales}` admits `sales-migrate`, but the Job pod is `depot-sales-migrate`.

This is latent: `deploy/postgres` serves only batch and metadata today. When parts b and c give sales a `deploy/postgres`
release, its Job will time out connecting.

The same naming produces owner Secrets that parts b and c must know about: `depot-sales-db-owner`,
`depot-compliance-db-owner`, `depot-receivables-db-owner` and `depot-migration-factory-db-owner`.

**Fix:** label the Job pod with the serving pod name (`ingressPolicy.podNames[0]`, or a generated `migrateJob.podName`)
plus `-migrate`, or document the rule. Extend `test_the_postgres_chart_admits_each_clients_migrate_pod` to every chart.

### 7. Minor: the dev Postgres admits any pod in `hello-ledger` labelled `app.kubernetes.io/component: migrate` (label borrowing, same class as #723)

**Where:** `deploy/envs/dev/apps/postgres.yaml:2473-2477`.

The rule has no namespaceSelector, so it is correctly limited to the same namespace. However, it is label-only: anyone who
can create a pod in `hello-ledger` can add `component: migrate` and reach 5432. No chart lets values inject pod labels, so
this needs pod-create rights, and credentials are still required.

The `deploy/postgres` variant pairs name and component, which is narrower but still label-based.

**Fix:**
- List the `<chart>-migrate` names (generated from `gen_migrate_jobs.charts()`) instead of the bare component label.
- Track it under the #723 label-borrowing item (an admission policy restricting who may set `app.kubernetes.io/*` labels).

### 8. Minor: the test does not check that `HandleMigrateCommand` runs before the service starts

**Where:** `deploy/scripts/test_migrate_jobs.py:99`.

The check is substring presence only.

**Mutation (scratch copy):** I moved `dbx.HandleMigrateCommand(migrations.FS)` in `services/ledger/cmd/ledger/main.go` to
after `run(...)`. `MigrateJobFilesTest` passed: 6 tests, OK.

That `ledger migrate` would serve, as the owner role, until the 600 s deadline, and the sync would then fail.

All 57 current mains are correct. Only `slog.SetDefault`, hr's `setupLogging` and finreport's `signal.NotifyContext` run
before the call, and none has side effects.

**Fix:** assert with `go/ast`, or a regex on the first statements of `func main`, that the call precedes any `run`, `consume`
or `os.Args` dispatch. Alternatively, build one binary and check that `<bin> migrate` with no URL exits 1 at once.

### 9. Nit: the TS and Python migrate commands claim "the same contract as dbx.RunMigrateCommand" but take no advisory lock, and the TS ones have no tests

**Where:**
- `services/agents-ts/src/store/pg.ts:52`
- `services/integration-hub-ts/src/pgStore.ts:162`
- `services/prediction-py/src/prediction/migrate.py:3`

Each file runs in a transaction together with its `schema_migrations` primary-key insert. Two overlapping runs, such as an old
Job pod still terminating after `BeforeHookCreation`, therefore fail spuriously instead of applying a file twice. Not
dangerous.

**Fix:** reword the docstring, or add `pg_advisory_lock` on a dedicated connection. Add a vitest for `migrateFromEnv` (URL
precedence, missing URL).

### 10. Nit: `serving_value()` silently disables the render guard when a template sets MIGRATE_ON_START in an unrecognised form

**Where:** `deploy/scripts/gen_migrate_jobs.py:80-92`.

The regex matches only `value: {{ .Values.x | quote }}`. A template using `| toString | quote`, `default`, or a literal gets
`servingMigrateOnStart: ""` and no guard. Today the four empty charts (close, eam, payables, shopfloor) set no
MIGRATE_ON_START, and the full render test still catches a rendered `"true"`.

**Fix:** raise in `serving_value()` when a template mentions `MIGRATE_ON_START` but no path was found.

### 11. Nit: the k3d script (never run)

**Where:** `deploy/scripts/k3d_migrate_test.sh:14,107-108`.

Under `set -e`, a failing `psql_db` (for example `schema_migrations` missing after a Job that "succeeded") aborts the whole
script instead of recording `FAIL <svc>` and moving on. `bash -n` passes; shellcheck is not installed.

The 600 s default deadline may be short for a prod `CREATE INDEX CONCURRENTLY` on a large table. It can be overridden per
environment, and ledger 0032 already handles an INVALID leftover.

## Checked and fine

**Migrate command:**
- Advisory lock: `Migrate` locks `hashtextextended('depot.dbx.migrate:'||current_schema())`. That is session-scoped and per
  database, released when the pod dies, and polled, so it cannot deadlock a CREATE INDEX CONCURRENTLY.
- Exit codes: 0 on success, 1 on failure, logged as JSON.
- Argument parsing: an exact match on `argv[1]` only, and extra arguments are refused (`migrate extra` gives rc 1). No serving
  Deployment, consumer or CronJob passes `migrate`. revrec, payables and the others dispatch `consume` after the migrate
  check.
- `MIGRATE_DATABASE_URL` wins over `DATABASE_URL`, and the Job sets both from the owner Secret, so it cannot fall back to
  `depot_app` (finding 4 covers an owner Secret that holds the wrong role).
- org seeds its reference data on the owner pool after migrating. Verified against a fresh database owned by a non-superuser
  owner role: 36 migrations, 249 countries, 164 currencies and so on. `depot_app` can read them (`0028_grants`), and a rerun
  is idempotent.
- close and eam migrate only with `MIGRATE_ON_START=true`, and no chart sets it. payables, shopfloor, close and eam reach
  their schema only through the Job (see finding 1 for dev).

**Job spec:**
- PreSync with BeforeHookCreation, plus Helm pre-install/pre-upgrade with before-hook-creation. No chart has any other hook or
  sync-wave. A failed Job is kept for its logs and replaced on the next sync, so nothing blocks permanently.
- backoffLimit 2, deadline 600 s, no `ttlSecondsAfterFinished` (which would conflict with Argo hook tracking).
- runAsNonRoot, seccomp, read-only root filesystem (no write needs: Go and Node write nothing; Python bytecode is compiled at
  `pip install` and write failures are ignored), no privilege escalation, `drop: [ALL]`, limits.
- `automountServiceAccountToken: false` on the default SA. No chart uses imagePullSecrets, and workload identity is not needed
  for migrations.
- The cloud-profile env comes through (`depot.cloudEnv`), so the verify-full TLS rule applies to the Job too.

**Render guard:** fails for ledger, transport, sales and receivables with string or boolean `true`, passes with the Job
disabled, and refuses an empty `ownerSecret`.

**NetworkPolicy and Services:** no chart policy or Service selects the Job pod (checked in every render by the test and by
`test_argocd_apps`), the Job opens no port, and `gen_networkpolicies` leaves it out of pod names and peers.

**Dev values exception:** fx, receiving and hello-ledger (`depot_app`) and eam and transport (`depot_*_app`, key `url`) point
at the role that owns those dev schemas, which is required to ALTER tables it owns. `stage_apps.py` strips
`ownerSecret`/`ownerSecretKey` from generated stage Applications, and the stage eam and transport values name the owner
Secrets.

**Entrypoints:**
- Every service with a Dockerfile has its ENTRYPOINT on the binary that calls `HandleMigrateCommand`, multi-binary images
  included (identity, metadata, migration, ops, production).
- agents-ts (distroless node, `args: [dist/main.js, migrate]`), integration-hub-ts (`node src/main.ts migrate` through
  docker-entrypoint) and prediction (`command: python -m prediction.migrate`) all work: smoke-tested below.

## Tests run

All runs used `PATH=$PATH:/usr/local/go/bin:/root/go/bin:/root/.local/bin`, in the worktree at a3d87d69 (clean).

**Generators and render tests:**

| Command | Result |
|---|---|
| `python3 deploy/scripts/gen_migrate_jobs.py --check` | rc 0 |
| `python3 deploy/scripts/gen_networkpolicies.py --check` | rc 0 |
| `python3 deploy/scripts/test_migrate_jobs.py` | 10 tests OK (12 s), helm and kubeconform on PATH |
| `python3 deploy/scripts/test_argocd_apps.py` | 48 tests OK (37 s) |

**Go:**

| Command | Result |
|---|---|
| `go test -count=1 ./dbx/...` (platform/go) with `DEPOT_TEST_DATABASE_URL` (root, socket) and `DEPOT_TEST_APP_DATABASE_URL` (`depot_app:depot_app`, TCP) | ok. The new MigrateURL, RunMigrateCommand (leaves other commands alone, refuses extra args and a missing URL, applies as the owner, idempotent, a failing hook fails) and the existing concurrent-migrate tests pass |
| `go vet` on every one of the 57 changed `cmd/*/main.go` packages | 57 of 57 clean, so all compile |

**helm template + kubeconform -strict:** ledger, agent-runtime, integration-hub, prediction, sales and
depot-migration-factory, all valid.

**Image-parity script** (all 60 charts × {default, tag, digest, registry mirror}): finreport and assets differences as in
finding 3; everything else matches.

**Hand smoke tests (local Postgres 16, port 5432):**

| Run | Result |
|---|---|
| `ledger migrate` on fresh `rev_b09a_2` with `MIGRATE_DATABASE_URL` set and an unreachable `DATABASE_URL` | rc 0, 38 of 38 recorded |
| Rerun | rc 0 |
| No URL | rc 1 |
| `migrate extra` | rc 1 |
| Unreachable database | rc 1 within ms (finding 2) |
| `org migrate` as a non-superuser owner of `rev_b09a_3` | rc 0, 36 migrations, 30 tables owned by the owner, reference data seeded and readable by `depot_app`; rerun rc 0 |
| `ledger migrate` as `depot_app` | rc 0, 41 tables owned by `depot_app` (finding 4) |
| `node src/main.ts migrate` (integration-hub-ts) | rc 0, 9 of 9; no URL rc 1 |
| `node dist/main.js migrate` (agents-ts, built with `tsconfig.build.json`) | rc 0, 9 of 9 |
| `pytest tests/test_migrate.py` (prediction, with `DEPOT_TEST_DATABASE_URL`) | 3 passed |

**Type checks:** `tsc --noEmit` passes for agents-ts and integration-hub-ts.

**Mutations (scratch copy from `git archive`):**

| Mutation | Result |
|---|---|
| Job image always `:latest` | not caught (finding 3) |
| `HandleMigrateCommand` moved after `run()` | not caught (finding 8) |

The other assertions (hook annotations, security context, labels, the `migrate` argument, the close/eam guard, Postgres
admission) were read and are direct equality checks.

**Not run:** the k3d script (no Docker daemon); no live Argo CD.

Scratch databases `rev_b09a_*` and role `rev_b09a_owner` were dropped afterwards.
