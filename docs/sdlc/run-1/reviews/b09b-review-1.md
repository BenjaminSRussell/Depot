# Review: fe2eb629 — B-09 part b, stage platform (#676)

**Verdict: CHANGES REQUIRED**

Reviewed the diff of fe2eb629 (93 files) against B-09 (backlog.md), D4/D26/D28, ADR-0012/0014/0015, in a detached
worktree at the commit.

## What I ran

- `make -C deploy test-argocd`: all green (62 + 15 + 12 + 9 tests, plus `gen_migrate_jobs`, `stage_apps`, `gen_databases --check`). Nothing skipped (helm present).
- `go test` for `services/hr/internal/{numbering,api}`, `services/party/internal/{numbering,api}` and `services/platform-health/internal/api`: ok.
- Drift checks in the scratch worktree, each reverted afterwards:
  - I edited a GRANT in `postgres-hello-ledger.yaml`. `gen_databases --check` exits 1.
  - I added an extra file to `databases/`. Exits 1 ("not generated").
  - I renamed hr's serving Secret in `values.yaml`. Exits 1 (`secrets-hello-ledger.yaml` stale).
  - I re-added a plain listener to the stage Kafka copy. `stage_apps --check` exits 1, and `test_the_stage_kafka_listens_only_with_tls_and_scram` fails.
  - I edited the issuer in the stage SecurityPolicy copy. Exits 1.
  - I added a stray file under `platform/gateway`. Exits 1.
  - I set hello-ledger to `targetRevision: v0.1.0` with a second comment line above it. The Go pin test **passes**; the Python test fails (see m7).
- Provisioning SQL on local PG16: I took the generated `provision.sql` (header, three services, keycloak), renamed roles and databases to `rev_b09b_*`, and used passwords containing `'`, `"`, `\`, `:`, `;--`, `$$` and spaces.
  - It ran twice cleanly, so it is idempotent.
  - The passwords round-trip: every role signs in with its file's password, so the `:'pw'` quoting is safe.
  - CONNECT is enforced: PUBLIC can no longer connect, and each owner and CDC role can connect only to its own database.
  - The shared `depot_app` connects to **every** service database (M3).
  - A CDC role (REPLICATION) streamed a **physical base backup of the whole cluster** (`global/`, i.e. `pg_authid`, and every database) over a `replication` hba line (M2).
  - Scratch databases and roles are dropped.
- A scan of every rendered stage service for Secrets that nothing in stage creates (see m8).

## Findings

### B1 — Blocker: the stage gateway copy serves the API over plain HTTP only, and trusts a placeholder key nobody can sign with
- `deploy/envs/stage/platform/gateway/gateway.yaml:16`: the copy keeps the `http-dev` listener (HTTP, port 80). `deploy/gateway/README.md:54` says "http-dev (80) is for dev only; remove it in any non-dev environment". Nothing in stage issues `depot-api-tls`, the Secret behind the `https` listener (no Certificate references it; the AS2, edge, portal and region gateways each have one). So the only working listener on `depot-api` carries bearer tokens in cleartext.
- `deploy/envs/stage/platform/gateway/securitypolicy.yaml:18-23`: the issuer was changed to `https://idp.stage.depot.dev/realms/depot`, but the provider still uses `localJWKS` from `depot-dev-jwks`. That JWKS holds the dev placeholder key (`kid depot-dev-placeholder`), whose private key does not exist (README "Dev JWKS"). The stage Keycloak's tokens can never validate, so every API request is 401, and D4's exit ("tools/e2e passes against stage") cannot be met.
- Fix in `stage_apps.platform_copies()` (`deploy/scripts/stage_apps.py:483-485`):
  - drop the `http-dev` listener;
  - add a cert-manager Certificate `depot-api-tls` (issuer `depot-stage-ca`, the stage API host), and give the `https` listener a hostname;
  - replace `localJWKS` with `remoteJWKS: {uri: https://idp.stage.depot.dev/realms/depot/protocol/openid-connect/certs}` (or the in-cluster Keycloak Service), and drop `jwks-configmap.yaml` from the stage kustomization;
  - extend `test_the_stage_gateway_and_keycloak_copies_name_stage` to assert: no HTTP listener, every HTTPS certificateRef is issued in stage, and no `localJWKS`.

