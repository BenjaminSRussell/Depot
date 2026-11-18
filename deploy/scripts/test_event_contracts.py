"""Every Kafka consumer reads only event fields its producer's AsyncAPI schema declares and its producer's code writes (D6,
B-05c), and the search index reads every type it indexes from a relayed topic (ADR-0013).

Consumers are the ones test_kafka_consumers.py finds in the code (Go eventing, kafkajs, kafka-python); the fields each
reads are found in the code too:
- Go: deploy/ci/eventfields type-checks the consumer's module. Every map[string]eventing.Handler a main package reaches
  is followed from each entry's handler through the functions and variables it names to the json.Unmarshal of the event
  data, and the decoded type's JSON paths are the fields (Go matches keys case-insensitively, so the comparison does).
- TypeScript: each `event as <Stem>Event` cast in the consumer's file, paired with the `<stem>Type`/`<stem>Types`
  constant of the module that declares the interface (the dispatch compares the event type with it); the fields are the
  interface's `data` object type.
- Python: the module-level set the consumer tests the event type against, every key read from the event's data
  (`data.get("k")`, `data["k"]`; `a or b` reads are alternatives, one of which must be declared) and the keys a
  recursive helper looks for at any depth (a `*_KEYS` tuple: at least one must occur in the payload).
For every event type a consumer reads, the producer is the service whose AsyncAPI has a `send` operation with that
message; each field must be a property path of its payload schema ($ref, allOf/anyOf/oneOf followed; an object with no
declared properties accepts any key), and each events.New call of the producer's code that writes the type (followed
through helpers to the call that names the type and payload) must write it. The producer side is also checked on its own:
every field those calls write is declared by the schema (the payload matches the schema). NOT_OUTBOX lists handler types
that are not outbox CloudEvents and PRODUCER_CODE_UNSEEN the producers whose payload the analyser cannot see, each with
the reason; anything else fails. An events.New call whose type the analyser cannot evaluate (sales' return-order helper
picks the type at run time) is listed in its report but not checked; a consumed type written only there fails as unwritten.

Search: services/search/events/asyncapi.yaml declares a `receive` operation for exactly the topics the consumer
subscribes to and the types its handlers index (one message per type, under the channel of the topic it arrives on, its
payload a $ref to the producer's message). Each producer has an outbox-<service> connector in deploy/kafka/connect.yaml
that relays the type to that topic: through the EventRouter regex for a connector that routes by event type, or, for one
that routes by aggregate_type, as an aggregate type the producer's outbox.Enqueue calls write; the debezium user may
write the topic. The index-count reconciliation (`search reconcile`) is tested in services/search; running it against
live source counts is #213.

Negative tests mutate a schema, a payload and the search AsyncAPI in memory and type-check a synthetic module, so each
rule is shown to fail. Not covered: whether events really flow on a cluster (#213), and the meaning of a field (a
consumer that reads `qty` as a count where the producer writes a decimal string passes).
"""

import ast
import copy
import functools
import json
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from collections import defaultdict
from dataclasses import dataclass, field
from pathlib import Path

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import test_argocd_apps as argo  # noqa: E402
import test_kafka_consumers as kc  # noqa: E402

DEPLOY = Path(__file__).resolve().parents[1]
REPO = DEPLOY.parent
EVENTFIELDS = DEPLOY / "ci/eventfields"
SEARCH_ASYNCAPI = REPO / "services/search/events/asyncapi.yaml"

# Handler-map event types that are not outbox CloudEvents, so no AsyncAPI `send` declares them: (service, type) -> why.
NOT_OUTBOX = {
    ("audit", "cdc:context"): "the audit CDC consumer's own decoder (cdc.Decoder) turns Debezium change records of the "
                              "tables in cdc.Tables into these synthetic types; test_argocd_apps checks the tables against "
                              "the audit-cdc-<service> connectors and services/audit/events/asyncapi.yaml documents them",
    ("audit", "cdc:row-change"): "as cdc:context",
}

# Producers whose events.New calls the analyser cannot tie to an event type or payload: (service, type) -> why. The schema
# check still applies to the consumer's fields; only the code comparison is skipped.
PRODUCER_CODE_UNSEEN = {}


# ---------------------------------------------------------------------------------------------------------------------
# AsyncAPI

class SchemaLoader(yaml.SafeLoader):
    """YAML 1.2 booleans: a property named no, on, yes or off (installments[].no) stays a string."""


SchemaLoader.yaml_implicit_resolvers = {
    k: [(tag, rx) for tag, rx in v if tag != "tag:yaml.org,2002:bool"] for k, v in yaml.SafeLoader.yaml_implicit_resolvers.items()}
SchemaLoader.add_implicit_resolver("tag:yaml.org,2002:bool", re.compile(r"^(?:true|True|TRUE|false|False|FALSE)$"), list("tTfF"))


@functools.lru_cache(maxsize=None)
def load_yaml(path):
    return yaml.load(Path(path).read_text(), Loader=SchemaLoader)  # noqa: S506 - a SafeLoader subclass


def pointer(doc, frag):
    node = doc
    for part in [p for p in frag.lstrip("/").split("/") if p]:
        node = node[part.replace("~1", "/").replace("~0", "~")]
    return node


