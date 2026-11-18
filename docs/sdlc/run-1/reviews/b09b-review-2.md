# Review round 2: c16d2d7e, the B-09b fixes (#676)

**Verdict: APPROVE.** Every round-1 Blocker and Major is fixed and holds up under the break tests. Four Minors and three Nits remain. None of them blocks the merge, and each can be done in a follow-up or in part c.

I reviewed the diff of c16d2d7e (base c68ecbb2) against `b09b-review-1.md`. The work was done in a detached worktree at c16d2d7e, now removed. Nothing was committed.

## What I ran

**Test suites**
- `make -C deploy test-argocd` passes:
  - `test_argocd_apps.py` (67 tests);
  - `test_kafka_consumers.py` (15);
  - `test_migrate_jobs.py` (12);
  - the session Valkey tests (9);
  - `gen_migrate_jobs`, `stage_apps` and `gen_databases` with `--check`.
- `go test` passes for:
  - hr's and party's `numbering` and `api` packages;
  - `platform-health/internal/api`.
- `deploy/cloud-profiles/test_egress_render.py` passes.

**Multi-region claim: confirmed**
- `deploy/multi-region/test_chart.py` fails the same 6 schema tests at c16d2d7e and at 2499557f, which is before B-09b.
- The 6 tests are:
  - `test_schema_rejects_an_unknown_store`;
  - `test_schema_rejects_bad_retention_and_schedule`;
  - `test_dns_ttl_range`;
  - `test_failover_lag_range`;
  - `test_mm2_lag_alert_floor`;
  - `test_role_enum`.
- `git diff 2499557f c16d2d7e -- deploy/multi-region` is empty.
- The cause is the helm version. With helm v3.16, the failure message reads `region.role: region.role must be one of …`, and the tests expect a JSON-pointer path such as `/region/role`.

**Break tests.** Each one was reverted afterwards.

| Break | Caught by | Result |
|---|---|---|
| Edit a generated GRANT | `gen_databases --check` | exit 1 |
| Extra `service-secrets/zzz/` | `gen_databases --check` | exit 1 ("not generated") |
| Rename hr's serving Secret in the chart | `gen_databases --check` | exit 1 (`service-secrets/hr` stale) |
| Generator writes `hostssl replication` again | `test_stage_postgres_serves_tls_only_without_physical_replication` | fails |
| `stage_apps` keeps `http-dev` | `test_the_stage_api_is_https_only_and_trusts_the_stage_realm` | fails |
| `localJWKS` back in the stage SecurityPolicy | same test | fails |
| Kyverno drops `batch` | `test_kyverno_verifies_images_in_every_stage_service_namespace` | fails (both policy files) |
| Remove the `agents-provider` UNMANAGED entry | `test_every_secret_a_stage_service_reads_is_created_or_explained` | fails |
| `depot_app WITH LOGIN` in the generated SQL | 2 StagePlatform tests | fail |
| A non-optional projected role source | `test_one_missing_vault_key_stops_only_its_own_service` | fails |
| hello-ledger at `v0.1.0` behind two comment lines | Go `TestStageApplicationsArePinnedToTheRelease` | fails (the round-1 bypass is closed) |
| fx `repoURL` without `.git`, at `v0.1.0` | Go test | **passes**; the Python test catches it (n1) |

**Provisioning on a scratch PG16 cluster** (`pg_createcluster 16 rev_b09b2`, since dropped)
- Setup:
  - The cluster had `wal_level=logical`.
  - Its pg_hba matched stage's lines, without `ssl`.
  - I ran the generated `provision.sql` for agents, hr, ledger and keycloak, with roles and databases renamed to `rev_b09b2_*`.
  - It ran through the generated `provision.sh`.
  - The superuser password contained `:`, `\` and `%`, to test the pgpass escaping.
- Passwords:
  - The role passwords contained `'`, `"`, `\`, `:`, `@`, `/`, `?`, `#`, `%`, `+` and spaces.
  - hr's `app-password` file was left empty and its `cdc-password` file absent.
- Results:
  - **Idempotent:** two runs, both exit 0. The second run prints only a NOTICE that the membership was already granted (see n3).
  - **pgpass escaping works:** the script connected through `p\:w\\d%s`.
  - **Every login works with its hostile password.** For hr, `cdc` and `app` stay NOLOGIN and the run prints a `skipped:` line for each, naming the Vault key.
  - **`depot_app` is NOLOGIN** with a NULL password.
  - **CONNECT is per database:**
    - `ledger_app` is refused on agents, hr and `postgres`;
    - `ledger_owner` is refused on agents;
    - `cdc_ledger` is refused on hr.
  - **`postgres` and `template1` no longer grant PUBLIC CONNECT.**
