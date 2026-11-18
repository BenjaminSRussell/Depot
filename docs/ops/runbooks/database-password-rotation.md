# Runbook: rotate a service database password (stage)

Applies to the roles `deploy/scripts/gen_databases.py` provisions in stage: a service's owner role `<database>_owner`
(the migration Job), its login role `<database>_app` (the serving pods), its CDC role (Kafka Connect), and each Postgres
superuser. Their passwords live in the secret store under `depot/stage/` (the paths are in `deploy/envs/stage/README.md`).

Why there are steps: the provisioning CronJob applies a new Vault value to the database within 15 minutes, the
ExternalSecrets refresh hourly (`refreshInterval: 1h`), and pods read `DATABASE_URL` only at start. Between the database
taking the new password and the pods restarting with it, new connections from running pods fail, so do the steps
back to back.

Below, `<svc>` is the service directory (for example `ledger`), `<ns>` its namespace (`hello-ledger`, `batch` or
`metadata`), `<serving>` the serving Secret and `<release>` the Deployment.

## 1. Write the new value

Write the new password to its Vault key, for example `depot/stage/<svc>-postgres/app-password`. Use a generated
password; any character is safe (the URLs URL-encode it and the provisioning reads it from a file).

## 2. Force-sync the ExternalSecrets

```sh
ts=$(date +%s)
kubectl -n <ns> annotate externalsecret postgres-role-<svc> force-sync=$ts --overwrite   # what the provisioning reads
kubectl -n <ns> annotate externalsecret <serving> force-sync=$ts --overwrite             # app password: the serving Secret
kubectl -n <ns> annotate externalsecret <chart>-db-owner force-sync=$ts --overwrite      # owner password: the Job's Secret
```

For a CDC password, also annotate `debezium-cdc-<svc>` in namespace `kafka`. Check each shows `SecretSynced`
(`kubectl -n <ns> get externalsecret`).

## 3. Apply it to the database now

```sh
kubectl -n <ns> create job --from=cronjob/postgres-provision postgres-provision-rotate-$ts
kubectl -n <ns> logs -f job/postgres-provision-rotate-$ts
```

The run takes an advisory lock, so it waits for a CronJob run already in progress. A `skipped:` line for the role means
its password file is still missing: go back to step 2.

## 4. Restart what uses it

- App password: `kubectl -n <ns> rollout restart deploy/<release>` (and any CronJob of the chart picks it up at its
  next run).
- Owner password: nothing to restart; the next sync's migration Job reads the new Secret.
- CDC password: restart the connector (`kubectl -n kafka annotate kafkaconnector outbox-<svc> strimzi.io/restart=true`).
- Superuser password (`<ns>-postgres/postgres-password`): only the provisioning uses it after initdb. Step 3 cannot
  set it (it signs in with it): run `ALTER ROLE depot PASSWORD ...` as the superuser first, then steps 1 to 3.

## 5. Verify

The service is ready (`kubectl -n <ns> rollout status deploy/<release>`), its logs show no authentication failures, and
for CDC the connector is `RUNNING`.
