#!/usr/bin/env python3
"""Compile docs/architecture.md into docs/architecture.compiled/ reproducibly, and check it for drift.

    python3 scripts/compile_architecture.py --write [--created YYYY-MM-DD]   # regenerate the JSON files
    python3 scripts/compile_architecture.py --check                          # CI: fail on any drift

The compiled form has two kinds of files.

Generated (written by --write, compared by --check):

  manifest.json    Provenance and the component index. Shape:
                     version         3
                     source          {path: "docs/architecture.md", sha256, lines}. sha256 is over the file's bytes
                                     with CRLF normalized to LF; lines is the number of lines (as `wc -l` counts them)
                     plugin_version  null (key kept from the v2 schema; no plugin compiles this any more)
                     compiler        "scripts/compile_architecture.py"
                     created         date of the compile; --created sets it, otherwise the existing value is kept.
                                     --check ignores it
                     spine, contracts  file names relative to docs/architecture.compiled/
                     components      [{name, slug, status, file, keywords, aliases, jira, depends_on, services}]
                                     copied from scripts/architecture_components.json. status is "as-built" or
                                     "not-built"; services lists the repo-relative directories the component owns
  contracts.json   What the specs say, plus the hand-written pattern contracts. Shape:
                     version, compiler, note
                     summary     {openapiDocuments, paths, operations, asyncapiDocuments, topics, topicsPublished,
                                 topicsSubscribed, patterns}
                     patterns    [{id, kind, severity, mustContain, mustNotContain, mustMatch, source, asBuilt}]
                                 copied from scripts/architecture_patterns.json; source is "<file>#<heading>" inside
                                 docs/architecture.compiled/ and asBuilt records the stage-2 verdict
                     openapi     [{service, component, spec, openapi, title, operations: [{method, path, operationId,
                                 duty, dutyAlternatives?, alsoDuty?, public?}]}] one entry per OpenAPI document, by
                                 spec path; operations by path, then method (GET, PUT, POST, DELETE, OPTIONS, HEAD,
                                 PATCH, TRACE). duty is the operation's x-depot-duty (null when absent)
                     asyncapi    [{service, component, spec, asyncapi, title, publishes: [{topic, types}],
                                 subscribes: [{topic, types}]}] one entry per AsyncAPI document. A topic is a channel
                                 address used by a `send` (publishes) or `receive` (subscribes) operation; types are the
                                 CloudEvents types of its messages (x-depot-type, else a message name that starts with
                                 dev.depot.)
                     topics      [{topic, publishers, subscribers}] every topic in any AsyncAPI document, with the
                                 owning service directories
                   Specs are services/*/api/openapi.yaml, services/*/events/asyncapi.yaml and the same two paths under
                   tools/*/ (tools/migration-factory has both). Test fixtures (testdata/) are not specs.

Hand-written prose (validated by --check, never generated):

  spine.md, components/<slug>.md, README.md
                   Every manifest component has its file, with the headings in REQUIRED_HEADINGS, starting with
                   "# <name>", and citing each directory it owns in backticks. No component file is orphaned. Every
                   repo path cited in backticks exists: a backticked token is a repo path when its first segment is a
                   top-level directory of the repository; a trailing ":<line>[-<line>]" or "#anchor" is ignored, a
                   token with * or ? must match at least one path, and tokens holding placeholders (<, >, {, }, |, $,
                   spaces, "...") are skipped. Write a designed-but-absent path in plain text, not in backticks.

Ownership: every directory under services/ and apps/ is owned by exactly one component, and every directory a
component lists exists.

--check exits 1 and prints one line per problem when the manifest's hash or line count does not match
docs/architecture.md, either JSON file differs from a fresh generation, ownership is incomplete or duplicated, a
component file or heading is missing, a pattern's source does not resolve, or a cited path is missing. Exit 2 is a
usage or input error. PyYAML is required (CI installs pyyaml==6.0.2). Tests: scripts/test_compile_architecture.py.
"""