def deref(node, file):
    """(node, file) with every $ref followed: local (#/...) or a relative file with an optional #fragment."""
    for _ in range(20):
        if isinstance(node, dict) and "x-ctx" in node:  # a branch built by envelope_data, already resolved
            node, file = node["x-ctx"]
            continue
        if not isinstance(node, dict) or "$ref" not in node:
            return node, file
        target, _, frag = node["$ref"].partition("#")
        if target:
            file = (Path(file).parent / target).resolve()
        node = pointer(load_yaml(str(file)), frag)
    raise ValueError(f"{file}: $ref cycle")


@dataclass
class Message:
    service: str
    type: str
    channel: str
    file: Path
    name: str
    payload: object

    def schema(self):
        return envelope_data((self.payload, self.file))


ENVELOPE_KEYS = {"specversion", "type", "source", "id", "subject", "tenantid"}


def envelope_data(ctx):
    """The data schema of a payload that models the whole CloudEvent (properties type, tenantid, ..., data, possibly through
    allOf with a shared Envelope), else the payload itself."""
    branches, names, todo = [], set(), [ctx]
    while todo:
        node, file = deref(*todo.pop())
        if not isinstance(node, dict):
            continue
        props = node.get("properties") or {}
        names |= set(props)
        if "data" in props:
            branches.append((props["data"], file))
        todo += [(sub, file) for k in COMPOSITIONS for sub in node.get(k) or []]
    if not branches or not names & ENVELOPE_KEYS:
        return ctx
    specific = [b for b in branches if not is_open(deref(*b)[0])] or branches
    return ({"anyOf": [{"x-ctx": b} for b in specific]}, ctx[1])


def asyncapi_files():
    return sorted(REPO.glob("services/*/events/asyncapi.yaml")) + sorted(REPO.glob("tools/*/events/asyncapi.yaml"))


def message_type(msg):
    return msg.get("x-depot-type") or msg.get("name")


def producer_messages(files=None):
    """event type -> the messages that a `send` operation of some AsyncAPI publishes with that type."""
    out = defaultdict(list)
    for f in files or asyncapi_files():
        spec = load_yaml(str(f))
        service = f.parts[-3]
        seen = set()
        for op in (spec.get("operations") or {}).values():
            if op.get("action") != "send":
                continue
            channel, _ = deref(op["channel"], f)
            refs = op.get("messages") or list((channel.get("messages") or {}).values())
            for ref in refs:
                msg, mfile = deref(ref, f)
                typ = message_type(msg)
                key = (typ, channel.get("address"))
                if typ and key not in seen:
                    seen.add(key)
                    out[typ].append(Message(service, typ, channel.get("address"), Path(mfile), msg.get("name", typ), msg.get("payload")))
    return out