- Ledger's 40 migrations, run as `ledger_owner` with the role names mapped:
  - `ledger_app` inherits the grants through `depot_app`: SELECT on `ledger`, INSERT on `journal_entry`, no UPDATE on it.
  - RLS is FORCEd on `ledger`, `journal_entry` and `outbox`.
  - `ledger_app` cannot create tables.
  - `cdc_ledger` holds SELECT on `outbox` only. The CDC role existed before the migration ran, so the m3 grant race is gone.
- Replication (M2):
  - A physical replication connection and `pg_basebackup` are both refused: "no pg_hba.conf entry for replication connection".
  - A pgoutput logical slot can be created on the role's own database.
  - A logical replication connection to another database is refused (no CONNECT).
- I checked the URL encoding in Go with `text/template`, `urlquery`, `replace "+" "%20"` and `net/url`. All four hostile passwords round-trip exactly, including literal `+`, spaces and non-ASCII.
- I also confirmed that the migrate commands' `AppRoleQuery` (Go, TS and Python) refuses any member of `depot_app`. A `<db>_app` login therefore cannot run migrations; only the owner role can.

## Status of the round-1 findings

| # | Status | Evidence |
|---|---|---|
| B1 | Fixed | `http-dev` gone; HTTPS on `app.stage.depot.dev` with `api-certificate.yaml` (stage CA); `remoteJWKS` from the stage realm; tested. Reachability of the JWKS host is m-a below. |
| M1 | Fixed | Both policy files match `hello-ledger, batch, metadata, depot-*`; the test walks every dev and stage service namespace. |
| M2 | Fixed | Verified on PG16 (above); also no replication line in the Apicurio and Temporal hba. |
| M3 | Fixed | Verified on PG16 with ledger's migrations (above). |
| M4 | Fixed | The server mounts only `postgres-superuser` and `postgres-tls`. Role passwords are optional projected sources, and a missing one is skipped and reported. There is one Application per service (`service-secrets-stage`, 63 elements matching 63 directories). |
| m1 | Fixed | `urlquery \| replace "+" "%20"`, verified. |
| m2 | Fixed | `#{{ … \| sha256sum }}` in the ACL file; a test forbids `>`. |
| m3 | Fixed | Every role is created before any database (verified). |
| m4 | Fixed | Namespaces carry `Prune=false,Delete=false`; tested. |
| m5 | Fixed | `tls.enabled` in `deploy/postgres` and `deploy/temporal-postgres` (cert DNS names match the Services `postgres` and `temporal-postgres-rw`); Apicurio JDBC uses `sslmode=require`; Temporal uses SQL TLS without host verification. |
| m6 | Fixed | The copy says only "nothing was saved". When the caller's deadline is the earlier one, the detail says so instead of naming 3 s (`callerFirst`). |
| m7 | Fixed | The Go test parses YAML and walks every source, including ApplicationSet elements; it requires an image pin on each service source. |
| m8 | Fixed | ExternalSecrets for `receiving-audit-seed` and the portals' OIDC and signing Secrets; `agents-provider` is in UNMANAGED with a reason; a test covers every rendered stage service. |
| m9 | Fixed | Keycloak uses `sslmode=verify-full` with the stage CA (only `ca.crt` projected); the service URLs are documented as `require`. |
| Nits | Fixed | PUBLIC CONNECT revoked; identifiers checked against `^[a-z_][a-z0-9_]{0,62}$`; pgpass file instead of `PGPASSWORD`; catalog entries moved to `hrRecruiting` (defined before the spread); runbook wording; realm `auth.` host; provider name. |

## New findings

### m-a — Minor: the stage gateway cannot reach its JWKS host, which nothing in Git serves
- **Where:**
  - `deploy/scripts/stage_apps.py:159-160`;
  - `deploy/envs/stage/platform/gateway/securitypolicy.yaml` (`remoteJWKS.uri`);
  - `deploy/envs/stage/README.md:52`.
- **Problem:**
  - Envoy fetches `https://idp.stage.depot.dev/...` over public DNS and checks its certificate against the system trust bundle.
  - No HTTPRoute, listener or Certificate in the repository serves that host. In stage, Keycloak is only a ClusterIP on HTTP 8080.
  - Every other stage host is issued by the private `depot-stage-ca`, which the system bundle does not trust.
  - Until something outside Git fronts Keycloak with a publicly trusted certificate, every API request is 401.
- **Why Minor:** it fails closed, it is documented in the README, and it does not reopen the round-1 hole.
- **Fix:** either of these.
  - Route `idp.stage.depot.dev` in Git: a listener on a Gateway with a stage Certificate, plus an HTTPRoute to `keycloak:8080`. Then give remoteJWKS `backendRefs` and a BackendTLSPolicy that trusts `depot-stage-ca`.
  - Or point remoteJWKS at the in-cluster `keycloak.hello-ledger.svc:8080` through `backendRefs`. The keycloak NetworkPolicy already admits the `depot-api` Envoy pods on 8080. The issuer stays `https://idp.stage.depot.dev/realms/depot`.
