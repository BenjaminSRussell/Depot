"""The payment webhook edge (#30): Stripe reaches only POST /webhooks/v1/stripe on the receivables webhook listener, through a
Gateway of its own outside the JWT policy, and the receivables chart admits only that Gateway's proxies to the webhook port.

Run: python3 -m unittest deploy/scripts/test_payments_webhook_gateway.py
"""

import re
import shutil
import subprocess
import unittest
from pathlib import Path

import yaml

DEPLOY = Path(__file__).resolve().parents[1]
EDGE = DEPLOY / "gateway" / "payments-webhook"
RECEIVABLES = DEPLOY.parent / "services" / "receivables"
CHART = RECEIVABLES / "charts" / "receivables"
GATEWAY = "depot-payments-webhook"
ROUTE = "receivables-payments-webhook"
WEBHOOK_PATH = "/webhooks/v1/stripe"


def load(path):
    return [d for d in yaml.safe_load_all(path.read_text()) if d]


def edge_docs():
    kustomization = yaml.safe_load((EDGE / "kustomization.yaml").read_text())
    docs = []
    for res in kustomization["resources"]:
        docs += load(EDGE / res)
    return docs


def of_kind(docs, kind):
    return [d for d in docs if d["kind"] == kind]


def go_body_cap():
    src = (RECEIVABLES / "internal/api/gateway.go").read_text()
    m = re.search(r"maxWebhookBody\s*=\s*(\d+) << (\d+)", src)
    assert m, "maxWebhookBody not found"
    return int(m.group(1)) << int(m.group(2))


def quantity(text):
    m = re.fullmatch(r"(\d+)(Ki|Mi)", text)
    assert m, text
    return int(m.group(1)) * {"Ki": 1 << 10, "Mi": 1 << 20}[m.group(2)]