def tokens(path):
    """'lines[].qty' -> ['lines', '[]', 'qty']."""
    out = []
    for part in path.split("."):
        name = part.rstrip("[]")
        if name:
            out.append(name)
        out += ["[]"] * ((len(part) - len(name)) // 2)
    return out


COMPOSITIONS = ("allOf", "anyOf", "oneOf")


def is_open(node):
    """A schema that declares no structure (no properties, items, composition or closed additionalProperties) accepts any key."""
    if node is True or node == {}:
        return True
    if not isinstance(node, dict):
        return False
    if any(k in node for k in ("properties", "items", "prefixItems", "$ref") + COMPOSITIONS):
        return False
    if node.get("additionalProperties") is False:
        return False
    types = node.get("type")
    types = types if isinstance(types, list) else [types]
    return any(t in (None, "object", "array") for t in types)


def schema_has(ctx, toks, ci=False, depth=0):
    """Whether the schema at ctx = (node, file) declares the path toks."""
    if not toks:
        return True
    if depth > 40:
        return False
    node, file = deref(*ctx)
    if not isinstance(node, dict):
        return node is True
    if any(schema_has((sub, file), toks, ci, depth + 1) for k in COMPOSITIONS for sub in node.get(k) or []):
        return True
    tok, rest = toks[0], toks[1:]
    if tok == "[]":
        if "items" in node:
            return schema_has((node["items"], file), rest, ci, depth + 1)
        return is_open(node)
    props = node.get("properties") or {}
    match = [k for k in props if k == tok] or ([k for k in props if k.lower() == tok.lower()] if ci else [])
    if match:
        return schema_has((props[match[0]], file), rest, ci, depth + 1)
    addl = node.get("additionalProperties")
    if isinstance(addl, dict):
        return schema_has((addl, file), rest, ci, depth + 1)
    if addl is True or node.get("patternProperties"):
        return True
    return is_open(node)


def schema_keys(ctx, depth=0):
    """Every property name the schema declares at any depth (for keys a consumer looks for anywhere)."""
    if depth > 40:
        return set()
    node, file = deref(*ctx)
    if not isinstance(node, dict):
        return set()
    out = set()
    for k in COMPOSITIONS:
        for sub in node.get(k) or []:
            out |= schema_keys((sub, file), depth + 1)
    for name, sub in (node.get("properties") or {}).items():
        out.add(name)
        out |= schema_keys((sub, file), depth + 1)
    for k in ("items", "additionalProperties"):
        if isinstance(node.get(k), dict):
            out |= schema_keys((node[k], file), depth + 1)
    return out


def written(path, emitted, ci):
    """Whether a payload with the JSON paths `emitted` (a producer's) has `path`: equal, or one is a prefix of the other
    (the consumer reads a whole object the producer fills, or the producer writes a value that ends the path)."""
    want = tokens(path)
    norm = (lambda t: [x.lower() for x in t]) if ci else (lambda t: t)
    want = norm(want)
    for e in emitted:
        have = norm(tokens(e))
        n = min(len(want), len(have))
        if want[:n] == have[:n]:
            return True
    return False


# ---------------------------------------------------------------------------------------------------------------------
# Go (deploy/ci/eventfields)

def go_command():
    return shutil.which("go")


@functools.lru_cache(maxsize=None)
def eventfields(dirs):
    """The analyser's report per module directory (repo-relative), for a tuple of directories."""
    if not dirs:
        return {}
    out = subprocess.run([go_command(), "run", ".", "-root", str(REPO)] + [str(REPO / d) for d in dirs], cwd=EVENTFIELDS,
                         check=True, capture_output=True, text=True)
    return {r["dir"]: r for r in json.loads(out.stdout)}


def go_module_dir(service):
    root = argo.service_root(service)
    return root.relative_to(REPO).as_posix() if (root / "go.mod").is_file() else None


# ---------------------------------------------------------------------------------------------------------------------
# What each consumer reads

@dataclass
class Read:
    """The fields one consumer reads from one event type. fields: requirements, each a tuple of alternative paths of which
    one must be declared; anywhere: keys of which one must occur at some depth; ci: keys match case-insensitively (Go)."""
    consumer: str
    service: str
    type: str
    at: str
    fields: list = field(default_factory=list)
    anywhere: tuple = ()
    ci: bool = False


def go_reads(reports, consumers):
    """Reads of every handler entry of the Go consumer services, and the analyser's problems."""
    out, problems = [], []
    by_service = defaultdict(list)
    for c in consumers:
        by_service[c.service].append(c)
    for service, cs in sorted(by_service.items()):
        report = reports[go_module_dir(service)]
        problems += [f"{service}: {p}" for p in report["problems"]]
        groups = ", ".join(sorted(g for c in cs for g in c.groups))
        if not report["handlers"]:
            problems.append(f"{service}: a consumer ({groups}) whose handler map the analyser does not find")
        for h in report["handlers"]:
            r = Read(f"{service} ({groups})", service, h["type"], h["at"], ci=True)
            for d in h["decodes"]:
                r.fields += [(f["path"],) for f in d["fields"]]
            out.append(r)
    return out, problems


TS_CAST = re.compile(r"\bas\s+([A-Z]\w*Event)\b")
TS_TYPE_CONST = re.compile(r"""export\s+const\s+(\w+?Types?)\s*=\s*(\[[^\]]*\]|"[^"]*"|'[^']*')""")


def ts_members(body):
    """Top-level members of a TypeScript object type body, split on ; , and newlines outside brackets."""
    out, depth, start = [], 0, 0
    for i, ch in enumerate(body):
        if ch in "{[(<":
            depth += 1
        elif ch in "}])>":
            depth -= 1
        elif depth == 0 and ch in ";,\n":
            out.append(body[start:i])
            start = i + 1
    out.append(body[start:])
    return [m.strip() for m in out if m.strip()]


def ts_fields(body, prefix=""):
    """JSON paths of a TypeScript object type literal's body."""
    out = []
    for member in ts_members(body):
        m = re.match(r"^(?:readonly\s+)?[\"']?(\w+)[\"']?\??\s*:\s*(.*)$", member, re.S)
        if not m:
            continue
        name, typ = m.group(1), m.group(2).strip()
        path = f"{prefix}.{name}" if prefix else name
        arrays = ""
        while True:
            if typ.endswith("[]"):
                typ, arrays = typ[:-2].strip(), arrays + "[]"
            elif re.match(r"^(?:Readonly)?Array<.*>$", typ, re.S):
                typ, arrays = typ[typ.index("<") + 1:-1].strip(), arrays + "[]"
            else:
                break
        if typ.startswith("{") and typ.endswith("}") and typ[1:-1].strip():
            out += ts_fields(typ[1:-1], path + arrays)
        else:
            out.append(path + arrays)
    return out


def ts_interface(name, file):
    """(file, the interface's body) for an interface declared in file or a module it imports name from."""
    text = kc.strip_comments(file.read_text())
    m = re.search(rf"\binterface\s+{name}\b[^{{]*\{{", text)
    if m:
        o = m.end() - 1
        return file, text[o + 1:kc.match(text, o)]
    imp = re.search(rf"""import\s*(?:type\s*)?\{{[^}}]*\b{name}\b[^}}]*\}}\s*from\s*["'](\.[^"']+)["']""", text)
    if imp:
        target = (file.parent / imp.group(1))
        for cand in (target, target.with_suffix(".ts"), Path(str(target).removesuffix(".js") + ".ts")):
            if cand.is_file() and cand != file:
                return ts_interface(name, cand)
    return None, None


def ts_reads(consumers):
    """Reads of the kafkajs consumers: each `event as <Stem>Event` cast in the consumer's file, the interface's `data`
    fields and the event types of the `<stem>Type(s)` constant beside it."""
    out, problems = [], []
    for service, where, values in consumers:
        file = REPO / where
        text = kc.strip_comments(file.read_text())
        casts = sorted(set(TS_CAST.findall(text)))
        for iface in casts:
            ifile, body = ts_interface(iface, file)
            if body is None:
                problems.append(f"{where}: casts the event to {iface}, whose declaration the check does not find")
                continue
            members = {m.split(":", 1)[0].strip().rstrip("?"): m.split(":", 1)[1].strip() for m in ts_members(body) if ":" in m}
            data = members.get("data", "")
            if not (data.startswith("{") and data.endswith("}")):
                problems.append(f"{where}: {iface} has no `data` object type")
                continue
            stem = iface[0].lower() + iface[1:-len("Event")]
            consts = {m.group(1): m.group(2) for m in TS_TYPE_CONST.finditer(kc.strip_comments(ifile.read_text()))}
            const = next((consts[k] for k in (stem + "Type", stem + "Types") if k in consts), None)
            if const is None:
                problems.append(f"{rel(ifile)}: {iface} has no {stem}Type or {stem}Types constant naming the event types it reads")
                continue
            types = re.findall(r"""["']([^"']+)["']""", const)
            fields = ts_fields(data[1:-1])
            for typ in types:
                out.append(Read(f"{service} ({iface})", service, typ, rel(ifile), [(f,) for f in fields]))
    return out, problems


def rel(path):
    return Path(path).resolve().relative_to(REPO).as_posix()


def py_value(node, module):
    """A module-level constant expression: strings, tuples/lists/sets, names, +, frozenset/tuple/set() and comprehensions."""
    if isinstance(node, ast.Constant):
        return node.value
    if isinstance(node, (ast.Tuple, ast.List, ast.Set)):
        return tuple(py_value(e, module) for e in node.elts)
    if isinstance(node, ast.Name):
        for stmt in module.body:
            if isinstance(stmt, ast.Assign) and any(isinstance(t, ast.Name) and t.id == node.id for t in stmt.targets):
                return py_value(stmt.value, module)
            if isinstance(stmt, ast.AnnAssign) and isinstance(stmt.target, ast.Name) and stmt.target.id == node.id and stmt.value:
                return py_value(stmt.value, module)
        raise ValueError(f"no module constant {node.id}")
    if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Add):
        return py_value(node.left, module) + py_value(node.right, module)
    if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id in ("frozenset", "set", "tuple", "list") \
            and len(node.args) == 1:
        return tuple(py_value(node.args[0], module))
    if isinstance(node, (ast.GeneratorExp, ast.ListComp, ast.SetComp)) and len(node.generators) == 1:
        gen = node.generators[0]
        if isinstance(gen.target, ast.Name) and not gen.ifs:
            out = []
            for item in py_value(gen.iter, module):
                bound = ast.Module(body=[ast.Assign(targets=[ast.Name(gen.target.id)], value=ast.Constant(item))] + module.body,
                                   type_ignores=[])
                out.append(py_value(node.elt, bound))
            return tuple(out)
    raise ValueError(f"not a module constant: {ast.unparse(node)}")


