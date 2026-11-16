"""Tests for the offline install bundle (#603). Run: python3 -m unittest discover -s deploy/offline

External programs (skopeo, cosign, helm) are replaced by a recording runner, so the tests need neither a registry nor a
signing key. They prove the commands the build and install issue, that verification catches a tampered archive, that
Argo CD renders with no reference to the internet, and that the offline Kyverno policy trusts a key, not Sigstore.
Installing on a cluster with egress blocked is a live proof, tracked on #213.
"""

from __future__ import annotations

import json
import re
import shutil
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import offline_bundle as ob  # noqa: E402

DIGEST_A = "sha256:" + "a" * 64
PEM = "-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE\n-----END PUBLIC KEY-----\n"


def unpack(archive: Path, dest: Path) -> None:
    with tarfile.open(archive) as tar:
        tar.extractall(dest, filter="data")


def repack(root: Path, archive: Path) -> None:
    with tarfile.open(archive, "w:gz") as tar:
        tar.add(root, arcname="passage-offline")
DIGEST_B = "sha256:" + "b" * 64


def provenance_with_postgres() -> dict:
    """The committed provenance plus the postgres image the fake helm renders."""
    prov = ob.load_provenance()
    prov["images"].append({"match": "docker.io/library/postgres", "unsignedUpstream": "Docker Official Images carry no cosign signature"})
    return prov


class FakeRunner:
    """Records every command; answers the few that return data, and creates the files the real tools would."""

    def __init__(self, fail_on: str | tuple[str, ...] | None = None) -> None:
        self.calls: list[list[str]] = []
        self.fail_on = (fail_on,) if isinstance(fail_on, str) else fail_on

    def __call__(self, cmd: list[str], *, cwd: Path | None = None) -> str:
        self.calls.append(cmd)
        if self.fail_on and tuple(cmd[: len(self.fail_on)]) == self.fail_on:
            raise RuntimeError(f"{' '.join(cmd[:2])} failed")
        if cmd[:2] == ["syft", "scan"]:
            out = Path(cmd[cmd.index("-o") + 1].split("=", 1)[1])
            out.write_text('{"spdxVersion": "SPDX-2.3"}', encoding="utf-8")
        if cmd[:2] == ["helm", "template"]:
            return "image: ghcr.io/benjaminsrussell/passage-ledger:0.1.0\nimage: docker.io/library/postgres:17\n# image: ignored\n"
        if cmd[:2] == ["skopeo", "inspect"]:
            return (DIGEST_A if "ledger" in cmd[-1] else DIGEST_B) + "\n"
        if cmd[:2] == ["skopeo", "copy"] and cmd[-1].startswith("oci:"):
            d = Path(cmd[-1][4:].rsplit(":", 1)[0])
            d.mkdir(parents=True, exist_ok=True)
            (d / "index.json").write_text("{}", encoding="utf-8")
        if cmd[:2] == ["cosign", "save"]:
            d = Path(cmd[cmd.index("--dir") + 1])
            d.mkdir(parents=True, exist_ok=True)
            (d / "sig").write_text("signature", encoding="utf-8")
        if cmd[:2] == ["helm", "package"] or cmd[:2] == ["helm", "pull"]:
            dest = Path(cmd[cmd.index("--destination") + 1])
            dest.mkdir(parents=True, exist_ok=True)
            (dest / f"{Path(cmd[2]).name}.tgz").write_text("chart", encoding="utf-8")
        if cmd[:2] == ["cosign", "sign-blob"]:
            Path(cmd[cmd.index("--output-signature") + 1]).write_text("blob-signature", encoding="utf-8")
        return ""

    def ran(self, *prefix: str) -> list[list[str]]:
        return [c for c in self.calls if c[: len(prefix)] == list(prefix)]


