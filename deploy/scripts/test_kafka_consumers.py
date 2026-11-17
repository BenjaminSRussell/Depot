"""Every Kafka consumer has a least-privilege KafkaUser, and dev runs the consumers it can (D34, B-05b).

Consumers are found in the code, not in a hand list:
- Go: every `kafka.ConfigFromEnv` call (platform/go/eventing/kafka) under services/ and tools/, with its default group and
  topic list, and every `eventing.Config` literal with its Name and DeadLetterTopic. Constants, package variables,
  composite literals, string concatenation, small helper functions and the parameters of the function the call sits in
  (bound at each of its call sites) are evaluated; a value the evaluator cannot work out fails the test unless
  DYNAMIC_TOPICS says where it comes from. A group that ends in a per-pod value (pricing's pricing-cache-<pod>) needs a
  prefix ACL.
- TypeScript: every kafkajs `consumer({ groupId })` and the config object its file returns (group, topics, dlqTopic),
  with `env.X || default`, `env["X"] ?? default` and constant arrays.
- Python: every class that builds a kafka-python `KafkaConsumer` and the calls that construct it (group, topics).
Environment variables resolve through the consumer's chart: the template that reads `.Values.<block>.username` maps each
env var to its values key, and the chart default (or the dev value) is what the pod gets.

For every consumer block of a chart (a values mapping with `username` under `kafka`, or with `brokers`) the test checks:
- each code consumer of the service belongs to a block (by group when the chart has several), and each block has one;
- the block's KafkaUser exists in deploy/kafka/<username>.yaml (synced by the kafka-consumer-users Application), uses
  SCRAM-SHA-512 with simple authorization like the cluster's TLS listener, and its ACLs are exactly: Read and Describe on
  each topic the code consumes, Read and Describe on each group (prefix for a per-pod group), Write and Describe on each
  dead-letter topic, and a KafkaTopic creates each dead-letter topic; no KafkaUser in deploy/kafka/*-consumer.yaml is left
  without a chart;
- the chart's group keys and dlqTopic agree with the code, and the template wires the user the way every consumer does:
  KAFKA_USERNAME from the block, KAFKA_PASSWORD_FILE and KAFKA_TLS_CA_FILE from the Secret named by the block's `secret`,
  mounted at /var/run/depot/kafka.
In dev (deploy/envs/dev), every block is either on or listed in DEV_OFF with the reason. A block that is on must: read only
topics an outbox connector relays (the debezium user may write them), point at the TLS listener, name a Secret an
ExternalSecret in its namespace writes from the KafkaUser's Secret and the cluster CA (through the kafka-users SecretStore,
whose Role may read exactly the users that are on), and, rendered with helm, reference only Secrets dev declares or
lists as provisioned outside the repository (test_migrate_jobs.EXTERNALLY_PROVISIONED, UNMANAGED_SECRETS).

Not covered here: whether an event is really consumed in a cluster. `make -C deploy smoke-consumers` does that against
a dev cluster (#213); it is not run in CI.
"""

import ast
import fnmatch
import json
import os
import re
import shutil
import sys
import unittest
from dataclasses import dataclass, field
from pathlib import Path

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parent))
import test_argocd_apps as argo  # noqa: E402
import test_migrate_jobs  # noqa: E402

DEPLOY = Path(__file__).resolve().parents[1]
REPO = DEPLOY.parent
EVENTING = "github.com/BenjaminSRussell/depot/platform/eventing"
EVENTING_KAFKA = EVENTING + "/kafka"
DEV_BROKERS = "depot-kafka-bootstrap.kafka.svc:9093"
CREDENTIALS_DIR = "/var/run/depot/kafka"
CLUSTER_CA_SECRET = "depot-cluster-ca-cert"

# Topic lists the code builds at run time from a table, and where the check reads the table instead: (service, expression).
DYNAMIC_TOPICS = {
    # `audit cdc-consume` reads cdc.NewRegistry(cdc.Tables).Topics(): one topic per Server of the registry
    # (test_argocd_apps checks the registry against the audit-cdc-<service> connectors).
    ("audit", "registry.Topics()"): lambda: tuple(sorted(set(re.findall(
        r'Server:\s*"([^"]+)"', (REPO / "services/audit/internal/cdc/tables.go").read_text())))),
}

# Consumers dev leaves off, by KafkaUser, and why. Everything else is on in dev and must pass the dev checks. A consumer
# whose topics are relayed (an outbox connector in connect.yaml writes them, true for every consumer since B-05a) stays
# off only because something it needs to start is not in dev.
NO_DB = "no {} Secret in dev: the service's dev database has no login role or Secret yet (D4), so the consumer would crash"
DEV_OFF = {
    "audit-consumer": NO_DB.format("audit-db"),
    "billing-consumer": NO_DB.format("billing-db") + "; it also needs consumer.icCharge.revenueAccount",
    "edi-consumer": NO_DB.format("edi-db"),
    "finreport-consumer": NO_DB.format("finreport-db") + "; it also needs serviceIdentity.signingKeySecret",
    "fulfillment-consumer": NO_DB.format("fulfillment database"),
    "integration-hub-consumer": NO_DB.format("integration-hub-db") + " (the hub's API pod cannot start either)",
    "intercompany-consumer": NO_DB.format("intercompany-db"),
    "inventory-consumer": NO_DB.format("inventory-db"),
    "payables-consumer": NO_DB.format("payables-db"),
    "prediction-consumer": NO_DB.format("prediction-db") + "; it also calls receivables, which has no dev database",
    "pricing-consumer": NO_DB.format("pricing database") + " (the cache consumer runs inside the API pod, which cannot "
                                                           "start without it)",
    "production-consumer": NO_DB.format("production-db"),
    "production-shopfloor-consumer": NO_DB.format("production-db"),
    "purchasing-consumer": NO_DB.format("purchasing database"),
    "receivables-consumer": NO_DB.format("receivables database"),
    "revrec-consumer": NO_DB.format("revrec-db"),
    "scheduling-consumer": NO_DB.format("scheduling database"),
    "search-consumer": NO_DB.format("search-db"),
    "shopfloor-consumer": NO_DB.format("shopfloor database"),
    "tax-consumer": NO_DB.format("tax-db"),
}

# Secrets a dev consumer that is on reads besides those test_migrate_jobs.EXTERNALLY_PROVISIONED and
# test_argocd_apps.UNMANAGED_SECRETS already list as created outside the repository: (namespace, name) -> where that is said.
DEV_EXTERNAL_SECRETS = {
    ("hello-ledger", "receiving-audit-seed"): "services/receiving/NOT-IMPLEMENTED.md: the audit sample seed is created outside "
                                              "the repository, like receiving-db; the receiving API reads it too (#213)",
}

