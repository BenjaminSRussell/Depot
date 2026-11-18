#!/usr/bin/env python3
"""Generate every service database and its Secrets for an environment (D4, ADR-0012, B-09 part b: stage).

    python3 deploy/scripts/gen_databases.py           # write the stage files below
    python3 deploy/scripts/gen_databases.py --check   # exit 1 when a committed file differs from the generated one

Needs helm: the Secret each service's serving pods read DATABASE_URL from (its name, key and the other keys they read from
it), the owner Secret of its migration Job and every other Secret a stage service reads are taken from the chart rendered
with its stage Application, so a chart change shows up here (and in --check) instead of in a pod that cannot start.

Writes:
- deploy/envs/stage/databases/postgres-<namespace>.yaml (Application service-databases-stage, wave -2): one Postgres per
  namespace whose services deploy/kafka/connect.yaml points at (postgres.<ns>.svc: hello-ledger, metadata, batch), so the
  connectors work unchanged. Each is a StatefulSet with a volume, TLS from a cert-manager certificate and only `hostssl`
  SCRAM lines for databases in pg_hba (no `replication` line: Debezium's pgoutput connections are logical and match the
  database column; a physical replication line would let any REPLICATION role copy the whole cluster). The server mounts
  only its own superuser password. A provisioning Job (PostSync, and a CronJob every 15 minutes, so a Vault key written
  later is picked up without a sync) reconciles roles and databases. Production topology (replicas, PITR) is B-48.
- deploy/envs/stage/service-secrets/<service>/secrets.yaml, one Application per service (`<service>-secrets-stage`, wave -1,
  plain children of root-stage in deploy/envs/stage/apps/service-secrets.yaml, so the root's sync waves order them before the
  services), so a Vault key that is missing degrades only its own service: the owner Secret `<chart>-db-owner` (the migration Job's DATABASE_URL), the serving Secret (the key
  the chart reads DATABASE_URL from plus the other required keys its pods read from that Secret, each from Vault
  <prefix>/<service>/<key>), the role passwords the provisioning Job reads (`postgres-role-<service>`), and the other
  Secrets the service reads (EXTRA_SECRETS). They sync before the services (wave 2), never as ordinary resources of the
  service's Application, so its PreSync Job finds its Secret on the first install (the D4 rule).
- deploy/envs/stage/kafka-secrets: dev's Kafka Connect CDC Secrets and Apicurio database Secret with the stage Vault keys.

Roles (ADR-0012 rule 2): per database an owner `<database>_owner` (owns the database; the migration Job signs in as it), a
login role `<database>_app` (NOBYPASSRLS, member of `depot_app`, CONNECT and TEMPORARY on its own database only; the serving
pods sign in as it) and, for an outbox connector, its REPLICATION role (the username dev's debezium-cdc-<service> Secret
names, which the service's migrations grant SELECT on outbox to), with CONNECT on that database only. `depot_app` itself is
NOLOGIN with CONNECT nowhere: the migrations grant table privileges and the RLS policies target it, and its members
inherit both. Every role is created first (NOLOGIN), then the databases, so a migration never runs before the CDC role it
grants to exists; a role signs in only once its password file exists, and the Job reports every role it left without one.
Passwords come from the ClusterSecretStore `depot`; nothing is created by hand. Rotating a Vault key rotates the role at
the next provisioning run. Database URLs URL-encode the password and use sslmode=require: no service chart can mount the
stage CA yet (the provisioning Job and Keycloak verify the server with it).
"""

import argparse
import re
import sys
from pathlib import Path

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import gen_migrate_jobs  # noqa: E402
import gen_networkpolicies  # noqa: E402
from test_argocd_apps import applications, pod_template, release_name, render, service_chart_sources  # noqa: E402

