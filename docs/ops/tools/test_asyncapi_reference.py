"""Tests for asyncapi_reference.py. Run: python3 -m unittest discover -s docs/ops/tools"""

import copy
import io
import json
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

import asyncapi_reference as ar


def good_doc() -> dict:
    """A minimal AsyncAPI 3 document in the shape services/org/events/asyncapi.yaml uses."""
    return {
        "asyncapi": "3.0.0",
        "info": {"title": "org", "version": "0.12.0"},
        "channels": {
            "depot.org.site": {
                "address": "depot.org.site",
                "messages": {"created": {"$ref": "#/components/messages/siteCreated"}},
            }
        },
        "operations": {
            "onSiteCreated": {
                "action": "send",
                "channel": {"$ref": "#/channels/depot.org.site"},
                "messages": [{"$ref": "#/channels/depot.org.site/messages/created"}],
            }
        },
        "components": {
            "messages": {
                "siteCreated": {
                    "name": "created",
                    "title": "Site created",
                    "payload": {
                        "type": "object",
                        "required": ["id", "type"],
                        "properties": {
                            "id": {"type": "string", "format": "uuid"},
                            "type": {"type": ["string", "null"]},
                        },
                    },
                    "x-depot-type": "dev.depot.org.site.created.v1",
                }
            }
        },
    }


def named_doc() -> dict:
    """The other repository shape (eam, sales, receivables): the message name is the CloudEvents type and the
    payload is a $ref to components.schemas."""
    doc = good_doc()
    msg = doc["components"]["messages"]["siteCreated"]
    del msg["x-depot-type"]
    msg["name"] = "dev.depot.org.site.created.v1"
    doc["components"]["schemas"] = {"Site": msg["payload"]}
    msg["payload"] = {"$ref": "#/components/schemas/Site"}
    return doc


class TestEventType(unittest.TestCase):
    def test_valid_types(self):
        for t in ("dev.depot.org.site.created.v1", "dev.depot.ops.license.state_changed.v12"):
            self.assertTrue(ar.event_type_valid(t), t)

    def test_invalid_types(self):
        for t in (
            None,
            "",
            "org.site.created.v1",
            "dev.depot.org.site.created",
            "dev.depot.org.site.created.v0",
            "dev.depot.Org.site.created.v1",
            "dev.depot.org.site.created.v1.extra",
        ):
            self.assertFalse(ar.event_type_valid(t), repr(t))

    def test_x_depot_type_wins_over_name(self):
        self.assertEqual(
            ar.event_type({"name": "dev.depot.a.b.c.v1", "x-depot-type": "dev.depot.x.y.z.v2"}),
            "dev.depot.x.y.z.v2",
        )

    def test_name_is_the_type_when_no_extension(self):
        self.assertEqual(ar.event_type({"name": "dev.depot.a.b.c.v1"}), "dev.depot.a.b.c.v1")

    def test_blank_extension_is_not_replaced_by_name(self):
        self.assertEqual(ar.event_type({"name": "dev.depot.a.b.c.v1", "x-depot-type": ""}), "")