OPEN = {"(": ")", "[": "]", "{": "}"}


class Unresolved:
    def __init__(self, why):
        self.why = why

    def __repr__(self):
        return f"<unresolved {self.why}>"


class Prefix(str):
    """A group name that ends in a per-instance value: the ACL is a prefix grant on this string."""


@dataclass(frozen=True)
class Env:
    name: str
    as_list: bool = False


@dataclass(frozen=True)
class Alt:
    """`env || default`: the env var's value when the pod gets a non-empty one, else the default."""
    first: object
    second: object


@dataclass
class Struct:
    fields: dict
    ctx: object


# ---------------------------------------------------------------------------------------------------------------------
# Source text helpers (Go and TypeScript share the bracket and string rules used here).

def skip_string(src, i):
    q, j = src[i], i + 1
    while j < len(src):
        if src[j] == "\\" and q != "`":
            j += 2
            continue
        if src[j] == q:
            return j + 1
        if src[j] == "\n" and q != "`":
            return j
        j += 1
    return j


def strip_comments(src):
    """src with // and /* */ comments blanked out (same length, so positions stay valid)."""
    out, i, n = [], 0, len(src)
    while i < n:
        c = src[i]
        if c == "/" and src.startswith("//", i):
            j = src.find("\n", i)
            j = n if j < 0 else j
            out.append(" " * (j - i))
            i = j
        elif c == "/" and src.startswith("/*", i):
            j = src.find("*/", i + 2)
            j = n if j < 0 else j + 2
            out.append("".join(ch if ch == "\n" else " " for ch in src[i:j]))
            i = j
        elif c in "\"'`":
            j = skip_string(src, i)
            out.append(src[i:j])
            i = j
        else:
            out.append(c)
            i += 1
    return "".join(out)


def match(src, i):
    depth, j = 0, i
    while j < len(src):
        c = src[j]
        if c in "\"'`":
            j = skip_string(src, j)
            continue
        if c in OPEN:
            depth += 1
        elif c in ")]}":
            depth -= 1
            if depth == 0:
                return j
        j += 1
    raise ValueError(f"unbalanced bracket at {i}")


def split_top(s, sep):
    parts, depth, start, j = [], 0, 0, 0
    while j < len(s):
        c = s[j]
        if c in "\"'`":
            j = skip_string(s, j)
            continue
        if c in OPEN:
            depth += 1
        elif c in ")]}":
            depth -= 1
        elif depth == 0 and s.startswith(sep, j):
            parts.append(s[start:j])
            j += len(sep)
            start = j
            continue
        j += 1
    parts.append(s[start:])
    return [p.strip() for p in parts if p.strip()]


def read_expr(text, i):
    """The expression starting at i, up to the end of its statement."""
    depth, j = 0, i
    while j < len(text):
        c = text[j]
        if c in "\"'`":
            j = skip_string(text, j)
            continue
        if c in OPEN:
            depth += 1
        elif c in ")]}":
            if depth == 0:
                break
            depth -= 1
        elif depth == 0 and c in "\n;":
            if not text[i:j].rstrip().endswith(("+", "||", "&&", "??", ",")):
                break
        j += 1
    return text[i:j].strip()


def unquote(lit):
    if lit.startswith("`"):
        return lit[1:-1]
    if lit.startswith("'"):
        lit = '"' + lit[1:-1].replace('"', '\\"') + '"'
    return json.loads(lit)


STRING = re.compile(r'^(?:"(?:[^"\\]|\\.)*"|`[^`]*`)$')
IDENT = re.compile(r"^[A-Za-z_]\w*$")


# ---------------------------------------------------------------------------------------------------------------------
# Go

def go_modules():
    mods = {}
    for root, dirs, files in os.walk(REPO):
        dirs[:] = [d for d in dirs if d not in ("node_modules", ".git", "testdata") and not d.startswith(".")]
        if "go.mod" in files:
            first = (Path(root) / "go.mod").read_text().splitlines()[0]
            mods[first.split()[1]] = Path(root)
    return mods


@dataclass
class GoFunc:
    name: str
    recv: str
    params: list
    file: Path
    body_start: int
    body_end: int


@dataclass
class Ctx:
    pkg: object
    file: Path
    func: object = None
    env: dict = None


FUNC_DECL = re.compile(r"^func[ \t]+(?:\((?P<recv>[^)]*)\)[ \t]*)?(?P<name>\w+)[ \t]*\(", re.M)
IMPORT_BLOCK = re.compile(r"^import[ \t]*\((.*?)^\)", re.M | re.S)
IMPORT_ONE = re.compile(r'^import[ \t]+(?:(\w+)[ \t]+)?"([^"]+)"', re.M)
IMPORT_SPEC = re.compile(r'^\s*(?:(\w+|\.)\s+)?"([^"]+)"', re.M)