DEPLOY = Path(__file__).resolve().parents[1]
REPO = DEPLOY.parent
ENV = "stage"
OUT = DEPLOY / "envs" / ENV / "databases"
SECRETS_OUT = DEPLOY / "envs" / ENV / "service-secrets"
APPSET = DEPLOY / "envs" / ENV / "apps" / "service-secrets.yaml"
KAFKA_SECRETS = DEPLOY / "envs" / ENV / "kafka-secrets"
DEV_KAFKA_SECRETS = DEPLOY / "envs" / "dev" / "kafka-secrets"
# Dev kafka-secrets files stage copies (consumer-credentials-reader.yaml is for the consumers dev turns on).
KAFKA_SECRET_FILES = ("apicurio-database.yaml", "debezium-cdc.yaml")
PREFIX = f"depot/{ENV}"
IMAGE = "postgres:17-alpine@sha256:eee6c6e1b0894fa4fe0075eb054af9fd1cf39a35321c2d4110626cfe506411fb"
ISSUER = "depot-stage-ca"
SUPERUSER = "depot"
IDENT = re.compile(r"^[a-z_][a-z0-9_]{0,62}$")
ROLES_DIR = "/run/secrets/roles"
# The advisory lock key that serializes provisioning runs on one server.
PROVISION_LOCK = 7_300_901
# Databases of platform components in a service Postgres: namespace -> [(key, database, owner role, client pod names)].
# The owner password is Vault <prefix>/<key>-postgres/owner-password, delivered as postgres-role-<key>.
PLATFORM_DATABASES = {
    "hello-ledger": [("keycloak", "keycloak", "keycloak", ["keycloak"])],
}
# Secrets other than the database ones that a stage service reads: service directory -> [(Secret, {key: Vault key under
# <prefix>/})]. Every non-optional Secret a rendered stage service reads is here, in a database Secret, created by its
# own chart, or in UNMANAGED with the reason (test_argocd_apps.py checks).
EXTRA_SECRETS = {
    "receiving": [("receiving-audit-seed", {"AUDIT_SAMPLE_SEED": "receiving/audit-sample-seed"})],
    "customer-portal-ts": [("customer-portal-oidc", {"client-secret": "customer-portal/oidc-client-secret"}),
                           ("customer-portal-signing", {"signing-key": "customer-portal/signing-key"})],
    "supplier-portal-ts": [("supplier-portal-oidc", {"client-secret": "supplier-portal/oidc-client-secret"}),
                           ("supplier-portal-signing", {"signing-key": "supplier-portal/signing-key"})],
}
# Secrets stage services read that stage does not create yet: (namespace, Secret) -> why.
UNMANAGED = {
    ("hello-ledger", "agents-provider"): "agent-runtime reads the model provider's credentials with envFrom; the keys depend on "
                                         "the provider an environment chooses, and stage has not chosen one (created outside Git)",
}
HEADER = ("# GENERATED by deploy/scripts/gen_databases.py (D4, B-09 part b). Do not edit: change the charts, deploy/kafka/connect.yaml\n"
          "# or the script and run `python3 deploy/scripts/gen_databases.py` (test_argocd_apps.py fails when this file is stale).\n")
URL_PASSWORD = '{{ .password | urlquery | replace "+" "%20" }}'
KEEP_NAMESPACE = {"argocd.argoproj.io/sync-options": "Prune=false,Delete=false"}


class Dumper(yaml.SafeDumper):
    pass


class Literal(str):
    pass


class FlowMap(dict):
    pass


Dumper.add_representer(Literal, lambda d, v: d.represent_scalar("tag:yaml.org,2002:str", v, style="|"))
Dumper.add_representer(FlowMap, lambda d, v: d.represent_mapping("tag:yaml.org,2002:map", v.items(), flow_style=True))
def _style(v):
    if '"' in v and "'" not in v:
        return "'"
    if v in ("", "true", "false", "on", "off", "no", "yes") or v.lstrip("-").isdigit() or ": " in v \
            or v.startswith(("{", "*", "&", "!", "-", "#")) or "{{" in v:
        return '"'
    return None


Dumper.add_representer(str, lambda d, v: d.represent_scalar("tag:yaml.org,2002:str", v, style=_style(v)))


def dump_all(docs):
    return yaml.dump_all(docs, Dumper=Dumper, sort_keys=False, default_flow_style=False, width=120)


def ident(name, where):
    if not IDENT.fullmatch(name or ""):
        raise SystemExit(f"{where}: {name!r} is not a plain Postgres identifier ([a-z_][a-z0-9_]{{0,62}})")
    return name


def connectors():
    """service key (outbox-<key>) -> (namespace of its Postgres, database, Kafka Connect Secret)."""
    out = {}
    for d in yaml.safe_load_all((DEPLOY / "kafka/connect.yaml").read_text()):
        if d and d.get("kind") == "KafkaConnector" and d["metadata"]["name"].startswith("outbox-"):
            cfg = d["spec"]["config"]
            host = cfg["database.hostname"]
            if not (host.startswith("postgres.") and host.endswith(".svc")):
                raise SystemExit(f"{d['metadata']['name']}: database.hostname {host} is not postgres.<namespace>.svc")
            secret = cfg["database.password"].split("kafka/", 1)[1].split(":", 1)[0]
            out[d["metadata"]["name"][len("outbox-"):]] = (host.split(".")[1], cfg["database.dbname"], secret)
    return out


