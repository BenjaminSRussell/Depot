"""D28 / ADR-0012 rule 3 (B-09a): every service with migrations has an owner-role migration Job and no serving pod migrates.

Without a cluster this test checks:
- deploy/scripts/gen_migrate_jobs.py --check passes, and serving_value() fails closed on a MIGRATE_ON_START it cannot read;
- every directory under services/ or tools/ with migrations/*.sql has a chart carrying the generated
  templates/migrate-job.yaml with migrateJob.enabled, and its image has a migrate command that runs before the service
  starts (Go: dbx.HandleMigrateCommand is the first call of the image's main, after logging set-up only; agents-ts and
  integration-hub-ts dispatch `migrate` before they open a pool or a server; prediction's is its own module);
- close and eam no longer migrate unconditionally at start, so a depot_app pod never runs DDL;
- gen_networkpolicies.py leaves the Job's pod out of every chart's pod names (it is neither selected nor a peer), the
  shared dev Postgres admits each migrate pod by its exact <pod>-migrate name (not the bare component label), and the
  deploy/postgres chart admits <entry>-migrate for each clientApps entry, the Job's label of every chart;
- with helm on PATH, every dev, stage, prod and local Application of a chart with migrations renders:
  - in stage and prod exactly one Job <release>-migrate; in dev and local one exactly for the charts that enable it
    (DEV_JOBS) and none for the rest;
  - a Job that is an Argo CD PreSync hook (and Helm pre-install/pre-upgrade) with a delete policy, backoffLimit and
    activeDeadlineSeconds, labelled <pod>-migrate / component migrate, runAsNonRoot, seccomp RuntimeDefault, a read-only
    root filesystem, no privilege escalation, all capabilities dropped, CPU and memory limits, no port, the serving
    pods' image and pull policy, and DATABASE_URL and MIGRATE_DATABASE_URL from one Secret, which in stage and prod is not
    a serving container's Secret (the owner role, not depot_app) and which is declared in the same environment or listed
    in EXTERNALLY_PROVISIONED with a reason; MIGRATE_ALLOW_APP_ROLE only in dev and local;
  - no serving container with MIGRATE_ON_START=true while a Job renders, and none at all in stage and prod;
  - no NetworkPolicy and no Service of the chart selecting the Job's pod;
  - for an Application that renders a Job, a syncPolicy.retry (one failed PreSync must not end the revision's sync);
  - setting the serving MIGRATE_ON_START to true while the Job is enabled fails the render.
  With kubeconform on PATH the rendered Jobs are validated.
"""

import fnmatch
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import gen_migrate_jobs as gen  # noqa: E402
import gen_networkpolicies  # noqa: E402
from test_argocd_apps import applications, load_docs, pod_template, render, release_name, service_chart_sources  # noqa: E402

DEPLOY = Path(__file__).resolve().parents[1]
REPO = DEPLOY.parent

# The dev (and local) charts whose Job is on: a Secret it can use exists (D28). The rest keep their Job off in dev
# (deploy/envs/dev/migrate-jobs.yaml); identity and org migrate on start in dev, as before B-09a.
DEV_JOBS = {"fx", "receiving", "hello-ledger", "eam", "transport"}
DEV_ON_START = {"identity", "org"}
# Secrets a Job may use without a manifest in the same environment, by (environment, Secret name pattern), and why.
EXTERNALLY_PROVISIONED = {
    ("dev", "fx-db"): "fx's serving Secret (depot_app owns the dev schema); created outside the repository like every dev "
                      "service database Secret (#213)",
    ("dev", "receiving-db"): "receiving's serving Secret (depot_app owns the dev schema); created outside the repository (#213)",
    ("dev", "hello-ledger-db"): "hello-ledger's serving Secret (depot_app owns the dev schema), with the dev Postgres passwords; "
                                "created outside the repository (#213)",
    ("local", "hello-ledger-db"): "created by make -C deploy deploy-local before the Applications",
    ("stage", "*-db-owner"): "the owner role's Secret, provisioned with the stage databases in B-09 part b (#213); never an "
                             "ordinary resource of the same Application, or PreSync would wait for it on the first install",
    ("prod", "*-db-owner"): "the owner role's Secret, provisioned with the prod databases in B-09 part c (#213); never an "
                            "ordinary resource of the same Application",
}
# Statements a Go main may run before dbx.HandleMigrateCommand: logging set-up and a signal context, nothing that serves.
PRELUDE = [re.compile(p) for p in (
    r"slog\.SetDefault\(.*\)",
    r"if err := setupLogging\(os\.Stdout, os\.Getenv\); err != nil \{", r'slog\.Error\("hr logging", "err", err\)', r"os\.Exit\(1\)", r"\}",
    r"ctx, stop := signal\.NotifyContext\(context\.Background\(\), os\.Interrupt, syscall\.SIGTERM\)", r"defer stop\(\)",
)]