class TestGaps(unittest.TestCase):
    def test_reference_documents_have_no_gaps(self):
        self.assertEqual(ar.gaps(good_doc()), [])
        self.assertEqual(ar.gaps(named_doc()), [])

    def test_channel_without_address(self):
        doc = good_doc()
        del doc["channels"]["depot.org.site"]["address"]
        self.assertIn("channel depot.org.site: missing address", ar.gaps(doc))

    def test_channel_message_must_be_an_object(self):
        doc = good_doc()
        doc["channels"]["depot.org.site"]["messages"]["gone"] = "not-a-ref"
        self.assertIn("channel depot.org.site: message gone is not an object", ar.gaps(doc))

    def test_message_without_type_description_or_payload(self):
        doc = good_doc()
        msg = doc["components"]["messages"]["siteCreated"]
        del msg["x-depot-type"]
        msg["name"] = " "
        del msg["title"]
        msg["payload"] = {}
        self.assertEqual(
            ar.gaps(doc),
            [
                "message siteCreated: no event type (set x-depot-type, or name the message after its type)",
                "message siteCreated: missing title, summary or description",
                "message siteCreated: missing payload schema",
                "operation onSiteCreated: missing title, summary or description (and its messages are not all described)",
            ],
        )

    def test_name_that_is_not_a_type_is_reported(self):
        doc = named_doc()
        doc["components"]["messages"]["siteCreated"]["name"] = "POCancelled"
        self.assertIn(
            "message siteCreated: event type 'POCancelled' is not dev.depot.<context>.<entity>.<verb>.v<n>",
            ar.gaps(doc),
        )

    def test_summary_or_description_is_accepted_in_place_of_title(self):
        for key in ("summary", "description"):
            doc = good_doc()
            msg = doc["components"]["messages"]["siteCreated"]
            del msg["title"]
            msg[key] = "Site created"
            self.assertEqual(ar.gaps(doc), [], key)

    def test_payload_reference_to_empty_schema_is_a_gap(self):
        doc = named_doc()
        doc["components"]["schemas"]["Site"] = {}
        self.assertIn("message siteCreated: missing payload schema", ar.gaps(doc))

    def test_bad_extension_type_is_reported(self):
        doc = good_doc()
        doc["components"]["messages"]["siteCreated"]["x-depot-type"] = "site.created"
        problems = ar.gaps(doc)
        self.assertIn(
            "message siteCreated: event type 'site.created' is not dev.depot.<context>.<entity>.<verb>.v<n>", problems
        )

    def test_duplicate_event_type_is_reported(self):
        doc = good_doc()
        doc["components"]["messages"]["siteUpdated"] = copy.deepcopy(doc["components"]["messages"]["siteCreated"])
        self.assertIn(
            "message siteUpdated: event type dev.depot.org.site.created.v1 also declared by siteCreated",
            ar.gaps(doc),
        )

    def test_inline_channel_message_is_checked(self):
        doc = good_doc()
        doc["channels"]["depot.org.site"]["messages"]["deleted"] = {"name": "deleted", "payload": {"type": "object"}}
        problems = ar.gaps(doc)
        self.assertIn(
            "message depot.org.site/deleted: event type 'deleted' is not dev.depot.<context>.<entity>.<verb>.v<n>",
            problems,
        )
        self.assertIn("message depot.org.site/deleted: missing title, summary or description", problems)

    def test_operation_action(self):
        doc = good_doc()
        doc["operations"]["onSiteCreated"]["action"] = "publish"
        self.assertEqual(ar.gaps(doc), ["operation onSiteCreated: action 'publish' is not send or receive"])

    def test_operation_described_by_its_own_summary_when_message_is_not(self):
        doc = good_doc()
        msg = doc["components"]["messages"]["siteCreated"]
        del msg["title"]
        doc["operations"]["onSiteCreated"]["summary"] = "Publishes site creations"
        self.assertEqual(ar.gaps(doc), ["message siteCreated: missing title, summary or description"])

    def test_operation_message_must_belong_to_its_channel(self):
        doc = good_doc()
        doc["channels"]["depot.org.other"] = copy.deepcopy(doc["channels"]["depot.org.site"])
        doc["channels"]["depot.org.other"]["address"] = "depot.org.other"
        doc["operations"]["onSiteCreated"]["channel"] = {"$ref": "#/channels/depot.org.other"}
        self.assertEqual(
            ar.gaps(doc),
            [
                "operation onSiteCreated: message #/channels/depot.org.site/messages/created is not one of "
                "channel #/channels/depot.org.other's messages"
            ],
        )

    def test_operation_channel_must_be_a_reference(self):
        doc = good_doc()
        doc["operations"]["onSiteCreated"]["channel"] = {"address": "depot.org.site"}
        self.assertIn("operation onSiteCreated: channel must be a $ref to a channel", ar.gaps(doc))

    def test_unresolved_channel_reference_is_an_input_error(self):
        doc = good_doc()
        doc["operations"]["onSiteCreated"]["channel"] = {"$ref": "#/channels/nope"}
        with self.assertRaises(ValueError):
            ar.gaps(doc)

    def test_unresolved_message_reference_is_an_input_error(self):
        doc = good_doc()
        doc["operations"]["onSiteCreated"]["messages"] = [{"$ref": "#/channels/depot.org.site/messages/missing"}]
        with self.assertRaises(ValueError):
            ar.gaps(doc)

    def test_external_payload_reference_is_an_input_error(self):
        doc = named_doc()
        doc["components"]["messages"]["siteCreated"]["payload"] = {"$ref": "../schemas/site.json"}
        with self.assertRaises(ValueError):
            ar.gaps(doc)


