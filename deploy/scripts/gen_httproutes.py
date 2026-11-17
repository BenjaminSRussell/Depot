#!/usr/bin/env python3
"""Generate the API gateway's HTTPRoutes from the code, and check them for drift (D31, B-02a, #661).

    python3 deploy/scripts/gen_httproutes.py            # rewrite deploy/gateway/httproutes.yaml and the targetRefs
                                                         # of deploy/gateway/backendtrafficpolicy.yaml
    python3 deploy/scripts/gen_httproutes.py --check    # CI: fail when either file differs from what would be written,
                                                         # or when a prefix the shell calls has no backend
    python3 deploy/scripts/gen_httproutes.py --list     # print prefix -> Service table

Where each piece comes from:

- Prefixes: every `/api/v1/<prefix>` the web shell calls (apps/web/src, tests and the demo backend left out), found as a
  literal or as a path joined to a bare `/api/v1` root in a module's client (`shell_prefixes`), plus the partner and
  agent prefixes in EXTRA_PREFIXES that the shell does not call but were routed before.
- Owner: the service whose OpenAPI document (`<service dir>/api/openapi.yaml`) has paths under that prefix, in one of
  the shapes `/v1/<prefix>/...` (the convention), `/api/v1/<prefix>/...`, `/<prefix>/v1/...` or `/<prefix>` (D22
  exceptions). The gateway rewrites `/api/v1/<prefix>` and `/v1/<prefix>` to whatever base the document uses, so the
  browser and partners see one shape whatever the service serves.
- Backend: the dev Argo CD Application that deploys that service's chart (deploy/envs/dev/apps) gives the release name
  and namespace; the chart's templates/service.yaml, with the chart's values and the dev values file, gives the Service
  name and its `http` port. A Service outside the Gateway's namespace gets a backendRef namespace and a ReferenceGrant.
- A prefix two services share (SHARED_PREFIXES) goes to its primary owner; every path element after the prefix that
  only the other service's document has gets a more specific route of its own (Gateway API picks the longest prefix).

Portal BFF upstreams (the services customer-portal and supplier-portal call with service tokens, inside the cluster)
are checked by `portal_upstreams` and deploy/scripts/test_gateway.py against the same Service table.
"""

from __future__ import annotations

import argparse
import copy
import json
import re
import sys
from dataclasses import dataclass, field
from pathlib import Path
from urllib.parse import urlsplit

import yaml

# libyaml when PyYAML has it: the OpenAPI documents are large.
Loader = getattr(yaml, "CSafeLoader", yaml.SafeLoader)


def _load(text):
    return yaml.load(text, Loader=Loader)


DEPLOY = Path(__file__).resolve().parents[1]
REPO = DEPLOY.parent
WEB_SRC = REPO / "apps" / "web" / "src"
GATEWAY_DIR = DEPLOY / "gateway"
ROUTES_FILE = GATEWAY_DIR / "httproutes.yaml"
BTP_FILE = GATEWAY_DIR / "backendtrafficpolicy.yaml"
DEV_APPS = DEPLOY / "envs" / "dev" / "apps"
DEV_VALUES = DEPLOY / "envs" / "dev" / "values"

GATEWAY = "depot-api"
NAMESPACE = "hello-ledger"
BTP_NAME = "depot-api-tenant-rate-limit"
# HTTPRoute allows 16 rules a route; a split route keeps one rule for the /v1 pass-through matches.
MAX_RULES = 16

# Prefixes routed although the shell does not call them: partners and agents use them on /v1. workflow-ts serves its
# decision API (`/v1/workflows/...`, services/workflow-ts/src/decisionApi.ts) without an OpenAPI document, so its base
# is stated here; `ar` is prediction-py's invoice prediction (#47) and comes from its document.
EXTRA_PREFIXES = {"workflows": ("workflow", "/v1/workflows"), "ar": None}

# Prefixes two services' documents share: the primary owner takes the prefix, the other the path elements only it has.
SHARED_PREFIXES = {"production": "production"}  # processmfg serves the process-manufacturing part of /v1/production

# Shell files that are not part of the shipped shell.
SKIP_PARTS = {"demo"}
SKIP_FILE = re.compile(r"(\.test\.|\.spec\.|^test-setup\.ts$|^test-utils\.ts$)")

