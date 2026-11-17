#!/usr/bin/env python3
"""Write the alerting rules the observability chart renders as Prometheus rule files, for `promtool check rules`.

The chart renders its rules as PrometheusRule objects (kube-prometheus-stack loads no other kind); the operator hands
each object's spec, `groups: [...]`, to Prometheus as one rule file. This renders the chart twice, with its defaults
(stage, prod) and with the values the dev Argo CD Application gives it, and writes each PrometheusRule's spec to
<out>/<variant>-<name>.yaml. CI runs promtool on the result (.github/workflows/ci.yml, deploy-release).

Usage: prometheus_rules.py --out DIR
"""
import argparse
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml

REPO = Path(__file__).resolve().parents[2]
CHART = REPO / "deploy/observability/charts/observability"
DEV_APP = REPO / "deploy/envs/dev/apps/observability.yaml"


def dev_values() -> dict:
    (app,) = [d for d in yaml.safe_load_all(DEV_APP.read_text()) if d and d.get("kind") == "Application"]
    return app["spec"]["source"].get("helm", {}).get("valuesObject", {})


def render(values: dict) -> list[dict]:
    with tempfile.NamedTemporaryFile("w", suffix=".yaml") as f:
        yaml.safe_dump(values, f)
        f.flush()
        out = subprocess.run(["helm", "template", "observability", str(CHART), "-f", f.name],
                             check=True, capture_output=True, text=True).stdout
    return [d for d in yaml.safe_load_all(out) if d and d.get("kind") == "PrometheusRule"]


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--out", required=True, type=Path)
    args = parser.parse_args()
    args.out.mkdir(parents=True, exist_ok=True)
    written = 0
    for variant, values in (("default", {}), ("dev", dev_values())):
        rules = render(values)
        if not rules:
            print(f"{variant}: the chart rendered no PrometheusRule", file=sys.stderr)
            return 1
        for rule in rules:
            path = args.out / f"{variant}-{rule['metadata']['name']}.yaml"
            path.write_text(yaml.safe_dump(rule["spec"], sort_keys=False))
            alerts = [r["alert"] for g in rule["spec"]["groups"] for r in g["rules"] if "alert" in r]
            print(f"{path}: {len(rule['spec']['groups'])} groups, alerts {', '.join(alerts)}")
            written += 1
    print(f"wrote {written} rule files to {args.out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