from __future__ import annotations

import argparse
import datetime as _dt
import glob
import hashlib
import json
import re
import sys
from pathlib import Path
from typing import Any

try:
    import yaml
except ImportError:  # pragma: no cover - exercised only without PyYAML
    sys.stderr.write("compile_architecture: PyYAML is required (pip install pyyaml==6.0.2)\n")
    sys.exit(2)

_Loader = getattr(yaml, "CSafeLoader", yaml.SafeLoader)

COMPILER = "scripts/compile_architecture.py"
SOURCE = "docs/architecture.md"
OUT_DIR = "docs/architecture.compiled"
COMPONENTS_INPUT = "scripts/architecture_components.json"
PATTERNS_INPUT = "scripts/architecture_patterns.json"
OWNED_ROOTS = ("services", "apps")
SPEC_GLOBS = {
    "openapi": ("services/*/api/openapi.yaml", "tools/*/api/openapi.yaml"),
    "asyncapi": ("services/*/events/asyncapi.yaml", "tools/*/events/asyncapi.yaml"),
}
HTTP_METHODS = ("get", "put", "post", "delete", "options", "head", "patch", "trace")
REQUIRED_HEADINGS = (
    "Purpose & Boundaries",
    "Services",
    "Interfaces",
    "Data Ownership",
    "Events Produced",
    "Events Consumed",
    "Dependencies",
    "Gotchas & Constraints",
    "Target — not built",
    "Source Sections",
)
COMPONENT_KEYS = ("name", "slug", "status", "keywords", "aliases", "jira", "depends_on", "services")
STATUSES = ("as-built", "not-built")
PLACEHOLDER_CHARS = set("<>{}|$ ")
LINE_SUFFIX = re.compile(r"(:\d+(?:[-,]\d+)*)+$")
BACKTICK = re.compile(r"`([^`\n]+)`")
INLINE_LIMIT = 200


class InputError(Exception):
    """A missing or malformed input; exit status 2."""


# ---------------------------------------------------------------------------------------------------------------
# Rendering: stable JSON with one line per leaf object, so a diff shows one operation per line.


def _is_scalar(v: Any) -> bool:
    return v is None or isinstance(v, (str, int, float, bool))


def _inline(v: Any) -> bool:
    if _is_scalar(v) or v == [] or v == {}:
        return True
    if isinstance(v, list):
        return all(_is_scalar(x) for x in v)
    if isinstance(v, dict):
        flat = all(_is_scalar(x) or (isinstance(x, list) and all(_is_scalar(y) for y in x)) for x in v.values())
        return flat and len(json.dumps(v, ensure_ascii=False)) <= INLINE_LIMIT
    return False


def _render(v: Any, depth: int) -> str:
    if _inline(v):
        return json.dumps(v, ensure_ascii=False)
    pad, end = "  " * (depth + 1), "  " * depth
    if isinstance(v, dict):
        body = ",\n".join(f"{pad}{json.dumps(k, ensure_ascii=False)}: {_render(x, depth + 1)}" for k, x in v.items())
        return "{\n" + body + "\n" + end + "}"
    body = ",\n".join(pad + _render(x, depth + 1) for x in v)
    return "[\n" + body + "\n" + end + "]"


def render(obj: Any) -> str:
    return _render(obj, 0) + "\n"


# ---------------------------------------------------------------------------------------------------------------
# Inputs


def read_json(root: Path, rel: str) -> Any:
    path = root / rel
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise InputError(f"{rel}: missing") from exc
    except json.JSONDecodeError as exc:
        raise InputError(f"{rel}: invalid JSON: {exc}") from exc


def source_facts(root: Path) -> dict[str, Any]:
    path = root / SOURCE
    if not path.is_file():
        raise InputError(f"{SOURCE}: missing")
    data = path.read_bytes().replace(b"\r\n", b"\n")
    return {
        "path": SOURCE,
        "sha256": hashlib.sha256(data).hexdigest(),
        "lines": len(data.decode("utf-8").splitlines()),
    }


