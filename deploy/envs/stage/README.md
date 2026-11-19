# Stage environment

`root-stage` (`deploy/argocd/root-stage.yaml`) tracks main and applies `apps/` to the Argo CD that manages the cluster
registered as `stage`. Nothing here is written by hand except `release.yaml`, the stage values in `values/`, and
`apps/hello-ledger.yaml` (whose digest `deploy/scripts/stage_release.py` promotes):

| Path | Written by | What |
|---|---|---|
| `release.yaml` | hand | The release tag every stage Application reads, and the signed image digests (B-09 part b) |
| `apps/` | `deploy/scripts/stage_apps.py` | Every service and platform Application, generated from `deploy/envs/dev/apps` |
| `apps/namespace-default-deny.yaml` | `deploy/scripts/gen_networkpolicies.py` | Default-deny ingress in every service namespace |
| `apps/service-secrets.yaml` | `deploy/scripts/gen_databases.py` | One Application per service for its Secrets (`service-secrets/<service>`), children of root-stage at wave -1, so they sync before the services |
| `platform/` | `deploy/scripts/stage_apps.py` | Stage copies of dev manifests that name dev: Kafka without its plain listener; the gateway (HTTPS only for `app.stage.depot.dev` with a stage certificate, tokens of the stage realm checked against its published keys, stage hosts and CA, the ext_authz OPA's bundle Secret); Keycloak (stage host, database over verified TLS, database and admin Secrets); the stage CA issuer |
| `databases/` | `deploy/scripts/gen_databases.py` | A TLS-only Postgres per service namespace with its roles, databases and provisioning Job and CronJob |
| `service-secrets/` | `deploy/scripts/gen_databases.py` | Per service: owner and serving database Secrets, the role passwords the provisioning reads, and its other Secrets |
| `kafka-secrets/` | `deploy/scripts/gen_databases.py` | Kafka Connect's CDC Secrets and Apicurio's database Secret, from the stage Vault keys |

Run `python3 deploy/scripts/stage_apps.py` and `python3 deploy/scripts/gen_databases.py` after changing dev, a chart or
`release.yaml`; `make -C deploy test-argocd` fails while a generated file is stale.

## Pins

- **Charts and manifests.** Every source of this repository reads `release.yaml`'s `revision` (a `vX.Y.Z` git tag), never
  main; third-party charts pin their exact version. To promote: cut the tag on the reviewed main commit, set `revision`,
  regenerate and merge. Until the tag exists, Argo CD cannot resolve the stage revision (v0.2.0 is not cut yet).
- **Images.** A service with a digest under `digests` in `release.yaml` is pinned to it (copy it from the CI image job's
  "Record the signed digest" line). Any other is pinned to the release version tag. Kyverno's `verify-signed-images`
  refuses a tag in every service namespace (hello-ledger, batch, metadata), so a service without a recorded digest does not
  start. As built no digest is recorded and CI builds only four images (hello-ledger, fx, receiving, org): no Depot service
  can run in stage yet. No stage file carries the all-zero placeholder digest (`test_argocd_apps.py`,
  `services/platform-health/internal/api/delivery_test.go`).

## Databases

Per database: an owner role `<database>_owner` (the migration Job), a login role `<database>_app` (the serving pods; a
member of `depot_app`, which the migrations grant to and the RLS policies name, and which itself cannot sign in or connect
anywhere) with CONNECT on its own database only, and the outbox connector's REPLICATION role with CONNECT on its own database
only. pg_hba admits TCP only over TLS with SCRAM and has no physical `replication` line. A role signs in once its Vault key
exists; the provisioning Job and the CronJob (every 15 minutes, one run at a time under an advisory lock) report the ones
still waiting. To rotate a password, follow `docs/ops/runbooks/database-password-rotation.md`. Database URLs URL-encode the
password and use `sslmode=require`: the service charts cannot mount the stage CA yet, so they encrypt without verifying the
server (Keycloak and the provisioning Job verify it with `verify-full`).

## Prerequisites outside Git (#213)

- cert-manager, the External Secrets Operator and a ClusterSecretStore named `depot` on the stage cluster (on Azure,
  `deploy/secret-store`).
- The Vault keys under `depot/stage/`: per namespace `<namespace>-postgres/postgres-password`; per service
  `<service>-postgres/owner-password`, `app-password` and, for an outbox, `cdc-password`; `keycloak-postgres/owner-password`
  and `keycloak/*`; `session-valkey/*`; `authz-opa-bundle/*`; the extra keys of a serving Secret (hr's field-encryption
  keys); `receiving/audit-sample-seed`; the portals' `customer-portal/*` and `supplier-portal/*` (OIDC client secret and
  service signing key). The release tag.
- Browser sign-in needs `idp.stage.depot.dev` to reach Keycloak; nothing in the repository routes that host yet. The gateway
  does not depend on it: it fetches the realm's keys from the in-cluster Keycloak Service (`remoteJWKS.backendRefs`,
  `keycloak:8080`, which Keycloak's NetworkPolicy admits from the depot-api Envoy pods).
- Not created by stage: `agents-provider` (agent-runtime's model provider credentials, read with envFrom; their keys depend
  on the provider stage chooses). Monitoring (D8), the payment webhook edge and web and portal entries (B-02) are not in
  stage yet.