def cdc_usernames():
    """Kafka Connect Secret -> the REPLICATION role it signs in as (dev's debezium-cdc.yaml templates)."""
    out = {}
    for d in yaml.safe_load_all((DEV_KAFKA_SECRETS / "debezium-cdc.yaml").read_text()):
        if d and d.get("kind") == "ExternalSecret":
            out[d["spec"]["target"]["name"]] = d["spec"]["target"]["template"]["data"]["username"]
    return out


def secret_refs(docs, job):
    """({(Secret, key)} the serving pods read DATABASE_URL from, {Secret: {required keys}} they read, the Job's owner ref)."""
    db, keys, owner = set(), {}, None
    for d in docs:
        pt = pod_template(d)
        if not pt:
            continue
        spec = pt.get("spec") or {}
        is_job = d["kind"] == "Job" and d["metadata"]["name"] == job
        for c in (spec.get("containers") or []) + (spec.get("initContainers") or []):
            for e in c.get("env") or []:
                ref = (e.get("valueFrom") or {}).get("secretKeyRef")
                if not ref:
                    continue
                if is_job:
                    if e["name"] == "DATABASE_URL":
                        owner = (ref["name"], ref["key"])
                    continue
                if e["name"] == "DATABASE_URL":
                    db.add((ref["name"], ref["key"]))
                if not ref.get("optional"):
                    keys.setdefault(ref["name"], set()).add(ref["key"])
            if not is_job:
                for ef in c.get("envFrom") or []:
                    sref = ef.get("secretRef")
                    if sref and not sref.get("optional"):
                        keys.setdefault(sref["name"], set()).add("*")
        if is_job:
            continue
        for v in spec.get("volumes") or []:
            sec = v.get("secret")
            if sec and not sec.get("optional"):
                items = sec.get("items")
                keys.setdefault(sec["secretName"], set()).update(i["key"] for i in items or [])
                if not items:
                    keys[sec["secretName"]].add("*")
    return db, keys, owner


def stage_services():
    """(service directory name, chart path, release, namespace, rendered docs) of every stage service Application."""
    out = []
    for env, _, app in applications():
        if env != ENV:
            continue
        for src in service_chart_sources(app):
            parts = src["path"].split("/")
            out.append((parts[1], src["path"], release_name(app, src), app["spec"]["destination"]["namespace"], render(app, src)))
    return sorted(out, key=lambda s: s[1])


def services(rendered=None):
    """One entry per chart with migrations, from its stage Application."""
    conns, users = connectors(), cdc_usernames()
    charts = {c.relative_to(REPO).as_posix(): (s, c) for s, c in gen_migrate_jobs.charts()}
    out = []
    for service_dir, path, release, ns, docs in rendered or stage_services():
        if path not in charts:
            continue
        service, chart = charts[path]
        db_refs, keys, owner = secret_refs(docs, f"{release}-migrate")
        where = f"{path} ({release})"
        if len(db_refs) != 1:
            raise SystemExit(f"{where}: serving pods read DATABASE_URL from {sorted(db_refs)}; want exactly one Secret key")
        if owner is None:
            raise SystemExit(f"{where}: no migration Job {release}-migrate renders in {ENV}")
        ((app_secret, app_key),) = db_refs
        extra = sorted(keys.get(app_secret, set()) - {app_key})
        if "*" in extra:
            raise SystemExit(f"{where}: a volume or envFrom takes all of {app_secret}; list its items so the keys are known")
        conn = conns.get(service.name)
        pg_ns, database, cdc_secret = conn if conn else (ns, "depot_" + service.name.replace("-", "_"), None)
        if pg_ns != ns:
            raise SystemExit(f"{where}: its connector reads postgres.{pg_ns}.svc but the service runs in {ns}")
        ident(database, where)
        cdc = ident(users[cdc_secret], where) if cdc_secret else None
        pods = gen_networkpolicies.pod_names(path)
        out.append({
            "service": service.name, "chart": path, "release": release, "namespace": ns, "database": database,
            "owner_role": ident(f"{database}_owner", where), "app_role": ident(f"{database}_app", where),
            "owner_secret": owner, "app_secret": (app_secret, app_key), "extra": extra, "cdc_role": cdc,
            "cdc_rls": "BYPASSRLS" if cdc == "debezium_cdc" else "NOBYPASSRLS",
            "pods": sorted(set(pods) | {f"{gen_migrate_jobs.pod_name(chart)}-migrate"}),
        })
    out.sort(key=lambda s: (s["namespace"], s["database"]))
    seen = {}
    for s in out:
        if s["database"] in seen:
            raise SystemExit(f"{s['chart']} and {seen[s['database']]} both use database {s['database']}")
        seen[s["database"]] = s["chart"]
    return out