class PlanTests(unittest.TestCase):
    def test_images_are_found_in_rendered_charts_and_comments_are_ignored(self) -> None:
        self.assertEqual(ob.images_in("image: a/b:1\n  - image: \"c.io/d@sha256:" + "0" * 64 + "\"\n# image: skipped\n"), {"a/b:1", "c.io/d@sha256:" + "0" * 64})

    def test_the_plan_names_every_in_repo_chart_and_the_third_party_ones_argocd_pulls(self) -> None:
        plan = ob.plan(FakeRunner())
        names = {c["name"] for c in plan["repoCharts"]}
        self.assertIn("passage-hr", names)
        pulled = {(c["chart"]) for c in plan["thirdPartyCharts"]}
        self.assertTrue({"kyverno", "temporal", "strimzi-kafka-operator", "cert-manager", "argo-cd"} <= pulled, pulled)
        self.assertIn("quay.io/keycloak/keycloak:26.0.8", [i["ref"] for i in plan["images"]])
        self.assertIn("deploy/policies/admission-offline", plan["policies"])

    def test_extra_images_file_has_only_image_references(self) -> None:
        for ref in ob.extra_images():
            self.assertRegex(ref, r"^[a-z0-9.\-]+(:[0-9]+)?/[A-Za-z0-9._/\-]+(:[A-Za-z0-9._\-]+)?(@sha256:[0-9a-f]{64})?$", ref)

    def test_a_chart_that_needs_values_is_skipped_and_its_images_come_from_the_extra_list(self) -> None:
        images = ob.collect_images(FakeRunner(fail_on="helm"))
        self.assertEqual(images, sorted(ob.extra_images()))

    def test_tofu_installed_charts_carry_the_versions_pinned_in_variables_tf(self) -> None:
        tf = (ob.DEPLOY / "tofu" / "cluster" / "variables.tf").read_text()
        pulled = {c["chart"]: c for c in ob.third_party_charts()}
        for chart, var in (("cert-manager", "cert_manager_chart_version"), ("argo-cd", "argocd_chart_version")):
            version = re.search(rf'variable "{var}" \{{.*?default\s*=\s*"([^"]+)"', tf, re.S).group(1)
            self.assertEqual(pulled[chart]["version"], version)
        self.assertNotIn(ob.GIT_URL, {c["repo"] for c in pulled.values()}, "the repository itself is mirrored as Git, not pulled as a chart")

    def test_a_tag_is_resolved_to_a_digest_and_a_digest_is_kept(self) -> None:
        pinned = "quay.io/keycloak/keycloak@" + DIGEST_B
        runner = FakeRunner()
        self.assertEqual(ob.resolve_digest(pinned, runner), pinned)
        self.assertEqual(runner.calls, [], "a pinned reference is not looked up")
        self.assertEqual(ob.resolve_digest("ghcr.io/benjaminsrussell/passage-ledger:0.1.0", runner), "ghcr.io/benjaminsrussell/passage-ledger@" + DIGEST_A)
        with self.assertRaises(RuntimeError):
            ob.resolve_digest("x.io/y:1", lambda cmd, cwd=None: "not-a-digest\n")

    def test_mirrored_paths_drop_the_registry_host_and_keep_the_digest(self) -> None:
        self.assertEqual(ob.mirrored_path("ghcr.io/benjaminsrussell/passage-ledger@" + DIGEST_A), "benjaminsrussell/passage-ledger@" + DIGEST_A)
        self.assertEqual(ob.mirrored_path("postgres:17"), "postgres:17")
        self.assertEqual(ob.mirrored_path("registry.local:5000/a/b:1"), "a/b:1")
        self.assertEqual(ob.mirrored_path("registry.local:5000/a/b:1@" + DIGEST_A), "a/b@" + DIGEST_A)
        self.assertEqual(ob.repository("registry.local:5000/a/b:1"), "registry.local:5000/a/b")


class BuildVerifyInstallTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)

    def built(self, runner: FakeRunner | None = None, provenance: dict | None = None) -> tuple[Path, FakeRunner]:
        runner = runner or FakeRunner()
        return ob.build(self.tmp, "cosign.key", runner, provenance=provenance or provenance_with_postgres()), runner

    def test_build_pins_every_image_by_digest_and_saves_its_signature_and_sbom(self) -> None:
        archive, runner = self.built()
        self.assertTrue(archive.exists() and Path(str(archive) + ".sig").exists())
        copies = runner.ran("skopeo", "copy")
        self.assertTrue(copies)
        # A tag is resolved to the digest skopeo reports; a reference already pinned by digest (extra-images.txt) keeps its own.
        pinned = {re.search(r"@sha256:([0-9a-f]{64})$", r).group(1) for r in ob.extra_images() if ob.DIGEST.search(r)}
        self.assertTrue(all(any(DIGEST_A[7:] in a or DIGEST_B[7:] in a or any(d in a for d in pinned) for a in c) for c in copies),
                        "images are copied by digest")
        self.assertEqual(len(runner.ran("cosign", "save")), len(copies) // 2, "two copies per image: into staging, then to the OCI layout")
        manifest = json.loads((self.tmp / "stage" / "manifest.json").read_text())
        self.assertTrue(all(ob.DIGEST.search(i["pinned"]) for i in manifest["images"]))
        self.assertTrue(runner.ran("helm", "pull"), "third-party charts are pulled")
        self.assertTrue(runner.ran("cosign", "sign-blob"))

    def image_calls(self, runner: FakeRunner, repo: str) -> list[list[str]]:
        """The commands that name repo, in order (resolve and the archive signature excluded)."""
        return [c for c in runner.calls if any(repo in a for a in c) and c[:2] != ["skopeo", "inspect"]]

    def test_each_image_is_verified_upstream_then_signed_and_attested_with_the_offline_key(self) -> None:
        _, runner = self.built()
        staged = "localhost:5000/benjaminsrussell/passage-ledger@" + DIGEST_A
        calls = self.image_calls(runner, "passage-ledger")
        self.assertEqual([c[:2] for c in calls], [
            ["cosign", "verify"], ["skopeo", "copy"], ["skopeo", "copy"], ["syft", "scan"],
            ["cosign", "sign"], ["cosign", "attest"], ["cosign", "save"],
        ])
        verify, to_staging, to_layout, syft, sign, attest, save = calls
        # (a) the publisher's keyless identity from provenance.json, on the digest
        self.assertEqual(verify[-1], "ghcr.io/benjaminsrussell/passage-ledger@" + DIGEST_A)
        self.assertIn("--certificate-identity-regexp", verify)
        self.assertIn("https://token.actions.githubusercontent.com", verify)
        self.assertEqual(to_staging[-2:], ["docker://ghcr.io/benjaminsrussell/passage-ledger@" + DIGEST_A, "docker://" + staged])
        self.assertIn("--preserve-digests", to_staging)
        self.assertTrue(to_layout[-1].startswith("oci:") and to_layout[-2] == "docker://" + staged)
        sbom = syft[-1].split("=", 1)[1]
        self.assertTrue(Path(sbom).exists())
        # (b) signature and SPDX attestation with the offline key, on the staged digest, no transparency log
        self.assertEqual(sign[-1], staged)
        self.assertEqual(sign[sign.index("--key") + 1], "cosign.key")
        self.assertIn("--tlog-upload=false", sign)
        self.assertEqual(attest[-1], staged)
        self.assertEqual(attest[attest.index("--key") + 1], "cosign.key")
        self.assertEqual(attest[attest.index("--type") + 1], "spdxjson")
        self.assertEqual(attest[attest.index("--predicate") + 1], sbom)
        self.assertEqual(save[-1], staged)
        manifest = json.loads((self.tmp / "stage" / "manifest.json").read_text())
        ledger = next(i for i in manifest["images"] if "passage-ledger" in i["ref"])
        self.assertEqual((ledger["upstream"], ledger["sbom"]), ("verified", f"sboms/{ledger['slug']}.spdx.json"))

    def test_an_image_marked_unsigned_upstream_skips_only_the_upstream_check(self) -> None:
        _, runner = self.built()
        calls = self.image_calls(runner, "keycloak")
        self.assertEqual([c[:2] for c in calls], [
            ["skopeo", "copy"], ["skopeo", "copy"], ["syft", "scan"], ["cosign", "sign"], ["cosign", "attest"], ["cosign", "save"],
        ])
        manifest = json.loads((self.tmp / "stage" / "manifest.json").read_text())
        self.assertEqual(next(i for i in manifest["images"] if "keycloak" in i["ref"])["upstream"], "unsigned")

    def test_an_image_without_provenance_stops_the_build_before_anything_is_copied(self) -> None:
        runner = FakeRunner()
        with self.assertRaises(RuntimeError) as ctx:
            ob.build(self.tmp, "cosign.key", runner, provenance=ob.load_provenance())
        self.assertIn("docker.io/library/postgres:17", str(ctx.exception))
        self.assertEqual(runner.ran("skopeo", "copy") + runner.ran("cosign"), [])
        empty = provenance_with_postgres()
        empty["images"].append({"match": "docker.io/library/*"})
        empty["images"].insert(0, empty["images"].pop())
        with self.assertRaises(RuntimeError) as ctx:
            ob.build(self.tmp, "cosign.key", FakeRunner(), provenance=empty)
        self.assertIn("names no identity", str(ctx.exception))

    def test_any_failing_step_fails_the_build_and_leaves_no_archive(self) -> None:
        for step in [("cosign", "verify"), ("skopeo", "copy"), ("syft",), ("cosign", "sign"), ("cosign", "attest"), ("cosign", "save"), ("helm", "pull"), ("cosign", "sign-blob")]:
            shutil.rmtree(self.tmp, ignore_errors=True)
            self.tmp.mkdir()
            with self.assertRaises(RuntimeError, msg=step):
                self.built(FakeRunner(fail_on=step))
            self.assertFalse((self.tmp / "passage-offline.tar.gz.sig").exists(), step)

    def test_charts_are_checked_as_their_provenance_says(self) -> None:
        prov = provenance_with_postgres()
        for c in prov["charts"]:
            if c["chart"] == "kyverno":
                c.pop("unverified")
                c["keyring"] = "deploy/offline/keys/kyverno.gpg"
        _, runner = self.built(provenance=prov)
        pulls = {c[2]: c for c in runner.ran("helm", "pull")}
        self.assertIn("--verify", pulls["kyverno"])
        self.assertTrue(pulls["kyverno"][pulls["kyverno"].index("--keyring") + 1].endswith("deploy/offline/keys/kyverno.gpg"))
        self.assertNotIn("--verify", pulls["temporal"])
        manifest = json.loads((self.tmp / "stage" / "manifest.json").read_text())
        state = {c["chart"]: c["provenance"] for c in manifest["thirdPartyCharts"]}
        self.assertEqual((state["kyverno"], state["temporal"]), ("keyring", "unverified"))

        # A sha256 pin that does not match the pulled archive fails the build.
        for c in prov["charts"]:
            if c["chart"] == "temporal":
                c.pop("unverified")
                c["sha256"] = "0" * 64
        shutil.rmtree(self.tmp)
        self.tmp.mkdir()
        with self.assertRaises(RuntimeError) as ctx:
            self.built(provenance=prov)
        self.assertIn("does not match the pinned", str(ctx.exception))

        # A chart with no entry fails before anything is copied.
        prov["charts"] = [c for c in prov["charts"] if c["chart"] != "argo-cd"]
        runner = FakeRunner()
        with self.assertRaises(RuntimeError) as ctx:
            self.built(runner, prov)
        self.assertIn("chart argo-cd", str(ctx.exception))
        self.assertEqual(runner.ran("skopeo", "copy"), [])

    def test_pin_charts_records_the_archive_hash_of_every_unverified_chart(self) -> None:
        path = self.tmp / "provenance.json"
        shutil.copy(ob.PROVENANCE, path)
        pinned = ob.pin_charts(FakeRunner(), path, self.tmp / "work")
        self.assertEqual(sorted(pinned), sorted(c["chart"] for c in ob.third_party_charts()))
        prov = json.loads(path.read_text())
        for c in prov["charts"]:
            self.assertRegex(c["sha256"], r"^[0-9a-f]{64}$")
            self.assertNotIn("unverified", c)

    def test_the_committed_provenance_covers_every_third_party_chart_and_the_extra_images(self) -> None:
        prov = ob.load_provenance()
        manifest = {"images": [{"ref": r} for r in ob.extra_images()] + [{"ref": "ghcr.io/benjaminsrussell/passage-hr:0.1.0"}],
                    "thirdPartyCharts": ob.third_party_charts()}
        self.assertEqual(ob.missing_provenance(manifest, prov), [])

    def test_verify_accepts_an_untouched_bundle_and_checks_the_signature_first(self) -> None:
        archive, _ = self.built()
        runner = FakeRunner()
        root = ob.verify(archive, "cosign.pub", runner)
        self.assertEqual(runner.calls[0][:2], ["cosign", "verify-blob"])
        self.assertIn("cosign.pub", runner.calls[0])
        self.assertTrue((root / "manifest.json").exists())

    def test_verify_refuses_a_bad_signature(self) -> None:
        archive, _ = self.built()
        with self.assertRaises(RuntimeError):
            ob.verify(archive, "cosign.pub", FakeRunner(fail_on="cosign"))

    def test_verify_refuses_a_tampered_file_even_if_the_signature_check_passes(self) -> None:
        archive, _ = self.built()
        edited = self.tmp / "edit"
        unpack(archive, edited)
        (next((edited / "passage-offline" / "images").glob("*/index.json"))).write_text('{"tampered":true}')
        repack(edited / "passage-offline", archive)
        with self.assertRaises(RuntimeError) as ctx:
            ob.verify(archive, "cosign.pub", FakeRunner())
        self.assertIn("hash mismatch", str(ctx.exception))

    def test_verify_refuses_an_added_file_and_an_image_without_its_signature(self) -> None:
        archive, _ = self.built()
        edited = self.tmp / "edit2"
        unpack(archive, edited)
        root = edited / "passage-offline"
        (root / "extra.sh").write_text("curl evil")
        shutil.rmtree(next((root / "signatures").iterdir()))
        repack(root, archive)
        with self.assertRaises(RuntimeError) as ctx:
            ob.verify(archive, "cosign.pub", FakeRunner())
        self.assertIn("not in SHA256SUMS: extra.sh", str(ctx.exception))
        self.assertIn("has no signature", str(ctx.exception))

    def test_an_archive_cannot_write_outside_its_directory(self) -> None:
        evil = self.tmp / "evil.tar.gz"
        src = self.tmp / "payload"
        src.write_text("x")
        with tarfile.open(evil, "w:gz") as tar:
            tar.add(src, arcname="../escaped")
        with self.assertRaises(RuntimeError):
            ob.extract(evil, self.tmp / "out")

    def test_an_archive_with_a_link_is_refused(self) -> None:
        evil = self.tmp / "link.tar.gz"
        with tarfile.open(evil, "w:gz") as tar:
            info = tarfile.TarInfo("passage-offline/SHA256SUMS")
            info.type = tarfile.SYMTYPE
            info.linkname = "/etc/passwd"
            tar.addfile(info)
        with self.assertRaises(RuntimeError):
            ob.extract(evil, self.tmp / "out")

    def test_a_sibling_directory_with_the_same_prefix_is_outside(self) -> None:
        evil = self.tmp / "sibling.tar.gz"
        src = self.tmp / "payload"
        src.write_text("x")
        with tarfile.open(evil, "w:gz") as tar:
            tar.add(src, arcname="../out-sibling/x")
        with self.assertRaises(RuntimeError):
            ob.extract(evil, self.tmp / "out")

    def test_sums_report_a_missing_file_and_an_image_not_pinned_by_digest(self) -> None:
        archive, _ = self.built()
        root = ob.extract(archive, self.tmp / "x")
        manifest = json.loads((root / "manifest.json").read_text())
        manifest["images"][0]["pinned"] = manifest["images"][0]["ref"]
        (root / "manifest.json").write_text(json.dumps(manifest))
        victim = next((root / "charts").glob("*.tgz"))
        victim.unlink()
        problems = ob.check_sums(root)
        self.assertIn(f"missing: charts/{victim.name}", problems)
        self.assertIn("hash mismatch: manifest.json", problems)
        self.assertTrue(any("is not pinned by digest" in p for p in problems), problems)

    def test_install_renders_the_offline_policy_with_the_key_the_bundle_was_verified_with(self) -> None:
        archive, _ = self.built()
        key = self.tmp / "cosign.pub"
        key.write_text(PEM)
        out = ob.install(archive, "registry.enclave:5000", "https://git.enclave/passage.git", str(key), FakeRunner())
        (policy,) = sorted((out / "policies").glob("*.yaml"))
        text = policy.read_text()
        self.assertEqual(text.count("MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE"), 2)
        self.assertNotIn("REPLACE_WITH_COSIGN_PUBLIC_KEY", text)
        self.assertIn("registry.enclave:5000/*", text)

    def test_install_stops_before_pushing_anything_when_verification_fails(self) -> None:
        archive, _ = self.built()
        runner = FakeRunner(fail_on="cosign")
        with self.assertRaises(RuntimeError):
            ob.install(archive, "registry.enclave:5000", "https://git.enclave/passage.git", "cosign.pub", runner)
        self.assertEqual(runner.ran("skopeo"), [])
        self.assertEqual(runner.ran("helm", "push"), [])

    def test_install_pushes_images_with_signatures_and_charts_and_renders_argocd_for_the_mirror(self) -> None:
        archive, _ = self.built()
        runner = FakeRunner()
        out = ob.install(archive, "registry.enclave:5000", "https://git.enclave/passage.git", "cosign.pub", runner)
        pushes = [c for c in runner.ran("skopeo", "copy") if c[-1].startswith("docker://registry.enclave:5000/")]
        self.assertTrue(pushes)
        self.assertEqual(len(runner.ran("cosign", "load")), len(pushes))
        self.assertTrue(runner.ran("helm", "push"))
        self.assertTrue(all(c[-1].startswith("oci://registry.enclave:5000/charts") for c in runner.ran("helm", "push")))
        root = next(iter(out.rglob("root-dev.yaml")))
        self.assertIn("https://git.enclave/passage.git", root.read_text())


class ArgoCDTests(unittest.TestCase):
    def test_every_source_is_rewritten_and_no_internet_url_is_left(self) -> None:
        tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, tmp, True)
        written = ob.render_argocd("https://git.enclave/passage.git", "registry.enclave:5000", tmp)
        self.assertGreater(len(written), 20)
        for path in written:
            text = path.read_text()
            self.assertNotIn(ob.GIT_URL, text, path.name)
            urls = set(re.findall(r"repoURL:\s*(\S+)", text))
            for u in urls:
                self.assertIn(u, {"https://git.enclave/passage.git", "registry.enclave:5000/charts"}, f"{path.name}: {u}")

    def test_chart_repositories_become_the_oci_registry_and_git_the_mirror(self) -> None:
        text = "source:\n  repoURL: https://kyverno.github.io/kyverno\n  chart: kyverno\n- repoURL: " + ob.GIT_URL + "\n  path: x\n"
        out = ob.rewrite_repo_urls(text, "https://git.enclave/p.git", "reg:5000")
        self.assertIn("repoURL: reg:5000/charts", out)
        self.assertIn("- repoURL: https://git.enclave/p.git", out)
        self.assertNotIn("github", out)

    def test_an_oci_chart_source_becomes_the_enclave_registry_too(self) -> None:
        text = "sources:\n  - repoURL: docker.io/envoyproxy\n    chart: gateway-helm\n  - repoURL: oci://ghcr.io/x/charts\n    chart: y\n"
        out = ob.rewrite_repo_urls(text, "https://git.enclave/p.git", "reg:5000")
        self.assertEqual(out.count("repoURL: reg:5000/charts"), 2)
        self.assertNotIn("docker.io", out)