def py_reads(consumers):
    """Reads of the kafka-python consumers (see the module docstring)."""
    out, problems = [], []
    for service, where, _ in consumers:
        file = REPO / where.split(":")[0]
        module = ast.parse(file.read_text())
        funcs = {f.name: f for f in module.body if isinstance(f, ast.FunctionDef)}
        found = False
        for fn in funcs.values():
            handled = None
            for node in ast.walk(fn):
                # env["type"] not in HANDLED / env.get("type") in HANDLED
                if isinstance(node, ast.Compare) and len(node.ops) == 1 and isinstance(node.ops[0], (ast.In, ast.NotIn)) \
                        and re.fullmatch(r"""\w+(\[["']type["']\]|\.get\(["']type["']\))""", ast.unparse(node.left)) \
                        and isinstance(node.comparators[0], ast.Name):
                    handled = node.comparators[0]
            if handled is None:
                continue
            found = True
            try:
                types = py_value(handled, module)
            except ValueError as e:
                problems.append(f"{where}: {e}")
                continue
            data_names = {t.id for node in ast.walk(fn) if isinstance(node, ast.Assign)
                          for t in node.targets if isinstance(t, ast.Name)
                          and re.fullmatch(r"""\w+\.get\(["']data["']\)( if .*)?""", ast.unparse(node.value))}
            if not data_names:
                problems.append(f"{where}: {fn.name} tests the event type but reads no `data` the check can follow")
                continue

            def key_of(node):
                s = ast.unparse(node)
                m = re.fullmatch(r"""(\w+)(?:\.get\(["'](\w+)["']\)|\[["'](\w+)["']\])""", s)
                return (m.group(2) or m.group(3)) if m and m.group(1) in data_names else None
            requirements, consumed = [], set()
            for node in ast.walk(fn):
                if isinstance(node, ast.BoolOp) and isinstance(node.op, ast.Or):
                    alts = [k for k in (key_of(v) for v in node.values) if k]
                    if alts:
                        requirements.append(tuple(alts))
                        consumed |= {id(v) for v in node.values}
            for node in ast.walk(fn):
                if id(node) not in consumed and key_of(node):
                    requirements.append((key_of(node),))
            anywhere = ()
            for node in ast.walk(fn):
                if isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id in funcs \
                        and any(isinstance(a, ast.Name) and a.id in data_names for a in node.args):
                    helper = funcs[node.func.id]
                    recursive = any(isinstance(n, ast.Call) and isinstance(n.func, ast.Name) and n.func.id == helper.name
                                    for n in ast.walk(helper))
                    for n in ast.walk(helper):
                        if recursive and isinstance(n, ast.Compare) and isinstance(n.ops[0], ast.In) \
                                and isinstance(n.comparators[0], ast.Name) and n.comparators[0].id.endswith("_KEYS"):
                            anywhere += tuple(py_value(n.comparators[0], module))
            for typ in types:
                out.append(Read(f"{service} ({fn.name})", service, typ, f"{rel(file)}:{fn.lineno}", sorted(set(requirements)),
                                tuple(sorted(set(anywhere)))))
        if not found:
            problems.append(f"{where}: a kafka-python consumer whose event type test the check does not find")
    return out, problems


