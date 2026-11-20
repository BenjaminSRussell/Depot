"""Isolated CUI deployment checks (DEPOT-310): the cui cloud profile, its egress NetworkPolicies, the sub-processor
registry and the boundary values the charts receive. Offline only: the AWS accounts, network and live probe pods are
follow-ups (docs/ops/NOT-IMPLEMENTED.md). Run:

    python3 -m unittest discover -s deploy/cloud-profiles -p 'test_cui_boundary.py'

The render checks need helm on PATH (skipped otherwise; CI installs it).
"""

from __future__ import annotations

import json
import re
import subprocess
import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import cloudprofile as cp  # noqa: E402
from test_egress_render import HELM, charts, external_hosts, is_internal, mirror_for, render, violations  # noqa: E402

REPO = cp.REPO
SUBPROCESSORS = HERE / "subprocessors.json"
EGRESS_CHART = REPO / "deploy/networkpolicy/cui-egress"
AGENT_CHART = REPO / "services/agents-ts/charts/agent-runtime"
COMPLIANCE_CHART = REPO / "services/compliance/charts/compliance"
OBSERVABILITY_CHART = REPO / "deploy/observability/charts/observability"
EXPECTED_IDS = {
    "slack-teams", "email-smtp", "ocr-invoice-capture", "tax-provider", "carriers",
    "payment-processor", "payroll-provider", "card-feed", "e-invoice-gateways",
}
ENTRY_KEYS = {"id", "name", "hosts", "enabled"}
EXPORTER_VENDOR = re.compile(r"jaeger|datadog|sentry|honeycomb|newrelic", re.I)
EXPORTER_ENV = re.compile(r'name:\s*(OTEL_EXPORTER\w*)\s+value:\s*"([^"]*)"')
EGRESS_WORKLOADS = {"agents-ts": "agent-runtime", "model-serving": "model-serving", "audit": "audit"}


def check_subprocessors(doc: dict, boundary: str) -> list[str]:
    """Failures of the sub-processor registry. Under boundary cui every entry must declare `enabled.cui` as the boolean false."""
    failures: list[str] = []
    entries = doc.get("subprocessors")
    if not isinstance(entries, list) or not entries:
        return ["subprocessors is missing or empty"]
    seen: set[str] = set()
    for entry in entries:
        ident = entry.get("id", "<no id>") if isinstance(entry, dict) else "<not an object>"
        if not isinstance(entry, dict) or set(entry) != ENTRY_KEYS:
            failures.append(f"{ident}: entry keys are not exactly {sorted(ENTRY_KEYS)}")
            continue
        if ident in seen:
            failures.append(f"{ident}: duplicate id")
        seen.add(ident)
        enabled = entry["enabled"]
        if not isinstance(enabled, dict) or "cui" not in enabled:
            failures.append(f"{ident}: enabled.cui is missing")
        elif boundary == "cui" and enabled["cui"] is not False:
            failures.append(f"{ident}: enabled.cui is {enabled['cui']!r} but boundary cui requires the boolean false")
    return failures


def exporter_findings(manifest: str) -> list[str]:
    """Telemetry exporters a render configures outside the cluster: a vendor name, or an OTEL_EXPORTER env with an external endpoint."""
    found = sorted({m.group(0).lower() for m in EXPORTER_VENDOR.finditer(manifest)})
    for name, value in EXPORTER_ENV.findall(manifest):
        if value and not is_internal(cp.host_of(value)):
            found.append(f"{name}={value}")
    return found


def egress_policy_violations(manifest: str, allowlist: list[str], subprocessor_hosts: set[str]) -> list[str]:
    """Reasons a rendered cui-egress manifest could reach outside the boundary, each naming the host or range."""
    problems = [f"host {h} is not on the cui allowlist" for h in violations(manifest, allowlist)]
    for match in re.finditer(r"depot\.dev/egress-allow-hosts:\s*\"([^\"]*)\"", manifest):
        for host in filter(None, match.group(1).split(",")):
            if not cp.host_allowed(host, allowlist):
                problems.append(f"host {host} is not on the cui allowlist")
            if host in subprocessor_hosts:
                problems.append(f"host {host} belongs to a sub-processor")
    for cidr in re.findall(r"cidr:\s*\"([^\"]+)\"", manifest):
        if cidr.endswith("/0") or cidr in ("0.0.0.0/0", "::/0"):
            problems.append(f"range {cidr} is open to the internet")
    return sorted(set(problems))