### M1 — Major: Kyverno does not cover the stage namespaces of batch and metadata, so the tag-pinned images run unsigned
- `deploy/policies/admission/verify-signed-images.yaml:20,75` match only `[hello-ledger, depot-*]`. `batch-stage` and `metadata-stage` run in the namespaces `batch` and `metadata`, and are pinned to the mutable tag `ghcr.io/benjaminsrussell/...:0.2.0`.
- Neither rule applies there: neither digest-pinning nor cosign verification. The commit's safety argument ("pinned to the release version tag, which Kyverno refuses") is false for those two; they would start from an unsigned, mutable tag.
- Fix:
  - extend both rules to every stage and prod service namespace (generate the list from `gen_networkpolicies.env_namespaces`, or use a namespace label);
  - add a test that the policy's namespaces cover every namespace a stage service runs in;
  - or, for images with no recorded digest, refuse to render instead of falling back to a tag.

### M2 — Major: `hostssl replication all all` lets every CDC role take a physical copy of the whole cluster
- `gen_databases.py:294-295` (`postgres-hello-ledger.yaml:531`). Debezium's pgoutput uses *logical* replication connections. Those match the database column of `hostssl all all all`, never the `replication` keyword.
- The `replication` line only enables *physical* replication. Any REPLICATION role can then `pg_basebackup` every database plus `pg_authid` (verified locally with a provisioned CDC role).
- On the shared hello-ledger server there are 57 CDC roles; Kafka Connect holds all their passwords, and the keycloak database sits on the same server. One leaked CDC credential therefore exposes everything.
- Fix: remove the `hostssl replication` line (and update the docstring and `test_stage_postgres_serves_tls_only` to forbid it).

### M3 — Major: one `depot_app` login, with one password, reaches all 58 databases on the shared server
- `gen_databases.py:217,232-233,247`. Every serving Secret in a namespace carries the same `depot_app` password (`<ns>-postgres/app-password`), and `depot_app` has CONNECT on every database (verified).
- RLS gives no protection to a credential holder, who can set the tenant GUC. Any compromised service pod in hello-ledger, or a leaked Secret, can therefore read hr, payroll, party and ledger data. The question "does depot_app get only what RLS needs" is answered no at the database level.
- The migrations grant to `depot_app` by name, but this can still be fixed without touching them:
  - make `depot_app` `NOLOGIN` and give it CONNECT on no database;
  - create one login role per database, `<database>_app`, `IN ROLE depot_app` (`NOBYPASSRLS`), with `GRANT CONNECT, TEMPORARY ON DATABASE <db> TO <db>_app` only;
  - give it a per-service password (`<service>-postgres/app-password`) in the serving Secret.
- The object grants are inherited through membership, but CONNECT is per database. Part c (prod) should not inherit the shared login.