class GoPackage:
    _cache = {}
    _modules = None

    @classmethod
    def at(cls, directory):
        directory = Path(directory).resolve()
        if directory not in cls._cache:
            cls._cache[directory] = cls(directory)
        return cls._cache[directory]

    @classmethod
    def for_import(cls, path):
        if cls._modules is None:
            cls._modules = go_modules()
        best = max((m for m in cls._modules if path == m or path.startswith(m + "/")), key=len, default=None)
        if best is None:
            return None
        d = cls._modules[best] / path[len(best):].lstrip("/")
        return cls.at(d) if d.is_dir() else None

    def __init__(self, directory):
        self.dir = directory
        self.files = {p: strip_comments(p.read_text()) for p in sorted(directory.glob("*.go"))
                      if not p.name.endswith("_test.go")}
        self._values = None
        self._funcs = None

    def imports(self, file):
        text = self.files[file]
        out = {}
        for block in IMPORT_BLOCK.findall(text):
            for alias, path in IMPORT_SPEC.findall(block):
                out[alias or path.rsplit("/", 1)[-1]] = path
        for alias, path in IMPORT_ONE.findall(text):
            out[alias or path.rsplit("/", 1)[-1]] = path
        return out

    def values(self):
        """Package-level const and var declarations: name -> (expression, file)."""
        if self._values is None:
            self._values = {}
            for file, text in self.files.items():
                for m in re.finditer(r"^(?:const|var)[ \t]*\(", text, re.M):
                    o = text.index("(", m.start())
                    for spec in split_top(text[o + 1:match(text, o)], "\n"):
                        s = re.match(r"^(\w+)(?:[ \t]+[^=]+?)?[ \t]*=[ \t]*(.*)$", spec, re.S)
                        if s:
                            self._values.setdefault(s.group(1), (s.group(2).strip(), file))
                for m in re.finditer(r"^(?:const|var)[ \t]+(\w+)(?:[ \t]+[^=\n]+?)?[ \t]*=[ \t]*", text, re.M):
                    self._values.setdefault(m.group(1), (read_expr(text, m.end()), file))
        return self._values

    def funcs(self):
        if self._funcs is None:
            self._funcs = []
            for file, text in self.files.items():
                for m in FUNC_DECL.finditer(text):
                    o = m.end() - 1
                    c = match(text, o)
                    params = [p.split()[0] for p in split_top(text[o + 1:c], ",")]
                    body = re.compile(r"\{[ \t]*(?:\n|\S)").search(text, c)
                    j = c + 1
                    while j < len(text) and text[j] != "\n":  # the body brace ends the signature line
                        if text[j] in "\"'`":
                            j = skip_string(text, j)
                            continue
                        if text[j] in "([":
                            j = match(text, j) + 1
                            continue
                        if text[j] == "{" and not text[j - 1].isalnum() and text[c + 1:j].strip()[-9:] != "interface":
                            break
                        j += 1
                    if j >= len(text) or text[j] != "{":
                        assert body, f"{file}: no body for func {m.group('name')}"
                        j = body.start()
                    self._funcs.append(GoFunc(m.group("name"), m.group("recv") or "", params, file, j, match(text, j)))
        return self._funcs

    def func(self, name):
        return next((f for f in self.funcs() if f.name == name and not f.recv), None)

    def enclosing(self, file, pos):
        return next((f for f in self.funcs() if f.file == file and f.body_start <= pos <= f.body_end), None)

    def call_sites(self, func):
        out = []
        if func.recv:
            return out
        for file, text in self.files.items():
            for m in re.finditer(rf"(?<![\w.]){re.escape(func.name)}\(", text):
                if text[max(0, m.start() - 5):m.start()] == "func ":
                    continue
                o = m.end() - 1
                out.append((file, m.start(), split_top(text[o + 1:match(text, o)], ",")))
        return out


def mentions(exprs, names):
    return any(re.search(rf"(?<![\w.]){re.escape(n)}\b", e) for e in exprs for n in names)


def bindings(pkg, func, exprs, depth=0):
    """Parameter bindings of func, one per call path, for the parameters exprs mention."""
    if func is None or depth > 6 or not mentions(exprs, func.params):
        yield None
        return
    sites = pkg.call_sites(func)
    if not sites:
        yield None
        return
    for file, pos, args in sites:
        caller = pkg.enclosing(file, pos)
        used = [a for p, a in zip(func.params, args) if mentions(exprs, [p])]
        for caller_env in bindings(pkg, caller, used, depth + 1):
            yield {p: (a, Ctx(pkg, file, caller, caller_env)) for p, a in zip(func.params, args)}


def go_eval(expr, ctx, depth=0):
    """The values expr can take: str, Prefix, tuple (a []string), Struct, Env or Unresolved."""
    e = expr.strip()
    if depth > 20:
        return [Unresolved(e)]
    while e.startswith("(") and match(e, 0) == len(e) - 1:
        e = e[1:-1].strip()
    parts = split_top(e, "+")
    if len(parts) > 1:
        head = [go_eval(p, ctx, depth + 1) for p in parts[:-1]]
        last = go_eval(parts[-1], ctx, depth + 1)
        if not all(len(h) == 1 and isinstance(h[0], str) for h in head):
            return [Unresolved(e)]
        prefix = "".join(h[0] for h in head)
        if all(isinstance(v, str) and not isinstance(v, Prefix) for v in last):
            return [prefix + v for v in last]
        return [Prefix(prefix)]  # a per-pod suffix (getenv, a generated id): only the prefix is known
    if STRING.match(e):
        return [unquote(e)]
    m = re.match(r"^\[\][\w.]+\{(.*)\}$", e, re.S)
    if m:
        items = []
        for item in split_top(m.group(1), ","):
            v = go_eval(item, ctx, depth + 1)
            if len(v) != 1 or not isinstance(v[0], str):
                return [Unresolved(e)]
            items.append(v[0])
        return [tuple(items)]
    m = re.match(r"^&?([\w.]+)\{(.*)\}$", e, re.S)
    if m:
        fields = {}
        for item in split_top(m.group(2), ","):
            k, _, v = item.partition(":")
            fields[k.strip()] = v.strip()
        return [Struct(fields, ctx)]
    m = re.match(r"^([\w.]+)\((.*)\)$", e, re.S)
    if m and match(e, len(m.group(1))) == len(e) - 1:
        name, args = m.group(1), split_top(m.group(2), ",")
        if name in ("getenv", "os.Getenv") and len(args) == 1:
            v = go_eval(args[0], ctx, depth + 1)
            return [Env(x) if isinstance(x, str) else Unresolved(e) for x in v]
        pkg, fname = ctx.pkg, name
        if "." in name:
            alias, fname = name.split(".", 1)
            path = ctx.pkg.imports(ctx.file).get(alias)
            pkg = GoPackage.for_import(path) if path else None
        f = pkg.func(fname) if pkg and "." not in fname else None
        if f is None:
            return [Unresolved(e)]
        env = {p: (a, ctx) for p, a in zip(f.params, args)}
        body = pkg.files[f.file][f.body_start:f.body_end]
        out = []
        for r in re.finditer(r"\breturn\b", body):
            ret = read_expr(body, r.end())
            if ret:
                out += go_eval(split_top(ret, ",")[0], Ctx(pkg, f.file, f, env), depth + 1)
        return out or [Unresolved(e)]
    m = re.match(r"^(\w+)\.(\w+)$", e)
    if m:
        alias, attr = m.groups()
        path = ctx.pkg.imports(ctx.file).get(alias)
        if path and not (ctx.env and alias in ctx.env) and not local_assignments(ctx, alias):
            pkg = GoPackage.for_import(path)
            decl = pkg.values().get(attr) if pkg else None
            return go_eval(decl[0], Ctx(pkg, decl[1]), depth + 1) if decl else [Unresolved(e)]
        out = []
        for v in go_eval(alias, ctx, depth + 1):
            if isinstance(v, Struct) and attr in v.fields:
                out += go_eval(v.fields[attr], v.ctx, depth + 1)
            else:
                out.append(Unresolved(e))
        return out
    if IDENT.match(e):
        if ctx.env and e in ctx.env:
            arg, arg_ctx = ctx.env[e]
            return go_eval(arg, arg_ctx, depth + 1)
        if ctx.func and e in ctx.func.params:
            return [Unresolved(f"unbound parameter {e}")]
        local = local_assignments(ctx, e)
        if local:
            return [v for a in local for v in go_eval(a, ctx, depth + 1)]
        decl = ctx.pkg.values().get(e)
        if decl:
            return go_eval(decl[0], Ctx(ctx.pkg, decl[1]), depth + 1)
    return [Unresolved(e)]