API_LITERAL = re.compile(r"/api/v1/([a-z0-9][a-z0-9-]*)(?![A-Za-z0-9_-])")
ROOT_CONST = re.compile(r"""(?:const|let)\s+(\w+)\s*=\s*["'`]/api/v1/?["'`]""")
ROOT_INLINE = re.compile(r"`/api/v1\$\{")
OTHER_BASE = re.compile(r"""(?:const|let)\s+\w+\s*=\s*["'`]/api/v1/[a-z]""")
PATH_LITERAL = re.compile(r"""["'`]/([a-z][a-z0-9-]*)(?![A-Za-z0-9_-]*\.[a-z]{2,4}["'`])""")

PATH_SHAPES = [
    (re.compile(r"^/v1/([a-z0-9][a-z0-9-]*)(?=[/:]|$)"), "/v1/{p}"),
    (re.compile(r"^/api/v1/([a-z0-9][a-z0-9-]*)(?=[/:]|$)"), "/api/v1/{p}"),
    (re.compile(r"^/([a-z0-9][a-z0-9-]*)/v1(?=/|$)"), "/{p}/v1"),
]
NOT_API = {"healthz", "readyz", "metrics", "internal", "bff"}


class GenError(Exception):
    pass


# --- shell ------------------------------------------------------------------------------------------------------------


def shell_files(root: Path = WEB_SRC):
    for path in sorted(root.rglob("*")):
        if path.suffix not in (".ts", ".tsx") or not path.is_file():
            continue
        rel = path.relative_to(root)
        if SKIP_PARTS & set(rel.parts) or SKIP_FILE.search(path.name):
            continue
        yield path


def shell_prefixes(root: Path = WEB_SRC) -> dict[str, set[str]]:
    """prefix -> files (relative to the repo) of the shell that call /api/v1/<prefix>."""
    found: dict[str, set[str]] = {}
    for path in shell_files(root):
        text = path.read_text()
        rel = path.relative_to(REPO).as_posix() if path.is_relative_to(REPO) else str(path)
        for m in API_LITERAL.finditer(text):
            found.setdefault(m.group(1), set()).add(rel)
        # A client that joins paths to a bare /api/v1 root (assets: /assets and /leases; service: the generic case
        # routes; migration-factory: /migration and /cutover). When the file has another /api/v1/<x> base too, only
        # the lines that name the root count.
        roots = ROOT_CONST.findall(text)
        if not roots and not ROOT_INLINE.search(text):
            continue
        only_root_lines = bool(roots) and bool(OTHER_BASE.search(text))
        for line in text.splitlines():
            if only_root_lines and not any(re.search(rf"\b{r}\b", line) for r in roots):
                continue
            for m in PATH_LITERAL.finditer(line):
                if m.group(1) != "api":
                    found.setdefault(m.group(1), set()).add(rel)
    return found


# --- Argo CD dev Applications and charts ------------------------------------------------------------------------------


def _expand(appset):
    (gen,) = appset["spec"]["generators"]
    raw = json.dumps(appset["spec"]["template"])
    for element in gen["list"]["elements"]:
        yield json.loads(re.sub(r"\{\{\s*\.(\w+)\s*\}\}", lambda m: str(element[m.group(1)]), raw))


def dev_applications():
    for path in sorted(DEV_APPS.glob("*.yaml")):
        for doc in yaml.safe_load_all(path.read_text()):
            if not doc or doc.get("apiVersion") != "argoproj.io/v1alpha1":
                continue
            yield from (_expand(doc) if doc["kind"] == "ApplicationSet" else [doc])


def _merge(base, over):
    out = copy.deepcopy(base)
    for k, v in (over or {}).items():
        out[k] = _merge(out.get(k) or {}, v) if isinstance(v, dict) and isinstance(out.get(k), dict) else v
    return out


@dataclass
class Release:
    release: str
    namespace: str
    chart: Path  # repo-relative chart directory
    values: dict
    service_dir: Path = field(init=False)

    def __post_init__(self):
        self.service_dir = self.chart.parent.parent  # services/<dir>/charts/<chart>


