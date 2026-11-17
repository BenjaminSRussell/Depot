"""D28 / ADR-0012 rule 3 (B-09a): every service with migrations has an owner-role migration Job and no serving pod migrates.

Without a cluster this test checks:
- deploy/scripts/gen_migrate_jobs.py --check passes;
- every directory under services/ or tools/ with migrations/*.sql has a chart carrying the generated
  templates/migrate-job.yaml with migrateJob.enabled, and its image has a migrate command (Go: the image's main calls
  dbx.HandleMigrateCommand; the TypeScript and Python services named in gen_migrate_jobs.NON_GO handle theirs);
- close and eam no longer migrate unconditionally at start, so a depot_app pod never runs DDL;
- gen_networkpolicies.py leaves the Job's pod out of every chart's pod names (it is neither selected nor a peer), and the
  Postgres NetworkPolicies (deploy/postgres, the shared dev server) admit migrate pods;
- with helm on PATH, every dev, stage, prod and local Application of a chart with migrations renders exactly one Job
  <release>-migrate: an Argo CD PreSync hook (and Helm pre-install/pre-upgrade) with a delete policy, backoffLimit and
  activeDeadlineSeconds, labelled <chart>-migrate / component migrate, runAsNonRoot, seccomp RuntimeDefault, a read-only
  root filesystem, no privilege escalation, all capabilities dropped, CPU and memory limits, no port, and DATABASE_URL and
  MIGRATE_DATABASE_URL from one Secret, which in stage and prod is not a serving container's Secret (the owner role, not
  depot_app); no Deployment, StatefulSet or CronJob container sets MIGRATE_ON_START=true; no NetworkPolicy and no Service
  of the chart selects the Job's pod; setting the serving MIGRATE_ON_START to true while the Job is enabled fails the
  render. With kubeconform on PATH the rendered Jobs are validated.
"""

import shutil
import subprocess
import sys
import unittest
from pathlib import Path

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import gen_migrate_jobs as gen  # noqa: E402
import gen_networkpolicies  # noqa: E402
from test_argocd_apps import applications, load_docs, pod_template, render, release_name, service_chart_sources  # noqa: E402

DEPLOY = Path(__file__).resolve().parents[1]
REPO = DEPLOY.parent


def migrating_services():
    """Every services/<dir> or tools/<dir> with migrations/*.sql, found here independently of the generator."""
    return sorted({p.parents[1] for p in list(REPO.glob("services/*/migrations/*.sql")) + list(REPO.glob("tools/*/migrations/*.sql"))})


def containers(pod):
    spec = pod.get("spec") or {}
    return (spec.get("initContainers") or []) + (spec.get("containers") or [])


def secret_ref(env, name):
    for e in env or []:
        if e.get("name") == name:
            return ((e.get("valueFrom") or {}).get("secretKeyRef") or {})
    return None


def selects(selector, labels):
    """Whether a Kubernetes label selector (matchLabels / matchExpressions) matches labels."""
    for k, v in (selector.get("matchLabels") or {}).items():
        if labels.get(k) != v:
            return False
    for expr in selector.get("matchExpressions") or []:
        key, op, values = expr["key"], expr["operator"], set(expr.get("values") or [])
        if op == "In" and labels.get(key) not in values:
            return False
        if op == "NotIn" and labels.get(key) in values:
            return False
        if op == "Exists" and key not in labels:
            return False
        if op == "DoesNotExist" and key in labels:
            return False
    return True


