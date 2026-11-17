#!/usr/bin/env bash
# k3d proof of the owner-role migration Jobs (D28, ADR-0012 rule 3, B-09a): on a fresh k3s cluster (which enforces
# NetworkPolicy with its embedded controller) it starts Postgres behind the dev Postgres NetworkPolicy and the namespace
# default-deny, gives each service an empty database owned by its own owner role plus the depot_app role, builds the
# service image, and `helm install`s the chart. The chart's migrate Job runs as a pre-install hook (the same Job Argo CD runs
# as PreSync); the script then checks that every migration file is recorded, that the tables belong to the owner role (not
# depot_app), that the serving Deployment has MIGRATE_ON_START=false, and that no Service selects the Job's pod.
#
#   make -C deploy test-migrate-k3d                      # the default services
#   make -C deploy test-migrate-k3d SERVICES="ledger fx"  # services/<dir> with a Dockerfile and migrations
#   KEEP=1 make -C deploy test-migrate-k3d               # leave the cluster up afterwards
#
# Needs docker, k3d, kubectl and helm. Services without a Dockerfile (billing, pricing and others) cannot be built here yet.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/../.." && pwd)
CLUSTER=${CLUSTER:-depot-migrate-test}
NS=hello-ledger
SERVICES=${SERVICES:-"ledger fx platform-health"}
CTX="k3d-$CLUSTER"
PG_IMAGE=$(sed -n 's/^ *image: \(postgres:17-alpine@sha256:[0-9a-f]*\)$/\1/p' "$REPO/deploy/envs/dev/apps/postgres.yaml" | head -1)
k() { kubectl --context "$CTX" -n "$NS" "$@"; }
psql_db() { k exec deploy/postgres -- psql -v ON_ERROR_STOP=1 -U depot -d "$1" -Atc "$2"; }
psql_super() { psql_db postgres "$1"; }

for tool in docker k3d kubectl helm python3; do
  command -v "$tool" >/dev/null || { echo "k3d_migrate_test: $tool is required" >&2; exit 2; }
done
[ -n "$PG_IMAGE" ] || { echo "k3d_migrate_test: no pinned postgres image in deploy/envs/dev/apps/postgres.yaml" >&2; exit 2; }

cleanup() { [ "${KEEP:-}" = 1 ] || k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true; }
trap cleanup EXIT
k3d cluster delete "$CLUSTER" >/dev/null 2>&1 || true
k3d cluster create "$CLUSTER" --agents 1 --no-lb --k3s-arg "--disable=traefik@server:0" --wait
kubectl --context "$CTX" create namespace "$NS"

# Postgres with the dev server's NetworkPolicy (it admits the migrate pods by app.kubernetes.io/component) and the
# namespace default-deny every service namespace gets.
k apply -f "$REPO/deploy/networkpolicy/default-deny.yaml"
python3 - "$REPO/deploy/envs/dev/apps/postgres.yaml" <<'EOF' | k apply -f -
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d and d["kind"] == "NetworkPolicy"]
for d in docs:
    d["metadata"].pop("annotations", None)
    d["metadata"].pop("namespace", None)
print(yaml.safe_dump_all(docs))
EOF
k create secret generic pg-super --from-literal=POSTGRES_PASSWORD="$(openssl rand -hex 16)"
cat <<EOF | k apply -f -
apiVersion: apps/v1
kind: Deployment
metadata: {name: postgres}
spec:
  selector: {matchLabels: {app.kubernetes.io/name: postgres}}
  template:
    metadata: {labels: {app.kubernetes.io/name: postgres}}
    spec:
      containers:
        - name: postgres
          image: $PG_IMAGE
          env:
            - {name: POSTGRES_USER, value: depot}
            - {name: POSTGRES_PASSWORD, valueFrom: {secretKeyRef: {name: pg-super, key: POSTGRES_PASSWORD}}}
          ports: [{name: pg, containerPort: 5432}]
          readinessProbe: {exec: {command: [pg_isready, -U, depot]}, periodSeconds: 2}