# ---------------------------------------------------------------------------------------------------------------------
# The comparison

def contract_problems(reads, messages, emits):
    """Problems of each read against the producers' schemas and code. emits: (service, type) -> the analyser's emits, or
    None when the producer's code was not analysed (not Go)."""
    problems = []
    anywhere = defaultdict(lambda: ([], []))
    for r in reads:
        if (r.service, r.type) in NOT_OUTBOX:
            continue
        producers = messages.get(r.type, [])
        if not producers:
            problems.append(f"{r.consumer}: reads {r.type} ({r.at}), which no AsyncAPI `send` operation declares")
            continue
        for m in producers:
            where = f"{rel(m.file)} {m.name}"
            for alts in r.fields:
                if not any(schema_has(m.schema(), tokens(a), r.ci) for a in alts):
                    problems.append(f"{r.consumer}: reads {' or '.join(alts)} of {r.type} ({r.at}); {where} does not declare it")
            if r.anywhere:
                anywhere[(r.consumer, r.anywhere)][0].append(bool(set(r.anywhere) & schema_keys(m.schema())))
            code = emits.get((m.service, r.type))
            if code is None or (m.service, r.type) in PRODUCER_CODE_UNSEEN:
                continue
            if not code:
                problems.append(f"{r.consumer}: reads {r.type}, but no events.New call of {m.service}'s code writes it "
                                "(list it in PRODUCER_CODE_UNSEEN with the reason if the analyser cannot see it)")
                continue
            for e in code:
                if e.get("open"):
                    continue
                paths = [f["path"] for f in e["fields"]]
                site = e["at"] + (f" via {e['via']}" if e.get("via") else "")
                for alts in r.fields:
                    if not any(written(a, paths, r.ci) for a in alts):
                        problems.append(f"{r.consumer}: reads {' or '.join(alts)} of {r.type} ({r.at}); {m.service} writes "
                                        f"{e['goType']} at {site}, which has no such field")
                if r.anywhere:
                    anywhere[(r.consumer, r.anywhere)][1].append(bool({k for p in paths for k in tokens(p)} & set(r.anywhere)))
    # Keys looked for anywhere are optional per event (prediction-py rescores the whole legal entity and only reports the
    # open items it finds), but a consumer that finds them in none of its events reads a key no producer writes.
    for (consumer, keys), (declared, coded) in sorted(anywhere.items()):
        if declared and not any(declared):
            problems.append(f"{consumer}: looks for {' or '.join(keys)} anywhere in its events; no producer schema declares one")
        if coded and not any(coded):
            problems.append(f"{consumer}: looks for {' or '.join(keys)} anywhere in its events; no producer code writes one")
    return problems


def producer_problems(types, messages, emits):
    """Each field the producers' code writes for the given types is declared by their schema."""
    problems = []
    for typ in sorted(types):
        for m in messages.get(typ, []):
            for e in emits.get((m.service, typ)) or []:
                if e.get("open"):
                    continue
                site = e["at"] + (f" via {e['via']}" if e.get("via") else "")
                for f in e["fields"]:
                    if not schema_has(m.schema(), tokens(f["path"])):
                        problems.append(f"{m.service} writes {f['path']} in {typ} ({e['goType']} at {site}); "
                                        f"{rel(m.file)} {m.name} does not declare it")
    return problems


def emits_by_type(reports):
    out = defaultdict(list)
    for d, r in reports.items():
        service = Path(d).name
        for e in r["emits"]:
            out[(service, e["type"])].append(e)
    return out


# ---------------------------------------------------------------------------------------------------------------------
# Search sources (ADR-0013)

def connectors():
    return {d["metadata"]["name"].removeprefix("outbox-"): d["spec"]["config"] for d in argo.load_docs(DEPLOY / "kafka/connect.yaml")
            if argo.is_outbox_connector(d)}


def debezium_writable():
    (user,) = [d for d in argo.load_docs(DEPLOY / "kafka/connect.yaml") if d["kind"] == "KafkaUser" and d["metadata"]["name"] == "debezium"]
    acls = [a["resource"] for a in user["spec"]["authorization"]["acls"] if a["resource"]["type"] == "topic" and "Write" in a["operations"]]
    return lambda topic: any(topic == r["name"] if r.get("patternType", "literal") == "literal" else topic.startswith(r["name"]) for r in acls)


def search_receives(spec, file=SEARCH_ASYNCAPI):
    """(topic, event type, message) of every receive operation of the search AsyncAPI, and its problems."""
    def local(node):
        # Local references resolve in spec as given (the negative tests pass a mutated copy), others through the files.
        while isinstance(node, dict) and str(node.get("$ref", "")).startswith("#"):
            node = pointer(spec, node["$ref"][1:])
        return deref(node, file)
    out, problems = [], []
    for name, op in (spec.get("operations") or {}).items():
        if op.get("action") != "receive":
            problems.append(f"operation {name} is not a receive operation; search publishes nothing")
            continue
        channel, _ = local(op["channel"])
        refs = op.get("messages") or list((channel.get("messages") or {}).values())
        for ref in refs:
            msg, mfile = local(ref)
            out.append((channel["address"], message_type(msg), msg, Path(mfile)))
    return out, problems