def url(role, ns, database):
    return f"postgres://{role}:{URL_PASSWORD}@postgres.{ns}.svc:5432/{database}?sslmode=require"


def external_secret(name, ns, data, template, wave="-1"):
    doc = {
        "apiVersion": "external-secrets.io/v1",
        "kind": "ExternalSecret",
        "metadata": {"name": name, "namespace": ns, "annotations": {"argocd.argoproj.io/sync-wave": wave}},
        "spec": {
            "refreshInterval": "1h",
            "secretStoreRef": {"kind": "ClusterSecretStore", "name": "depot"},
            "target": {"name": name, "creationPolicy": "Owner"},
            "data": [{"secretKey": k, "remoteRef": {"key": v, "property": "value"}} for k, v in data.items()],
        },
    }
    if template:
        doc["spec"]["target"]["template"] = {"data": template}
    return doc


def var(key):
    """An ESO template variable for a Secret key (Go identifiers have no '-' or '.')."""
    return key.replace("-", "_").replace(".", "_")


def role_keys(s):
    """{file in the provisioning Job: Vault key} of one service's role passwords."""
    base = f"{PREFIX}/{s['service']}-postgres"
    keys = {"owner-password": f"{base}/owner-password", "app-password": f"{base}/app-password"}
    if s["cdc_role"]:
        keys["cdc-password"] = f"{base}/cdc-password"
    return keys


def service_secrets(s):
    """The owner, serving and role-password ExternalSecrets of one service."""
    ns, db = s["namespace"], s["database"]
    owner_name, owner_key = s["owner_secret"]
    app_name, app_key = s["app_secret"]
    keys = role_keys(s)
    owner = external_secret(owner_name, ns, {"password": keys["owner-password"]}, {owner_key: url(s["owner_role"], ns, db)})
    data = {"password": keys["app-password"]}
    template = {app_key: url(s["app_role"], ns, db)}
    for key in s["extra"]:
        data[var(key)] = f"{PREFIX}/{s['service']}/{key.lower().replace('_', '-')}"
        template[key] = f"{{{{ .{var(key)} }}}}"
    app = external_secret(app_name, ns, data, template)
    roles = external_secret(f"postgres-role-{s['service']}", ns, keys, None, wave="-2")
    return [owner, app, roles]


def sql_role(role, attrs, password_file, vault_key, member_of=None):
    """Create the role NOLOGIN if missing, fix its attributes, and let it sign in only once its password file exists."""
    lines = [
        f"SELECT 'CREATE ROLE {role} NOLOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '{role}')\\gexec",
        f"ALTER ROLE {role} WITH NOSUPERUSER NOCREATEDB NOCREATEROLE {attrs};",
    ]
    if member_of:
        lines.append(f"SELECT 'GRANT {member_of} TO {role}' WHERE NOT pg_has_role('{role}', '{member_of}', 'MEMBER')\\gexec")
    lines += [
        f"\\set ok `test -s {password_file} && echo true || echo false`",
        "\\if :ok",
        f"\\set pw `cat {password_file}`",
        f"ALTER ROLE {role} WITH LOGIN PASSWORD :'pw';",
        "\\else",
        f"\\echo 'skipped: {role} cannot sign in yet, its password is not in the secret store ({vault_key})'",
        "\\endif",
    ]
    return lines


def provision_sql(entries):
    lines = [
        "-- Reconciles the roles and databases of this Postgres (gen_databases.py), run by the PostSync Job and the CronJob.",
        "-- Idempotent. Every role first (NOLOGIN until its password file exists), then the databases, then CONNECT.",
        "\\set ON_ERROR_STOP on",
        "-- One run at a time: the PostSync Job and the CronJob may overlap. Released when the session ends.",
        f"SELECT pg_advisory_lock({PROVISION_LOCK}) \\g /dev/null",
        "REVOKE CONNECT ON DATABASE postgres FROM PUBLIC;",
        "REVOKE CONNECT ON DATABASE template1 FROM PUBLIC;",
        "-- depot_app: the role the migrations grant to and the RLS policies name. No login, CONNECT nowhere; each database's",
        "-- <database>_app login role is a member and inherits its privileges.",
        "SELECT 'CREATE ROLE depot_app NOLOGIN' WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'depot_app')\\gexec",
        "ALTER ROLE depot_app WITH NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD NULL;",
    ]
    for e in entries:
        lines.append(f"-- roles of {e['label']}")
        if e.get("cdc_role"):
            lines += sql_role(e["cdc_role"], f"REPLICATION {e['cdc_rls']}", f"{ROLES_DIR}/{e['key']}-cdc-password", e["vault"]["cdc-password"])
        if e.get("app_role"):
            lines += sql_role(e["app_role"], "NOREPLICATION NOBYPASSRLS", f"{ROLES_DIR}/{e['key']}-app-password",
                              e["vault"]["app-password"], member_of="depot_app")
        lines += sql_role(e["owner_role"], "NOREPLICATION NOBYPASSRLS", f"{ROLES_DIR}/{e['key']}-owner-password", e["vault"]["owner-password"])
    for e in entries:
        db, owner = e["database"], e["owner_role"]
        lines += [
            f"-- database of {e['label']}",
            f"SELECT 'CREATE DATABASE {db} OWNER {owner}' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = '{db}')\\gexec",
            f"ALTER DATABASE {db} OWNER TO {owner};",
            f"REVOKE ALL ON DATABASE {db} FROM PUBLIC;",
            f"REVOKE ALL ON DATABASE {db} FROM depot_app;",
        ]
        if e.get("app_role"):
            lines.append(f"GRANT CONNECT, TEMPORARY ON DATABASE {db} TO {e['app_role']};")
        if e.get("cdc_role"):
            lines.append(f"GRANT CONNECT ON DATABASE {db} TO {e['cdc_role']};")
    return "\n".join(lines) + "\n"