def migrating_services():
    """Every services/<dir> or tools/<dir> with migrations/*.sql, found here independently of the generator."""
    return sorted({p.parents[1] for p in list(REPO.glob("services/*/migrations/*.sql")) + list(REPO.glob("tools/*/migrations/*.sql"))})


def containers(pod):
    spec = pod.get("spec") or {}
    return (spec.get("initContainers") or []) + (spec.get("containers") or [])


def env_value(env, name):
    for e in env or []:
        if e.get("name") == name:
            return e
    return None


def secret_ref(env, name):
    e = env_value(env, name)
    return None if e is None else ((e.get("valueFrom") or {}).get("secretKeyRef") or {})


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


def normalized_image(ref):
    """repo@digest when a digest is pinned (the tag is then only a label), else repo:tag."""
    if "@" in ref:
        repo, digest = ref.split("@", 1)
        return f"{repo.rsplit(':', 1)[0] if ':' in repo.rsplit('/', 1)[-1] else repo}@{digest}"
    return ref


def declared_secrets(env):
    """Names of the Secrets the manifests of deploy/envs/<env> (or deploy/argocd for local) declare: Secret, SealedSecret,
    and the target of an ExternalSecret."""
    roots = [DEPLOY / "argocd"] if env == "local" else [DEPLOY / "envs" / env]
    names = set()
    for root in roots:
        for path in root.rglob("*.yaml"):
            for doc in yaml.safe_load_all(path.read_text()):
                if not isinstance(doc, dict):
                    continue
                kind, meta = doc.get("kind"), doc.get("metadata") or {}
                if kind in ("Secret", "SealedSecret"):
                    names.add(meta.get("name"))
                elif kind == "ExternalSecret":
                    names.add(((doc.get("spec") or {}).get("target") or {}).get("name") or meta.get("name"))
    return names


def main_body(path):
    text = path.read_text()
    start = text.index("func main() {\n") + len("func main() {\n")
    end = text.index("\n}\n", start)
    return text[start:end]