---
apiVersion: v1
kind: Service
metadata: {name: postgres}
spec:
  selector: {app.kubernetes.io/name: postgres}
  ports: [{name: pg, port: 5432, targetPort: pg}]
EOF
k rollout status deploy/postgres --timeout=180s
app_pw=$(openssl rand -hex 16)
psql_super "CREATE ROLE depot_app LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD '$app_pw'"

fail=0
for svc in $SERVICES; do
  dir="$REPO/services/$svc"
  chart=$(ls -d "$dir"/charts/*/ | head -1)
  chart=${chart%/}
  name=$(sed -n 's/^name: *//p' "$chart/Chart.yaml")
  release=$name
  db="depot_$(echo "$name" | tr - _)"
  owner="${db}_owner"
  owner_pw=$(openssl rand -hex 16)
  echo "== $svc: chart $name, database $db, owner $owner"
  psql_super "CREATE ROLE $owner LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD '$owner_pw'"
  psql_super "CREATE DATABASE $db OWNER $owner"
  psql_super "REVOKE CONNECT ON DATABASE $db FROM PUBLIC; GRANT CONNECT ON DATABASE $db TO depot_app"
  # Owner Secret (the chart default migrateJob.ownerSecret) and the serving Secret; the key is DATABASE_URL in both.
  k create secret generic "$name-db-owner" --from-literal=DATABASE_URL="postgres://$owner:$owner_pw@postgres:5432/$db?sslmode=disable"
  k create secret generic "$name-db-app" --from-literal=DATABASE_URL="postgres://depot_app:$app_pw@postgres:5432/$db?sslmode=disable"

  image="depot-local/$svc:migrate-test"
  docker build -q -f "$dir/Dockerfile" -t "$image" "$REPO" >/dev/null
  k3d image import "$image" -c "$CLUSTER" >/dev/null
  # The pre-install hook (the chart's migrate Job) must complete before helm returns; pods are not awaited (their other
  # dependencies are not in this cluster).
  if ! helm --kube-context "$CTX" install "$release" "$chart" -n "$NS" --timeout 10m \
      --set image.repository="depot-local/$svc" --set image.tag=migrate-test --set image.digest= \
      --set image.pullPolicy=Never --set-string databaseSecret="$name-db-app" --set replicaCount=1; then
    echo "FAIL $svc: helm install (the migration Job) failed"; k logs "job/$release-migrate" --tail=50 || true; fail=1; continue
  fi
  want=$(ls "$dir"/migrations/*.sql | wc -l)
  got=$(psql_db "$db" "SELECT count(*) FROM schema_migrations")
  owners=$(psql_db "$db" "SELECT string_agg(DISTINCT tableowner, ',') FROM pg_tables WHERE schemaname = 'public'")
  migrate_env=$(k get deploy -l "app.kubernetes.io/name=$name" -o jsonpath='{range .items[*]}{.spec.template.spec.containers[*].env[?(@.name=="MIGRATE_ON_START")].value}{" "}{end}')
  routed=$(k get endpoints -o jsonpath='{range .items[*]}{.subsets[*].addresses[*].targetRef.name}{" "}{end}' | tr ' ' '\n' | grep -c -- "-migrate-" || true)
  echo "   applied $got of $want; table owners: $owners; serving MIGRATE_ON_START: [$migrate_env]; endpoints on the Job pod: $routed"
  [ "$got" = "$want" ] || { echo "FAIL $svc: $got of $want migrations recorded"; fail=1; }
  [ "$owners" = "$owner" ] || { echo "FAIL $svc: tables owned by $owners, want $owner"; fail=1; }
  case " $migrate_env " in *" true "*) echo "FAIL $svc: a serving Deployment migrates on start"; fail=1 ;; esac
  [ "$routed" = 0 ] || { echo "FAIL $svc: a Service routes to the migration Job's pod"; fail=1; }
done
[ "$fail" = 0 ] && echo "k3d_migrate_test: every schema applied by its owner-role Job" || exit 1