def search_source_problems(spec, handled, topics, messages, conns, writable, aggregates):
    """The search AsyncAPI against the consumer's code and the connectors (see the module docstring)."""
    receives, problems = search_receives(spec)
    declared_topics = {t for t, _, _, _ in receives}
    declared_types = {typ for _, typ, _, _ in receives}
    for t in sorted(set(topics) - declared_topics):
        problems.append(f"the consumer subscribes to {t}, which no receive operation of {rel(SEARCH_ASYNCAPI)} declares")
    for t in sorted(declared_topics - set(topics)):
        problems.append(f"{rel(SEARCH_ASYNCAPI)} receives from {t}, which the consumer does not subscribe to")
    for typ in sorted(set(handled) - declared_types):
        problems.append(f"the index handles {typ}, which no receive operation of {rel(SEARCH_ASYNCAPI)} declares")
    for typ in sorted(declared_types - set(handled)):
        problems.append(f"{rel(SEARCH_ASYNCAPI)} receives {typ}, which no index handler handles")
    for topic, typ, msg, mfile in sorted(receives, key=lambda r: (r[0], r[1])):
        producers = messages.get(typ, [])
        if not producers:
            problems.append(f"{typ} on {topic}: no AsyncAPI send operation declares it")
            continue
        payload = msg.get("payload")
        if not (isinstance(payload, dict) and "$ref" in payload and not payload["$ref"].startswith("#")):
            problems.append(f"{typ} on {topic}: the payload must be a $ref to the producer's message payload, not a copy")
        else:
            node, pfile = deref(payload, mfile)
            if not any(Path(pfile).resolve() == Path(f).resolve() and node is n
                       for n, f in (deref(m.payload, m.file) for m in producers)):
                problems.append(f"{typ} on {topic}: the payload is not the producer's ({', '.join(rel(m.file) for m in producers)})")
        for m in producers:
            cfg = conns.get(m.service)
            if cfg is None:
                problems.append(f"{typ}: its producer {m.service} has no outbox-{m.service} connector in deploy/kafka/connect.yaml, "
                                "so the index never sees it (D2)")
                continue
            if cfg.get("transforms.outbox.route.by.field") == "type":
                matched, routed = argo.outbox_route(cfg, typ)
                if not matched or routed != topic:
                    problems.append(f"{typ}: outbox-{m.service} routes it to {routed if matched else 'no topic'}, the index reads {topic}")
            else:
                prefix = cfg["transforms.outbox.route.topic.replacement"].split("${", 1)[0]
                agg = topic.removeprefix(prefix) if topic.startswith(prefix) else None
                if agg is None or agg not in aggregates.get(m.service, ()):
                    problems.append(f"{typ}: outbox-{m.service} routes by aggregate type to {prefix}<aggregate>, and {m.service}'s "
                                    f"code enqueues no aggregate type that lands on {topic} "
                                    f"(it enqueues {sorted(aggregates.get(m.service, ()))})")
            if not writable(topic):
                problems.append(f"{typ}: the debezium user may not write {topic}")
    return problems


# ---------------------------------------------------------------------------------------------------------------------