def load_components(root: Path) -> list[dict[str, Any]]:
    doc = read_json(root, COMPONENTS_INPUT)
    comps = doc.get("components") if isinstance(doc, dict) else None
    if not isinstance(comps, list) or not comps:
        raise InputError(f"{COMPONENTS_INPUT}: expected a non-empty 'components' list")
    seen: set[str] = set()
    for i, c in enumerate(comps):
        missing = [k for k in COMPONENT_KEYS if k not in c]
        if missing:
            raise InputError(f"{COMPONENTS_INPUT}: component #{i} lacks {', '.join(missing)}")
        if c["status"] not in STATUSES:
            raise InputError(f"{COMPONENTS_INPUT}: {c['slug']}: status must be one of {', '.join(STATUSES)}")
        if c["slug"] in seen:
            raise InputError(f"{COMPONENTS_INPUT}: duplicate slug {c['slug']}")
        seen.add(c["slug"])
    for c in comps:
        unknown = [d for d in c["depends_on"] if d not in seen]
        if unknown:
            raise InputError(f"{COMPONENTS_INPUT}: {c['slug']}: depends_on names unknown {', '.join(unknown)}")
    return comps


def load_patterns(root: Path) -> list[dict[str, Any]]:
    doc = read_json(root, PATTERNS_INPUT)
    pats = doc.get("patterns") if isinstance(doc, dict) else None
    if not isinstance(pats, list):
        raise InputError(f"{PATTERNS_INPUT}: expected a 'patterns' list")
    ids = [p.get("id") for p in pats]
    dupes = sorted({i for i in ids if ids.count(i) > 1})
    if dupes:
        raise InputError(f"{PATTERNS_INPUT}: duplicate ids {', '.join(map(str, dupes))}")
    return pats


def load_yaml(root: Path, rel: str) -> dict[str, Any]:
    try:
        doc = yaml.load((root / rel).read_text(encoding="utf-8"), Loader=_Loader)
    except yaml.YAMLError as exc:
        raise InputError(f"{rel}: invalid YAML: {exc}") from exc
    if not isinstance(doc, dict):
        raise InputError(f"{rel}: not a mapping")
    return doc


def spec_paths(root: Path, kind: str) -> list[str]:
    found: set[str] = set()
    for pattern in SPEC_GLOBS[kind]:
        for p in glob.glob(str(root / pattern)):
            found.add(Path(p).relative_to(root).as_posix())
    return sorted(found)


def service_of(spec: str) -> str:
    """services/ledger/api/openapi.yaml -> services/ledger."""
    return "/".join(spec.split("/")[:2])


# ---------------------------------------------------------------------------------------------------------------
# Generation


def owner_index(components: list[dict[str, Any]]) -> dict[str, str]:
    index: dict[str, str] = {}
    for c in components:
        for s in c["services"]:
            index.setdefault(s, c["slug"])
    return index


def build_manifest(root: Path, components: list[dict[str, Any]], created: str) -> dict[str, Any]:
    return {
        "version": 3,
        "source": source_facts(root),
        "plugin_version": None,
        "compiler": COMPILER,
        "created": created,
        "spine": "spine.md",
        "contracts": "contracts.json",
        "components": [
            {
                "name": c["name"],
                "slug": c["slug"],
                "status": c["status"],
                "file": f"components/{c['slug']}.md",
                "keywords": c["keywords"],
                "aliases": c["aliases"],
                "jira": c["jira"],
                "depends_on": c["depends_on"],
                "services": c["services"],
            }
            for c in components
        ],
    }


