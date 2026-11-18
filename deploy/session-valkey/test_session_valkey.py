"""Render tests for the portal session store (ADR-0014, D26). Run: python3 -m unittest discover -s deploy/session-valkey

Needs helm on PATH (skipped otherwise). Renders the chart with the stage Application's values and checks the four D26
properties: auth (default user off, one ACL user per client limited to its key prefix and commands, passwords from the
secret store), TLS only (plain port closed, a cert-manager certificate, rediss:// client URLs), persistence off (no RDB, no
AOF, no volume claim) and a NetworkPolicy (ingress from the listed clients only, on the TLS port, no egress). With
kubeconform on PATH the core resources are validated.
"""

from __future__ import annotations

import re
import shutil
import subprocess
import sys
import unittest
from pathlib import Path

import yaml

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
HELM = shutil.which("helm")
STAGE_APP = REPO / "deploy/envs/stage/apps/session-valkey.yaml"


def stage_values() -> dict:
    (app,) = [d for d in yaml.safe_load_all(STAGE_APP.read_text()) if d]
    (src,) = [s for s in app["spec"].get("sources") or [app["spec"]["source"]] if s.get("path") == "deploy/session-valkey"]
    return src["helm"]["valuesObject"]


def render(values: dict | None = None, namespace: str = "valkey") -> subprocess.CompletedProcess:
    cmd = [HELM, "template", "session-valkey", str(HERE), "--namespace", namespace, "-f", "-"]
    return subprocess.run(cmd, input=yaml.safe_dump(stage_values() if values is None else values), capture_output=True, text=True)


def docs(values: dict | None = None) -> list[dict]:
    res = render(values)
    if res.returncode:
        raise AssertionError(res.stderr)
    return [d for d in yaml.safe_load_all(res.stdout) if d]


def one(rendered: list[dict], kind: str, name: str | None = None) -> dict:
    found = [d for d in rendered if d["kind"] == kind and (name is None or d["metadata"]["name"] == name)]
    if len(found) != 1:
        raise AssertionError(f"want one {kind} {name or ''}, got {len(found)}")
    return found[0]


def flag(args: list[str], name: str) -> str | None:
    return args[args.index(name) + 1] if name in args else None


