"""The source tools the agents' risk facts read are annotated in the owning services' OpenAPI files (#47).

agents-ts/src/runtime/risk.ts reads passage.ap.invoice.get, passage.ap.vendor.history.list, passage.ar.payment.get and
passage.ar.invoice.prediction.get through the MCP gateway, which builds its catalog from the operations that carry x-passage-tool.
Each must be a GET with an x-passage-duty and a 200 response, so the gateway generates a read-only tool for it. The gateway loads one
merged document (PASSAGE_OPENAPI_JSON, built at deploy time from the services' specs); these are the specs that document must include.

Run: python3 -m unittest discover -s docs/ops/tools
"""

import unittest
from pathlib import Path

try:
    import yaml
except ImportError:  # pragma: no cover
    yaml = None

ROOT = Path(__file__).resolve().parents[3]
SOURCE_TOOLS = {
    "passage.ap.invoice.get": "services/payables/api/openapi.yaml",
    "passage.ap.vendor.history.list": "services/payables/api/openapi.yaml",
    "passage.ar.payment.get": "services/receivables/api/openapi.yaml",
    "passage.ar.invoice.prediction.get": "services/prediction-py/api/openapi.yaml",
}


@unittest.skipIf(yaml is None, "PyYAML is not installed")
class AgentSourceTools(unittest.TestCase):
    def test_every_source_tool_is_a_read_operation_with_a_duty(self):
        found = {}
        for tool, spec in SOURCE_TOOLS.items():
            doc = yaml.safe_load((ROOT / spec).read_text())
            for path, item in doc["paths"].items():
                for verb, op in item.items():
                    if isinstance(op, dict) and op.get("x-passage-tool") == tool:
                        found.setdefault(tool, []).append((spec, path, verb, op))
        for tool, spec in SOURCE_TOOLS.items():
            with self.subTest(tool=tool):
                self.assertEqual(len(found.get(tool, [])), 1, f"{tool} must annotate exactly one operation of {spec}")
                _, path, verb, op = found[tool][0]
                self.assertEqual(verb, "get", f"{tool} ({path}) must be a read operation")
                self.assertTrue(op.get("x-passage-duty"), f"{tool} ({path}) needs x-passage-duty")
                self.assertIn("200", op["responses"], f"{tool} ({path}) needs a 200 response")


if __name__ == "__main__":
    unittest.main()