class MigrateJobFilesTest(unittest.TestCase):
    def test_generated_files_are_current(self):
        self.assertEqual(gen.main(["--check"]), 0, "run python3 deploy/scripts/gen_migrate_jobs.py")

    def test_every_service_with_migrations_has_the_job(self):
        services = migrating_services()
        self.assertGreaterEqual(len(services), 60, "the migrations scan found too few services; did they move?")
        missing = []
        for service in services:
            charts = sorted(p.parent for p in service.glob("charts/*/Chart.yaml"))
            if len(charts) != 1:
                missing.append(f"{service.relative_to(REPO)}: {len(charts)} charts")
                continue
            tpl = charts[0] / "templates" / gen.TEMPLATE_NAME
            values = yaml.safe_load((charts[0] / "values.yaml").read_text()) or {}
            if not tpl.is_file() or tpl.read_text() != gen.TEMPLATE:
                missing.append(f"{tpl.relative_to(REPO)} missing or not the generated template")
            elif not (values.get("migrateJob") or {}).get("enabled"):
                missing.append(f"{charts[0].relative_to(REPO)}: migrateJob.enabled is not true")
        self.assertEqual(missing, [], "services with migrations/ but no owner-role migration Job (D28)")

    def test_every_image_has_a_migrate_command(self):
        missing = []
        for service in migrating_services():
            if service.name in gen.NON_GO:
                continue
            mains = [p for p in service.glob("cmd/*/main.go") if "dbx.HandleMigrateCommand(migrations.FS" in p.read_text()]
            entry = (service / "Dockerfile").read_text() if (service / "Dockerfile").is_file() else ""
            if not mains:
                missing.append(str(service.relative_to(REPO)))
            elif entry and not any(f"./cmd/{m.parent.name}" in entry for m in mains):
                missing.append(f"{service.relative_to(REPO)}: the Dockerfile builds a binary without dbx.HandleMigrateCommand")
        self.assertEqual(missing, [], "Go services whose image cannot run `<binary> migrate`")
        self.assertIn("MIGRATE_COMMAND", (REPO / "services/agents-ts/src/main.ts").read_text())
        self.assertIn("MIGRATE_COMMAND", (REPO / "services/integration-hub-ts/src/main.ts").read_text())
        self.assertTrue((REPO / "services/prediction-py/src/prediction/migrate.py").is_file())
        self.assertEqual(set(gen.NON_GO), {s.name for s in migrating_services() if not (s / "go.mod").is_file()})

    def test_no_service_migrates_unconditionally_at_start(self):
        for service in ("close", "eam"):
            text = (REPO / "services" / service / "cmd" / service / "main.go").read_text()
            body = text[text.index("func run("):]
            migrate_at = body.index("dbx.Migrate(")
            self.assertIn('getenv("MIGRATE_ON_START") == "true"', body[:migrate_at], service)

    def test_the_job_pod_is_never_a_network_policy_pod_or_peer(self):
        for _, chart in gen.charts():
            rel = chart.relative_to(REPO).as_posix()
            self.assertFalse([n for n in gen_networkpolicies.pod_names(rel) if n.endswith("-migrate")], rel)

    def test_postgres_admits_the_migrate_pods(self):
        (policy,) = [d for d in load_docs(DEPLOY / "envs/dev/apps/postgres.yaml") if d["kind"] == "NetworkPolicy"]
        sources = [f for rule in policy["spec"]["ingress"] for f in rule.get("from", [])]
        self.assertIn({"podSelector": {"matchLabels": {"app.kubernetes.io/component": "migrate"}}}, sources)