def dev_releases() -> dict[str, Release]:
    """release name -> Release, for every dev Application whose source is a chart under services/ or tools/."""
    out = {}
    for app in dev_applications():
        spec = app["spec"]
        for src in spec.get("sources") or [spec["source"]]:
            chart = src.get("path", "")
            if not chart.startswith(("services/", "tools/")) or not (REPO / chart / "Chart.yaml").is_file():
                continue
            helm = src.get("helm") or {}
            name = helm.get("releaseName", app["metadata"]["name"])
            values = yaml.safe_load((REPO / chart / "values.yaml").read_text()) or {}
            for vf in helm.get("valueFiles", []):
                f = REPO / vf.partition("/")[2]
                if f.is_file():
                    values = _merge(values, yaml.safe_load(f.read_text()) or {})
            values = _merge(values, helm.get("valuesObject") or {})
            out[name] = Release(name, spec["destination"]["namespace"], Path(chart), values)
    return out


def _value(values, dotted):
    v = values
    for part in dotted.split("."):
        if not isinstance(v, dict) or part not in v:
            raise GenError(f"no value {dotted}")
        v = v[part]
    return v


def _render(text, rel: Release, chart_name):
    def sub(m):
        expr = m.group(1).strip()
        if expr == ".Release.Name":
            return rel.release
        if expr == ".Chart.Name":
            return chart_name
        if expr.startswith(".Values."):
            return str(_value(rel.values, expr[len(".Values."):]))
        raise GenError(f"{rel.chart}/templates/service.yaml: cannot evaluate {{{{ {expr} }}}}")
    return re.sub(r"\{\{-?\s*([^}]*?)\s*-?\}\}", sub, text)


@dataclass(frozen=True)
class Backend:
    name: str
    namespace: str
    port: int
    release: str


def chart_service(rel: Release) -> Backend | None:
    """The chart's API Service (the first Service in templates/service.yaml, port `http`), or None without one."""
    path = REPO / rel.chart / "templates" / "service.yaml"
    if not path.is_file():
        return None
    chart_name = yaml.safe_load((REPO / rel.chart / "Chart.yaml").read_text())["name"]
    for doc in path.read_text().split("\n---"):
        if not re.search(r"^kind: Service$", doc, re.M):
            continue
        name = re.search(r"^metadata:\n(?:  .*\n)*?  name: (.+)$", doc, re.M)
        port = (re.search(r"\{\s*name: http,\s*port: (\{\{[^}]*\}\}|\d+)", doc)
                or re.search(r"- name: http\n\s+port: (\{\{[^}]*\}\}|\d+)\s*$", doc, re.M))
        if not name or not port:
            raise GenError(f"{path.relative_to(REPO)}: no metadata.name or http port in the first Service")
        return Backend(_render(name.group(1).strip(), rel, chart_name), rel.namespace,
                       int(_render(port.group(1).strip(), rel, chart_name)), rel.release)
    return None


def service_table(releases=None) -> dict[tuple[str, str], Backend]:
    """(namespace, Service name) -> Backend for every dev service chart with an API Service."""
    releases = releases if releases is not None else dev_releases()
    out = {}
    for rel in releases.values():
        b = chart_service(rel)
        if b:
            out[(b.namespace, b.name)] = b
    return out


# --- OpenAPI ownership ------------------------------------------------------------------------------------------------


def openapi_owners(releases) -> dict[str, dict[str, tuple[str, set[str]]]]:
    """prefix -> {release: (upstream base, the path elements after the prefix that release's document has)}."""
    owners: dict[str, dict[str, tuple[str, set[str]]]] = {}
    for rel in releases.values():
        doc_path = REPO / rel.service_dir / "api" / "openapi.yaml"
        if not doc_path.is_file():
            continue
        doc = _load(doc_path.read_text()) or {}
        for p in doc.get("paths") or {}:
            if p == "/mcp":
                owners.setdefault("mcp", {}).setdefault(rel.release, ("/mcp", set()))
                continue
            for shape, base in PATH_SHAPES:
                m = shape.match(p)
                if not m or m.group(1) in NOT_API:
                    continue
                prefix = m.group(1)
                upstream = base.format(p=prefix)
                entry = owners.setdefault(prefix, {}).setdefault(rel.release, (upstream, set()))
                if entry[0] != upstream:
                    raise GenError(f"{doc_path.relative_to(REPO)}: /{prefix} under both {entry[0]} and {upstream}")
                rest = p[m.end():]
                if rest.startswith("/") and len(rest) > 1:
                    entry[1].add(rest[1:].split("/")[0])
                break
    return owners