def local_assignments(ctx, name):
    if not ctx.func:
        return []
    body = ctx.pkg.files[ctx.func.file][ctx.func.body_start:ctx.func.body_end]
    return [read_expr(body, m.end()) for m in
            re.finditer(rf"(?:(?<=\n)|(?<=\t)|(?<=var ))[ \t]*{re.escape(name)}[ \t]*(?::=|=(?!=))[ \t]*", body)]


def call_args(text, start):
    o = text.index("(", start)
    return split_top(text[o + 1:match(text, o)], ",")


def literal_fields(text, start):
    o = text.index("{", start)
    out = {}
    for item in split_top(text[o + 1:match(text, o)], ","):
        k, _, v = item.partition(":")
        out[k.strip()] = v.strip()
    return out


@dataclass
class Consumer:
    service: str
    where: str
    groups: set = field(default_factory=set)
    topics: set = field(default_factory=set)
    dlqs: set = field(default_factory=set)
    name: str = ""
    pairs: list = field(default_factory=list)  # (group, topics) of each consumer, for the smoke test


def go_consumers():
    """(consumers, dead-letter configs, problems) of every Go package that builds a consumer."""
    consumers, dlqs, problems = [], [], []
    dirs = set()
    for base in (REPO / "services", REPO / "tools"):
        for p in base.rglob("*.go"):
            if "node_modules" not in p.parts and not p.name.endswith("_test.go") and EVENTING_KAFKA in p.read_text():
                dirs.add(p.parent)
    for d in sorted(dirs):
        pkg = GoPackage.at(d)
        service = d.relative_to(REPO).parts[1]
        for file, text in pkg.files.items():
            imports = {path: alias for alias, path in pkg.imports(file).items()}
            where = lambda pos: f"{file.relative_to(REPO)}:{text.count(chr(10), 0, pos) + 1}"  # noqa: E731
            if EVENTING_KAFKA in imports:
                for m in re.finditer(rf"(?<![\w.]){imports[EVENTING_KAFKA]}\.ConfigFromEnv\(", text):
                    args = call_args(text, m.start())
                    func = pkg.enclosing(file, m.start())
                    for env in bindings(pkg, func, [args[1], args[3]]):
                        ctx = Ctx(pkg, file, func, env)
                        groups = go_eval(args[1], ctx)
                        dynamic = DYNAMIC_TOPICS.get((service, args[3]))
                        topics = [dynamic()] if dynamic else go_eval(args[3], ctx)
                        c = Consumer(service, where(m.start()))
                        if len(groups) != 1 or not isinstance(groups[0], str):
                            problems.append(f"{c.where}: cannot work out the consumer group {args[1]!r}: {groups}")
                        elif len(topics) != 1 or not isinstance(topics[0], tuple):
                            problems.append(f"{c.where}: cannot work out the topics {args[3]!r}: {topics}")
                        else:
                            c.groups, c.topics = {groups[0]}, set(topics[0])
                            consumers.append(c)
            if EVENTING in imports:
                for m in re.finditer(rf"(?<![\w.]){imports[EVENTING]}\.Config\{{", text):
                    fields = literal_fields(text, m.start())
                    if "DeadLetterTopic" not in fields:
                        problems.append(f"{where(m.start())}: eventing.Config without a DeadLetterTopic")
                        continue
                    func = pkg.enclosing(file, m.start())
                    for env in bindings(pkg, func, [fields.get("Name", '""'), fields["DeadLetterTopic"]]):
                        ctx = Ctx(pkg, file, func, env)
                        names = go_eval(fields.get("Name", '""'), ctx)
                        values = go_eval(fields["DeadLetterTopic"], ctx)
                        bad = [v for v in values if not isinstance(v, (str, Env)) or isinstance(v, Prefix)]
                        if len(names) != 1 or not isinstance(names[0], str) or bad:
                            problems.append(f"{where(m.start())}: cannot work out Name/DeadLetterTopic: {names} {values}")
                            continue
                        dlqs.append(Consumer(service, where(m.start()), dlqs=set(values), name=names[0]))
    for base in (REPO / "services", REPO / "tools"):
        for p in base.rglob("*.go"):
            if "node_modules" in p.parts or p.name.endswith("_test.go"):
                continue
            if "kgo.ConsumerGroup" in p.read_text() or "sarama" in p.read_text():
                problems.append(f"{p.relative_to(REPO)}: a Kafka consumer outside platform/go/eventing/kafka")
    return consumers, dlqs, problems


# ---------------------------------------------------------------------------------------------------------------------
# TypeScript (kafkajs)

def ts_files(directory):
    return [p for p in sorted(directory.rglob("*.ts")) if "node_modules" not in p.parts
            and not re.search(r"\.(test|spec)\.ts$", p.name) and "__tests__" not in p.parts]


def ts_const(name, file, seen=()):
    """(expression, file) of a top-level or function-level const, following named imports."""
    text = strip_comments(file.read_text())
    m = re.search(rf"\b(?:export\s+)?const\s+{re.escape(name)}\b(?:\s*:\s*[^=]+?)?\s*=\s*", text)
    if m:
        e = read_expr(text, m.end())
        return re.sub(r"\s+as\s+const$", "", e), file
    imp = re.search(rf'import\s*\{{[^}}]*\b{re.escape(name)}\b[^}}]*\}}\s*from\s*"(\.[^"]+)"', text)
    if imp and file not in seen:
        target = (file.parent / imp.group(1)).with_suffix(".ts")
        if target.is_file():
            return ts_const(name, target, seen + (file,))
    return None


