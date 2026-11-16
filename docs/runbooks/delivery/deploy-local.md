# Deploy locally

Brings up a k3d cluster (1 server, 3 agents), cert-manager, Argo CD, a dev Postgres and hello-ledger.

## Prerequisites

Docker, k3d, OpenTofu (`tofu`), kubectl, openssl, and a 16 GB machine. Ports 80 and 443 must be free. Argo CD syncs from GitHub, so the chart must exist on the revision you deploy.

## Run

```
make -C deploy deploy-local
make -C deploy deploy-local REVISION=my-branch
```

The target builds the image from `services/platform-health/Dockerfile`, imports it into k3d, creates the `hello-ledger-db` Secret with a random password (never committed), and applies the local root Application.

## Reach the service

Add `127.0.0.1 app.local.passage.dev` to `/etc/hosts`, then open `https://app.local.passage.dev/readyz`. The ingress exposes only `/healthz` and `/readyz`; call the entries API with `kubectl -n hello-ledger port-forward svc/hello-ledger 8080:8080` and an `X-Passage-Tenant` header.

Trust the local CA to remove the browser warning:

```
kubectl -n cert-manager get secret passage-local-ca -o jsonpath='{.data.ca\.crt}' | base64 -d > passage-local-ca.crt
```

## Troubleshoot

| Symptom | Check |
|---|---|
| `tofu apply` fails on provider | `provider_kind` other than `k3d` is not implemented |
| pod `CreateContainerConfigError` | Secret `hello-ledger-db` missing in namespace `hello-ledger` |
| Argo app `Unknown` | the revision is not pushed to GitHub, or the repository is private and needs credentials in Argo CD |
| readiness 503 | Postgres pod not ready: `kubectl -n hello-ledger get pods` |

## Monitoring in dev

`root-dev` also syncs the monitoring stack (`deploy/envs/dev/apps/kube-prometheus-stack.yaml`): the
Prometheus Operator CRDs early (wave -2) and the stack itself last (wave 3), so it never holds up the
services. Nothing has to be created by hand: Grafana's admin Secret `grafana-admin` is generated in the
cluster by the External Secrets Operator (`deploy/observability/grafana-admin`). To sign in:

```sh
kubectl -n observability get secret grafana-admin -o jsonpath='{.data.admin-password}' | base64 -d
kubectl -n observability port-forward svc/kube-prometheus-stack-grafana 3000:80
```

If the stack stays Progressing, check `kubectl -n observability get externalsecret grafana-admin`: the
Password generator needs the External Secrets Operator's generator CRDs.

## Outbox connector databases and CDC keys (dev)

The shared dev Postgres (`deploy/envs/dev/apps/postgres.yaml`) creates the database of every outbox connector in
`deploy/kafka/connect.yaml` that points at `postgres.hello-ledger.svc`, and the connector's REPLICATION role
`debezium_cdc_<service>`. The role's password is the Vault key `passage/dev/<service>-postgres/cdc-password`, which
Kafka Connect reads too (`debezium-cdc-<service>` in `deploy/envs/dev/kafka-secrets/debezium-cdc.yaml`).

- For the `95-<service>-db.sh` services (billing, budgeting, warehouse, intercompany, fieldservice, ops, receivables,
  expense, projects, party, product, purchasing, production, hr) the key is optional. Without it the pod still starts, the
  database is created, and the init log says `debezium_cdc_<service> not created`; that connector fails to log in.
- To enable one: write the key (URL-safe hex, property `value`), wait for the ExternalSecrets `<service>-cdc`
  (namespace `hello-ledger`) and `debezium-cdc-<service>` (namespace `kafka`) to sync, then restart the Postgres pod:
  `kubectl -n hello-ledger rollout restart deploy/postgres`. Init scripts run only on an empty data directory, which is
  an emptyDir, so the restart recreates every dev database.
- The receiving, eam, transport, sales and fulfillment keys stay required: a missing one keeps the pod in
  `CreateContainerConfigError`.
- The Postgres allows 32 replication slots and walsenders, one each per connector; raise `max_replication_slots` and
  `max_wal_senders` in its args before adding connectors past that (`deploy/scripts/test_argocd_apps.py` checks).

## Tear down

`make -C deploy destroy-local`

## Promote or roll back stage