@unittest.skipUnless(go_command(), "go not on PATH")
class EventContractsTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.go, _, go_disc = kc.go_consumers()
        cls.ts, ts_disc = kc.ts_consumers()
        cls.py, py_disc = kc.py_consumers()
        cls.discovery_problems = go_disc + ts_disc + py_disc
        cls.messages = producer_messages()
        consumer_dirs = {go_module_dir(c.service) for c in cls.go}
        cls.reads, cls.read_problems = [], []
        reports = eventfields(tuple(sorted(d for d in consumer_dirs if d)))
        for reader, args in ((go_reads, (reports, cls.go)), (ts_reads, (cls.ts,)), (py_reads, (cls.py,))):
            reads, problems = reader(*args)
            cls.reads += reads
            cls.read_problems += problems
        cls.read_types = {r.type for r in cls.reads if (r.service, r.type) not in NOT_OUTBOX}
        producer_services = {m.service for t in cls.read_types for m in cls.messages.get(t, [])}
        producer_dirs = {go_module_dir(s) for s in producer_services} - {None}
        cls.reports = dict(reports)
        cls.reports.update(eventfields(tuple(sorted(producer_dirs))))
        found = emits_by_type(cls.reports)
        cls.emits = {}
        for s in producer_services:
            for t in cls.read_types:
                if go_module_dir(s) is not None:
                    cls.emits[(s, t)] = found.get((s, t), [])

    def test_consumers_are_found_and_their_reads_worked_out(self):
        self.assertEqual([], self.discovery_problems)
        self.assertEqual([], self.read_problems)
        services = {r.service for r in self.reads}
        for s in ("search", "inventory", "fulfillment", "workflow-ts", "prediction-py"):
            self.assertIn(s, services)
        self.assertGreater(len(self.reads), 60, "too few handler entries found; did the analyser stop following handlers?")
        # integration-hub-ts forwards whole events to subscribers and reads no data field, so it has no reads to check.
        self.assertIn("integration-hub-ts", {service for service, _, _ in self.ts})
        self.assertNotIn("integration-hub-ts", services)

    def test_every_consumer_reads_only_fields_its_producer_declares_and_writes(self):
        by_consumer = defaultdict(list)
        for r in self.reads:
            by_consumer[r.consumer].append(r)
        for consumer, reads in sorted(by_consumer.items()):
            with self.subTest(consumer=consumer):
                self.assertEqual([], contract_problems(reads, self.messages, self.emits))

    def test_producers_write_only_fields_their_schema_declares(self):
        self.assertEqual([], producer_problems(self.read_types, self.messages, self.emits))

    def test_exemptions_are_still_needed(self):
        handled = {(r.service, r.type) for r in self.reads}
        self.assertEqual([], sorted(set(NOT_OUTBOX) - handled), "NOT_OUTBOX entries no consumer handles any more")
        for key in PRODUCER_CODE_UNSEEN:
            self.assertEqual([], self.emits.get(key, []), f"{key}: the analyser now sees the producer; drop the exemption")

    # Negative tests: each rule fails on a mutated schema, payload or consumer.

    def test_a_field_dropped_from_the_producer_schema_fails(self):
        read = next(r for r in self.reads if r.service == "search" and r.type == "dev.depot.sales.order.confirmed.v1")
        messages = copy.deepcopy(self.messages)
        (m,) = messages[read.type]
        node, file = deref(*m.schema())
        node = copy.deepcopy(node)
        node["properties"].pop("order_number", None)
        node["required"] = [k for k in node.get("required", []) if k != "order_number"]
        m.payload, m.file = node, file
        problems = contract_problems([read], messages, self.emits)
        self.assertTrue(any("order_number" in p and "does not declare it" in p for p in problems), problems)

    def test_a_field_the_producer_code_does_not_write_fails(self):
        read = next(r for r in self.reads if r.service == "inventory" and r.type == "dev.depot.receiving.receipt.posted.v1")
        emits = copy.deepcopy(self.emits)
        for e in emits[("receiving", read.type)]:
            e["fields"] = [f for f in e["fields"] if f["path"] != "lines[].unit_price"]
        problems = contract_problems([read], self.messages, emits)
        self.assertTrue(any("lines[].unit_price" in p and "has no such field" in p for p in problems), problems)
        emits[("receiving", read.type)] = []
        self.assertTrue(any("no events.New call" in p for p in contract_problems([read], self.messages, emits)))

    def test_a_field_the_producer_writes_but_does_not_declare_fails(self):
        typ = "dev.depot.sales.order.confirmed.v1"
        emits = copy.deepcopy(self.emits)
        emits[("sales", typ)][0]["fields"].append({"path": "undeclared_total"})
        problems = producer_problems({typ}, self.messages, emits)
        self.assertTrue(any("undeclared_total" in p for p in problems), problems)

    def test_ts_and_python_reads_fail_on_a_missing_field(self):
        ts = next(r for r in self.reads if r.service == "workflow-ts" and r.type == "dev.depot.tms.load.released.v1")
        self.assertIn(("legalEntityScope[]",), ts.fields)
        py = next(r for r in self.reads if r.service == "prediction-py" and r.type == "dev.depot.receivables.payment.received.v1")
        self.assertIn(("legal_entity_id", "legalEntityId"), py.fields)
        self.assertEqual(("credit_item_id", "open_item_id"), py.anywhere)
        bad = copy.deepcopy(ts)
        bad.fields = bad.fields + [("noSuchField",)]
        self.assertTrue(any("noSuchField" in p for p in contract_problems([bad], self.messages, self.emits)))
        bad = [copy.deepcopy(r) for r in self.reads if r.service == "prediction-py"]
        for r in bad:
            r.anywhere = ("no_such_item_id",)
        problems = contract_problems(bad, self.messages, self.emits)
        self.assertTrue(any("no_such_item_id" in p and "no producer schema" in p for p in problems), problems)
        self.assertTrue(any("no_such_item_id" in p and "no producer code" in p for p in problems), problems)

    def test_the_analyser_follows_handlers_helpers_and_parameters(self):
        # A synthetic module: a handler map in code main reaches, a decode through a helper taking `any`, a json.RawMessage
        # passed to a mapper, an unreached map, and producers that name the type and payload through a helper.
        with tempfile.TemporaryDirectory() as tmp:
            mod = Path(tmp) / "synth"
            (mod / "cmd/synth").mkdir(parents=True)
            (mod / "internal/h").mkdir(parents=True)
            gomod = (REPO / "services/search/go.mod").read_text()
            gomod = gomod.replace("module github.com/BenjaminSRussell/depot/services/search", "module example.com/synth")
            gomod = gomod.replace("=> ../../platform/go", f"=> {REPO / 'platform/go'}")
            (mod / "go.mod").write_text(gomod)
            shutil.copy(REPO / "services/search/go.sum", mod / "go.sum")
            (mod / "internal/h/h.go").write_text(SYNTH_HANDLERS)
            (mod / "cmd/synth/main.go").write_text(SYNTH_MAIN)
            out = subprocess.run([go_command(), "run", ".", "-root", tmp, str(mod)], cwd=EVENTFIELDS, check=True,
                                 capture_output=True, text=True)
        (report,) = json.loads(out.stdout)
        self.assertEqual([], report["problems"])
        got = {h["type"]: sorted(f["path"] for d in h["decodes"] for f in d["fields"]) for h in report["handlers"]}
        self.assertEqual({
            "dev.depot.synth.thing.created.v1": ["id", "lines[].qty", "name"],
            "dev.depot.synth.thing.moved.v1": ["Where", "id"],
        }, got)
        emitted = {(e["type"], e["goType"]): sorted(f["path"] for f in e["fields"]) for e in report["emits"]}
        self.assertEqual({
            ("dev.depot.synth.thing.created.v1", "h.created"): ["id", "lines[].qty", "name"],
            ("dev.depot.synth.thing.moved.v1", "map[string]any"): ["id"],
        }, emitted)
        self.assertEqual(["thing"], report["aggregates"])


