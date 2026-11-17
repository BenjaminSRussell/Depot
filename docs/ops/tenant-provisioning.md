# Tenant provisioning and decommission (FR-50.8, BR-50.9)

## Provision: `POST /api/v1/ops/tenants` (`ops.tenant.provision`, `Idempotency-Key` required)

Built in `services/ops` (#50 part 3): the residency check (step 1) and the plan, step tracking and event (step 7). The runner that
performs steps 2 to 6 reports each with `PUT /api/v1/ops/tenants/{id}/provision-steps/{action}` (`realm`, `databases`, `kafka-acls`,
`lakehouse`, `license`); see `services/ops/NOT-IMPLEMENTED.md` for what the runner still needs.
Target: <= 15 min end to end (`provisioning.timeout_min`, default 15). Five concurrent provisions.

Steps, each idempotent so a retry resumes:
1. Validate the tenant's region against its residency (OPS-1705 otherwise, see "Tenant residency" below). EU tenants go to EU regions only.
2. Create the Keycloak realm for the tenant.
3. Create databases and per-tenant schemas with RLS policies (`dbx.RLSPolicy`).
4. Create Kafka ACLs for the tenant topic prefix.
5. Create the lakehouse namespace and object-store prefixes.
6. Install the signed license (`PUT .../license`, Ed25519).
7. Emit `dev.depot.ops.tenant.provisioned.v1` with tenant_id, region, realm, databases[].

Acceptance (AC-50.6): residency EU provisions into `eu-west` with RLS, realm, ACLs and license in <= 15 min.

## Tenant residency (#605)
Every tenant record carries a **residency**: `identity_tenants.residency` (migration `services/identity/migrations/0016_tenant_residency.sql`) is
one of `EU`, `US`, `UK`, `GLOBAL`, and `identity_tenants.region` is where the tenant was placed. `POST /v1/tenants` (identity, operator only)
takes `residency` (default `GLOBAL`, which allows any region; `EU` under the `eu-data-boundary` overlay) and a `region` (required unless the residency is `GLOBAL`), and refuses a region outside the residency, or outside the cloud profile's `allowedRegions` when it lists any, with
422 `OPS-1705` ("Residency region {region} is not deployed; tenant not provisioned" in the PRD catalogue, here also the placement refusal)
before it writes anything: no tenant row, audit row or event exists for a refused placement. Tenants created before this migration are
`GLOBAL` with no region, so nothing changes for them until an operator sets a residency; the evidence report lists them as unplaced.

The map from a residency code to the regions it allows is one file, `platform/go/residency/residency.json`, embedded by the Go package
`platform/go/residency` (`residency.Check`, `residency.Allows`) and read by `deploy/multi-region/tools/residency_report.py`, so the
provisioning check and the auditor's report cannot disagree. Regions are written both as Azure region names (`westeurope`,
`swedencentral`, ...: the `eu-data-boundary` overlay's `allowedRegions` in `deploy/cloud-profiles/profiles.json`) and as the legacy ids of
`deploy/multi-region/values` (`eu-west`, `eu-central`, `eu-north`, `us-east`, `us-west`). `EU` is the EU and EFTA set of the EU Data
Boundary. Tests fail when the table drifts from the cloud profile (Go `TestTableMatchesTheCloudProfile`, Python
`test_residency_report.py`). To add a region, add it to the one residency that owns it in `residency.json`; a region is in at most one residency.

Not built yet: provisioning in the ops control plane (`POST /api/v1/ops/tenants`, the steps above) does not exist in `services/ops`, so only the
identity tenant create path enforces the check today; the ops flow must call `residency.Check(tenant.residency, region)` as its first step.
There is no route to change a tenant's residency or region after creation; moving a tenant is a data migration, not an update.

## Residency evidence report (#605)
For an auditor: tenant -> regions for the database, object store, Kafka and backups, with every tenant whose residency allows less than
where its data is flagged.

```
psql -At -c "select coalesce(json_agg(json_build_object('tenantId', tenant_id, 'name', display_name,
  'residency', residency, 'region', region)), '[]') from identity_tenants" > tenants.json
python3 deploy/multi-region/tools/residency_report.py --tenants tenants.json --json residency.json --csv residency.csv
```

It reads the tenants export, the region value files (`deploy/multi-region/values/**`, merged over the chart defaults) and the residency
table, and writes one row per tenant per data store plus a `violations` list (exit status 1 when there is one). A tenant's data is in its own
region and in the pair peer's (the replica, the MirrorMaker 2 target, the peer's backups and object store), so each store lists both.
Regions the values do not state (the database and, without `objectStore.region`, the object store; an s3 backup without `backup.s3.region`)
are derived from `region.id` and marked `derived`. Violation kinds: `outside-residency` (an EU tenant whose backup is in a US region),
`placement`, `unplaced`, `no-region-values`, `unknown-residency`. See the tool's docstring for the exact rules.

