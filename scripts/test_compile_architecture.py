"""Tests for compile_architecture.py. Run: python3 -m unittest discover -s scripts -p 'test_compile_architecture.py'"""

import contextlib
import io
import json
import shutil
import tempfile
import textwrap
import unittest
from pathlib import Path

import compile_architecture as ca

OPENAPI = """\
openapi: 3.1.0
info: {title: Alpha API, version: 1.0.0}
paths:
  /v1/alpha/things/{id}:
    parameters:
      - {name: id, in: path, required: true}
    delete:
      operationId: deleteThing
      x-depot-duty: thing.delete
      responses: {"204": {description: gone}}
    get:
      operationId: getThing
      x-depot-duty: thing.read
      responses: {"200": {description: ok}}
  /v1/alpha/things:
    post:
      operationId: createThing
      x-depot-duty: thing.write
      x-depot-duty-alternatives: [thing.admin]
      responses: {"201": {description: created}}
  /healthz:
    get:
      operationId: healthz
      x-depot-public: true
      responses: {"200": {description: ok}}
"""

ASYNCAPI = """\
asyncapi: 3.0.0
info: {title: Alpha events, version: 1.0.0}
channels:
  thingCreated:
    address: depot.alpha.thing
    messages:
      thingCreated: {$ref: "#/components/messages/ThingCreated"}
  thingDeleted:
    address: depot.alpha.thing
    messages:
      thingDeleted:
        name: dev.depot.alpha.thing.deleted.v1
        payload: {type: object}
  betaOrder:
    address: depot.beta.order
    messages:
      orderPlaced: {$ref: "#/components/messages/OrderPlaced"}
operations:
  publishThingDeleted:
    action: send
    channel: {$ref: "#/channels/thingDeleted"}
  publishThingCreated:
    action: send
    channel: {$ref: "#/channels/thingCreated"}
    messages:
      - $ref: "#/channels/thingCreated/messages/thingCreated"
  receiveOrderPlaced:
    action: receive
    channel: {$ref: "#/channels/betaOrder"}
components:
  messages:
    ThingCreated:
      name: ThingCreated
      x-depot-type: dev.depot.alpha.thing.created.v1
      payload: {type: object}
    OrderPlaced:
      name: dev.depot.beta.order.placed.v1
      payload: {type: object}
"""

COMPONENTS = {
    "components": [
        {
            "name": "Alpha", "slug": "alpha", "status": "as-built", "keywords": ["alpha"], "aliases": [],
            "jira": {"epics": [], "labels": []}, "depends_on": [], "services": ["services/alpha", "apps/web"],
        },
        {
            "name": "Beta", "slug": "beta", "status": "not-built", "keywords": [], "aliases": ["b"],
            "jira": {"epics": [], "labels": []}, "depends_on": ["alpha"], "services": ["services/beta"],
        },
    ]
}

PATTERNS = {
    "patterns": [
        {"id": "p1", "kind": "other", "severity": "major", "mustContain": ["x"], "mustNotContain": [], "mustMatch": [],
         "source": "components/alpha.md#Interfaces", "asBuilt": {"verdict": "match", "refs": [], "note": ""}},
        {"id": "p2", "kind": "other", "severity": "minor", "mustContain": [], "mustNotContain": [], "mustMatch": [],
         "source": "spine.md#Naming", "asBuilt": {"verdict": "drift", "refs": ["D1"], "note": "n"}},
    ]
}


def sheet(name, cites):
    body = "\n\n".join(f"## {h}\ntext" for h in ca.REQUIRED_HEADINGS)
    return f"# {name}\n\nOwns {cites}.\n\n{body}\n"