### M4 — Major: one missing Vault key stops every database in a namespace
- `gen_databases.py:261-276,338,379` put every owner and CDC password of the namespace (about 120 keys for hello-ledger) into one ExternalSecret, `postgres-credentials`. The Postgres StatefulSet mounts it, so a single missing key (any one service's `cdc-password`) means the Secret is never written and the server for all 58 services never starts.
- Separately, the PostSync provisioning Job runs only when every resource of `service-databases-stage` is Healthy. A Degraded serving ExternalSecret for one service (for example hr's `fieldcrypt-keys`) therefore blocks provisioning for all of them. With the stage Vault populated by hand (#213), partial population is the likely first state.
- Fix:
  - give the server only `postgres-password` (its own Secret);
  - give the Job the role passwords as per-service projected sources with `optional: true`, and skip a role whose file is missing (`\if` on a `\set` from `test -f`), logging which ones were skipped;
  - move the owner and serving ExternalSecrets into an Application separate from the Postgres and its hook (still at an earlier wave than the services).

### m1 — Minor: passwords go into `postgres://` URLs unencoded
- `gen_databases.py:184`. A Vault password containing `@ : / ? # %` produces a URL that pgx, node-postgres and psycopg mis-parse, while the provisioning Job sets the raw password, so the service cannot sign in.
- Fix: `{{ .password | urlquery | replace "+" "%20" }}` in the template, or document and enforce a URL-safe alphabet for these Vault keys.

### m2 — Minor: the session Valkey ACL file takes raw passwords
- `deploy/session-valkey/templates/externalsecret.yaml:24,26`. A password containing whitespace splits into extra ACL rules (for example `x +@all`), or breaks loading, and the cleartext sits in the ACL Secret.
- Fix: use the hashed form, `#{{ .probe | sha256sum }}` and `#{{ .customer_portal | sha256sum }}` (both sprig, accepted by Valkey).

### m3 — Minor: provisioning can let a migration skip the CDC grant for good
- `gen_databases.py:235-255` creates the CDC role after the owner role, its password and the database. The service migrations (for example ledger 0037 and hr 0016) only WARN and skip `GRANT SELECT ON outbox` when the role is missing, and record the migration as applied. Application health ignores hooks, so a retrying PreSync Job can connect inside that window.
- Fix: create every role (`depot_app`, CDC) first, then set owner passwords and create the databases.

### m4 — Minor: the `service-databases-stage` Application owns and prunes Namespaces
- `gen_databases.py:285` declares `Namespace` hello-ledger, metadata and batch in an Application with `prune: true`. If a file stops being generated, or the Application is deleted with cascade, the whole namespace is deleted, with every service, Keycloak and the gateway in it.
- Fix: annotate the Namespace documents `argocd.argoproj.io/sync-options: Prune=false,Delete=false`, or drop them and rely on `CreateNamespace=true`.

### m5 — Minor: Apicurio and Temporal Postgres are not TLS
- `deploy/postgres` and `deploy/temporal-postgres` (used by `apicurio-postgres-stage` and `temporal-postgres-stage`) have no `ssl`. Temporal's database holds workflow payloads. This contradicts "TLS-only Postgres" as a stage property.
- Fix: give them the same TLS and hba treatment, or record the exception in D4.

### m6 — Minor: numbering timeout copy and classification
- `services/{hr,party}/internal/numbering/client.go:80/78` and `catalog.ts:831,1246`.
  - On a timeout, numbering may already have allocated the number: the request reached it, and only the answer was late. If the caller sent no Idempotency-Key, the retry uses a fresh random key and burns a number. "No party/requisition number was given out" is then not known to be true.
  - `errors.Is(ctx.Err(), DeadlineExceeded)` on the derived ctx also reports the *caller's* shorter deadline as "did not answer within 3s".
- Fix:
  - copy: "The number service didn't answer in time, so nothing was saved." (drop the number claim, in the catalog and in `retryHint`);
  - code: compare against the parent's deadline before the `WithTimeout`, or word the detail without the duration when the parent expired first.

### m7 — Minor: the Go pin test can be bypassed
- `services/platform-health/internal/api/delivery_test.go:114` decides "source of this repo" by finding the repoURL within two lines. A second comment line between `repoURL` and `targetRevision` lets `targetRevision: v0.1.0` pass (verified; the Python test catches it). The image check also only validates `image.tag` parameters that exist; it does not require one.
- Fix: unmarshal the YAML (the repo already vendors a YAML package for Go tests, or parse with a small struct) and walk `spec.source(s)`; or state in the test that `test_argocd_apps.py` is authoritative.

### m8 — Minor: Secrets stage still does not create
- Rendering every stage service shows non-optional Secret references with no ExternalSecret in stage:
  - `agent-runtime-stage`: `agents-provider` (envFrom);
  - `customer-portal-stage` and `supplier-portal-stage`: `*-oidc` (`client-secret`) and `*-signing` (`signing-key`);
  - `receiving-stage`: `receiving-audit-seed` (`AUDIT_SAMPLE_SEED`).
- B-09's scope is "every service database and Secret". Add them as ExternalSecrets, or list them under D4's "Still open" with the reason (portals: B-02).

### m9 — Minor: serving and owner URLs use `sslmode=require`
- `gen_databases.py:184` and the keycloak JDBC URL encrypt but do not authenticate the server, so an in-cluster MITM is possible. Acceptable while the charts cannot mount `depot-stage-ca`, but record it next to the session-store CA gap in D4.

### Nits
- n1:
  - The `postgres` maintenance database keeps PUBLIC CONNECT, so every owner, app and CDC role can sign in there. Add `REVOKE CONNECT ON DATABASE postgres FROM PUBLIC` (and on `template1`) to `provision_sql`.
  - The server container mounts every role password but needs only `postgres-password` (see M4).
- n2: `provision_sql` interpolates identifiers from `connect.yaml` and the dev Secrets unvalidated. Assert `^[a-z_][a-z0-9_]{0,62}$` on every database and role name in `services()`.
- n3: `gen_databases.py:293`: the comment says the socket is "for the readiness probe", but the probe uses `-h 127.0.0.1`.
- n4: the stage realm's SAML `entityId` becomes `https://auth.stage.depot.dev/realms/depot` while Keycloak's host is `idp.stage.depot.dev` (`stage_apps.py:510-511`, generic `.depot.example.invalid` replacement). Map `auth.` to `idp.` too. The provider is still named `depot-dev-idp`.
- n5: the `HCM-NUMBERING-*` and `HCM-LEGAL-ENTITY-REQUIRED` catalog entries were added inside `phiReadLog` (`catalog.ts:1245-1247`). Move them to an hr map.
- n6: the runbook says the portal BFFs keep their per-session limit "in their session store (deploy/session-valkey)". They do not use it yet; today the limit is per pod and in memory.
- n7: the Job passes the superuser password via `PGPASSWORD` in the environment. That is fine; a `PGPASSFILE` built in `/tmp` would keep it out of `/proc/*/environ`.

## Answers to the focus questions

- **Postgres.**
  - pg_hba: TLS-only on TCP with SCRAM. Good, apart from the `replication` line (M2).
  - PUBLIC loses ALL on every service database (verified), but keeps CONNECT on `postgres` (n1).
  - `depot_app`: NOBYPASSRLS, with CONNECT and TEMPORARY only. Per database that is right; across the server it is shared (M3).
  - Owner roles: NOSUPERUSER, NOCREATEDB, NOCREATEROLE, NOREPLICATION, NOBYPASSRLS. They can create the trusted extensions the migrations use (pg_trgm, btree_gist; verified).
  - CDC roles: REPLICATION, with CONNECT on their own database only. NOBYPASSRLS, except `debezium_cdc` on metadata and batch, which mirrors dev. Each role name matches what its migrations grant to (cross-checked: none missing).
- **Provisioning Job.**
  - No injection: passwords go through psql `\set` from files and `:'pw'` literal quoting (verified with hostile passwords).
  - Nothing appears in Job args or logs (`-q`, no echo).
  - Idempotent (ran twice).
  - Ordering (m3) and coupling (M4) need work.
- **ExternalSecret keys.**
  - Every `<chart>-db-owner` key matches the Job's `ownerSecretKey`, and the Job reads `DATABASE_URL` and `MIGRATE_DATABASE_URL` from the same key.
  - Every serving Secret name and key, and hr's two extra keys, match what the rendered chart reads.
  - No chart also creates those Secrets (checked).
  - Other missing Secrets: see m8.
- **Session Valkey.**
  - ACL: default user off; one user per portal with a key prefix and a command allow-list that covers `redisStore.ts`/`resp.ts`; a ping-only probe. Raw passwords: m2.
  - The NetworkPolicy's namespace and `app.kubernetes.io/name` match the portal charts' pod labels (hello-ledger, `customer-portal`/`supplier-portal`), with egress denied.
- **Gateway.** Not correct (B1).
  - Correct: hosts, CA, issuer string, and the OPA bundle ExternalSecret keys (match `authz-opa.yaml`).
  - Wrong: JWKS and listeners.
- **Kafka.** The plain listener is removed, and the generator and test enforce that every listener has TLS and SCRAM. Nothing in stage uses 9092.
- **Generators.** `--check` catches edited, extra and stale outputs, and chart changes (verified).
- **Pinning.**
  - The Python checks are sound: every repo source is at the release tag, there is exactly one image pin, renders are pinned, and there is no zero digest.
  - The Go test is weaker (m7).
  - The tag fallback relies on Kyverno, which misses two namespaces (M1).
  - Nothing can run until v0.2.0 is cut and digests are recorded. This is documented, and it means FR-1.4 is still open.
- **Numbering timeout.** 3s end to end, via both the ctx deadline and the http.Client timeout. Classified correctly for client timeouts; detail and copy need m6.
- **Fail-open.**
  - Justified: the limits are fairness, not access control; JWT and ext_authz fail closed; the store is a single pod without persistence; the 20 ms cost is bounded; the runbook is clear.
  - Caveat: the unauthenticated per-address limits (AS2, region and portal edges) also fail open; the runbook says so.