# --- route model ------------------------------------------------------------------------------------------------------


@dataclass
class Route:
    name: str
    prefix: str  # the public prefix the route serves (or splits)
    backend: Backend
    upstream: str  # the base the service serves the prefix under
    elements: tuple[str, ...] = ()  # non-empty: only these path elements after the prefix

    def matches(self):
        """(public base, upstream base) pairs, one rule each."""
        if not self.elements:
            return [(f"/api/v1/{self.prefix}", self.upstream), (f"/v1/{self.prefix}", self.upstream)]
        return [(f"/api/v1/{self.prefix}/{e}", f"{self.upstream}/{e}") for e in self.elements]


@dataclass
class Model:
    routes: list[Route]
    shell: dict[str, set[str]]
    services: dict[tuple[str, str], Backend]


def build(shell=None, releases=None) -> Model:
    releases = releases if releases is not None else dev_releases()
    shell = shell if shell is not None else shell_prefixes()
    services = service_table(releases)
    by_release = {b.release: b for b in services.values()}
    owners = openapi_owners(releases)
    errors, routes = [], []
    wanted = {p: None for p in shell} | EXTRA_PREFIXES
    for prefix in sorted(wanted):
        fixed = wanted[prefix]
        if fixed:
            release, upstream = fixed
            if release not in by_release:
                errors.append(f"/api/v1/{prefix}: release {release} has no Service")
                continue
            routes.append(Route(prefix, prefix, by_release[release], upstream))
            continue
        found = {r: v for r, v in owners.get(prefix, {}).items() if r in by_release}
        if not found:
            where = ", ".join(sorted(shell.get(prefix, []))) or "EXTRA_PREFIXES"
            errors.append(f"/api/v1/{prefix} (called in {where}): no dev chart's OpenAPI document serves it")
            continue
        if len(found) > 1:
            primary = SHARED_PREFIXES.get(prefix)
            if primary not in found:
                errors.append(f"/api/v1/{prefix}: served by {sorted(found)}; name the primary in SHARED_PREFIXES")
                continue
            routes.append(Route(prefix, prefix, by_release[primary], found[primary][0]))
            taken = found[primary][1]
            for other in sorted(set(found) - {primary}):
                upstream, elements = found[other]
                if upstream != found[primary][0]:
                    errors.append(f"/api/v1/{prefix}: {primary} and {other} serve it under different bases")
                own = sorted(elements - taken)
                if any("{" in e for e in own):
                    errors.append(f"/api/v1/{prefix}: {other} has a path parameter right after the prefix")
                    continue
                per = MAX_RULES - 1
                chunks = [own[i:i + per] for i in range(0, len(own), per)]
                for i, chunk in enumerate(chunks, 1):
                    name = other if len(chunks) == 1 else f"{other}-{i}"
                    routes.append(Route(name, prefix, by_release[other], upstream, tuple(chunk)))
                taken = taken | elements
            continue
        (release, (upstream, _)), = found.items()
        routes.append(Route(prefix, prefix, by_release[release], upstream))
    if errors:
        raise GenError("\n".join(errors))
    names = [r.name for r in routes]
    dup = sorted({n for n in names if names.count(n) > 1})
    if dup:
        raise GenError(f"route names used twice: {dup}")
    return Model(routes, shell, services)


# --- rendering --------------------------------------------------------------------------------------------------------


def _backend_ref(b: Backend):
    ref = {"name": b.name, "port": b.port}
    if b.namespace != NAMESPACE:
        ref = {"name": b.name, "namespace": b.namespace, "port": b.port}
    return [ref]


def _rule(public, upstream, b):
    rule = {"matches": [{"path": {"type": "PathPrefix", "value": public}}]}
    if upstream != public:
        rule["filters"] = [{"type": "URLRewrite",
                            "urlRewrite": {"path": {"type": "ReplacePrefixMatch", "replacePrefixMatch": upstream}}}]
    rule["backendRefs"] = _backend_ref(b)
    return rule