class Repo:
    """A minimal repository laid out like Depot."""

    def __init__(self):
        self.root = Path(tempfile.mkdtemp(prefix="compile-arch-"))
        self.put("docs/architecture.md", "# Architecture\n\nline two\n")
        self.put("services/alpha/api/openapi.yaml", OPENAPI)
        self.put("services/alpha/events/asyncapi.yaml", ASYNCAPI)
        self.put("services/beta/README.md", "beta\n")
        self.put("apps/web/README.md", "web\n")
        self.put("services/README.md", "not a service directory\n")
        self.put(ca.COMPONENTS_INPUT, json.dumps(COMPONENTS))
        self.put(ca.PATTERNS_INPUT, json.dumps(PATTERNS))
        out = ca.OUT_DIR
        self.put(f"{out}/spine.md", "# Spine\n\n## Naming\nSee `services/alpha/api/openapi.yaml:3` and `services/<name>-ts`.\n")
        self.put(f"{out}/components/alpha.md", sheet("Alpha", "`services/alpha` and `apps/web`"))
        self.put(f"{out}/components/beta.md", sheet("Beta", "`services/beta`"))

    def put(self, rel, text):
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def read(self, rel):
        return (self.root / rel).read_text(encoding="utf-8")

    def run(self, *args):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = ca.main([*args, "--root", str(self.root)])
        return code, out.getvalue() + err.getvalue()

    def cleanup(self):
        shutil.rmtree(self.root, ignore_errors=True)