class OCIChartTests(unittest.TestCase):
    """#213: Envoy Gateway's gateway-helm and kube-prometheus-stack are pulled from OCI registries."""

    def test_oci_charts_are_listed_and_pulled_by_oci_reference(self) -> None:
        charts = {c["chart"]: c for c in ob.third_party_charts()}
        self.assertEqual(charts["gateway-helm"]["repo"], "docker.io/envoyproxy")
        self.assertEqual(charts["kube-prometheus-stack"]["repo"], "ghcr.io/prometheus-community/charts")
        tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, tmp, True)
        runner = FakeRunner()
        archive = ob.pull_chart(charts["gateway-helm"], {}, tmp, runner)
        (pull,) = runner.ran("helm", "pull")
        self.assertEqual(pull[2], "oci://docker.io/envoyproxy/gateway-helm")
        self.assertNotIn("--repo", pull)
        self.assertEqual(pull[pull.index("--version") + 1], charts["gateway-helm"]["version"])
        self.assertTrue(archive.exists())
        self.assertTrue(ob.is_oci("oci://ghcr.io/x") and ob.is_oci("docker.io/envoyproxy"))
        self.assertFalse(ob.is_oci("https://kyverno.github.io/kyverno"))

    def test_the_envoy_gateway_images_follow_the_pinned_chart_version(self) -> None:
        refs = set(ob.extra_images())
        version = {c["chart"]: c["version"] for c in ob.third_party_charts()}["gateway-helm"]
        self.assertIn(f"docker.io/envoyproxy/gateway:{version}", refs)
        self.assertTrue(any(r.startswith("docker.io/envoyproxy/ratelimit:") for r in refs))
        self.assertTrue(any(r.startswith("docker.io/envoyproxy/envoy:distroless-v") for r in refs))
        # The rate-limit store's image is pinned by the same digest in its manifest and in the bundle list.
        manifest = (ob.DEPLOY / "envoy-gateway" / "ratelimit-valkey.yaml").read_text(encoding="utf-8")
        (digest,) = re.findall(r"image: docker\.io/valkey/valkey:[^@\s]+@(sha256:[0-9a-f]{64})", manifest)
        self.assertIn(f"docker.io/valkey/valkey@{digest}", refs)