def _resolve(node: Any, doc: dict[str, Any], rel: str) -> Any:
    hops = 0
    while isinstance(node, dict) and "$ref" in node:
        ref = node["$ref"]
        if not isinstance(ref, str) or not ref.startswith("#/"):
            raise InputError(f"{rel}: unsupported $ref {ref!r}")
        cur: Any = doc
        for part in ref[2:].split("/"):
            part = part.replace("~1", "/").replace("~0", "~")
            if not isinstance(cur, dict) or part not in cur:
                raise InputError(f"{rel}: unresolvable $ref {ref}")
            cur = cur[part]
        node = cur
        hops += 1
        if hops > 32:
            raise InputError(f"{rel}: $ref cycle at {ref}")
    return node


def openapi_entry(rel: str, doc: dict[str, Any], owners: dict[str, str]) -> dict[str, Any]:
    ops = []
    paths = doc.get("paths") or {}
    if not isinstance(paths, dict):
        raise InputError(f"{rel}: paths is not a mapping")
    for path in sorted(paths):
        item = _resolve(paths[path], doc, rel)
        if not isinstance(item, dict):
            continue
        for method in HTTP_METHODS:
            op = item.get(method)
            if not isinstance(op, dict):
                continue
            row: dict[str, Any] = {
                "method": method.upper(),
                "path": path,
                "operationId": op.get("operationId"),
                "duty": op.get("x-depot-duty"),
            }
            if "x-depot-duty-alternatives" in op:
                row["dutyAlternatives"] = op["x-depot-duty-alternatives"]
            if "x-depot-also-duty" in op:
                row["alsoDuty"] = op["x-depot-also-duty"]
            if op.get("x-depot-public"):
                row["public"] = True
            ops.append(row)
    service = service_of(rel)
    return {
        "service": service,
        "component": owners.get(service),
        "spec": rel,
        "openapi": str(doc.get("openapi")),
        "title": (doc.get("info") or {}).get("title"),
        "operations": ops,
    }


def _message_types(messages: Any, doc: dict[str, Any], rel: str) -> set[str]:
    types: set[str] = set()
    items = messages.values() if isinstance(messages, dict) else messages if isinstance(messages, list) else []
    for m in items:
        msg = _resolve(m, doc, rel)
        if not isinstance(msg, dict):
            continue
        t = msg.get("x-depot-type")
        if not isinstance(t, str):
            name = msg.get("name")
            t = name if isinstance(name, str) and name.startswith("dev.depot.") else None
        if t:
            types.add(t)
    return types


def asyncapi_entry(rel: str, doc: dict[str, Any], owners: dict[str, str]) -> dict[str, Any]:
    directions: dict[str, dict[str, set[str]]] = {"send": {}, "receive": {}}
    operations = doc.get("operations") or {}
    if not isinstance(operations, dict):
        raise InputError(f"{rel}: operations is not a mapping")
    for name in sorted(operations):
        op = operations[name]
        if not isinstance(op, dict) or op.get("action") not in directions:
            continue
        channel = _resolve(op.get("channel"), doc, rel)
        if not isinstance(channel, dict) or not isinstance(channel.get("address"), str):
            raise InputError(f"{rel}: operation {name} has no channel address")
        types = _message_types(op.get("messages") or channel.get("messages"), doc, rel)
        directions[op["action"]].setdefault(channel["address"], set()).update(types)
    service = service_of(rel)

    def rows(d: dict[str, set[str]]) -> list[dict[str, Any]]:
        return [{"topic": t, "types": sorted(d[t])} for t in sorted(d)]

    return {
        "service": service,
        "component": owners.get(service),
        "spec": rel,
        "asyncapi": str(doc.get("asyncapi")),
        "title": (doc.get("info") or {}).get("title"),
        "publishes": rows(directions["send"]),
        "subscribes": rows(directions["receive"]),
    }