PROVISION_SH = """set -eu
umask 077
# The superuser's password goes into a pgpass file in the pod's /tmp, not the environment (pgpass escapes : and \\).
printf 'postgres.%s.svc:5432:postgres:{user}:%s\\n' "$NAMESPACE" "$(sed 's/[\\\\:]/\\\\&/g' /run/secrets/superuser/postgres-password)" > /tmp/pgpass
export PGPASSFILE=/tmp/pgpass
exec psql "host=postgres.$NAMESPACE.svc port=5432 dbname=postgres user={user} sslmode=verify-full sslrootcert=/tls/ca.crt" -X -q -f /provision/provision.sql
""".replace("{user}", SUPERUSER)


def provision_pod(ns, entries, restricted, pod_security):
    sources = [{"secret": {"name": f"postgres-role-{e['key']}", "optional": True,
                           "items": [{"key": k, "path": f"{e['key']}-{k}"} for k in e["vault"]]}} for e in entries]
    return {
        "metadata": {"labels": {"app.kubernetes.io/name": "postgres-provision", "app.kubernetes.io/part-of": "depot-databases"}},
        "spec": {
            "restartPolicy": "OnFailure", "automountServiceAccountToken": False, "securityContext": pod_security,
            "containers": [{
                "name": "provision", "image": IMAGE, "command": ["sh", "/provision/provision.sh"],
                "env": [{"name": "HOME", "value": "/tmp"}, {"name": "NAMESPACE", "value": ns}],
                "resources": {"requests": {"cpu": "10m", "memory": "32Mi"}, "limits": {"cpu": "200m", "memory": "128Mi"}},
                "securityContext": restricted,
                "volumeMounts": [{"name": "provision", "mountPath": "/provision", "readOnly": True},
                                 {"name": "tmp", "mountPath": "/tmp"},
                                 {"name": "tls", "mountPath": "/tls", "readOnly": True},
                                 {"name": "superuser", "mountPath": "/run/secrets/superuser", "readOnly": True},
                                 {"name": "roles", "mountPath": ROLES_DIR, "readOnly": True}],
            }],
            "volumes": [
                {"name": "provision", "configMap": {"name": "postgres-config", "items": [
                    {"key": "provision.sql", "path": "provision.sql"}, {"key": "provision.sh", "path": "provision.sh"}]}},
                {"name": "tmp", "emptyDir": {}},
                {"name": "tls", "secret": {"secretName": "postgres-tls", "items": [{"key": "ca.crt", "path": "ca.crt"}]}},
                {"name": "superuser", "secret": {"secretName": "postgres-superuser", "defaultMode": 0o440}},
                # One optional source per service: a Secret not written yet (its Vault key missing) leaves only that
                # service's roles without a password; the Job reports them and the CronJob picks them up later.
                {"name": "roles", "projected": {"defaultMode": 0o440, "sources": sources}},
            ],
        },
    }


def entries_of(ns, svcs):
    entries, clients = [], set()
    for s in svcs:
        entries.append({"label": f"{s['chart']} ({s['release']})", "key": s["service"], "database": s["database"],
                        "owner_role": s["owner_role"], "app_role": s["app_role"], "cdc_role": s["cdc_role"],
                        "cdc_rls": s["cdc_rls"], "vault": role_keys(s)})
        clients |= set(s["pods"])
    for key, database, owner, pods in PLATFORM_DATABASES.get(ns, []):
        entries.append({"label": f"platform: {database}", "key": key, "database": ident(database, key),
                        "owner_role": ident(owner, key), "vault": {"owner-password": f"{PREFIX}/{key}-postgres/owner-password"}})
        clients |= set(pods)
    return entries, clients


