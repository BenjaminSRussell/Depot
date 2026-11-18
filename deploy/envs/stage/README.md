# Stage environment

`root-stage` (`deploy/argocd/root-stage.yaml`) tracks main and applies `apps/` to the Argo CD that manages the cluster
registered as `stage`. Nothing here is written by hand except `release.yaml`, the stage values in `values/`, and
`apps/hello-ledger.yaml` (whose digest `deploy/scripts/stage_release.py` promotes):

| Path | Written by | What |
|---|---|---|
| `release.yaml` | hand | The release tag every stage Application reads, and the signed image digests (B-09 part b) |
| `apps/` | `deploy/scripts/stage_apps.py` | Every service and platform Application, generated from `deploy/envs/dev/apps` |
| `apps/namespace-default-deny.yaml` | `deploy/scripts/gen_networkpolicies.py` | Default-deny ingress in every service namespace |
| `platform/` | `deploy/scripts/stage_apps.py` | Stage copies of dev manifests that name dev: Kafka without its plain listener, the gateway (stage hosts, CA and issuer, the ext_authz OPA's bundle Secret), Keycloak (stage host, database and admin Secrets), the stage CA issuer |
| `databases/` | `deploy/scripts/gen_databases.py` | A TLS-only Postgres per service namespace, every service database with its owner role, and the owner and serving Secrets as ExternalSecrets |
| `kafka-secrets/` | `deploy/scripts/gen_databases.py` | Kafka Connect's CDC Secrets and Apicurio's database Secret, from the stage Vault keys |

Run `python3 deploy/scripts/stage_apps.py` and `python3 deploy/scripts/gen_databases.py` after changing dev, a chart or
`release.yaml`; `make -C deploy test-argocd` fails while a generated file is stale.

## Pins

- **Charts and manifests.** Every source of this repository reads `release.yaml`'s `revision` (a `vX.Y.Z` git tag), never
  main; third-party charts pin their exact version. To promote: cut the tag on the reviewed main commit, set `revision`,
  regenerate and merge. Until the tag exists, Argo CD cannot resolve the stage revision (v0.2.0 is not cut yet).
- **Images.** A service with a digest under `digests` in `release.yaml` is pinned to it (copy it from the CI image job's
  "Record the signed digest" line). Any other is pinned to the release version tag. Kyverno's `verify-signed-images`
  refuses a tag in stage, so a service without a recorded digest does not start. As built no digest is recorded and CI
  builds only four images (hello-ledger, fx, receiving, org): no Depot service can run in stage yet. No stage file carries
  the all-zero placeholder digest (`test_argocd_apps.py`, `services/platform-health/internal/api/delivery_test.go`).

## Prerequisites outside Git (#213)

cert-manager, the External Secrets Operator and a ClusterSecretStore named `depot` on the stage cluster (on Azure,
`deploy/secret-store`); the Vault keys the ExternalSecrets read under `depot/stage/` (each service's
`<service>-postgres/owner-password` and `cdc-password`, each namespace's `<namespace>-postgres/postgres-password` and
`app-password`, `session-valkey/*`, `keycloak/*`, `authz-opa-bundle/*`, and the extra keys a serving Secret needs, such as
hr's field-encryption keys); the release tag. Monitoring (D8), the payment webhook edge and web and portal entries (B-02)
are not in stage yet.