def registry() -> dict:
    return json.loads(SUBPROCESSORS.read_text(encoding="utf-8"))


def subprocessor_hosts() -> set[str]:
    return {h for e in registry()["subprocessors"] for h in e["hosts"]}


def helm_render(chart: Path, *args: str) -> str:
    proc = subprocess.run([HELM, "template", "cui", str(chart), *args], capture_output=True, text=True)
    if proc.returncode != 0:
        raise AssertionError(f"helm template {chart.relative_to(REPO)} failed:\n{proc.stderr.strip()}")
    return proc.stdout


def cui_values_args() -> list[str]:
    return ["-f", str(cp.values_path("cui")), "--set", f"global.imageRegistry={mirror_for('cui')}"]


class CuiProfileTests(unittest.TestCase):
    def setUp(self) -> None:
        self.doc = cp.load()
        self.resolved = cp.resolve(self.doc, "cui")

    def test_the_profile_passes_parity_and_declares_the_boundary(self) -> None:
        self.assertEqual(cp.check_parity(self.doc), [])
        self.assertEqual(
            (self.resolved["boundary"], self.resolved["aiEnabled"], self.resolved["deploymentName"]),
            ("cui", False, "depot-cui"),
        )

    def test_bedrock_is_the_only_ai_host_and_no_other_external_saas_is_allowed(self) -> None:
        allow = self.resolved["egressAllowlist"]
        self.assertEqual(self.resolved["llmProviders"], ["bedrock"])
        self.assertTrue(self.resolved["enforceProviders"])
        self.assertTrue(cp.host_allowed("bedrock-runtime.us-gov-west-1.amazonaws.com", allow))
        for host in ("api.anthropic.com", "api.openai.com", "res.openai.azure.com", "events.pagerduty.com", "ghcr.io", *subprocessor_hosts()):
            self.assertFalse(cp.host_allowed(host, allow), host)

    def test_the_deployment_owns_its_stack_name_and_profile_values(self) -> None:
        names, profiles = {}, {}
        for tfvars in sorted((HERE / "tofu").glob("*.tfvars.json")):
            data = json.loads(tfvars.read_text(encoding="utf-8"))
            names[tfvars.stem.removesuffix(".tfvars")] = data.get("name")
            profiles[data["cloud_profile"]] = tfvars.name
        self.assertEqual(names["cui"], "depot-cui")
        self.assertEqual([k for k, v in names.items() if v == "depot-cui"], ["cui"])
        self.assertNotEqual(names["cui"], "depot-local")
        self.assertEqual(len(profiles), len(names))
        values = {p: cp.values_path(p).read_text(encoding="utf-8") for p in self.doc["profiles"]}
        self.assertIn('deploymentName: "depot-cui"', values["cui"])
        for name, text in values.items():
            if name != "cui":
                self.assertNotIn("deploymentName", text)
                self.assertNotIn("boundary:", text)
                self.assertNotIn("aiEnabled", text)


class RunbookTests(unittest.TestCase):
    def test_the_runbook_has_the_required_procedures_and_lists_every_sub_processor(self) -> None:
        text = (REPO / "docs/runbooks/cui-deployment.md").read_text(encoding="utf-8")
        headings = set(re.findall(r"^## (.+)$", text, re.M))
        for required in ("Key rotation", "Endpoint approval", "Model or provider change", "Incident response", "Deployment topology", "Sub-processors in the CUI deployment"):
            self.assertIn(required, headings)
        for ident in EXPECTED_IDS:
            self.assertIn(f"`{ident}`", text)
        self.assertTrue((REPO / "docs/signoff/cui-ai-enablement-template.md").is_file())