def instance(ns, svcs):
    """The Postgres of one namespace: superuser Secret, certificate, server, Service, NetworkPolicy, provisioning Job/CronJob."""
    entries, clients = entries_of(ns, svcs)
    slots = sum(1 for e in entries if e.get("cdc_role"))
    wave = {"argocd.argoproj.io/sync-wave": "-1"}
    labels = {"app.kubernetes.io/name": "postgres", "app.kubernetes.io/part-of": "depot-databases"}
    restricted = {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True, "capabilities": {"drop": ["ALL"]}}
    pod_security = {"runAsNonRoot": True, "runAsUser": 70, "runAsGroup": 70, "fsGroup": 70, "seccompProfile": {"type": "RuntimeDefault"}}
    docs = [
        # Never pruned or deleted with this Application: every service of the namespace lives in it.
        {"apiVersion": "v1", "kind": "Namespace",
         "metadata": {"name": ns, "annotations": {"argocd.argoproj.io/sync-wave": "-2", **KEEP_NAMESPACE}}},
        external_secret("postgres-superuser", ns, {"postgres-password": f"{PREFIX}/{ns}-postgres/postgres-password"}, None, wave="-2"),
        {"apiVersion": "cert-manager.io/v1", "kind": "Certificate",
         "metadata": {"name": "postgres-tls", "namespace": ns, "annotations": {"argocd.argoproj.io/sync-wave": "-2"}},
         "spec": {"secretName": "postgres-tls", "dnsNames": ["postgres", f"postgres.{ns}.svc", f"postgres.{ns}.svc.cluster.local"],
                  "privateKey": {"algorithm": "ECDSA", "size": 256}, "issuerRef": {"kind": "ClusterIssuer", "name": ISSUER}}},
        {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "postgres-config", "namespace": ns, "annotations": wave},
         "data": {
             # Every TCP connection is TLS and SCRAM. No `replication` line: Kafka Connect's pgoutput connections are logical
             # replication connections, which match the database column of `hostssl all`. The socket serves the image's
             # initdb only.
             "pg_hba.conf": Literal("local all all scram-sha-256\nhostssl all all all scram-sha-256\n"),
             "provision.sql": Literal(provision_sql(entries)),
             "provision.sh": Literal(PROVISION_SH),
         }},
        {"apiVersion": "apps/v1", "kind": "StatefulSet",
         "metadata": {"name": "postgres", "namespace": ns, "labels": labels, "annotations": wave},
         "spec": {
             "serviceName": "postgres", "replicas": 1, "selector": {"matchLabels": {"app.kubernetes.io/name": "postgres"}},
             "template": {
                 "metadata": {"labels": labels},
                 "spec": {
                     "automountServiceAccountToken": False,
                     "securityContext": pod_security,
                     "containers": [{
                         "name": "postgres", "image": IMAGE,
                         # One logical slot and walsender per outbox connector on this Postgres, with headroom (dev's sizing);
                         # max_slot_wal_keep_size caps the WAL a stalled connector can hold.
                         "args": ["-c", "ssl=on", "-c", "ssl_cert_file=/tls/tls.crt", "-c", "ssl_key_file=/tls/tls.key",
                                  "-c", "ssl_min_protocol_version=TLSv1.2", "-c", "hba_file=/etc/postgresql/pg_hba.conf",
                                  "-c", "password_encryption=scram-sha-256", "-c", "wal_level=logical",
                                  "-c", f"max_replication_slots={slots + 8}", "-c", f"max_wal_senders={slots + 8}",
                                  "-c", "max_slot_wal_keep_size=4GB", "-c", "logical_decoding_work_mem=16MB"],
                         "env": [
                             {"name": "POSTGRES_USER", "value": SUPERUSER},
                             {"name": "POSTGRES_DB", "value": "postgres"},
                             {"name": "POSTGRES_PASSWORD_FILE", "value": "/run/secrets/superuser/postgres-password"},
                             {"name": "PGDATA", "value": "/var/lib/postgresql/data/pgdata"},
                         ],
                         "ports": [{"name": "pg", "containerPort": 5432}],
                         "readinessProbe": {"exec": {"command": ["pg_isready", "-h", "127.0.0.1", "-U", SUPERUSER, "-d", "postgres"]},
                                            "periodSeconds": 10},
                         "resources": {"requests": {"cpu": "250m", "memory": "1Gi"}, "limits": {"memory": "3Gi"}},
                         "securityContext": {**restricted, "readOnlyRootFilesystem": False},
                         "volumeMounts": [
                             {"name": "data", "mountPath": "/var/lib/postgresql/data"},
                             {"name": "run", "mountPath": "/var/run/postgresql"},
                             {"name": "config", "mountPath": "/etc/postgresql", "readOnly": True},
                             {"name": "tls", "mountPath": "/tls", "readOnly": True},
                             {"name": "superuser", "mountPath": "/run/secrets/superuser", "readOnly": True},
                         ],
                     }],
                     "volumes": [
                         {"name": "run", "emptyDir": {}},
                         {"name": "config", "configMap": {"name": "postgres-config", "items": [{"key": "pg_hba.conf", "path": "pg_hba.conf"}]}},
                         {"name": "tls", "secret": {"secretName": "postgres-tls", "defaultMode": 0o440}},
                         # Only the server's own password: a role password missing from the secret store never stops it.
                         {"name": "superuser", "secret": {"secretName": "postgres-superuser", "defaultMode": 0o440}},
                     ],
                 },
             },
             "volumeClaimTemplates": [{"metadata": {"name": "data"},
                                       "spec": {"accessModes": ["ReadWriteOnce"], "resources": {"requests": {"storage": "50Gi"}}}}],
         }},
        {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "postgres", "namespace": ns, "labels": labels, "annotations": wave},
         "spec": {"selector": {"app.kubernetes.io/name": "postgres"}, "ports": [{"name": "pg", "port": 5432, "targetPort": "pg"}]}},
    ]
    peers = [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": p}}} for p in sorted(clients | {"postgres-provision"})]
    if slots:
        peers.append({"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kafka"}},
                      "podSelector": {"matchLabels": {"strimzi.io/cluster": "depot-connect"}}})
    docs.append({"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
                 "metadata": {"name": "postgres", "namespace": ns, "annotations": wave},
                 "spec": {"podSelector": {"matchLabels": {"app.kubernetes.io/name": "postgres"}}, "policyTypes": ["Ingress"],
                          "ingress": [{"from": peers, "ports": [{"protocol": "TCP", "port": 5432}]}]}})
    pod = provision_pod(ns, entries, restricted, pod_security)
    docs.append({"apiVersion": "batch/v1", "kind": "Job",
                 "metadata": {"name": "postgres-provision", "namespace": ns,
                              "annotations": {"argocd.argoproj.io/hook": "PostSync",
                                              "argocd.argoproj.io/hook-delete-policy": "BeforeHookCreation"}},
                 "spec": {"backoffLimit": 6, "activeDeadlineSeconds": 900, "template": pod}})
    docs.append({"apiVersion": "batch/v1", "kind": "CronJob",
                 "metadata": {"name": "postgres-provision", "namespace": ns, "annotations": wave},
                 "spec": {"schedule": "*/15 * * * *", "concurrencyPolicy": "Forbid", "successfulJobsHistoryLimit": 1,
                          "failedJobsHistoryLimit": 3,
                          "jobTemplate": {"spec": {"backoffLimit": 2, "activeDeadlineSeconds": 600, "template": pod}}}})
    return docs