- Add the chosen host or backend to the B1 test, and keep the item under D4 "Still open" until it is done.

### m-b — Minor: the service-secrets Applications are not ordered before the services
- **Where:** `gen_databases.py:521-526`, `deploy/envs/stage/apps/service-secrets.yaml`.
- **Problem:**
  - root-stage's sync waves order the two ApplicationSet resources, `service-secrets-stage` (wave -1) and `services-stage` (wave 2).
  - The waves do not order the Applications those sets generate. On a first install, a service's PreSync migration Job can start before its `<svc>-secrets-stage` Application has written `<chart>-db-owner`.
  - This mostly self-heals: the pod sits in CreateContainerConfigError until the Secret appears, and the sync has a retry. But it no longer meets D4's rule "a separate Application in an earlier sync wave" as written.
- **Fix:** either of these.
  - Make the per-service Secrets Applications children of root-stage (generate plain Applications, as `stage_apps` does for services).
  - Or keep the ApplicationSet and state the reliance on the CreateContainerConfigError retry in D4, with the migrate Job's `activeDeadlineSeconds` (600 s) as the bound.

### m-c — Minor: the PostSync Job and the CronJob can collide
- **Where:** `gen_databases.py:~495-505`.
- **Problem:**
  - `concurrencyPolicy: Forbid` only covers the CronJob's own Jobs.
  - When the PostSync Job and a CronJob run overlap on a fresh database, both can pass `WHERE NOT EXISTS` and issue the same `CREATE ROLE` or `CREATE DATABASE`.
  - One then fails with a duplicate and, because of `ON_ERROR_STOP`, retries through `backoffLimit`. That is harmless but noisy, and it marks the PostSync hook failed for a cycle.
- **Fix:** take `SELECT pg_advisory_lock(<constant>)` at the top of `provision.sql`. It is released at session end.

### m-d — Minor: role-password rotation has no procedure
- **Problem:**
  - The CronJob applies a rotated Vault value to the database within 15 minutes.
  - The serving and owner Secrets refresh hourly (`refreshInterval: 1h`).
  - Pods read `DATABASE_URL` only at start.
  - So after a rotation, new connections from running pods fail until the pods restart with the new Secret.
- **Fix:** add a short rotation note under D4 or a runbook:
  1. Write the new value in Vault.
  2. Force-sync the ExternalSecrets (annotate `force-sync`).
  3. Wait for the CronJob, or run it now with `kubectl create job --from=cronjob/postgres-provision`.
  4. Restart the service.

### Nits
- **n1** (`delivery_test.go:110`): the Go test recognises this repository only by the exact `…/Depot.git` URL. A source with `repoURL: …/Depot` (no `.git`) at `v0.1.0` passes as a "chart" with a semver. The Python test catches this. Fix: match the URL with `strings.TrimSuffix(repo, ".git")`, or fail any github.com URL that is not exactly `REPO_URL`.
- **n2** (`test_one_missing_vault_key_stops_only_its_own_service`): the test lists only `secret` volumes of the server. A role Secret added as a `projected` source on the StatefulSet would pass. Also collect `projected.sources[].secret.name`.
- **n3** (`gen_databases.py:305`): `GRANT depot_app TO <role>` prints a NOTICE on every run, which is 96 a day in the CronJob logs. Wrap it in `SELECT 'GRANT …' WHERE NOT pg_has_role('<role>','depot_app','MEMBER')\gexec`.
- Accepted as is, no action: a role keeps LOGIN with its last password if its Vault key is later removed. That is acceptable for stage; part c may want to revoke LOGIN when the file is missing and the role exists.

## Answers on the areas asked about

- **CronJob:** sound. Same pod as the Job, label admitted by the Postgres NetworkPolicy, superuser password in a 0600 pgpass under `/tmp`, `restricted` security context, Forbid within its own schedule. Concurrency with the PostSync Job is m-c.
- **Optional mounts:** sound. One optional projected source per service, the server unaffected, a missing or empty file means the role stays NOLOGIN with a `skipped:` line (verified). ESO writes a Secret only when every key resolves, so one missing key affects all three roles of that one service only.
- **ApplicationSet:** sound. goTemplate with `missingkey=error`, a list matching the directories (tested), release tag pinned, prune with finalizer, ExternalSecret templates in plain directory sources (no double templating). Ordering is m-b.
- **remoteJWKS trust:** system trust for a public host is reasonable, since the attacker would need a publicly trusted certificate for `idp.stage.depot.dev`. The gap is that nothing serves the host (m-a).