class CompileArchitectureTest(unittest.TestCase):
    def setUp(self):
        self.repo = Repo()
        self.addCleanup(self.repo.cleanup)
        code, out = self.repo.run("--write", "--created", "2026-11-17")
        self.assertEqual(code, 0, out)

    def check(self):
        return self.repo.run("--check")

    def assertDrift(self, fragment):
        code, out = self.check()
        self.assertEqual(code, 1, out)
        self.assertIn(fragment, out)

    def test_fresh_compile_passes_check(self):
        code, out = self.check()
        self.assertEqual(code, 0, out)
        self.assertIn("architecture.compiled is up to date", out)
        self.assertIn("4 operations in 1 OpenAPI documents", out)

    def test_manifest_records_repo_relative_source_and_owned_services(self):
        m = json.loads(self.repo.read(f"{ca.OUT_DIR}/manifest.json"))
        self.assertEqual(m["version"], 3)
        self.assertEqual(m["source"]["path"], "docs/architecture.md")
        self.assertEqual(m["source"]["lines"], 3)
        self.assertEqual(len(m["source"]["sha256"]), 64)
        self.assertEqual(m["compiler"], "scripts/compile_architecture.py")
        self.assertEqual(m["created"], "2026-11-17")
        self.assertEqual(m["components"][0]["file"], "components/alpha.md")
        self.assertEqual(m["components"][0]["services"], ["services/alpha", "apps/web"])
        self.assertEqual(m["components"][1]["status"], "not-built")

    def test_crlf_checkout_hashes_like_lf(self):
        lf = ca.source_facts(self.repo.root)
        (self.repo.root / "docs/architecture.md").write_bytes(b"# Architecture\r\n\r\nline two\r\n")
        self.assertEqual(ca.source_facts(self.repo.root), lf)

    def test_write_is_deterministic_and_keeps_created(self):
        before = self.repo.read(f"{ca.OUT_DIR}/contracts.json"), self.repo.read(f"{ca.OUT_DIR}/manifest.json")
        code, out = self.repo.run("--write")
        self.assertEqual(code, 0, out)
        after = self.repo.read(f"{ca.OUT_DIR}/contracts.json"), self.repo.read(f"{ca.OUT_DIR}/manifest.json")
        self.assertEqual(before, after)

    def test_created_date_does_not_fail_check(self):
        path = f"{ca.OUT_DIR}/manifest.json"
        self.repo.put(path, self.repo.read(path).replace("2026-11-17", "2030-01-01"))
        code, out = self.check()
        self.assertEqual(code, 0, out)

    def test_edited_architecture_fails_until_rewritten(self):
        self.repo.put("docs/architecture.md", "# Architecture\n\nline two, reviewed\n")
        self.assertDrift("manifest: source is sha256")
        self.assertEqual(self.repo.run("--write")[0], 0)
        self.assertEqual(self.check()[0], 0)

    def test_stale_absolute_source_path_fails(self):
        path = f"{ca.OUT_DIR}/manifest.json"
        self.repo.put(path, self.repo.read(path).replace('"docs/architecture.md"', '"/Users/x/docs/architecture.md"'))
        self.assertDrift("source.path is '/Users/x/docs/architecture.md'")

    def test_component_input_change_fails_until_rewritten(self):
        comps = json.loads(json.dumps(COMPONENTS))
        comps["components"][0]["keywords"].append("new")
        self.repo.put(ca.COMPONENTS_INPUT, json.dumps(comps))
        self.assertDrift("manifest: components differ")

    def test_openapi_operations_are_ordered_with_duties(self):
        c = json.loads(self.repo.read(f"{ca.OUT_DIR}/contracts.json"))
        (entry,) = c["openapi"]
        self.assertEqual((entry["service"], entry["component"], entry["openapi"]), ("services/alpha", "alpha", "3.1.0"))
        rows = [(o["method"], o["path"], o["operationId"], o["duty"]) for o in entry["operations"]]
        self.assertEqual(rows, [
            ("GET", "/healthz", "healthz", None),
            ("POST", "/v1/alpha/things", "createThing", "thing.write"),
            ("GET", "/v1/alpha/things/{id}", "getThing", "thing.read"),
            ("DELETE", "/v1/alpha/things/{id}", "deleteThing", "thing.delete"),
        ])
        self.assertTrue(entry["operations"][0]["public"])
        self.assertEqual(entry["operations"][1]["dutyAlternatives"], ["thing.admin"])
        self.assertEqual(c["summary"]["paths"], 3)
        self.assertEqual(c["summary"]["operations"], 4)

    def test_asyncapi_topics_merge_channels_and_resolve_types(self):
        c = json.loads(self.repo.read(f"{ca.OUT_DIR}/contracts.json"))
        (entry,) = c["asyncapi"]
        self.assertEqual(entry["publishes"], [
            {"topic": "depot.alpha.thing",
             "types": ["dev.depot.alpha.thing.created.v1", "dev.depot.alpha.thing.deleted.v1"]},
        ])
        self.assertEqual(entry["subscribes"], [{"topic": "depot.beta.order", "types": ["dev.depot.beta.order.placed.v1"]}])
        self.assertEqual(c["topics"], [
            {"topic": "depot.alpha.thing", "publishers": ["services/alpha"], "subscribers": []},
            {"topic": "depot.beta.order", "publishers": [], "subscribers": ["services/alpha"]},
        ])
        self.assertEqual((c["summary"]["topics"], c["summary"]["topicsPublished"], c["summary"]["topicsSubscribed"]), (2, 1, 1))
        self.assertEqual([p["id"] for p in c["patterns"]], ["p1", "p2"])

    def test_one_operation_per_line(self):
        text = self.repo.read(f"{ca.OUT_DIR}/contracts.json")
        self.assertIn('{"method": "GET", "path": "/v1/alpha/things/{id}", "operationId": "getThing", "duty": "thing.read"}', text)

    def test_new_operation_in_a_spec_fails_check(self):
        spec = "services/alpha/api/openapi.yaml"
        self.repo.put(spec, self.repo.read(spec).replace("  /healthz:", "  /v1/alpha/extra:\n    get: {operationId: extra}\n  /healthz:"))
        self.assertDrift("contracts: docs/architecture.compiled/contracts.json differs from the specs")

    def test_hand_edited_contracts_fail_check(self):
        path = f"{ca.OUT_DIR}/contracts.json"
        self.repo.put(path, self.repo.read(path).replace("thing.read", "thing.anything"))
        self.assertDrift("first difference at line")

    def test_unowned_service_directory_fails(self):
        self.repo.put("services/gamma/README.md", "gamma\n")
        self.assertDrift("ownership: services/gamma is owned by no component")

    def test_service_owned_twice_fails(self):
        comps = json.loads(json.dumps(COMPONENTS))
        comps["components"][1]["services"].append("services/alpha")
        self.repo.put(ca.COMPONENTS_INPUT, json.dumps(comps))
        self.repo.run("--write")
        self.assertDrift("ownership: services/alpha is owned by more than one component (alpha, beta)")

    def test_owned_directory_that_does_not_exist_fails(self):
        comps = json.loads(json.dumps(COMPONENTS))
        comps["components"][1]["services"].append("services/ghost")
        self.repo.put(ca.COMPONENTS_INPUT, json.dumps(comps))
        self.repo.run("--write")
        self.assertDrift("ownership: beta lists services/ghost, which is not a directory")

    def test_missing_cited_path_fails_and_placeholders_are_skipped(self):
        self.repo.put(f"{ca.OUT_DIR}/spine.md",
                      "# Spine\n\n## Naming\n`services/alpha/missing.go:12-20` `services/<x>` `/v1/alpha` `apps/*/README.md`\n")
        code, out = self.check()
        self.assertEqual(code, 1, out)
        self.assertIn("spine.md cites `services/alpha/missing.go`, which does not exist", out)
        self.assertEqual(out.count("DRIFT"), 1, out)

    def test_cited_path_extraction(self):
        roots = {"services", "apps", "docs"}
        text = "`services/a/b.go:1,4` `docs/x.md#anchor` `deploy/y` `services/{a,b}` `services/a ...` `apps/web`"
        self.assertEqual(ca.cited_paths(text, roots), ["services/a/b.go", "docs/x.md", "apps/web"])

    def test_missing_component_file_and_orphan_sheet_fail(self):
        (self.repo.root / ca.OUT_DIR / "components/beta.md").rename(self.repo.root / ca.OUT_DIR / "components/old.md")
        code, out = self.check()
        self.assertEqual(code, 1, out)
        self.assertIn("components/beta.md is missing (component beta)", out)
        self.assertIn("components/old.md belongs to no manifest component", out)

    def test_component_sheet_needs_title_headings_and_owned_services(self):
        self.repo.put(f"{ca.OUT_DIR}/components/beta.md", "# Bravo\n\n## Interfaces\nnothing\n")
        code, out = self.check()
        self.assertEqual(code, 1, out)
        self.assertIn("must start with '# Beta'", out)
        self.assertIn("lacks heading(s): ## Purpose & Boundaries", out)
        self.assertIn("does not cite `services/beta`", out)

    def test_pattern_source_must_resolve(self):
        pats = json.loads(json.dumps(PATTERNS))
        pats["patterns"][0]["source"] = "components/alpha.md#Nope"
        self.repo.put(ca.PATTERNS_INPUT, json.dumps(pats))
        self.repo.run("--write")
        self.assertDrift("patterns: p1: source 'components/alpha.md#Nope': no '## Nope' heading")

    def test_input_errors_exit_2(self):
        self.repo.put(ca.COMPONENTS_INPUT, json.dumps({"components": [{"name": "x"}]}))
        code, out = self.check()
        self.assertEqual(code, 2, out)
        self.assertIn("lacks slug", out)

    def test_unknown_dependency_is_an_input_error(self):
        comps = json.loads(json.dumps(COMPONENTS))
        comps["components"][1]["depends_on"] = ["nobody"]
        self.repo.put(ca.COMPONENTS_INPUT, json.dumps(comps))
        code, out = self.check()
        self.assertEqual(code, 2, out)
        self.assertIn("depends_on names unknown nobody", out)

    def test_render_inlines_leaf_objects_only(self):
        text = ca.render({"a": [{"x": 1, "y": ["p", "q"]}], "b": {}})
        self.assertEqual(text, textwrap.dedent("""\
            {
              "a": [
                {"x": 1, "y": ["p", "q"]}
              ],
              "b": {}
            }
            """))


if __name__ == "__main__":
    unittest.main()