class OfflinePolicyTests(unittest.TestCase):
    POLICY = ob.OFFLINE_POLICIES / "verify-signed-images.yaml"

    def test_the_policy_trusts_a_static_key_and_not_sigstore(self) -> None:
        text = "\n".join(l for l in self.POLICY.read_text().splitlines() if not l.lstrip().startswith("#"))
        for needle in ("keyless", "rekor.sigstore.dev", "fulcio", "githubusercontent", "ghcr.io"):
            self.assertNotIn(needle, text)
        self.assertEqual(text.count("publicKeys:"), 2, "the image signature and the SBOM attestation both use the key")
        self.assertEqual(text.count("ignoreTlog: true"), 2)
        self.assertIn("failureAction: Enforce", text)
        self.assertIn("verifyDigest: true", text)

    def test_rendering_fills_in_the_registry_and_the_key_at_every_indent(self) -> None:
        tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, tmp, True)
        key = "-----BEGIN PUBLIC KEY-----\nMFkw\n-----END PUBLIC KEY-----"
        (out,) = ob.render_policies(key, "registry.enclave:5000", tmp)
        text = out.read_text()
        self.assertNotIn("REPLACE_WITH_COSIGN_PUBLIC_KEY", text)
        self.assertNotIn("REGISTRY_HOST/", "\n".join(l for l in text.splitlines() if not l.lstrip().startswith("#")))
        self.assertEqual(text.count("-----BEGIN PUBLIC KEY-----"), 2)
        self.assertIn("registry.enclave:5000/*", text)
        # the key stays inside its block scalar: every key line is indented deeper than `publicKeys:`
        for m in re.finditer(r"^(\s*)publicKeys: \|-\n((?:\1 +.*\n)+)", text, re.M):
            self.assertIn("MFkw", m.group(2))