class TestRender(unittest.TestCase):
    def test_tables_and_sections(self):
        text = ar.render("org", good_doc())
        self.assertIn("# org event reference", text)
        self.assertIn("AsyncAPI 3.0.0, API version 0.12.0", text)
        self.assertIn("1 channels, 1 operations, 1 events.", text)
        self.assertIn("| onSiteCreated | send | depot.org.site | Site created |", text)
        self.assertIn("| dev.depot.org.site.created.v1 | Site created | `id`, `type` |", text)
        self.assertIn("## dev.depot.org.site.created.v1", text)
        self.assertIn("- `id` (string)", text)
        self.assertIn("- `type` (string|null)", text)

    def test_referenced_payload_is_followed(self):
        text = ar.render("org", named_doc())
        self.assertIn("| dev.depot.org.site.created.v1 | Site created | `id`, `type` |", text)
        self.assertIn("- `id` (string)", text)

    def test_operation_summary_and_pipe_escaping(self):
        doc = good_doc()
        doc["operations"]["onSiteCreated"]["summary"] = "a | b"
        self.assertIn("| onSiteCreated | send | depot.org.site | a \\| b |", ar.render("org", doc))

    def test_render_is_deterministic(self):
        self.assertEqual(ar.render("org", good_doc()), ar.render("org", good_doc()))


class TestServiceName(unittest.TestCase):
    def test_service_events_path(self):
        self.assertEqual(ar.service_name(Path("services/org/events/asyncapi.json")), "org")

    def test_other_file_uses_stem(self):
        self.assertEqual(ar.service_name(Path("build/org-events.json")), "org-events")


class TestMain(unittest.TestCase):
    def _write(self, path: Path, doc: dict) -> Path:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(doc), encoding="utf-8")
        return path

    def _run(self, argv: list[str]) -> tuple[int, str]:
        buf = io.StringIO()
        with redirect_stdout(buf):
            code = ar.main(argv)
        return code, buf.getvalue()

    def test_passes_and_writes_one_page_per_service(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            org = self._write(root / "services/org/events/asyncapi.json", good_doc())
            eam = self._write(root / "services/eam/events/asyncapi.json", named_doc())
            out = root / "events"
            code, text = self._run(["--spec", str(org), "--spec", str(eam), "--out", str(out)])
            self.assertEqual(code, 0)
            self.assertIn("EVENT REFERENCE PASSED: 2 events, 0 undocumented", text)
            self.assertEqual(sorted(p.name for p in out.iterdir()), ["eam.md", "org.md"])

    def test_fails_on_undocumented_event_and_counts_events(self):
        with tempfile.TemporaryDirectory() as tmp:
            doc = good_doc()
            msg = doc["components"]["messages"]["siteCreated"]
            del msg["x-depot-type"]
            del msg["title"]
            spec = self._write(Path(tmp) / "services/org/events/asyncapi.json", doc)
            code, text = self._run(["--spec", str(spec)])
            self.assertEqual(code, 1)
            self.assertIn("FAIL  org: message siteCreated: missing title, summary or description", text)
            self.assertIn("EVENT REFERENCE FAILED: 1 of 1 events undocumented (3 gap(s))", text)

    def test_two_specs_with_the_same_service_name_is_usage_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            a = self._write(root / "a/services/org/events/asyncapi.json", good_doc())
            b = self._write(root / "b/services/org/events/asyncapi.json", good_doc())
            self.assertEqual(self._run(["--spec", str(a), "--spec", str(b)])[0], 2)

    def test_yaml_input_is_usage_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            spec = Path(tmp) / "asyncapi.yaml"
            spec.write_text("asyncapi: 3.0.0\n", encoding="utf-8")
            self.assertEqual(self._run(["--spec", str(spec)])[0], 2)

    def test_openapi_or_asyncapi_2_document_is_usage_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            for doc in ({"openapi": "3.1.0", "paths": {}}, {"asyncapi": "2.6.0", "channels": {}}):
                spec = self._write(Path(tmp) / "spec.json", doc)
                self.assertEqual(self._run(["--spec", str(spec)])[0], 2, doc)

    def test_unresolvable_reference_is_usage_error(self):
        with tempfile.TemporaryDirectory() as tmp:
            doc = good_doc()
            doc["operations"]["onSiteCreated"]["channel"] = {"$ref": "#/channels/nope"}
            spec = self._write(Path(tmp) / "broken.json", doc)
            self.assertEqual(self._run(["--spec", str(spec)])[0], 2)


if __name__ == "__main__":
    unittest.main()