def ts_eval(expr, file):
    e = expr.strip()
    for op in ("||", "??"):
        parts = split_top(e, op)
        if len(parts) == 2:
            return Alt(ts_eval(parts[0], file), ts_eval(parts[1], file))
    m = re.match(r"""^env(?:\.(\w+)|\[["'](\w+)["']\])$""", e)
    if m:
        return Env(m.group(1) or m.group(2))
    if re.match(r"""^(?:"[^"]*"|'[^']*')$""", e):
        return unquote(e)
    if e.startswith("[") and match(e, 0) == len(e) - 1:
        out = []
        for item in split_top(e[1:-1], ","):
            v = ts_eval(item[3:] if item.startswith("...") else item, file)
            if isinstance(v, tuple):
                out += v
            elif isinstance(v, str):
                out.append(v)
            else:
                return Unresolved(e)
        return tuple(out)
    m = re.match(r"^\w+\((.*)\)$", e, re.S)
    if m and match(e, e.index("(")) == len(e) - 1:
        arg = ts_eval(m.group(1), file)
        return Env(arg.name, as_list=True) if isinstance(arg, Env) else Unresolved(e)
    if IDENT.match(e):
        decl = ts_const(e, file)
        if decl:
            return ts_eval(*decl)
    return Unresolved(e)


def ts_consumers():
    out, problems = [], []
    for base in (REPO / "services", REPO / "tools"):
        for d in sorted(p for p in base.iterdir() if (p / "package.json").is_file() and "kafkajs" in (p / "package.json").read_text()):
            for file in ts_files(d / "src"):
                text = strip_comments(file.read_text())
                if "kafkajs" not in text or not re.search(r"\.consumer\(\s*\{\s*groupId", text):
                    continue
                where = file.relative_to(REPO).as_posix()
                found = None
                for m in re.finditer(r"\breturn\s*\{", text):
                    o = text.index("{", m.start())
                    props = {}
                    for item in split_top(text[o + 1:match(text, o)], ","):
                        if item.startswith("..."):
                            continue
                        k, sep, v = item.partition(":")
                        props[k.strip()] = v.strip() if sep else k.strip()
                    if {"group", "topics"} <= set(props):
                        found = props
                if not found:
                    problems.append(f"{where}: a kafkajs consumer whose config object (group, topics) is not found")
                    continue
                values = {k: ts_eval(found[k], file) for k in ("group", "topics", "dlqTopic") if k in found}
                out.append((d.relative_to(REPO).parts[1], where, values))
    return out, problems


# ---------------------------------------------------------------------------------------------------------------------
# Python (kafka-python)

def py_eval(node, scope, module):
    """Evaluate a small expression: constants, getenv, `a or b`, names, tuples, ",".join and .split(",")."""
    if isinstance(node, ast.Constant) and isinstance(node.value, str):
        return node.value
    if isinstance(node, ast.BoolOp) and isinstance(node.op, ast.Or) and len(node.values) == 2:
        return Alt(py_eval(node.values[0], scope, module), py_eval(node.values[1], scope, module))
    if isinstance(node, (ast.Tuple, ast.List)):
        items = [py_eval(e, scope, module) for e in node.elts]
        return tuple(items) if all(isinstance(i, str) for i in items) else Unresolved(ast.unparse(node))
    if isinstance(node, ast.Call):
        f = node.func
        if isinstance(f, ast.Name) and f.id == "getenv" or ast.unparse(f) in ("os.environ.get", "os.getenv"):
            arg = py_eval(node.args[0], scope, module)
            return Env(arg) if isinstance(arg, str) else Unresolved(ast.unparse(node))
        if isinstance(f, ast.Attribute) and f.attr == "join" and isinstance(f.value, ast.Constant):
            v = py_eval(node.args[0], scope, module)
            return f.value.value.join(v) if isinstance(v, tuple) else Unresolved(ast.unparse(node))
        if isinstance(f, ast.Attribute) and f.attr == "split":
            return py_split(py_eval(f.value, scope, module))
    if isinstance(node, ast.ListComp) and len(node.generators) == 1:
        return py_eval(node.generators[0].iter, scope, module)
    if isinstance(node, ast.Name):
        for body in (scope, module):
            for stmt in body:
                if isinstance(stmt, ast.Assign) and any(isinstance(t, ast.Name) and t.id == node.id for t in stmt.targets):
                    return py_eval(stmt.value, scope if body is scope else [], module)
    return Unresolved(ast.unparse(node))


def py_split(v):
    if isinstance(v, str):
        return tuple(s.strip() for s in v.split(",") if s.strip())
    if isinstance(v, Env):
        return Env(v.name, as_list=True)
    if isinstance(v, Alt):
        return Alt(py_split(v.first), py_split(v.second))
    return Unresolved(repr(v))


def py_consumers():
    out, problems = [], []
    for base in (REPO / "services", REPO / "tools"):
        for file in sorted(base.rglob("*.py")):
            if "tests" in file.parts or file.name.startswith("test_") or "node_modules" in file.parts:
                continue
            src = file.read_text()
            if "KafkaConsumer" not in src:
                continue
            tree = ast.parse(src)
            where = file.relative_to(REPO).as_posix()
            classes = {}
            for cls in [n for n in tree.body if isinstance(n, ast.ClassDef)]:
                if any(isinstance(n, ast.Call) and ast.unparse(n.func) == "KafkaConsumer" for n in ast.walk(cls)):
                    init = next(f for f in cls.body if isinstance(f, ast.FunctionDef) and f.name == "__init__")
                    classes[cls.name] = [a.arg for a in init.args.args[1:]]
            if not classes:
                problems.append(f"{where}: KafkaConsumer outside a class this check knows how to read")
                continue
            for fn in [n for n in ast.walk(tree) if isinstance(n, ast.FunctionDef)]:
                for call in [n for n in ast.walk(fn) if isinstance(n, ast.Call) and isinstance(n.func, ast.Name)
                             and n.func.id in classes]:
                    params = classes[call.func.id]
                    bound = dict(zip(params, call.args))
                    bound.update({k.arg: k.value for k in call.keywords})
                    if "group" not in bound or "topics" not in bound:
                        problems.append(f"{where}:{call.lineno}: {call.func.id} without group and topics")
                        continue
                    values = {k: py_eval(bound[k], fn.body, tree.body) for k in ("group", "topics")}
                    out.append((file.relative_to(REPO).parts[1], f"{where}:{call.lineno}", values))
    return out, problems


# ---------------------------------------------------------------------------------------------------------------------
# Charts

@dataclass(eq=False)
class Block:
    chart: Path
    service: str
    path: tuple
    data: dict

    @property
    def dotted(self):
        return ".".join(self.path)

    def owner(self, values):
        """The mapping that switches the consumer on: the parent of a `kafka` block that has enabled or brokers."""
        node = values
        for key in self.path[:-1]:
            node = node[key]
        if self.path[-1] == "kafka" and isinstance(node, dict) and ({"enabled", "brokers"} & set(node)):
            return node
        return self.get(values)

    def get(self, values):
        node = values
        for key in self.path:
            node = node[key]
        return node

    def templates(self):
        needle = f".Values.{self.dotted}.username"
        return [t for t in sorted((self.chart / "templates").glob("*.yaml")) if needle in t.read_text()]