def route_doc(r: Route):
    rules = [_rule(pub, up, r.backend) for pub, up in r.matches()]
    if r.elements:  # /v1 pass-through (or rewrite) for the same elements in one rule when the bases agree
        v1 = [(f"/v1/{r.prefix}/{e}", f"{r.upstream}/{e}") for e in r.elements]
        if all(pub == up for pub, up in v1):
            rules.append({"matches": [{"path": {"type": "PathPrefix", "value": pub}} for pub, _ in v1],
                          "backendRefs": _backend_ref(r.backend)})
        else:
            rules += [_rule(pub, up, r.backend) for pub, up in v1]
    if len(rules) > MAX_RULES:
        raise GenError(f"HTTPRoute {r.name}: {len(rules)} rules, more than {MAX_RULES}")
    return {"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
            "metadata": {"name": r.name, "namespace": NAMESPACE},
            "spec": {"parentRefs": [{"name": GATEWAY}], "rules": rules}}


def reference_grants(routes):
    by_ns: dict[str, set[str]] = {}
    for r in routes:
        if r.backend.namespace != NAMESPACE:
            by_ns.setdefault(r.backend.namespace, set()).add(r.backend.name)
    return [{"apiVersion": "gateway.networking.k8s.io/v1beta1", "kind": "ReferenceGrant",
             "metadata": {"name": f"{GATEWAY}-routes", "namespace": ns},
             "spec": {"from": [{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute", "namespace": NAMESPACE}],
                      "to": [{"group": "", "kind": "Service", "name": n} for n in sorted(names)]}}
            for ns, names in sorted(by_ns.items())]


HEADER = """\
# GENERATED by deploy/scripts/gen_httproutes.py; do not edit. Run `python3 deploy/scripts/gen_httproutes.py` after
# changing a shell API client, a service's OpenAPI paths, a chart's Service or a dev Application; CI runs it with
# --check (make -C deploy test-gateway).
#
# One HTTPRoute per /api/v1/<prefix> the web shell calls, plus {extra} for partners and agents, on Gateway
# depot-api behind the JWT SecurityPolicy. /api/v1/<prefix> and /v1/<prefix> are rewritten to the base the service's
# OpenAPI document uses (/v1/<prefix> by convention; the D22 exceptions are listed in deploy/gateway/README.md), to the
# chart's Service and `http` port as the dev Application deploys it. {routes} routes, {services} Services.
"""


class _Dumper(yaml.SafeDumper):
    """Indents block sequences under their key, as the hand-written manifests in deploy/gateway do."""

    def increase_indent(self, flow=False, indentless=False):
        return super().increase_indent(flow, False)


def _dump(doc):
    return yaml.dump(doc, Dumper=_Dumper, sort_keys=False, default_flow_style=False, width=1000)


def render_routes(model: Model) -> str:
    docs = [route_doc(r) for r in model.routes] + reference_grants(model.routes)
    header = HEADER.format(extra=" and ".join(sorted(EXTRA_PREFIXES)),
                           routes=len(model.routes), services=len({r.backend for r in model.routes}))
    return header + "---\n".join(_dump(d) for d in docs)


BTP_BEGIN = "  # BEGIN targetRefs generated by deploy/scripts/gen_httproutes.py: every HTTPRoute on depot-api\n"
BTP_END = "  # END targetRefs\n"


def render_btp(model: Model, current: str) -> str:
    refs = "  targetRefs:\n" + "".join(
        f"    - group: gateway.networking.k8s.io\n      kind: HTTPRoute\n      name: {r.name}\n" for r in model.routes)
    block = BTP_BEGIN + refs + BTP_END
    if BTP_BEGIN in current:
        head, _, rest = current.partition(BTP_BEGIN)
        _, _, tail = rest.partition(BTP_END)
        return head + block + tail
    m = re.search(r"^  targetRefs:\n(?:    .*\n)+", current, re.M)
    if not m:
        raise GenError(f"{BTP_FILE.relative_to(REPO)}: no spec.targetRefs")
    return current[:m.start()] + block + current[m.end():]


# --- portal BFF upstreams ---------------------------------------------------------------------------------------------


def portal_upstreams(releases=None):
    """(release, key, url, (namespace, Service name, port) or None) for every BFF upstream of the portal charts."""
    releases = releases if releases is not None else dev_releases()
    out = []
    for rel in releases.values():
        for key, url in sorted((rel.values.get("upstreams") or {}).items()):
            parts = urlsplit(url)
            host = parts.hostname or ""
            labels = host.split(".")
            target = None
            if len(labels) >= 3 and labels[2] == "svc":
                target = (labels[1], labels[0], parts.port or 80)
            elif len(labels) == 1:
                target = (rel.namespace, labels[0], parts.port or 80)
            out.append((rel.release, key, url, target))
    return out


# --- checks on rendered manifests -------------------------------------------------------------------------------------


def route_problems(docs, shell, services) -> list[str]:
    """What is wrong with the rendered gateway manifests `docs` (the kustomize output of deploy/gateway): a prefix the
    shell calls without exactly one depot-api route for /api/v1/<prefix>, a backendRef to a Service or port the dev
    charts do not render, or a cross-namespace backendRef without a ReferenceGrant."""
    problems = []
    api = [d for d in docs if d.get("kind") == "HTTPRoute"
           and GATEWAY in {p["name"] for p in d["spec"]["parentRefs"]}]
    owners: dict[str, list[str]] = {}
    for route in api:
        for rule in route["spec"]["rules"]:
            for match in rule["matches"]:
                owners.setdefault(match["path"]["value"], []).append(route["metadata"]["name"])
    for prefix in sorted(shell):
        found = owners.get(f"/api/v1/{prefix}", [])
        if len(found) != 1:
            problems.append(f"/api/v1/{prefix}: {len(found)} routes ({', '.join(found) or 'none'}), want exactly one")
    grants = {(d["metadata"]["namespace"], t["name"]) for d in docs if d.get("kind") == "ReferenceGrant"
              for f in d["spec"]["from"] if f["kind"] == "HTTPRoute" and f["namespace"] == NAMESPACE
              for t in d["spec"]["to"] if t["kind"] == "Service"}
    for route in api:
        ns = route["metadata"]["namespace"]
        for rule in route["spec"]["rules"]:
            for ref in rule["backendRefs"]:
                key = (ref.get("namespace", ns), ref["name"])
                svc = services.get(key)
                where = f"HTTPRoute {route['metadata']['name']}"
                if svc is None:
                    problems.append(f"{where}: Service {key[0]}/{key[1]} is not rendered by any dev chart")
                elif svc.port != ref.get("port"):
                    problems.append(f"{where}: Service {key[0]}/{key[1]} has http port {svc.port}, not {ref.get('port')}")
                if key[0] != ns and key not in grants:
                    problems.append(f"{where}: no ReferenceGrant in {key[0]} admits it to Service {key[1]}")
    return problems


# --- main -------------------------------------------------------------------------------------------------------------


def main(argv=None):
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--check", action="store_true", help="fail on drift instead of writing")
    ap.add_argument("--list", action="store_true", help="print the prefix table")
    args = ap.parse_args(argv)
    try:
        model = build()
        routes = render_routes(model)
        current_btp = BTP_FILE.read_text()
        btp = render_btp(model, current_btp)
    except GenError as e:
        print(f"gen_httproutes: {e}", file=sys.stderr)
        return 1
    if args.list:
        for r in model.routes:
            el = f" ({len(r.elements)} path elements)" if r.elements else ""
            ns = f"{r.backend.namespace}/" if r.backend.namespace != NAMESPACE else ""
            print(f"{r.name:28} /api/v1/{r.prefix:18} -> {ns}{r.backend.name}:{r.backend.port} {r.upstream}{el}")
        return 0
    if args.check:
        stale = [f.relative_to(REPO).as_posix() for f, want in ((ROUTES_FILE, routes), (BTP_FILE, btp))
                 if f.read_text() != want]
        if stale:
            print(f"gen_httproutes: {', '.join(stale)} out of date; run python3 deploy/scripts/gen_httproutes.py",
                  file=sys.stderr)
            return 1
        print(f"gen_httproutes: {len(model.routes)} routes up to date")
        return 0
    ROUTES_FILE.write_text(routes)
    BTP_FILE.write_text(btp)
    print(f"gen_httproutes: wrote {len(model.routes)} routes")
    return 0


if __name__ == "__main__":
    sys.exit(main())