class MigrateJobFilesTest(unittest.TestCase):
    def test_generated_files_are_current(self):
        self.assertEqual(gen.main(["--check"]), 0, "run python3 deploy/scripts/gen_migrate_jobs.py")

    def test_the_render_guard_fails_closed(self):
        with tempfile.TemporaryDirectory(dir=REPO / "services") as tmp:
            chart = Path(tmp) / "charts" / "x"
            (chart / "templates").mkdir(parents=True)
            (chart / "values.yaml").write_text("migrateOnStart: false\n")
            (chart / "templates" / "deployment.yaml").write_text(
                "env:\n  - name: MIGRATE_ON_START\n    value: {{ .Values.migrateOnStart | toString | quote }}\n")
            with self.assertRaises(SystemExit) as err:
                gen.serving_value(chart)
            self.assertIn("serving_value() does not read", str(err.exception))
            (chart / "templates" / "deployment.yaml").write_text(
                "env:\n  - name: MIGRATE_ON_START\n    value: {{ .Values.migrateOnStart | quote }}\n")
            self.assertEqual(gen.serving_value(chart), "migrateOnStart")

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

    def test_every_image_migrates_before_the_service_starts(self):
        problems = []
        for service in migrating_services():
            if service.name in gen.NON_GO:
                continue
            mains = [p for p in service.glob("cmd/*/main.go") if "dbx.HandleMigrateCommand(migrations.FS" in p.read_text()]
            entry = (service / "Dockerfile").read_text() if (service / "Dockerfile").is_file() else ""
            if not mains:
                problems.append(f"{service.relative_to(REPO)}: no main calls dbx.HandleMigrateCommand")
                continue
            if entry and not any(f"./cmd/{m.parent.name}" in entry for m in mains):
                problems.append(f"{service.relative_to(REPO)}: the Dockerfile builds a binary without dbx.HandleMigrateCommand")
            for m in mains:
                lines = [ln.strip() for ln in main_body(m).splitlines()]
                lines = [ln for ln in lines if ln and not ln.startswith("//")]
                first = next((i for i, ln in enumerate(lines) if ln.startswith("dbx.HandleMigrateCommand(")), None)
                if first is None:
                    problems.append(f"{m.relative_to(REPO)}: dbx.HandleMigrateCommand is not a statement of main()")
                    continue
                before = [ln for ln in lines[:first] if not any(p.fullmatch(ln) for p in PRELUDE)]
                if before:
                    problems.append(f"{m.relative_to(REPO)}: main() runs {before[0]!r} before dbx.HandleMigrateCommand")
        self.assertEqual(problems, [], "`<binary> migrate` must migrate before anything of the service starts")
        agents = (REPO / "services/agents-ts/src/main.ts").read_text()
        entry = agents.index("const isEntrypoint")
        self.assertLess(agents.index("process.argv[2] === MIGRATE_COMMAND", entry), agents.index("main().catch", entry))
        hub = (REPO / "services/integration-hub-ts/src/main.ts").read_text()
        dispatch = hub.index("process.argv[2] === MIGRATE_COMMAND")
        for start in ("new pg.Pool(", "createServer(", "startConsumer("):
            if start in hub:
                self.assertLess(dispatch, hub.index(start), f"integration-hub-ts calls {start} before the migrate command")
        py = (REPO / "services/prediction-py/src/prediction/migrate.py").read_text()
        self.assertNotIn("prediction.api", py)
        self.assertNotIn("uvicorn", py)
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

    def test_dev_postgres_admits_each_migrate_pod_by_name(self):
        (policy,) = [d for d in load_docs(DEPLOY / "envs/dev/apps/postgres.yaml") if d["kind"] == "NetworkPolicy"]
        sources = [f for rule in policy["spec"]["ingress"] for f in rule.get("from", [])]
        bare = [f for f in sources if (f.get("podSelector") or {}).get("matchLabels") == {"app.kubernetes.io/component": "migrate"}]
        self.assertEqual(bare, [], "a bare component label admits any pod that sets it (#723)")
        admitted = {f["podSelector"]["matchLabels"]["app.kubernetes.io/name"] for f in sources
                    if (f.get("podSelector") or {}).get("matchLabels", {}).get("app.kubernetes.io/component") == "migrate"}
        dev = {s["path"]: s["namespace"] for s in gen_networkpolicies.env_services("dev").values()}
        want = {f"{gen.pod_name(c)}-migrate" for _, c in gen.charts() if dev.get(c.relative_to(REPO).as_posix()) == "hello-ledger"}
        self.assertEqual(admitted, want)