## Real adapters (`services/ops/internal/provision`, #50)
`depot-ops tenant provision|decommission` builds each system's adapter from the `OPS_*` environment (chart values `provisioning.*`, secrets from
`secretKeyRef`). A system with no configuration stays on the logging adapter and a real run is refused, naming what is missing, before the API is
touched; `--dry-run` always works and calls nothing.

| Step | Adapter | Configuration | Provision | Decommission |
|---|---|---|---|---|
| realm | Keycloak Admin REST API | `OPS_KEYCLOAK_URL`, `OPS_KEYCLOAK_CLIENT_ID/SECRET` (client credentials in `OPS_KEYCLOAK_ADMIN_REALM`, default `master`), optional `OPS_KEYCLOAK_REALM_PREFIX` (`depot-`), `OPS_KEYCLOAK_REALM_TEMPLATE` | Creates realm `<prefix><tenant id>` from the template (`deploy/keycloak/realm-depot.json`: `REPLACE_WITH_TENANT_ID` is substituted, the example identity providers are dropped, a `REPLACE_WITH_` left in what remains is refused, and the `depot-services` client gets a hard-coded `tid` claim because its service account has no `tenant_id` attribute) or a built-in minimal realm (security settings, portal roles, the `depot-services` client with the `depot-api` audience mapper and a hard-coded `tid` claim). An existing realm (200 or 409) is DONE. The realm name is reported with the realm step and recorded by ops (`realmName`) | Deletes exactly the recorded realm; absent is DONE only for a recorded name (a name that was never recorded and does not exist is an error, not a guess) |
| databases | pgx against each service database | `OPS_PROVISION_DATABASES` (JSON of context to connection string, or `@file`; as `depot_app`) | See "Decision: databases" | DONE with the detail "tenant data retained under the retention policy; purge separately" (nothing is deleted) |
| kafka-acls | none | none | DONE: "not needed under the shared-topic model" | same |
| lakehouse | Iceberg REST catalog | `OPS_LAKEHOUSE_CATALOG_URL`, optional `OPS_LAKEHOUSE_CATALOG_PREFIX`, `OPS_LAKEHOUSE_CATALOG_TOKEN`, `OPS_LAKEHOUSE_NAMESPACE_PREFIX` (`tenant_`) | `POST /v1/namespaces` for `tenant_<id with underscores>`; 409 is DONE | `DELETE`; 404 is DONE; a namespace that still has tables (409) fails: the tables are the tenant's data |

### Decision: databases
Depot is shared-schema (`docs/architecture.md` E.1, E.2): one database per bounded context, `tenant_id` first in every table, row-level security. A
tenant has no database or schema to create, so the step does not run `CREATE DATABASE`. The tenant's rows (its legal entities in identity, number
sequences in numbering) are written by those services through their own APIs, not by ops. The databases step therefore proves the tenant can start
safely: for each configured service database, connected as `depot_app`, it checks the role has neither SUPERUSER nor BYPASSRLS, that every table
with a `tenant_id` column has row security enabled and forced, and that a session scoped to the new tenant sees no row of another tenant. The
deliberate exceptions are the ones `tools/rlscheck` enforces: both read one list, `platform/go/rlsexempt` (not-forced tables such as
`service_token_replay`, tables with a documented open SELECT policy), and the platform tenant's rows (the nil UUID, system reference data such as org
`country`) are not "another tenant". A table the role cannot read at all is skipped. It writes nothing. It is tested against the real migrated
schemas of org, identity, ledger and agents-ts.