def build_contracts(root: Path, components: list[dict[str, Any]], patterns: list[dict[str, Any]]) -> dict[str, Any]:
    owners = owner_index(components)
    openapi = [openapi_entry(rel, load_yaml(root, rel), owners) for rel in spec_paths(root, "openapi")]
    asyncapi = [asyncapi_entry(rel, load_yaml(root, rel), owners) for rel in spec_paths(root, "asyncapi")]
    topics: dict[str, dict[str, set[str]]] = {}
    for entry in asyncapi:
        for key, field in (("publishers", "publishes"), ("subscribers", "subscribes")):
            for row in entry[field]:
                t = topics.setdefault(row["topic"], {"publishers": set(), "subscribers": set()})
                t[key].add(entry["service"])
    topic_rows = [
        {"topic": t, "publishers": sorted(v["publishers"]), "subscribers": sorted(v["subscribers"])}
        for t, v in sorted(topics.items())
    ]
    return {
        "version": 3,
        "compiler": COMPILER,
        "note": "Generated from the OpenAPI and AsyncAPI specs and scripts/architecture_patterns.json. "
        "Do not edit; run python3 scripts/compile_architecture.py --write.",
        "summary": {
            "openapiDocuments": len(openapi),
            "paths": sum(len({o["path"] for o in e["operations"]}) for e in openapi),
            "operations": sum(len(e["operations"]) for e in openapi),
            "asyncapiDocuments": len(asyncapi),
            "topics": len(topic_rows),
            "topicsPublished": sum(1 for t in topic_rows if t["publishers"]),
            "topicsSubscribed": sum(1 for t in topic_rows if t["subscribers"]),
            "patterns": len(patterns),
        },
        "patterns": patterns,
        "openapi": openapi,
        "asyncapi": asyncapi,
        "topics": topic_rows,
    }


# ---------------------------------------------------------------------------------------------------------------
# Checks


def owned_dirs(root: Path) -> list[str]:
    dirs = []
    for top in OWNED_ROOTS:
        base = root / top
        if not base.is_dir():
            continue
        for p in sorted(base.iterdir()):
            if p.is_dir() and not p.name.startswith((".", "__")):
                dirs.append(f"{top}/{p.name}")
    return dirs


def check_ownership(root: Path, components: list[dict[str, Any]]) -> list[str]:
    problems = []
    owners: dict[str, list[str]] = {}
    for c in components:
        for s in c["services"]:
            owners.setdefault(s, []).append(c["slug"])
            if not (root / s).is_dir():
                problems.append(f"ownership: {c['slug']} lists {s}, which is not a directory")
    for s, slugs in sorted(owners.items()):
        if len(slugs) > 1:
            problems.append(f"ownership: {s} is owned by more than one component ({', '.join(slugs)})")
    for d in owned_dirs(root):
        if d not in owners:
            problems.append(f"ownership: {d} is owned by no component; add it to {COMPONENTS_INPUT}")
    return problems


def headings(text: str) -> set[str]:
    return {line[3:].strip() for line in text.splitlines() if line.startswith("## ")}


def check_component_files(root: Path, components: list[dict[str, Any]]) -> list[str]:
    problems = []
    comp_dir = root / OUT_DIR / "components"
    expected = {f"{c['slug']}.md" for c in components}
    for c in components:
        rel = f"{OUT_DIR}/components/{c['slug']}.md"
        path = root / rel
        if not path.is_file():
            problems.append(f"prose: {rel} is missing (component {c['slug']})")
            continue
        text = path.read_text(encoding="utf-8")
        first = text.splitlines()[0] if text else ""
        if first != f"# {c['name']}":
            problems.append(f"prose: {rel} must start with '# {c['name']}'")
        missing = [h for h in REQUIRED_HEADINGS if h not in headings(text)]
        if missing:
            problems.append(f"prose: {rel} lacks heading(s): {', '.join('## ' + h for h in missing)}")
        cited = set(BACKTICK.findall(text))
        for s in c["services"]:
            if s not in cited:
                problems.append(f"prose: {rel} does not cite `{s}`, which the component owns")
    if comp_dir.is_dir():
        for p in sorted(comp_dir.glob("*.md")):
            if p.name not in expected:
                problems.append(f"prose: {OUT_DIR}/components/{p.name} belongs to no manifest component")
    if not (root / OUT_DIR / "spine.md").is_file():
        problems.append(f"prose: {OUT_DIR}/spine.md is missing")
    return problems