class SubprocessorRegistryTests(unittest.TestCase):
    def test_all_nine_are_listed_once_and_off_under_cui(self) -> None:
        doc = registry()
        self.assertEqual({e["id"] for e in doc["subprocessors"]}, EXPECTED_IDS)
        self.assertEqual(len(doc["subprocessors"]), 9)
        self.assertEqual(check_subprocessors(doc, "cui"), [])

    def test_an_enabled_entry_fails_the_check_and_names_it(self) -> None:
        doc = registry()
        doc["subprocessors"][3]["enabled"]["cui"] = True
        self.assertEqual(
            check_subprocessors(doc, "cui"),
            ["tax-provider: enabled.cui is True but boundary cui requires the boolean false"],
        )
        self.assertEqual(check_subprocessors(doc, "standard"), [])

    def test_malformed_entries_fail_closed(self) -> None:
        for bad in ("false", 0, None, [], "true"):
            with self.subTest(enabled_cui=bad):
                doc = registry()
                doc["subprocessors"][0]["enabled"]["cui"] = bad
                self.assertEqual(len(check_subprocessors(doc, "cui")), 1)
        missing = registry()
        del missing["subprocessors"][1]["enabled"]["cui"]
        self.assertEqual(check_subprocessors(missing, "cui"), ["email-smtp: enabled.cui is missing"])
        extra = registry()
        extra["subprocessors"][2]["apiKey"] = "x"
        self.assertEqual(len(check_subprocessors(extra, "cui")), 1)
        duplicate = registry()
        duplicate["subprocessors"][4]["id"] = "slack-teams"
        self.assertTrue(any("duplicate id" in f for f in check_subprocessors(duplicate, "cui")))
        for empty in ({}, {"subprocessors": []}, {"subprocessors": "none"}):
            self.assertEqual(len(check_subprocessors(empty, "cui")), 1)

    def test_no_sub_processor_host_is_on_the_cui_allowlist(self) -> None:
        allow = cp.resolve(cp.load(), "cui")["egressAllowlist"]
        for host in sorted(subprocessor_hosts()):
            with self.subTest(host=host):
                self.assertFalse(cp.host_allowed(host, allow))


@unittest.skipUnless(HELM, "helm not installed")
class CuiRenderTests(unittest.TestCase):
    maxDiff = None
    renders: dict[str, str]

    @classmethod
    def setUpClass(cls) -> None:
        cls.renders = {chart.relative_to(REPO).as_posix(): render(chart, "cui", mirror_for("cui")) for chart in charts()}

    def test_the_boundary_values_reach_the_pods(self) -> None:
        agents = helm_render(AGENT_CHART, *cui_values_args())
        self.assertRegex(agents, r'name: AGENTS_BOUNDARY\s+value: "cui"')
        self.assertRegex(agents, r'name: AGENTS_AI_ENABLED\s+value: "false"')
        compliance = helm_render(COMPLIANCE_CHART, *cui_values_args())
        self.assertRegex(compliance, r'name: COMPLIANCE_BOUNDARY\s+value: "cui"')

    def test_a_standard_deployment_keeps_the_assistant_and_the_standard_boundary(self) -> None:
        values = ["-f", str(cp.values_path("gcc-high")), "--set", f"global.imageRegistry={mirror_for('gcc-high')}"]
        agents = helm_render(AGENT_CHART, *values)
        self.assertRegex(agents, r'name: AGENTS_BOUNDARY\s+value: "standard"')
        self.assertRegex(agents, r'name: AGENTS_AI_ENABLED\s+value: "true"')
        self.assertRegex(helm_render(COMPLIANCE_CHART, *values), r'name: COMPLIANCE_BOUNDARY\s+value: "standard"')

    def test_every_chart_stays_inside_the_cui_allowlist(self) -> None:
        allow = cp.resolve(cp.load(), "cui")["egressAllowlist"]
        mirror = mirror_for("cui")
        if not cp.host_allowed(mirror, allow):
            allow = allow + [mirror]
        self.assertEqual({k: v for k, v in ((k, violations(m, allow)) for k, m in self.renders.items()) if v}, {})

    def test_no_telemetry_exporter_and_no_paging_receiver(self) -> None:
        found = {k: exporter_findings(text) for k, text in self.renders.items()}
        self.assertEqual({k: v for k, v in found.items() if v}, {})
        self.assertNotIn("pagerduty_configs", self.renders["deploy/observability/charts/observability"])

    def test_no_sub_processor_host_appears_in_any_render(self) -> None:
        for ident_hosts in registry()["subprocessors"]:
            for host in ident_hosts["hosts"]:
                with self.subTest(subprocessor=ident_hosts["id"], host=host):
                    self.assertEqual([k for k, text in self.renders.items() if host in text.lower()], [])

    def test_the_exporter_scan_is_not_vacuous(self) -> None:
        outside = 'name: OTEL_EXPORTER_OTLP_ENDPOINT\n  value: "https://collector.example.com:4318"'
        inside = 'name: OTEL_EXPORTER_OTLP_ENDPOINT\n  value: "http://collector.observability.svc:4318"'
        self.assertEqual(exporter_findings(outside), ["OTEL_EXPORTER_OTLP_ENDPOINT=https://collector.example.com:4318"])
        self.assertEqual(exporter_findings(inside), [])
        self.assertEqual(exporter_findings("url: https://api.datadoghq.com"), ["datadog"])