### Decommission keeps the data; the purge is separate and gated
The decommission's `databases` step reports DONE with "tenant data retained under the retention policy; purge separately": deleting the realm already
blocks sign-in, and the data may be under the record-class retention (BR-50.9, 10 years in DE/FR). Deleting it is `depot-ops tenant purge`, which:

1. needs the decommission DECOMMISSIONED and a verified export on record: `depot-ops tenant verify-export --tenant <id> --export-id <id> --note <text>`
   (`PUT /v1/ops/tenants/{id}/export-verification`, duty `ops.tenant.purge`) stores the export id, who verified it and when; the marker is fixed once a
   purge is requested (OPS-1738, OPS-1739);
2. asks `POST /v1/ops/tenants/{id}:purge` (audited) and then deletes with its own credentials, `OPS_PURGE_DATABASES` (its own secret: a role that may
   delete, never the `depot_app` role of the verification) and `OPS_PURGE_KEEP_TABLES` (added to the built-in `identity_tenants`; chart
   `provisioning.purge.secret` and `provisioning.purge.keepTables`);
3. runs each database's deletes in one transaction scoped to the tenant, in foreign-key order, then counts the tenant's remaining rows in every purged
   table in the same transaction. A table whose policy hid rows from the DELETE (it affects zero rows without an error) leaves rows behind and fails the purge;
4. skips, and names as retained, tables that are listed, that have a BEFORE DELETE trigger (append-only ledger and audit tables) or that the role has no
   DELETE grant on, and lists tables keyed by a tenant column not named `tenant_id` (`report_tenant`, `customer_tenant`: platform state) without purging them;
5. prepares every database first and commits only if every one is clean. If any fails, every transaction is rolled back and the failed databases are
   named. The commits themselves are sequential (databases cannot commit atomically): a failing commit reports the databases already committed, and a rerun
   finishes the rest;
6. reports DONE or FAILED with the per-database detail to `PUT .../purge-result` (audited as `tenant.purge.completed` or `tenant.purge.failed`).
   `--dry-run` prints the plan and touches nothing.

### Decision: Kafka
Entity topics are shared and keyed by tenant (architecture C.2, decision D6, Q5), so a per-tenant ACL on them is impossible. External consumers read
egress topics `depot.egress.<tenantSlug>.<context>` that integration creates, with the KafkaUser, when the tenant subscribes
(`POST /v1/integration/event-egress/{context}:subscribe`). Provisioning has nothing to do, and says why in the step detail.

Not verified live: no real Keycloak, catalog or tenant database was available (#213); the adapters are tested against `httptest` fakes and a local
Postgres.

## Decommission: `POST /api/v1/ops/tenants/{id}:decommission` (suspended tenants only)
1. Export all tenant data encrypted, retained for the longest applicable record-class minimum from the
   tenant's country packs (DE/FR: 10 years) unless the customer takes custody and signs a release (BR-50.9).
2. Revoke the realm and ACLs; drop tenant schemas only after the export is verified.
3. Record the decommission in the audit chain.

What is built (#50): `POST /v1/ops/tenants/{id}:decommission` (`ops.tenant.decommission`, body `{"reason": "..."}`) opens it only when identity reports the
tenant `suspended` (409 `OPS-1731` otherwise, 503 `OPS-1730` when identity cannot be asked) and the tenant has a provision; the request, each step and the
completion go to the ops audit trail (`GET /v1/ops/tenants/{id}/decommission` returns it). The steps run in reverse order, lakehouse, Kafka ACLs, databases, realm,
and a step is DONE only when the one before it is (`OPS-1733`); `depot-ops tenant decommission --tenant <id> --reason "..."` runs them with the executors of the
machine (`--dry-run` asks each adapter what it would do and changes nothing), and a rerun resumes at the first step that is not DONE. The last DONE step writes
`tenant.decommissioned.v1`. Steps 1 (the verified export, BR-50.9) and the second approver are not built: until they are, the export is a manual step before the command.
The adapters shipped log what would change (their step detail says "logged only"); a real adapter for each system implements the interfaces in `services/ops/internal/provision`.

## Sandboxes
Sandboxes are re-created from masked production through the same provisioning path with
`provisioning.default_tier` set to DEV, TEST or UAT.