@unittest.skipUnless(shutil.which("helm"), "helm not on PATH")
class MigrateJobRenderTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.charts = {chart.relative_to(REPO).as_posix(): chart for _, chart in gen.charts()}
        cls.apps = [(env, app, src) for env, _, app in applications() if env in ("dev", "stage", "prod", "local")
                    for src in service_chart_sources(app) if src["path"] in cls.charts]
        cls.declared = {env: declared_secrets(env) for env in ("dev", "stage", "prod", "local")}

    def secret_problem(self, env, name):
        if name in self.declared[env]:
            return None
        if any(e == env and fnmatch.fnmatchcase(name, pattern) for e, pattern in EXTERNALLY_PROVISIONED):
            return None
        return (f"{env}: the Job's Secret {name} is declared nowhere in this environment and is not in EXTERNALLY_PROVISIONED: "
                "the PreSync Job would wait for it and fail every sync")

    def job_problems(self, env, docs, release, pod):
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
        tpl = spec["template"]
        labels = tpl["metadata"]["labels"]
        if labels.get("app.kubernetes.io/name") != f"{pod}-migrate" or labels.get("app.kubernetes.io/component") != "migrate":
            problems.append(f"pod labels {labels}, want app.kubernetes.io/name {pod}-migrate")
        ps = tpl["spec"]
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
        serving = [sc_ for d in docs if d["kind"] != "Job" and pod_template(d) for sc_ in containers(pod_template(d))]
        images = {(normalized_image(s["image"]), s.get("imagePullPolicy")) for s in serving}
        if (normalized_image(c["image"]), c.get("imagePullPolicy")) not in images:
            problems.append(f"the Job runs {c['image']} ({c.get('imagePullPolicy')}), no serving container does: {sorted(images)}")
        db, owner = secret_ref(c.get("env"), "DATABASE_URL"), secret_ref(c.get("env"), "MIGRATE_DATABASE_URL")
        if not db or db != owner:
            problems.append(f"DATABASE_URL {db} and MIGRATE_DATABASE_URL {owner} must come from the owner Secret")
        elif (problem := self.secret_problem(env, db["name"])):
            problems.append(problem)
        if env in ("stage", "prod") and db:
            app_secrets = {(r["name"], r["key"]) for s in serving if (r := secret_ref(s.get("env"), "DATABASE_URL"))}
            if (db["name"], db["key"]) in app_secrets:
                problems.append(f"the Job uses the serving pods' Secret {db['name']}: it must be the owner role's")
        if env_value(c.get("env"), "MIGRATE_ALLOW_APP_ROLE") is not None and env not in ("dev", "local"):
            problems.append("MIGRATE_ALLOW_APP_ROLE outside dev")
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
            chart = self.charts[src["path"]]
            with self.subTest(env=env, release=release):
                docs = render(app, src)
                rendered = [d for d in docs if d["kind"] == "Job"]
                want_job = env in ("stage", "prod") or release in DEV_JOBS
                if want_job:
                    self.assertEqual(self.job_problems(env, docs, release, gen.pod_name(chart)), [])
                    retry = (app["spec"].get("syncPolicy") or {}).get("retry") or {}
                    self.assertGreater(retry.get("limit", 0), 0, "an Application with a PreSync migration Job needs syncPolicy.retry")
                else:
                    self.assertEqual(rendered, [], f"{env}: {release} has no Secret for its Job; keep it off (DEV_JOBS)")
                migrating = [f"{d['kind']} {d['metadata']['name']}/{c['name']}" for d in docs if d["kind"] != "Job" and pod_template(d)
                             for c in containers(pod_template(d))
                             for e in c.get("env") or [] if e.get("name") == "MIGRATE_ON_START" and str(e.get("value")) == "true"]
                if want_job or env in ("stage", "prod") or release not in DEV_ON_START:
                    self.assertEqual(migrating, [], "serving containers that migrate on start")
                jobs += rendered
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

    def test_the_job_runs_the_serving_image_for_every_image_setting(self):
        settings = [[], ["--set", "image.tag=1.2.3"], ["--set", "image.digest=sha256:" + "a" * 64],
                    ["--set", "global.imageRegistry=mirror.example/depot"]]
        for rel in self.charts:
            for args in settings:
                with self.subTest(chart=rel, args=args):
                    res = self.helm(rel, *args)
                    self.assertEqual(res.returncode, 0, res.stderr)
                    docs = [d for d in yaml.safe_load_all(res.stdout) if d]
                    (job,) = [d for d in docs if d["kind"] == "Job"]
                    image = normalized_image(job["spec"]["template"]["spec"]["containers"][0]["image"])
                    serving = {normalized_image(c["image"]) for d in docs if d["kind"] != "Job" and pod_template(d)
                               for c in containers(pod_template(d))}
                    self.assertIn(image, serving)

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
        pods = sorted({gen.pod_name(c) for c in self.charts.values()})
        res = subprocess.run(["helm", "template", "p", str(DEPLOY / "postgres"), "--set", "database=depot_x",
                              "--set", "clientApps={" + ",".join(pods) + "}", "--set", "externalSecret.environment=dev"],
                             capture_output=True, text=True)
        self.assertEqual(res.returncode, 0, res.stderr)
        (policy,) = [d for d in yaml.safe_load_all(res.stdout) if d and d["kind"] == "NetworkPolicy"]
        sources = [f["podSelector"] for rule in policy["spec"]["ingress"] for f in rule["from"] if "namespaceSelector" not in f]
        for rel, chart in self.charts.items():
            res = self.helm(rel)
            (job,) = [d for d in yaml.safe_load_all(res.stdout) if d and d["kind"] == "Job"]
            labels = job["spec"]["template"]["metadata"]["labels"]
            with self.subTest(chart=rel):
                self.assertTrue(any(selects(s, labels) for s in sources), f"deploy/postgres with clientApps {gen.pod_name(chart)} "
                                f"does not admit the Job pod {labels['app.kubernetes.io/name']}")


if __name__ == "__main__":
    unittest.main()