def check_patterns(root: Path, patterns: list[dict[str, Any]]) -> list[str]:
    problems = []
    for p in patterns:
        src = p.get("source", "")
        file, _, heading = src.partition("#")
        path = root / OUT_DIR / file
        if not file or not path.is_file():
            problems.append(f"patterns: {p.get('id')}: source {src!r} names no file in {OUT_DIR}/")
        elif heading and heading not in headings(path.read_text(encoding="utf-8")):
            problems.append(f"patterns: {p.get('id')}: source {src!r}: no '## {heading}' heading in {file}")
    return problems


def repo_roots(root: Path) -> set[str]:
    return {p.name for p in root.iterdir() if p.is_dir() and p.name != ".git"}


def cited_paths(text: str, roots: set[str]) -> list[str]:
    out = []
    for token in BACKTICK.findall(text):
        token = token.strip()
        if "/" not in token or token.split("/", 1)[0] not in roots:
            continue
        if PLACEHOLDER_CHARS & set(token) or "..." in token or "…" in token:
            continue
        token = LINE_SUFFIX.sub("", token.split("#", 1)[0])
        if token:
            out.append(token)
    return out


def check_cited_paths(root: Path) -> tuple[list[str], int]:
    problems = []
    roots = repo_roots(root)
    count = 0
    out = root / OUT_DIR
    files = sorted(out.glob("*.md")) + sorted((out / "components").glob("*.md"))
    for f in files:
        rel = f.relative_to(root).as_posix()
        for token in sorted(set(cited_paths(f.read_text(encoding="utf-8"), roots))):
            count += 1
            exists = bool(glob.glob(str(root / token))) if any(ch in token for ch in "*?[") else (root / token).exists()
            if not exists:
                problems.append(f"prose: {rel} cites `{token}`, which does not exist")
    return problems, count


def _first_difference(old: str, new: str) -> str:
    a, b = old.splitlines(), new.splitlines()
    for i, (x, y) in enumerate(zip(a, b), 1):
        if x != y:
            return f"first difference at line {i}: {x.strip()[:100]!r} != generated {y.strip()[:100]!r}"
    return f"{len(a)} lines on disk, {len(b)} generated"