@unittest.skipUnless(HELM, "helm not installed")
class CuiEgressPolicyTests(unittest.TestCase):
    maxDiff = None

    def setUp(self) -> None:
        self.allow = cp.resolve(cp.load(), "cui")["egressAllowlist"]
        self.hosts = subprocessor_hosts()

    def policies(self, *extra: str) -> str:
        return helm_render(EGRESS_CHART, "-f", str(cp.values_path("cui")), *extra)

    def test_one_egress_only_policy_per_workload_with_no_open_range(self) -> None:
        out = self.policies()
        self.assertEqual(re.findall(r"^  name: cui-(\S+)-egress$", out, re.M), ["agents-ts", "model-serving", "audit"])
        self.assertEqual(out.count("kind: NetworkPolicy"), 3)
        self.assertEqual(out.count("policyTypes: [Egress]"), 3)
        self.assertNotIn("Ingress", out.replace("depot.dev/network-policy", ""))
        self.assertNotIn("0.0.0.0/0", out)
        self.assertNotIn("ipBlock", out)
        self.assertEqual(egress_policy_violations(out, self.allow, self.hosts), [])

    def test_selectors_match_the_pod_labels_of_the_real_charts(self) -> None:
        self.assertEqual(set(re.findall(r'app\.kubernetes\.io/name: "([^"]+)"', self.policies())), set(EGRESS_WORKLOADS.values()))
        for chart, label in (("services/agents-ts/charts/agent-runtime", "agent-runtime"), ("services/audit/charts/audit", "audit")):
            text = (REPO / chart / "templates/deployment.yaml").read_text(encoding="utf-8")
            self.assertIn(f"app.kubernetes.io/name: {label}", text)

    def test_private_endpoint_ranges_render_as_given(self) -> None:
        out = self.policies("--set", "privateEndpointCidrs={10.20.0.0/24}")
        self.assertEqual(out.count('ipBlock: { cidr: "10.20.0.0/24" }'), 3)
        self.assertEqual(egress_policy_violations(out, self.allow, self.hosts), [])

    def test_an_outside_host_is_reported(self) -> None:
        out = self.policies("--set", "allowHosts={bedrock-runtime.us-gov-west-1.amazonaws.com,evil.example.com}")
        self.assertEqual(egress_policy_violations(out, self.allow, self.hosts), ["host evil.example.com is not on the cui allowlist"])

    def test_a_sub_processor_host_is_reported(self) -> None:
        out = self.policies("--set", "allowHosts={api.stripe.com}")
        self.assertEqual(
            egress_policy_violations(out, self.allow, self.hosts),
            ["host api.stripe.com belongs to a sub-processor", "host api.stripe.com is not on the cui allowlist"],
        )

    def test_an_open_range_is_reported(self) -> None:
        out = self.policies("--set", "privateEndpointCidrs={0.0.0.0/0}")
        self.assertEqual(egress_policy_violations(out, self.allow, self.hosts), ["range 0.0.0.0/0 is open to the internet"])

    def test_an_outside_host_in_a_url_is_reported(self) -> None:
        self.assertEqual(egress_policy_violations("url: https://hooks.slack.com/x\n", self.allow, self.hosts), ["host hooks.slack.com is not on the cui allowlist"])
        self.assertEqual(external_hosts("url: https://hooks.slack.com/x\n"), {"hooks.slack.com"})

    def test_nothing_renders_outside_the_cui_boundary(self) -> None:
        for profile in ("commercial", "gcc-high"):
            with self.subTest(profile=profile):
                self.assertEqual(helm_render(EGRESS_CHART, "-f", str(cp.values_path(profile))).strip(), "")
        self.assertEqual(helm_render(EGRESS_CHART).strip(), "")


if __name__ == "__main__":
    unittest.main()