class PaymentsWebhookEdge(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.docs = edge_docs()
        (cls.gateway,) = of_kind(cls.docs, "Gateway")
        (cls.listener,) = cls.gateway["spec"]["listeners"]
        (cls.route,) = of_kind(cls.docs, "HTTPRoute")

    def test_every_resource_is_namespaced_in_hello_ledger(self):
        for d in self.docs:
            self.assertEqual(d["metadata"].get("namespace"), "hello-ledger", d["metadata"]["name"])

    def test_gateway_is_https_only_on_its_own_hostname_with_a_certificate(self):
        self.assertEqual(self.gateway["metadata"]["name"], GATEWAY)
        self.assertEqual((self.listener["protocol"], self.listener["port"]), ("HTTPS", 443))
        self.assertTrue(self.listener["hostname"].startswith("payments-webhook."))
        (ref,) = self.listener["tls"]["certificateRefs"]
        (cert,) = of_kind(self.docs, "Certificate")
        self.assertEqual(cert["spec"]["secretName"], ref["name"])
        self.assertEqual(cert["spec"]["dnsNames"], [self.listener["hostname"]])
        self.assertEqual(self.listener["allowedRoutes"]["namespaces"]["from"], "Same")

    def test_only_post_of_the_exact_webhook_path_reaches_the_receivables_webhook_service(self):
        spec = self.route["spec"]
        self.assertEqual(self.route["metadata"]["name"], ROUTE)
        self.assertEqual(spec["parentRefs"], [{"name": GATEWAY, "sectionName": self.listener["name"]}])
        self.assertEqual(spec["hostnames"], [self.listener["hostname"]])
        (rule,) = spec["rules"]
        self.assertEqual(rule["matches"], [{"path": {"type": "Exact", "value": WEBHOOK_PATH}, "method": "POST"}])
        self.assertNotIn("filters", rule)
        (backend,) = rule["backendRefs"]
        values = yaml.safe_load((CHART / "values.yaml").read_text())
        self.assertEqual(backend["port"], values["payments"]["webhookPort"])
        service = (CHART / "templates/service-webhook.yaml").read_text()
        self.assertIn("name: {{ .Release.Name }}-receivables-webhook", service)
        self.assertEqual(backend["name"], "receivables-receivables-webhook")  # release "receivables" in deploy/envs/dev/apps/services.yaml
        self.assertIn('"' + WEBHOOK_PATH + '"', (RECEIVABLES / "internal/api/gateway.go").read_text())

    def test_no_jwt_policy_reaches_the_edge_and_no_api_route_attaches_to_it(self):
        main = yaml.safe_load((DEPLOY / "gateway/kustomization.yaml").read_text())
        self.assertNotIn("payments-webhook", " ".join(main["resources"]))
        for path in (DEPLOY / "gateway").glob("*.yaml"):
            for d in load(path):
                if d["kind"] == "SecurityPolicy":
                    targets = {(t["kind"], t["name"]) for t in d["spec"].get("targetRefs", [])}
                    self.assertFalse(targets & {("Gateway", GATEWAY), ("HTTPRoute", ROUTE)}, path)
                if d["kind"] == "HTTPRoute":
                    self.assertNotIn(GATEWAY, {p["name"] for p in d["spec"]["parentRefs"]}, path)
        self.assertEqual(of_kind(self.docs, "SecurityPolicy"), [])

    def test_body_limit_matches_the_listener_and_the_route_is_rate_limited(self):
        (btp,) = of_kind(self.docs, "BackendTrafficPolicy")
        self.assertEqual({(t["kind"], t["name"]) for t in btp["spec"]["targetRefs"]}, {("HTTPRoute", ROUTE)})
        self.assertEqual(quantity(btp["spec"]["requestBuffer"]["limit"]), go_body_cap())
        self.assertEqual(btp["spec"]["rateLimit"]["type"], "Global")

    def test_identity_headers_are_stripped_like_the_as2_edge_and_tls_1_2(self):
        (ctp,) = of_kind(self.docs, "ClientTrafficPolicy")
        self.assertEqual(ctp["spec"]["tls"]["minVersion"], "1.2")
        (as2,) = load(DEPLOY / "gateway/as2-clienttrafficpolicy.yaml")
        self.assertEqual(sorted(ctp["spec"]["headers"]["earlyRequestHeaders"]["remove"]), sorted(as2["spec"]["headers"]["earlyRequestHeaders"]["remove"]))

    def test_argocd_applies_the_edge(self):
        app = yaml.safe_load((DEPLOY / "envs/dev/apps/payments-webhook-gateway.yaml").read_text())
        self.assertEqual(app["spec"]["source"]["path"], "deploy/gateway/payments-webhook")
        self.assertIn("#213", app["metadata"]["annotations"]["depot.dev/requires"])
        self.assertIn("SkipDryRunOnMissingResource=true", app["spec"]["syncPolicy"]["syncOptions"])

    def test_receivables_network_policy_admits_only_this_gateway_to_the_webhook_port(self):
        values = yaml.safe_load((CHART / "values.yaml").read_text())
        np = values["networkPolicy"]
        self.assertTrue(np["enabled"])
        self.assertEqual(np["webhookGateway"]["namespaces"], ["envoy-gateway-system"])
        self.assertEqual(np["webhookGateway"]["podLabels"], {
            "gateway.envoyproxy.io/owning-gateway-name": GATEWAY,
            "gateway.envoyproxy.io/owning-gateway-namespace": self.gateway["metadata"]["namespace"],
        })

    @unittest.skipIf(shutil.which("helm") is None, "helm is not installed")
    def test_chart_renders_the_policy_and_requires_a_publishable_key(self):
        base = ["helm", "template", "receivables", str(CHART), "--set", "payments.provider=stripe", "--set", "payments.existingSecret=receivables-stripe"]
        missing = subprocess.run(base, capture_output=True, text=True)
        self.assertNotEqual(missing.returncode, 0)
        self.assertIn("publishableKey", missing.stderr)
        out = subprocess.run(base + ["--set", "payments.publishableKey=pk_test_x"], check=True, capture_output=True, text=True).stdout
        (policy,) = [d for d in yaml.safe_load_all(out) if d and d["kind"] == "NetworkPolicy"]
        webhook = [r for r in policy["spec"]["ingress"] if r["ports"] == [{"protocol": "TCP", "port": 8081}]]
        self.assertEqual(len(webhook), 1)
        (peer,) = webhook[0]["from"]
        self.assertEqual(peer["podSelector"]["matchLabels"]["gateway.envoyproxy.io/owning-gateway-name"], GATEWAY)
        self.assertEqual(peer["namespaceSelector"]["matchLabels"], {"kubernetes.io/metadata.name": "envoy-gateway-system"})
        off = subprocess.run(["helm", "template", "receivables", str(CHART)], check=True, capture_output=True, text=True).stdout
        self.assertNotIn("kind: NetworkPolicy", off)


if __name__ == "__main__":
    unittest.main()