class CLITests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp, True)

    def test_render_commands_write_their_files(self) -> None:
        import contextlib
        import io

        key = self.tmp / "cosign.pub"
        key.write_text(PEM)
        with contextlib.redirect_stdout(io.StringIO()):
            self.run_render(key)

    def run_render(self, key: Path) -> None:
        self.assertEqual(ob.main(["render-policies", "--key", str(key), "--registry", "reg:5000", "--out", str(self.tmp / "pol")]), 0)
        self.assertTrue((self.tmp / "pol" / "verify-signed-images.yaml").exists())
        self.assertEqual(ob.main(["render-argocd", "--git-url", "https://git.enclave/p.git", "--registry", "reg:5000", "--out", str(self.tmp / "argo")]), 0)
        self.assertTrue(list((self.tmp / "argo").rglob("root-dev.yaml")))

    def test_a_failure_is_exit_code_1_with_the_reason_on_stderr(self) -> None:
        import contextlib
        import io

        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            code = ob.main(["verify", str(self.tmp / "absent.tar.gz"), "--key", str(self.tmp / "absent.pub")])
        self.assertEqual(code, 1)
        self.assertIn("error:", err.getvalue())


class ThirdPartyImageTests(unittest.TestCase):
    """#603: the operator images of the third-party charts are in the bundle plan, each with a provenance entry."""

    def test_every_extra_image_has_a_usable_provenance_entry(self) -> None:
        prov = ob.load_provenance()
        manifest = {"images": [{"ref": r} for r in ob.extra_images()], "thirdPartyCharts": []}
        self.assertEqual(ob.missing_provenance(manifest, prov), [])

    def test_the_operator_images_follow_the_pinned_chart_versions(self) -> None:
        refs = set(ob.extra_images())
        charts = {c["chart"]: c["version"] for c in ob.third_party_charts()}
        self.assertIn(f"quay.io/strimzi/operator:{charts['strimzi-kafka-operator']}", refs)
        kafka = re.search(r"^\s*version:\s*(\S+)", (ob.DEPLOY / "kafka" / "kafka.yaml").read_text(encoding="utf-8").split("kafka:", 1)[1], re.M)
        self.assertIsNotNone(kafka)
        self.assertIn(f"quay.io/strimzi/kafka:{charts['strimzi-kafka-operator']}-kafka-{kafka.group(1)}", refs)
        for part in ("controller", "webhook", "cainjector", "acmesolver", "startupapicheck"):
            self.assertIn(f"quay.io/jetstack/cert-manager-{part}:{charts['cert-manager']}", refs, part)

    def test_the_plan_lists_the_operator_images(self) -> None:
        planned = {i["ref"] for i in ob.plan(FakeRunner())["images"]}
        self.assertTrue({"quay.io/strimzi/operator", "quay.io/jetstack/cert-manager-controller"} <= {ob.repository(r) for r in planned})

    def test_chart_images_renders_each_chart_with_the_repositorys_values(self) -> None:
        runner = FakeRunner()
        found = ob.chart_images(runner, Path(tempfile.mkdtemp()))
        self.assertEqual(set(found), {"kyverno", "temporal", "strimzi-kafka-operator", "cert-manager", "argo-cd", "gateway-helm",
                                      "kube-prometheus-stack"})
        self.assertIn("docker.io/library/postgres:17", found["kyverno"])
        renders = runner.ran("helm", "template")
        self.assertEqual(len(renders), 7)
        for c in renders:
            self.assertEqual("-f" in c, "temporal" in c[3], c)

    def test_chart_images_flag_images_are_found_and_write_is_idempotent(self) -> None:
        self.assertEqual({m.group(1) for m in ob.IMAGE_FLAG.finditer('args:\n- --acme-http01-solver-image=quay.io/jetstack/x:v1\n- "--other-image=a.io/b:1"')}, {"quay.io/jetstack/x:v1", "a.io/b:1"})
        path = Path(tempfile.mkdtemp()) / "extra-images.txt"
        path.write_text("# header\nquay.io/keycloak/keycloak:26.0.8\n", encoding="utf-8")
        images = {"kyverno": ["reg.kyverno.io/kyverno/kyverno:v1.1.0", "quay.io/keycloak/keycloak:26.0.8"]}
        self.assertEqual(ob.write_chart_images(images, path), ["reg.kyverno.io/kyverno/kyverno:v1.1.0"])
        self.assertEqual(ob.write_chart_images(images, path), [])
        text = path.read_text(encoding="utf-8")
        self.assertEqual(text.count("reg.kyverno.io/kyverno/kyverno:v1.1.0"), 1)
        self.assertEqual(text.count(ob.GENERATED_MARK), 1)


if __name__ == "__main__":
    unittest.main()