Stage is a separate Argo CD root application at `deploy/argocd/root-stage.yaml`.
Its child Application reads the chart at the Git revision selected by `main`
and pins the container by SHA-256 digest. A promotion or rollback edits only
`deploy/envs/stage/apps/hello-ledger.yaml`; commit that change and merge it
through a reviewed PR so Argo CD records the merged Git revision in Application
history.

After verifying that a digest was built, scanned, signed, and healthy in dev:

```sh
make -C deploy promote-stage IMAGE_DIGEST=sha256:<64-lowercase-hex-characters>
git diff --check
# Commit the stage manifest change on a branch and merge its PR.
argocd app history hello-ledger-stage
argocd app get hello-ledger-stage
```

To restore the previous distinct, non-placeholder digest recorded in committed
stage-manifest history:

```sh
make -C deploy rollback-stage
git diff --check
# Commit the rollback manifest change on a branch and merge its PR.
argocd app history hello-ledger-stage
argocd app get hello-ledger-stage
```

Both commands refuse an uncommitted stage manifest. Promotion accepts only a
lowercase `sha256:<64 hex characters>` digest. Rollback refuses when Git history
has no prior non-placeholder digest. The stage manifests use a self-signed CA
only if one is provisioned for internal/local proof. The stage cluster must be
registered in Argo CD as `stage`, provide the `passage-stage-ca` ClusterIssuer
and `hello-ledger-db` Secret outside Git, and use a trusted certificate issuer
for real deployments.

### The other services in stage

Every service with a dev Application has a stage one, generated from dev by
`python3 deploy/scripts/stage_apps.py`: the `services-stage` ApplicationSet
(`deploy/envs/stage/apps/services.yaml`, the dev element list) and
`<name>-stage` for fx, identity, org and receiving. Each Application is named
`<name>-stage` but keeps the dev Helm release name, targets the cluster
registered as `stage`, reads `deploy/envs/stage/values/<name>.yaml` (stage
hostnames, the stage realm and issuer, two replicas where the chart default is
one), and syncs automatically like `hello-ledger-stage`. Do not edit the
generated files; change dev or the script and rerun it
(`test_argocd_apps.py` fails when they are stale). The image digest is the zero
placeholder until the service's image is built and signed (the `image` matrix in
`ci.yml` covers hello-ledger, fx, receiving and org); pin it in the dev and
stage files by a reviewed PR. `stage_release.py` promotes and rolls back only
hello-ledger. Each service's database Secret and the shared infrastructure
(Postgres, Kafka, Keycloak, Temporal, the gateway and its overlay with stage
hostnames, Envoy Gateway and monitoring) are not yet Applications in stage.

## Promote or roll back prod

Prod is the Argo CD root application at `deploy/argocd/root-prod.yaml`, reading
`deploy/envs/prod/apps`. Like stage, it syncs automatically with prune and
self-heal; the reviewed PR merge is the approval gate. Prod promotes only the
digest stage pins on `origin/main`, once stage has pinned it for at least 60
minutes (the script refuses otherwise), so stage and prod consume the same signed
release artifact. `--skip-verify` is refused for prod. The check for zero
`SLOBurnRateHigh` alerts remains manual until the Release table lands.

Prerequisite: register the prod cluster in Argo CD only after a repo admin enables
branch protection on `main` (required review plus code-owner review).
`.github/CODEOWNERS` already covers everything via `*`. Until then any push to
`main` auto-deploys prod.

The 60-minute soak is measured from the committer date of the `origin/main`
commit that pinned the digest in stage. That is reliable for GitHub merges but not
for direct pushes of old commits, which keep their original committer date.

```sh
make -C deploy promote-prod IMAGE_DIGEST=<digest pinned in deploy/envs/stage on main>
# Commit the prod manifest change on a branch and merge its PR.
# Argo syncs on merge: watch root-prod -> hello-ledger-prod until Healthy.
argocd app get hello-ledger-prod
argocd app history hello-ledger-prod
```

To roll back, restore the prior distinct digest from the prod manifest history on
`origin/main` (its signature is re-verified first):

```sh
make -C deploy rollback-prod
# Commit the rollback manifest change on a branch and merge its PR; Argo syncs on merge.
argocd app history hello-ledger-prod
```

The FR-1.10 target is the prior digest active within 5 minutes of the merge.

The prod cluster must be registered in Argo CD as `prod` and provide the
`passage-prod-ca` ClusterIssuer and `hello-ledger-db` Secret outside Git.