def check(root: Path) -> tuple[list[str], str]:
    components = load_components(root)
    patterns = load_patterns(root)
    problems: list[str] = []
    out = root / OUT_DIR

    manifest_path = out / "manifest.json"
    try:
        current = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError) as exc:
        current = None
        problems.append(f"manifest: {OUT_DIR}/manifest.json unreadable ({exc}); run --write")
    fresh = build_manifest(root, components, (current or {}).get("created", ""))
    if current is not None:
        src, want = current.get("source") or {}, fresh["source"]
        if src.get("sha256") != want["sha256"] or src.get("lines") != want["lines"]:
            problems.append(
                f"manifest: source is sha256 {str(src.get('sha256'))[:12]}… / {src.get('lines')} lines, but "
                f"{SOURCE} is sha256 {want['sha256'][:12]}… / {want['lines']} lines; run --write"
            )
        if src.get("path") != want["path"]:
            problems.append(f"manifest: source.path is {src.get('path')!r}, expected {SOURCE!r}")
        rest_now = {k: v for k, v in current.items() if k != "source"}
        rest_fresh = {k: v for k, v in fresh.items() if k != "source"}
        if rest_now != rest_fresh:
            keys = sorted(k for k in set(rest_now) | set(rest_fresh) if rest_now.get(k) != rest_fresh.get(k))
            problems.append(f"manifest: {', '.join(keys)} differ from {COMPONENTS_INPUT}; run --write")

    contracts_text = render(build_contracts(root, components, patterns))
    contracts_path = out / "contracts.json"
    if not contracts_path.is_file():
        problems.append(f"contracts: {OUT_DIR}/contracts.json is missing; run --write")
    else:
        on_disk = contracts_path.read_text(encoding="utf-8")
        if on_disk != contracts_text:
            problems.append(
                f"contracts: {OUT_DIR}/contracts.json differs from the specs ({_first_difference(on_disk, contracts_text)}); "
                "run --write"
            )

    problems += check_ownership(root, components)
    problems += check_component_files(root, components)
    problems += check_patterns(root, patterns)
    path_problems, n_paths = check_cited_paths(root)
    problems += path_problems

    summary = json.loads(contracts_text)["summary"]
    n_owned = sum(len(c["services"]) for c in components)
    ok = (
        f"architecture.compiled is up to date: {SOURCE} sha256 {fresh['source']['sha256'][:12]}… "
        f"({fresh['source']['lines']} lines); {summary['operations']} operations in {summary['openapiDocuments']} "
        f"OpenAPI documents; {summary['topics']} topics ({summary['topicsPublished']} published, "
        f"{summary['topicsSubscribed']} subscribed) in {summary['asyncapiDocuments']} AsyncAPI documents; "
        f"{len(components)} components own {n_owned} directories; {n_paths} cited paths exist; "
        f"{len(patterns)} pattern sources resolve"
    )
    return problems, ok


def write(root: Path, created: str | None) -> str:
    components = load_components(root)
    patterns = load_patterns(root)
    out = root / OUT_DIR
    out.mkdir(parents=True, exist_ok=True)
    if created is None:
        try:
            created = json.loads((out / "manifest.json").read_text(encoding="utf-8")).get("created")
        except (FileNotFoundError, json.JSONDecodeError):
            created = None
        created = created or _dt.date.today().isoformat()
    manifest = build_manifest(root, components, created)
    contracts = build_contracts(root, components, patterns)
    (out / "manifest.json").write_text(render(manifest), encoding="utf-8")
    (out / "contracts.json").write_text(render(contracts), encoding="utf-8")
    s = contracts["summary"]
    return (
        f"wrote {OUT_DIR}/manifest.json (sha256 {manifest['source']['sha256'][:12]}…, {manifest['source']['lines']} "
        f"lines, {len(components)} components) and {OUT_DIR}/contracts.json ({s['operations']} operations, "
        f"{s['topics']} topics, {s['patterns']} patterns)"
    )


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__.split("\n", 1)[0])
    mode = ap.add_mutually_exclusive_group(required=True)
    mode.add_argument("--write", action="store_true", help="regenerate manifest.json and contracts.json")
    mode.add_argument("--check", action="store_true", help="fail if the compiled form drifted")
    ap.add_argument("--created", help="manifest 'created' date (YYYY-MM-DD) for --write; default keeps the current one")
    ap.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent, help=argparse.SUPPRESS)
    args = ap.parse_args(argv)
    if args.created is not None:
        try:
            _dt.date.fromisoformat(args.created)
        except ValueError:
            ap.error(f"--created must be YYYY-MM-DD, got {args.created!r}")
    root = args.root.resolve()
    try:
        if args.write:
            print(write(root, args.created))
            return 0
        problems, ok = check(root)
    except InputError as exc:
        print(f"compile_architecture: {exc}", file=sys.stderr)
        return 2
    if problems:
        for p in problems:
            print(f"DRIFT {p}")
        print(
            f"architecture.compiled: {len(problems)} problem(s). Regenerate with "
            "`python3 scripts/compile_architecture.py --write` and fix the prose in "
            f"{OUT_DIR}/ (see {OUT_DIR}/README.md).",
            file=sys.stderr,
        )
        return 1
    print(ok)
    return 0


if __name__ == "__main__":
    sys.exit(main())