def extra_secrets(rendered):
    """{service directory: [ExternalSecret]} for EXTRA_SECRETS, in the namespace its stage Application deploys to."""
    ns_of = {d: ns for d, _, _, ns, _ in rendered}
    out = {}
    for service_dir, secrets in EXTRA_SECRETS.items():
        if service_dir not in ns_of:
            raise SystemExit(f"EXTRA_SECRETS: no stage Application for {service_dir}")
        out[service_dir] = [external_secret(name, ns_of[service_dir], {var(k): f"{PREFIX}/{v}" for k, v in keys.items()},
                                            {k: f"{{{{ .{var(k)} }}}}" for k in keys}) for name, keys in secrets]
    return out


def secrets_apps(dirs):
    """One plain Application per service for its Secrets, a child of root-stage at wave -1: root-stage's sync waves order
    its own children, so every service's Secrets are synced before the services (wave 2) and their PreSync Jobs. (The
    Applications an ApplicationSet generates are not ordered by the waves.)"""
    import stage_apps  # deploy/scripts
    pin = stage_apps.release()
    apps = [{
        "apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
        "metadata": {"name": f"{d}-secrets-stage", "namespace": "argocd", "annotations": {"argocd.argoproj.io/sync-wave": "-1"},
                     "finalizers": ["resources-finalizer.argocd.argoproj.io"]},
        "spec": {
            "project": "default",
            "source": {"repoURL": stage_apps.REPO_URL, "targetRevision": pin["revision"], "path": f"deploy/envs/stage/service-secrets/{d}"},
            "destination": {"name": "stage", "namespace": ns},
            "syncPolicy": {"automated": {"prune": True, "selfHeal": True},
                           "retry": {"limit": 5, "backoff": {"duration": "30s", "factor": 2, "maxDuration": "5m"}},
                           "syncOptions": ["CreateNamespace=true", "SkipDryRunOnMissingResource=true"]},
        },
    } for d, ns in sorted(dirs.items())]
    return (HEADER + "# One Application per service with that service's Secrets (deploy/envs/stage/service-secrets/<service>), so a\n"
            "# missing Vault key degrades only its own service. Children of root-stage at wave -1, before the services (wave 2).\n"
            + dump_all(apps))