@unittest.skipUnless(HELM, "helm not installed")
class SessionValkeyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.values = stage_values()
        cls.docs = docs()
        cls.deployment = one(cls.docs, "Deployment", "session-valkey")
        (cls.container,) = cls.deployment["spec"]["template"]["spec"]["containers"]
        cls.args = [str(a) for a in cls.container["args"]]

    def test_stage_lists_both_portal_bffs(self) -> None:
        self.assertEqual({c["name"] for c in self.values["clients"]}, {"customer-portal", "supplier-portal"})
        self.assertEqual(self.values["externalSecret"]["environment"], "stage")

    def test_auth_default_user_off_and_one_acl_user_per_client(self) -> None:
        acl = one(self.docs, "ExternalSecret", "session-valkey-acl")
        text = acl["spec"]["target"]["template"]["data"]["users.acl"]
        lines = [ln for ln in text.splitlines() if ln.strip()]
        self.assertEqual(lines[0], "user default off")
        self.assertNotIn("nopass", text)
        vault = {d["secretKey"]: d["remoteRef"]["key"] for d in acl["spec"]["data"]}
        for client in self.values["clients"]:
            (line,) = [ln for ln in lines if ln.startswith(f"user {client['name']} ")]
            var = client["name"].replace("-", "_")
            self.assertIn(f" on >{{{{ .{var} }}}} ", line)
            self.assertIn(f" ~{client['keyPrefix']}* ", line + " ")
            self.assertIn(" -@all ", line)
            for dangerous in ("+@all", "+keys", "+flushall", "+config", "+debug", "+eval"):
                self.assertNotIn(dangerous, line)
            self.assertEqual(vault[var], f"depot/stage/session-valkey/{client['name']}-password")
        (probe,) = [ln for ln in lines if ln.startswith("user probe ")]
        self.assertTrue(probe.endswith("-@all +ping"), probe)
        self.assertEqual(flag(self.args, "--aclfile"), "/acl/users.acl")
        self.assertNotIn("--requirepass", self.args, "a password on the command line is visible in the process list")
        env = {e["name"]: e for e in self.container["env"]}
        self.assertEqual(env["REDISCLI_AUTH"]["valueFrom"]["secretKeyRef"], {"name": "session-valkey-acl", "key": "probe-password"})

    def test_tls_only_with_rediss_client_urls(self) -> None:
        self.assertEqual(flag(self.args, "--port"), "0", "the plain port must be closed")
        self.assertEqual(flag(self.args, "--tls-port"), "6379")
        for name in ("--tls-cert-file", "--tls-key-file", "--tls-ca-cert-file"):
            self.assertTrue((flag(self.args, name) or "").startswith("/tls/"), name)
        cert = one(self.docs, "Certificate", "session-valkey-tls")
        self.assertEqual(cert["spec"]["issuerRef"], {"kind": "ClusterIssuer", "name": "depot-stage-ca"})
        self.assertIn("session-valkey.valkey.svc", cert["spec"]["dnsNames"])
        volumes = {v["name"]: v for v in self.deployment["spec"]["template"]["spec"]["volumes"]}
        self.assertEqual(volumes["tls"]["secret"]["secretName"], cert["spec"]["secretName"])
        probe = self.container["readinessProbe"]["exec"]["command"]
        self.assertIn("--tls", probe)
        for client in self.values["clients"]:
            es = one(self.docs, "ExternalSecret", f"{client['name']}-session-store")
            self.assertEqual(es["metadata"]["namespace"], client["namespace"])
            url = es["spec"]["target"]["template"]["data"]["url"]
            self.assertEqual(url, f"rediss://{client['name']}@session-valkey.valkey.svc:6379")
            self.assertEqual(es["spec"]["data"][0]["remoteRef"]["key"], f"depot/stage/session-valkey/{client['name']}-password")

    def test_persistence_off(self) -> None:
        self.assertEqual(flag(self.args, "--save"), "", "RDB snapshots must be off")
        self.assertEqual(flag(self.args, "--appendonly"), "no", "the AOF must be off")
        kinds = {d["kind"] for d in self.docs}
        self.assertNotIn("PersistentVolumeClaim", kinds)
        self.assertNotIn("StatefulSet", kinds)
        for v in self.deployment["spec"]["template"]["spec"]["volumes"]:
            self.assertNotIn("persistentVolumeClaim", v, v["name"])
        volumes = {v["name"]: v for v in self.deployment["spec"]["template"]["spec"]["volumes"]}
        self.assertIn("emptyDir", volumes["data"])

    def test_network_policy_admits_only_the_clients_and_allows_no_egress(self) -> None:
        policy = one(self.docs, "NetworkPolicy", "session-valkey")
        spec = policy["spec"]
        self.assertEqual(spec["podSelector"], {"matchLabels": {"app.kubernetes.io/name": "session-valkey"}})
        self.assertEqual(sorted(spec["policyTypes"]), ["Egress", "Ingress"])
        self.assertEqual(spec["egress"], [])
        (rule,) = spec["ingress"]
        self.assertEqual(rule["ports"], [{"protocol": "TCP", "port": 6379}])
        peers = {(f["namespaceSelector"]["matchLabels"]["kubernetes.io/metadata.name"], f["podSelector"]["matchLabels"]["app.kubernetes.io/name"])
                 for f in rule["from"]}
        self.assertEqual(peers, {(c["namespace"], c["podName"]) for c in self.values["clients"]})
        for f in rule["from"]:
            self.assertIn("podSelector", f, "a namespace-wide peer would admit every pod of the namespace")
        labels = self.deployment["spec"]["template"]["metadata"]["labels"]
        self.assertEqual(labels["app.kubernetes.io/name"], "session-valkey")

    def test_the_portal_charts_name_the_same_pods(self) -> None:
        for client in self.values["clients"]:
            chart = REPO / f"services/{client['name']}-ts/charts/{client['name']}/templates/deployment.yaml"
            self.assertIn(f"app.kubernetes.io/name: {client['podName']}", chart.read_text())
            store = (REPO / f"services/{client['name']}-ts/src/redisStore.ts").read_text()
            self.assertIn(f'prefix = "{client["keyPrefix"]}"', store)

    def test_the_commands_cover_what_the_store_sends(self) -> None:
        sent = set()
        for client in self.values["clients"]:
            for f in (REPO / f"services/{client['name']}-ts/src").glob("*.ts"):
                if not f.name.endswith(".test.ts"):
                    sent |= {m.lower() for m in re.findall(r'(?:command|run|send)\(\s*\[?"([A-Z]+)"', f.read_text())}
        self.assertTrue(sent, "found no commands in the portal stores")
        self.assertLessEqual(sent, set(self.values.get("commands") or yaml.safe_load((HERE / "values.yaml").read_text())["commands"]))

    def test_required_values_fail_the_render(self) -> None:
        base = stage_values()
        for key, message in (("clients", "clients is required"), ("tls", "tls.issuer is required")):
            values = dict(base)
            values[key] = [] if key == "clients" else {"issuer": ""}
            res = render(values)
            self.assertNotEqual(res.returncode, 0, key)
            self.assertIn(message, res.stderr)

    @unittest.skipUnless(shutil.which("kubeconform"), "kubeconform not installed")
    def test_kubeconform(self) -> None:
        core = [d for d in self.docs if d["kind"] in ("Deployment", "Service", "NetworkPolicy")]
        res = subprocess.run(["kubeconform", "-strict", "-summary", "-"], input=yaml.safe_dump_all(core), capture_output=True, text=True)
        self.assertEqual(res.returncode, 0, res.stdout + res.stderr)


if __name__ == "__main__":
    sys.exit(unittest.main())