def chart_blocks():
    out = []

    def walk(node, path, chart, service):
        if isinstance(node, dict):
            if "username" in node and (path and path[-1] == "kafka" or "brokers" in node):
                out.append(Block(chart, service, tuple(path), node))
            for k, v in node.items():
                walk(v, path + [k], chart, service)
    for values_file in sorted(REPO.glob("services/*/charts/*/values.yaml")) + sorted(REPO.glob("tools/*/charts/*/values.yaml")):
        walk(yaml.safe_load(values_file.read_text()) or {}, [], values_file.parent, values_file.relative_to(REPO).parts[1])
    return out


ENV_ENTRY = re.compile(r"name:\s*(\w+)\s*,?\s*\n?\s*value:\s*\{\{(.*?)\}\}", re.S)


def lookup(values, dotted):
    node = values
    for key in dotted.split("."):
        if not isinstance(node, dict) or key not in node:
            return None
        node = node[key]
    return node


def template_env(block, values):
    """Env var -> the value the block's templates give it (lists joined with commas, empty dropped)."""
    out = {}
    for t in block.templates():
        for name, expr in ENV_ENTRY.findall(t.read_text()):
            ref = re.search(r"\.Values\.([\w.]+)", expr)
            if not ref:
                continue
            v = lookup(values, ref.group(1))
            if isinstance(v, list):
                v = ",".join(str(x) for x in v)
            if v not in (None, ""):
                out.setdefault(name, str(v))
    return out


def resolve_env(v, env):
    """A code value with its env vars replaced by what the chart gives the pod; None when it ends up unset."""
    if isinstance(v, Alt):
        first = resolve_env(v.first, env)
        return first if first not in (None, "", ()) else resolve_env(v.second, env)
    if isinstance(v, Env):
        raw = env.get(v.name)
        if raw is None:
            return None
        return tuple(s.strip() for s in raw.split(",") if s.strip()) if v.as_list else raw
    return v


def deep_merge(base, over):
    out = dict(base)
    for k, v in (over or {}).items():
        out[k] = deep_merge(out[k], v) if isinstance(v, dict) and isinstance(out.get(k), dict) else v
    return out


def set_path(values, dotted, value):
    node = values
    keys = dotted.split(".")
    for k in keys[:-1]:
        node = node.setdefault(k, {})
    node[keys[-1]] = value


def app_values(app, src):
    """The chart's values as the Application renders them (value files, valuesObject, parameters)."""
    values = yaml.safe_load((REPO / src["path"] / "values.yaml").read_text()) or {}
    helm = src.get("helm") or {}
    for vf in helm.get("valueFiles", []):
        f = REPO / vf.partition("/")[2]
        if f.is_file():
            values = deep_merge(values, yaml.safe_load(f.read_text()) or {})
    values = deep_merge(values, helm.get("valuesObject") or {})
    for p in helm.get("parameters", []):
        set_path(values, p["name"], p["value"])
    return values


def acls(user):
    return {(a["resource"]["type"], a["resource"]["name"], a["resource"].get("patternType", "literal")): set(a["operations"])
            for a in user["spec"]["authorization"]["acls"]}


# ---------------------------------------------------------------------------------------------------------------------

class KafkaConsumersTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.go, cls.go_dlqs, go_problems = go_consumers()
        ts, ts_problems = ts_consumers()
        py, py_problems = py_consumers()
        cls.discovery_problems = go_problems + ts_problems + py_problems
        cls.blocks = chart_blocks()
        cls.apps = argo.applications()
        cls.users, cls.topics_created = {}, set()
        cls.user_files = {}
        for _, _, files, _ in argo.directory_sources(cls.apps, "dev"):
            for f in files:
                for doc in argo.load_docs(f):
                    if doc.get("kind") == "KafkaUser":
                        cls.users[doc["metadata"]["name"]] = doc
                        cls.user_files[doc["metadata"]["name"]] = f
                    elif doc.get("kind") == "KafkaTopic":
                        cls.topics_created.add(doc["spec"]["topicName"])
        # The consumers of each block, with their env vars resolved through the chart defaults.
        cls.expected, cls.assign_problems = {}, []
        by_service = {}
        for b in cls.blocks:
            by_service.setdefault(b.service, []).append(b)
        for b in cls.blocks:
            cls.expected[b] = Consumer(b.service, b.dotted)
        values_of = {b: yaml.safe_load((b.chart / "values.yaml").read_text()) for b in cls.blocks}

        def block_for(service, group, where):
            blocks = by_service.get(service, [])
            if len(blocks) == 1:
                return blocks[0]
            hits = [b for b in blocks if any(b.data.get(k) == group or (isinstance(group, Prefix) and str(b.data.get(k, "")).startswith(group))
                                             for k in b.data if k == "group" or k.endswith("Group"))]
            if len(hits) == 1:
                return hits[0]
            cls.assign_problems.append(f"{where}: {service} has {len(blocks)} consumer blocks in its chart and none or several "
                                       f"name group {group}")
            return None
        for c in cls.go:
            b = block_for(c.service, next(iter(c.groups)), c.where)
            if b:
                e = cls.expected[b]
                e.groups |= c.groups
                e.topics |= c.topics
                e.pairs.append((next(iter(c.groups)), tuple(sorted(c.topics))))
        for d in cls.go_dlqs:
            blocks = by_service.get(d.service, [])
            owners = blocks if len(blocks) == 1 else [b for b in blocks if any(
                g == d.name or (isinstance(g, Prefix) and d.name.startswith(g.rstrip("-"))) for g in cls.expected[b].groups)]
            if len(owners) != 1:
                cls.assign_problems.append(f"{d.where}: dead-letter config {d.name} matches no single consumer block of {d.service}")
                continue
            env = template_env(owners[0], values_of[owners[0]])
            cls.expected[owners[0]].dlqs |= {v for v in (resolve_env(x, env) for x in d.dlqs) if v}
        for service, where, values in ts + py:
            b = block_for(service, None, where)
            if not b:
                continue
            env = template_env(b, values_of[b])
            e = cls.expected[b]
            group, topics = resolve_env(values["group"], env), resolve_env(values["topics"], env)
            dlq = resolve_env(values["dlqTopic"], env) if "dlqTopic" in values else None
            if not isinstance(group, str) or not isinstance(topics, tuple) or (dlq is not None and not isinstance(dlq, str)):
                cls.assign_problems.append(f"{where}: cannot work out group/topics/dlqTopic: {values}")
                continue
            e.groups.add(group)
            e.topics |= set(topics)
            e.pairs.append((group, tuple(sorted(topics))))
            if dlq:
                e.dlqs.add(dlq)

    def test_every_consumer_in_the_code_is_found_and_belongs_to_a_chart_block(self):
        self.assertEqual([], self.discovery_problems)
        self.assertEqual([], self.assign_problems)
        services = {c.service for c in self.go}
        # The scan must see the consumers we know of, or a refactor that hides them would pass silently.
        self.assertLessEqual({"audit", "finreport", "fulfillment", "pricing", "production", "purchasing", "shopfloor", "receiving",
                              "scheduling", "search"}, services)
        self.assertIn(("production", "production-shopfloor"), {(c.service, g) for c in self.go for g in c.groups})
        for b, e in self.expected.items():
            with self.subTest(block=f"{b.chart.relative_to(REPO)}:{b.dotted}"):
                self.assertTrue(e.groups and e.topics, "the chart wires a consumer the code does not build")

    def test_each_consumer_block_has_a_least_privilege_kafka_user(self):
        claimed = set()
        for b, e in self.expected.items():
            user = b.data["username"]
            claimed.add(user)
            with self.subTest(user=user):
                self.assertRegex(user, r"^[a-z0-9-]+-consumer$", "consumer KafkaUsers are named <service>-consumer")
                self.assertIn(user, self.users, f"no KafkaUser {user} in deploy/kafka synced in dev "
                                                f"({b.chart.relative_to(REPO)} {b.dotted}.username)")
                doc = self.users[user]
                self.assertEqual(self.user_files[user].name, f"{user}.yaml")
                self.assertEqual(doc["spec"]["authentication"], {"type": "scram-sha-512"}, "the cluster's TLS listener is SCRAM-SHA-512")
                self.assertEqual(doc["spec"]["authorization"]["type"], "simple")
                want = {("topic", t, "literal"): {"Read", "Describe"} for t in e.topics}
                want.update({("topic", t, "literal"): {"Write", "Describe"} for t in e.dlqs})
                want.update({("group", g, "prefix" if isinstance(g, Prefix) else "literal"): {"Read", "Describe"} for g in e.groups})
                self.assertEqual(want, acls(doc), f"{user}: ACLs must be exactly the topics, groups and dead-letter topics the code uses")
                for dlq in e.dlqs:
                    self.assertTrue(dlq.endswith(".dlq"), f"{user}: {dlq} is not named like a dead-letter topic")
                    self.assertIn(dlq, self.topics_created, f"{user}: no synced KafkaTopic creates {dlq} (auto.create.topics.enable is off)")
        orphans = sorted(u for u, f in self.user_files.items() if f.name.endswith("-consumer.yaml") and u not in claimed)
        self.assertEqual([], orphans, "consumer KafkaUsers no chart names")

    def test_chart_values_agree_with_the_code(self):
        for b, e in self.expected.items():
            with self.subTest(block=f"{b.chart.relative_to(REPO)}:{b.dotted}"):
                for k, v in b.data.items():
                    if (k == "group" or k.endswith("Group")) and v:
                        self.assertIn(v, e.groups, f"{b.dotted}.{k} names a group the code does not use")
                if b.data.get("dlqTopic"):
                    self.assertIn(b.data["dlqTopic"], e.dlqs, f"{b.dotted}.dlqTopic is not the code's dead-letter topic")

    def test_chart_templates_wire_the_kafka_user_credentials(self):
        for b in self.blocks:
            with self.subTest(block=f"{b.chart.relative_to(REPO)}:{b.dotted}"):
                templates = b.templates()
                self.assertTrue(templates, f"no template reads .Values.{b.dotted}.username")
                text = "\n".join(t.read_text() for t in templates)
                self.assertRegex(text, rf"KAFKA_USERNAME\W+value:\s*\{{\{{\s*\.Values\.{re.escape(b.dotted)}\.username")
                self.assertRegex(text, rf"KAFKA_PASSWORD_FILE\W+value:\s*{CREDENTIALS_DIR}/password")
                self.assertRegex(text, rf"KAFKA_TLS_CA_FILE\W+value:\s*{CREDENTIALS_DIR}/ca\.crt")
                self.assertRegex(text, rf"secretName:\s*\{{\{{[^}}]*\.Values\.{re.escape(b.dotted)}\.secret\b")
                self.assertRegex(text, rf"mountPath:\s*{CREDENTIALS_DIR}\b")
                self.assertNotRegex(text, r"name:\s*KAFKA_PASSWORD\s*\n", "the password is mounted as a file, not put in the env")

    # -------------------------------------------------------------------------------------------------------------- dev

    def dev_blocks(self):
        """(block, Application, chart source, dev values) for every block whose chart has a dev Application."""
        out = []
        for env, _, app in self.apps:
            if env != "dev":
                continue
            for src in argo.service_chart_sources(app):
                for b in self.blocks:
                    if (REPO / src["path"]).resolve() == b.chart.resolve():
                        out.append((b, app, src, app_values(app, src)))
        return out

    @staticmethod
    def enabled(b, values):
        owner, node = b.owner(values), b.get(values)
        return bool(owner.get("enabled", True)) and bool(node.get("brokers") or owner.get("brokers"))

    def test_dev_turns_on_exactly_the_consumers_it_can_run(self):
        rows = self.dev_blocks()
        self.assertEqual({b.data["username"] for b in self.blocks}, {b.data["username"] for b, *_ in rows},
                         "every consumer chart has a dev Application")
        on = {b.data["username"] for b, _, _, v in rows if self.enabled(b, v)}
        self.assertEqual(set(), on & set(DEV_OFF), "on in dev but listed in DEV_OFF")
        self.assertEqual(set(), {b.data["username"] for b in self.blocks} - on - set(DEV_OFF),
                         "off in dev without a reason in DEV_OFF")
        self.assertEqual(set(), set(DEV_OFF) - {b.data["username"] for b in self.blocks}, "stale DEV_OFF entries")
        self.assertTrue(on, "D34: dev runs at least the consumers whose producers are relayed and whose needs dev meets")

    def test_dev_consumers_that_are_on_can_start(self):
        docs = [(app, d) for app, _, files, _ in argo.directory_sources(self.apps, "dev") for f in files for d in argo.load_docs(f)]
        written = {}
        for app, d in docs:
            if d["kind"] == "ExternalSecret":
                ns = d["metadata"].get("namespace") or app["spec"]["destination"]["namespace"]
                written[(ns, (d["spec"].get("target") or {}).get("name") or d["metadata"]["name"])] = d
        stores = {(d["metadata"]["namespace"], d["metadata"]["name"]): d for _, d in docs if d["kind"] == "SecretStore"}
        roles = {(d["metadata"]["namespace"], d["metadata"]["name"]): d for _, d in docs if d["kind"] == "Role"}
        bindings_ = [d for _, d in docs if d["kind"] == "RoleBinding"]
        connect = argo.load_docs(DEPLOY / "kafka/connect.yaml")
        (debezium,) = [d for d in connect if d["kind"] == "KafkaUser" and d["metadata"]["name"] == "debezium"]
        relay = [a["resource"] for a in debezium["spec"]["authorization"]["acls"]
                 if a["resource"]["type"] == "topic" and "Write" in a["operations"]]

        def relayed(topic):
            return any(topic == r["name"] if r.get("patternType", "literal") == "literal" else topic.startswith(r["name"]) for r in relay)
        readers = set()
        for b, app, src, values in self.dev_blocks():
            if not self.enabled(b, values):
                continue
            user, node, owner = b.data["username"], b.get(values), b.owner(values)
            ns = app["spec"]["destination"]["namespace"]
            with self.subTest(user=user):
                e = self.expected[b]
                self.assertEqual([], sorted(t for t in e.topics if not relayed(t)), "topics no outbox connector relays")
                self.assertEqual(DEV_BROKERS, node.get("brokers") or owner.get("brokers"), "the SCRAM-SHA-512 TLS listener")
                self.assertEqual(user, node["username"])
                for k, v in node.items():
                    if (k == "group" or k.endswith("Group")) and v:
                        self.assertIn(v, e.groups, f"dev {b.dotted}.{k}")
                if node.get("dlqTopic"):
                    self.assertIn(node["dlqTopic"], e.dlqs, f"dev {b.dotted}.dlqTopic")
                if node.get("topics") or owner.get("topics"):
                    topics = node.get("topics") or owner.get("topics")
                    topics = set(topics.split(",") if isinstance(topics, str) else topics)
                    self.assertLessEqual(topics, e.topics, "dev topics the KafkaUser does not grant")
                secret = node.get("secret")
                es = written.get((ns, secret))
                self.assertIsNotNone(es, f"no ExternalSecret synced in dev writes {ns}/{secret}")
                data = {i["secretKey"]: i["remoteRef"] for i in es["spec"]["data"]}
                self.assertEqual({"password": {"key": user, "property": "password"},
                                  "ca.crt": {"key": CLUSTER_CA_SECRET, "property": "ca.crt"}}, data,
                                 "the Strimzi user Secret's password and the cluster CA")
                ref = es["spec"]["secretStoreRef"]
                store = stores.get((ns, ref["name"]))
                self.assertEqual("SecretStore", ref.get("kind"))
                self.assertIsNotNone(store, f"no SecretStore {ns}/{ref['name']}")
                k8s = store["spec"]["provider"]["kubernetes"]
                self.assertEqual("kafka", k8s["remoteNamespace"])
                sa = k8s["auth"]["serviceAccount"]["name"]
                granted = set()
                for rb in bindings_:
                    if any(s.get("kind") == "ServiceAccount" and s["name"] == sa and s.get("namespace") == ns for s in rb["subjects"]):
                        role = roles[(rb["metadata"]["namespace"], rb["roleRef"]["name"])]
                        self.assertEqual("kafka", rb["metadata"]["namespace"])
                        granted |= {n for rule in role["rules"] if rule["verbs"] == ["get"] for n in rule.get("resourceNames", [])}
                        self.assertTrue(all(r["resources"] == ["secrets"] and r["verbs"] == ["get"] for r in role["rules"]))
                self.assertLessEqual({user, CLUSTER_CA_SECRET}, granted, f"the kafka-users store may not read {user}")
                readers |= granted
        on = {b.data["username"] for b, _, _, v in self.dev_blocks() if self.enabled(b, v)}
        self.assertEqual(on | {CLUSTER_CA_SECRET}, readers, "the store reads exactly the users of the consumers that are on")

    @unittest.skipUnless(shutil.which("helm"), "helm not on PATH")
    def test_dev_consumers_that_are_on_render_with_secrets_dev_provides(self):
        docs = [(app, d) for app, _, files, _ in argo.directory_sources(self.apps, "dev") for f in files for d in argo.load_docs(f)]
        written = set()
        for app, d in docs:
            if d["kind"] == "ExternalSecret":
                written.add(argo.es_output(d)[:2] if d["metadata"].get("namespace") else
                            (app["spec"]["destination"]["namespace"], argo.es_output(d)[1]))

        def provided(ns, name):
            return ((ns, name) in written or (ns, name) in argo.UNMANAGED_SECRETS or (ns, name) in DEV_EXTERNAL_SECRETS
                    or any(e == "dev" and fnmatch.fnmatchcase(name, p) for e, p in test_migrate_jobs.EXTERNALLY_PROVISIONED))
        for b, app, src, values in self.dev_blocks():
            if not self.enabled(b, values):
                continue
            user, ns = b.data["username"], app["spec"]["destination"]["namespace"]
            with self.subTest(user=user):
                pods = [d for d in argo.render(app, src) if argo.pod_containers(d)]
                hits = [(d, c) for d in pods for c in argo.pod_containers(d)
                        if {"name": "KAFKA_USERNAME", "value": user} in (c.get("env") or [])]
                self.assertEqual(1, len(hits), f"one rendered container runs as {user}")
                workload, container = hits[0]
                pod = workload["spec"]["template"]["spec"]
                env = {x["name"]: x for x in container.get("env") or []}
                self.assertEqual(f"{CREDENTIALS_DIR}/password", env["KAFKA_PASSWORD_FILE"]["value"])
                self.assertEqual(f"{CREDENTIALS_DIR}/ca.crt", env["KAFKA_TLS_CA_FILE"]["value"])
                self.assertEqual(DEV_BROKERS, env["KAFKA_BROKERS"]["value"])
                self.assertTrue(pod.get("securityContext", {}).get("fsGroup"), "fsGroup: the credentials are mounted 0440")
                needed = {x["valueFrom"]["secretKeyRef"]["name"] for c in argo.pod_containers(workload)
                          for x in c.get("env") or [] if "secretKeyRef" in (x.get("valueFrom") or {})}
                needed |= {v["secret"]["secretName"] for v in pod.get("volumes") or [] if "secret" in v}
                self.assertEqual([], sorted(n for n in needed if not provided(ns, n)),
                                 f"{workload['metadata']['name']}: Secrets nothing in dev provides (the pod would not start)")


if __name__ == "__main__":
    unittest.main()