def outputs():
    rendered = stage_services()
    svcs = services(rendered)
    files, by_ns, per_service, dirs = {}, {}, {}, {}
    for s in svcs:
        by_ns.setdefault(s["namespace"], []).append(s)
        per_service.setdefault(s["service"], []).extend(service_secrets(s))
        dirs[s["service"]] = s["namespace"]
    for ns, group in sorted(by_ns.items()):
        for key, _, _, _ in PLATFORM_DATABASES.get(ns, []):
            per_service.setdefault(key, []).append(
                external_secret(f"postgres-role-{key}", ns, {"owner-password": f"{PREFIX}/{key}-postgres/owner-password"}, None, wave="-2"))
            dirs[key] = ns
        comment = (f"# The {ns} Postgres: {len(group)} service databases"
                   + (f" and {', '.join(d for _, d, _, _ in PLATFORM_DATABASES.get(ns, []))}" if PLATFORM_DATABASES.get(ns) else "")
                   + ", their roles, and the TLS, NetworkPolicy and provisioning Job around them.\n")
        files[OUT / f"postgres-{ns}.yaml"] = HEADER + comment + dump_all(instance(ns, group))
    ns_of = {d: ns for d, _, _, ns, _ in rendered}
    for service_dir, docs in extra_secrets(rendered).items():
        per_service.setdefault(service_dir, []).extend(docs)
        dirs[service_dir] = ns_of[service_dir]
    for key, docs in sorted(per_service.items()):
        files[SECRETS_OUT / key / "secrets.yaml"] = (HEADER + f"# The Secrets of {key} in stage: database owner and serving Secrets, the role "
                                                     "passwords the provisioning Job reads, and any other Secret it reads.\n" + dump_all(docs))
    files[APPSET] = secrets_apps(dirs)
    for name in KAFKA_SECRET_FILES:
        text = (DEV_KAFKA_SECRETS / name).read_text()
        if "depot/dev/" not in text:
            raise SystemExit(f"deploy/envs/dev/kafka-secrets/{name}: no depot/dev/ Vault key to point at stage")
        text = text.replace("depot/dev/", f"{PREFIX}/")
        if name == "apicurio-database.yaml":
            # The stage apicurio-postgres serves TLS only (tls.enabled in its stage Application).
            text = text.replace("jdbc:postgresql://postgres.kafka.svc:5432/apicurio\n", "jdbc:postgresql://postgres.kafka.svc:5432/apicurio?sslmode=require\n")
            if "sslmode=require" not in text:
                raise SystemExit("apicurio-database.yaml: the JDBC URL changed; teach gen_databases.py the stage TLS form")
        files[KAFKA_SECRETS / name] = (f"# GENERATED by deploy/scripts/gen_databases.py from deploy/envs/dev/kafka-secrets/{name} with the "
                                       f"{ENV} Vault keys. Do not edit.\n" + text)
    return files


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--check", action="store_true", help="report stale files instead of writing them")
    args = parser.parse_args(argv)
    files = outputs()
    stale = []
    for path, text in files.items():
        if (path.read_text() if path.is_file() else None) == text:
            continue
        stale.append(path.relative_to(REPO).as_posix())
        if not args.check:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
    for directory in (OUT, KAFKA_SECRETS, SECRETS_OUT):
        for path in sorted(p for p in directory.rglob("*") if p.is_file()) if directory.is_dir() else []:
            if path not in files:
                stale.append(path.relative_to(REPO).as_posix() + " (not generated)")
                if not args.check:
                    path.unlink()
    if args.check and stale:
        print("stale (run python3 deploy/scripts/gen_databases.py): " + ", ".join(stale), file=sys.stderr)
        return 1
    if not args.check:
        for directory in [SECRETS_OUT]:
            for d in sorted(directory.iterdir()) if directory.is_dir() else []:
                if d.is_dir() and not any(d.iterdir()):
                    d.rmdir()
        print(f"wrote {len(stale)} file(s)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