@unittest.skipUnless(shutil.which("helm"), "helm not on PATH")
class MigrateJobRenderTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.charts = {chart.relative_to(REPO).as_posix(): chart for _, chart in gen.charts()}
        cls.apps = [(env, app, src) for env, _, app in applications() if env in ("dev", "stage", "prod", "local")
                    for src in service_chart_sources(app) if src["path"] in cls.charts]

    def job_problems(self, env, docs, release, chart_name):
        jobs = [d for d in docs if d["kind"] == "Job"]
        if [j["metadata"]["name"] for j in jobs] != [f"{release}-migrate"]:
            return [f"want exactly one Job {release}-migrate, got {[j['metadata']['name'] for j in jobs]}"]
        job, problems = jobs[0], []
        ann = job["metadata"].get("annotations") or {}
        want = {"argocd.argoproj.io/hook": "PreSync", "argocd.argoproj.io/hook-delete-policy": "BeforeHookCreation",
                "helm.sh/hook": "pre-install,pre-upgrade", "helm.sh/hook-delete-policy": "before-hook-creation"}
        for k, v in want.items():
            if ann.get(k) != v:
                problems.append(f"annotation {k}={ann.get(k)!r}, want {v!r}")
        spec = job["spec"]
        if not isinstance(spec.get("backoffLimit"), int) or not 0 < spec["backoffLimit"] <= 5:
            problems.append(f"backoffLimit {spec.get('backoffLimit')}")
        if not isinstance(spec.get("activeDeadlineSeconds"), int):
            problems.append("no activeDeadlineSeconds")
        pod = spec["template"]
        labels = pod["metadata"]["labels"]
        if labels.get("app.kubernetes.io/name") != f"{chart_name}-migrate" or labels.get("app.kubernetes.io/component") != "migrate":
            problems.append(f"pod labels {labels}")
        ps = pod["spec"]
        if ps.get("restartPolicy") != "Never" or ps.get("automountServiceAccountToken") is not False:
            problems.append("restartPolicy Never and automountServiceAccountToken false are required")
        sc = ps.get("securityContext") or {}
        if sc.get("runAsNonRoot") is not True or (sc.get("seccompProfile") or {}).get("type") != "RuntimeDefault":
            problems.append(f"pod securityContext {sc}")
        (c,) = ps["containers"]
        csc = c.get("securityContext") or {}
        if csc.get("readOnlyRootFilesystem") is not True or csc.get("allowPrivilegeEscalation") is not False \
                or (csc.get("capabilities") or {}).get("drop") != ["ALL"]:
            problems.append(f"container securityContext {csc}")
        limits = (c.get("resources") or {}).get("limits") or {}
        if not limits.get("cpu") or not limits.get("memory"):
            problems.append(f"resource limits {limits}")
        if c.get("ports"):
            problems.append("the migration Job opens a port")
        if not (c.get("command") or c.get("args")) or "migrate" not in " ".join((c.get("command") or []) + (c.get("args") or [])):
            problems.append(f"command {c.get('command')} args {c.get('args')} do not run the migrate command")
        db, owner = secret_ref(c.get("env"), "DATABASE_URL"), secret_ref(c.get("env"), "MIGRATE_DATABASE_URL")
        if not db or db != owner:
            problems.append(f"DATABASE_URL {db} and MIGRATE_DATABASE_URL {owner} must come from the owner Secret")
        if env in ("stage", "prod") and db:
            serving = {(r["name"], r["key"]) for d in docs if d["kind"] != "Job" and pod_template(d)
                       for sc_ in containers(pod_template(d)) if (r := secret_ref(sc_.get("env"), "DATABASE_URL"))}
            if (db["name"], db["key"]) in serving:
                problems.append(f"the Job uses the serving pods' Secret {db['name']}: it must be the owner role's")
        for d in docs:
            if d["kind"] == "NetworkPolicy" and "Ingress" in d["spec"].get("policyTypes", []) \
                    and selects(d["spec"].get("podSelector") or {}, labels):
                problems.append(f"NetworkPolicy {d['metadata']['name']} selects the Job's pod")
            if d["kind"] == "Service" and (d["spec"].get("selector") or {}) and selects({"matchLabels": d["spec"]["selector"]}, labels):
                problems.append(f"Service {d['metadata']['name']} routes to the Job's pod")
        return problems

    def test_every_environment_renders_the_job_and_no_serving_pod_migrates(self):
        jobs, seen = [], set()
        for env, app, src in self.apps:
            release = release_name(app, src)
            chart_name = yaml.safe_load((self.charts[src["path"]] / "Chart.yaml").read_text())["name"]
            with self.subTest(env=env, release=release):
                docs = render(app, src)
                self.assertEqual(self.job_problems(env, docs, release, chart_name), [])
                migrating = [f"{d['kind']} {d['metadata']['name']}/{c['name']}" for d in docs if d["kind"] != "Job" and pod_template(d)
                             for c in containers(pod_template(d))
                             for e in c.get("env") or [] if e.get("name") == "MIGRATE_ON_START" and str(e.get("value")) == "true"]
                self.assertEqual(migrating, [], "serving containers that still migrate on start")
                jobs += [d for d in docs if d["kind"] == "Job"]
                seen.add((env, src["path"]))
        self.assertEqual({p for e, p in seen if e == "dev"}, set(self.charts), "a chart with migrations without a dev render")
        self.assertEqual({p for e, p in seen if e == "stage"}, set(self.charts), "a chart with migrations without a stage render")
        self.assertIn(("prod", "services/platform-health/charts/hello-ledger"), seen)
        if shutil.which("kubeconform"):
            res = subprocess.run(["kubeconform", "-strict", "-summary", "-"], input=yaml.safe_dump_all(jobs), capture_output=True, text=True)
            self.assertEqual(res.returncode, 0, res.stdout + res.stderr)

    def helm(self, chart, *args):
        return subprocess.run(["helm", "template", "r", str(REPO / chart), "--namespace", "hello-ledger", *args],
                              capture_output=True, text=True)

    def test_migrating_on_start_with_the_job_enabled_fails_the_render(self):
        cases = [("services/ledger/charts/ledger", "migrateOnStart"), ("services/transport/charts/transport", "env.MIGRATE_ON_START"),
                 ("services/sales/charts/sales", "config.migrateOnStart"), ("services/receivables/charts/receivables", "migrations.migrateOnStart")]
        for chart, path in cases:
            with self.subTest(chart=chart):
                res = self.helm(chart, "--set-string", f"{path}=true")
                self.assertNotEqual(res.returncode, 0)
                self.assertIn(f"{path} is true while migrateJob.enabled", res.stderr)
                res = self.helm(chart, "--set-string", f"{path}=true", "--set", "migrateJob.enabled=false")
                self.assertEqual(res.returncode, 0, res.stderr)
                self.assertNotIn("kind: Job", res.stdout)

    def test_the_job_refuses_an_empty_owner_secret(self):
        res = self.helm("services/ledger/charts/ledger", "--set", "migrateJob.ownerSecret=")
        self.assertNotEqual(res.returncode, 0)
        self.assertIn("migrateJob.ownerSecret", res.stderr)

    def test_the_postgres_chart_admits_each_clients_migrate_pod(self):
        res = subprocess.run(["helm", "template", "p", str(DEPLOY / "postgres"), "--set", "database=depot_x",
                              "--set", "clientApps={metadata}", "--set", "externalSecret.environment=dev"], capture_output=True, text=True)
        self.assertEqual(res.returncode, 0, res.stderr)
        (policy,) = [d for d in yaml.safe_load_all(res.stdout) if d and d["kind"] == "NetworkPolicy"]
        sources = [f["podSelector"] for rule in policy["spec"]["ingress"] for f in rule["from"] if "namespaceSelector" not in f]
        self.assertIn({"matchLabels": {"app.kubernetes.io/name": "metadata"}}, sources)
        self.assertIn({"matchLabels": {"app.kubernetes.io/name": "metadata-migrate", "app.kubernetes.io/component": "migrate"}}, sources)


if __name__ == "__main__":
    unittest.main()