SYNTH_HANDLERS = '''package h

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/outbox"
)

const (
	Created = "dev.depot.synth.thing.created.v1"
	moved   = "moved"
)

type created struct {
	ID    string `json:"id"`
	Name  string `json:"name,omitempty"`
	Lines []struct {
		Qty string `json:"qty"`
	} `json:"lines"`
	skip string
}

type movedData struct {
	ID    string `json:"id"`
	Where string
}

func decode(evt events.Event, into any) error { return json.Unmarshal(evt.Data, into) }

func fromRaw(data json.RawMessage) (movedData, error) {
	var m movedData
	return m, json.Unmarshal(data, &m)
}

func Handlers() map[string]eventing.Handler {
	h := func(f func(events.Event) error) eventing.Handler {
		return eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error { return f(evt) })
	}
	return map[string]eventing.Handler{
		Created: h(func(evt events.Event) error {
			var c created
			return decode(evt, &c)
		}),
		events.Type("synth", "thing", moved, 1): eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			_, err := fromRaw(evt.Data)
			return err
		}),
	}
}

// Unreached is a handler map no main package uses: it is not a consumer.
func Unreached() map[string]eventing.Handler {
	return map[string]eventing.Handler{"dev.depot.synth.thing.unreached.v1": nil}
}

func emit(ctx context.Context, tx pgx.Tx, typ string, data any) error {
	evt, err := events.New(typ, "synth", "s", "t", data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "thing", "id", evt)
}

func Publish(ctx context.Context, tx pgx.Tx) error {
	if err := emit(ctx, tx, Created, created{}); err != nil {
		return err
	}
	return emit(ctx, tx, events.Type("synth", "thing", moved, 1), map[string]any{"id": "x"})
}
'''

SYNTH_MAIN = '''package main

import "example.com/synth/internal/h"

func main() { _ = h.Handlers() }
'''


@unittest.skipUnless(go_command(), "go not on PATH")
class SearchSourcesTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        go, _, _ = kc.go_consumers()
        (cls.consumer,) = [c for c in go if c.service == "search"]
        report = eventfields(("services/search",))["services/search"]
        cls.handled = {h["type"] for h in report["handlers"]}
        cls.messages = producer_messages()
        producers = sorted({m.service for t in cls.handled for m in cls.messages.get(t, [])})
        reports = eventfields(tuple(d for d in (go_module_dir(s) for s in producers) if d))
        cls.aggregates = {Path(d).name: set(r["aggregates"]) for d, r in reports.items()}
        cls.spec = yaml.safe_load(SEARCH_ASYNCAPI.read_text())
        cls.conns = connectors()
        cls.writable = staticmethod(debezium_writable())

    def problems(self, spec=None, conns=None, aggregates=None):
        return search_source_problems(spec or self.spec, self.handled, self.consumer.topics, self.messages,
                                      conns or self.conns, self.writable, aggregates or self.aggregates)

    def test_every_indexed_type_arrives_on_a_topic_its_producers_connector_relays(self):
        self.assertGreaterEqual(len(self.handled), 18)
        self.assertEqual([], self.problems())

    def test_a_source_without_a_connector_fails(self):
        conns = {k: v for k, v in self.conns.items() if k != "hr"}
        self.assertTrue(any("no outbox-hr connector" in p for p in self.problems(conns=conns)))

    def test_a_type_routed_to_another_topic_fails(self):
        spec = copy.deepcopy(self.spec)
        for ch in spec["channels"].values():
            if ch["address"] == "depot.fulfillment.packingslip":
                ch["address"] = "depot.fulfillment.shipment"
        problems = self.problems(spec=spec)
        self.assertTrue(any("routes it to depot.fulfillment.packingslip" in p for p in problems), problems)

    def test_an_aggregate_the_producer_does_not_enqueue_fails(self):
        aggregates = {k: (v - {"vendorinvoice"} if k == "receiving" else v) for k, v in self.aggregates.items()}
        problems = self.problems(aggregates=aggregates)
        self.assertTrue(any("depot.receiving.vendorinvoice" in p and "enqueues no aggregate type" in p for p in problems), problems)

    def test_an_undeclared_topic_or_type_fails(self):
        spec = copy.deepcopy(self.spec)
        name = next(k for k, op in spec["operations"].items() if op["channel"]["$ref"].endswith("/worker"))
        del spec["operations"][name]
        problems = self.problems(spec=spec)
        self.assertTrue(any("subscribes to depot.hr.worker" in p for p in problems), problems)
        self.assertTrue(any("handles dev.depot.hr.worker.created.v1" in p for p in problems), problems)


if __name__ == "__main__":
    unittest.main()
