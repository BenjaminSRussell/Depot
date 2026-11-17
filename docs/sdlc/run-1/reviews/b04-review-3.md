# B-04 re-review, round 3: commit e6e4bbd6 (branch claude/confident-gauss-ncr4l9, on top of 060f9f75)

Scope: the e6e4bbd6 diff, checked against the round-2 findings N1–N7. N5 stays open under #213.

No repository files were edited and no test database was used. The scratch files and the generated cache were removed. As instructed, I did not install promtool or any PromQL parser.

## Verdict: APPROVE

N1, N2, N3 and N7 are fixed in code and covered by tests that fail when the fix is undone; I mutated the N1 test in two ways to confirm. N4 and N6 are documented. The CI promtool step is correct.

What remains is one Minor, about documentation guidance (R1), and three Nits. None blocks the merge.

## Round-2 findings

| # | Status | Evidence |
|---|---|---|
| N1 | **Fixed** | See "N1 in detail" below. |
| N2 | **Fixed** | See "N2 in detail" below. |
| N3 | **Fixed** | `authz.rego` reads the key only inside `log_subject` through `object.get(opa.runtime().env, …)`, and the `log_pseudonym_key` rule is gone. `TestNoAuthzDocumentHoldsThePseudonymKey` evaluates the whole of `data.authz` (bundle files only, key set, an Envoy input). It confirms the key is read (`log_subject` and `envoy.record.subject` start with `pn_`) and that neither the padded nor the unpadded key appears anywhere. The rego test `test_no_answer_or_rule_holds_the_pseudonym_key` checks the same for the Envoy answer. See Nit R3 for what is left. |
| N4 | **Documented** | The ADR and gateway README say the Secret must hold fieldcrypt's current key until the charts are wired. See R1: that advice spreads the master key. |
| N5 | **Open by agreement** | Left under #213. |
| N6 | **Documented** | ADR-0017 and `docs/engineering/CONVENTIONS.md`: path identifiers must be unreserved ASCII, and free-text keys go in the query or the body. The ADR also notes that a live Envoy may decode `%73`. |
| N7 | **Fixed** | `authz.opaUnavailableAlert` defaults to true, and the dev Application sets it to false. Helm merges `valuesObject` deeply, so the other `authz` values are kept. The test asserts that the defaults render every alert and that dev renders every alert except `DepotAuthzOPAUnavailable`. |

### N1 in detail

**The rule object.**
- `templates/alerts.yaml` now renders a `monitoring.coreos.com/v1` `PrometheusRule` named `<release>-alerts`.
- Its labels are `app.kubernetes.io/name: observability` plus `prometheusRule.labels`, which defaults to `release: kube-prometheus-stack`.
- `spec.groups` holds all three groups. The two Alertmanager-receiver ConfigMaps are unchanged.
- I rendered the chart: `helm template` gives `kind: PrometheusRule`, `name: observability-alerts`, `release: kube-prometheus-stack`, and no alert ConfigMap.

**Does it match the kube-prometheus-stack selector?** Yes. `deploy/envs/dev/apps/kube-prometheus-stack.yaml:70-71` sets `ruleSelectorNilUsesHelmValues: false` and no `ruleSelector`. The chart therefore renders an empty `ruleSelector`, which selects every PrometheusRule. `ruleNamespaceSelector` is the chart default `{}`, meaning all namespaces, and the observability Application deploys to `observability`, the stack's own namespace (the new test asserts this). The `release: kube-prometheus-stack` label equals the stack's `releaseName`, so the rule is also selected under the chart's default selector.

**Sync order.** The PrometheusRule CRD comes from `prometheus-operator-crds` at wave -2, and the observability app runs at wave 0 with `SkipDryRunOnMissingResource`. The operator's admission webhook arrives with the stack at wave 3, so it validates the rule only on later syncs, and promtool in CI covers rule validity.

**The new test** (`test_every_alert_is_in_a_prometheusrule_the_dev_prometheus_selects`):
- It renders the chart with the defaults and with the dev `valuesObject`.
- It fails on any ConfigMap whose data contains `groups:`.
- It requires every PrometheusRule's labels to satisfy both the dev selector and the default selector.
- It compares the rendered alerts with the alerts listed in the template.
- It passes. Two mutations on scratch copies of the chart, with the test pointed at them, both failed it:

| Mutation | Result |
|---|---|
| Release label replaced with `team: depot` | fails with "not selected by {'release': 'kube-prometheus-stack'}" |
| Template turned back into a ConfigMap | fails with "alert rules in a ConfigMap are never loaded" |

**The CI step** (`.github/workflows/ci.yml:570-586`, read, not run).
- **Placement:** it is in the `deploy-release` job, after `setup-python` 3.12 (so `python` is 3.12, with pyyaml installed by an earlier step) and after `setup-helm`.
- **Download and checksum:** it downloads `prometheus-3.5.0.linux-amd64.tar.gz` into `$RUNNER_TEMP`. It checks the tarball with `curl … sha256sums.txt | grep " $dist.tar.gz$" | sha256sum -c -`. Under `set -euo pipefail`, a missing line or a mismatch fails the step.
- **Extraction:** `tar --strip-components=1 "$dist/promtool"` leaves `$RUNNER_TEMP/promtool`.
- **Rendering and checking:** `prometheus_rules.py` renders the default and dev variants, writes each `spec` as a `groups:` file, and fails if no PrometheusRule renders. Then `promtool check rules` runs on both files. The step is correct.
- **Local run:** `prometheus_rules.py` wrote `default-observability-alerts.yaml` (3 groups, 7 alerts) and `dev-observability-alerts.yaml` (3 groups, 6 alerts). Each file has only `groups`, and every rule uses only valid rule keys with a string `expr`. The `{{ $labels.* }}` annotations survive rendering.
- **Gap:** see Nit R2 on how the checksum is anchored.

### N2 in detail

- **Allow-list:** `fieldcryptServices` (`tools/depot-server/execrunner.go`) lists compliance, hr, migration, party, payables, receiving and sales. Those children inherit the supervisor's `FIELDCRYPT_*`. No child inherits the pseudonym key.
- **Precedence:** a child's own configuration is appended later, and os/exec keeps the last value of a duplicate name (case-insensitively on Windows).
- **Tests:**
  - `TestFieldcryptServicesAreTheOnesThatLoadKeys` keeps the list equal to the Go services whose `cmd` calls `fieldcrypt.FromEnv`.
  - `TestChildEnvDropsTheSupervisorsKeysUnlessTheChildSetsThem` covers inherit, drop and override.
- **Docs:** `docs/ops/windows-install.md` and the CHANGELOG describe the behaviour.
- **Non-Go services:** the Windows node and python features (workflow-ts, agents-ts, routing-py, scheduler-py) read `FIELDCRYPT_*` only for log pseudonymization, and that fails safe to `[redacted]`. Leaving them off the list is acceptable.

## New findings

### R1. Minor: the N4 documentation tells operators to copy the field-encryption master key into the ext_authz OPA's Secret
- **Where:** `docs/adr/0017-one-authorization-enforcement-model.md` ("Until it is wired, the Secret must equal fieldcrypt's current key") and the same sentence in `deploy/gateway/README.md` (Authorization section). I suggested this wording in round 2 without weighing the cost.
- **Failure scenario:** an operator follows the docs and puts fieldcrypt's current master key, base64, into Secret `depot-log-pseudonym`. That Secret is mounted into the `depot-authz-opa` pods beside Envoy. These pods never held field-encryption keys, and their only job is ext_authz. The raw master key is not only a MAC key: fieldcrypt derives the field-encryption keys from it too. So a compromised OPA pod, or anyone who can read that Secret, can now decrypt PII fields. This widens exactly the exposure round-1 finding 4 removed. It also breaks again at the next fieldcrypt key rotation.
- **Fix:** reverse the advice. `KeyFromEnv` already prefers `DEPOT_LOG_PSEUDONYM_KEY` over fieldcrypt's key. So give the seven fieldcrypt-keyed services the dedicated Secret (a `secretKeyRef` with `optional: true` in their charts); they and Envoy then share the dedicated key and the master key stays where it is. Until that is done, document that pseudonyms differ between Envoy and those services, and say never to copy the master key into `depot-log-pseudonym`.

### R2. Nit: the promtool checksum is anchored to the release, not pinned in the repository
- **Where:** `.github/workflows/ci.yml:577-581`.
- **Failure scenario:** the tarball and `sha256sums.txt` come from the same GitHub release. A replaced or tampered release asset passes the check, so the check guards only against transit corruption. The Kyverno step in the same job (`ci.yml:587-594`) pins `KYVERNO_SHA256` in the workflow, which is the repository's convention.
- **Fix:** add `PROMTOOL_TARBALL_SHA256: <hash of prometheus-3.5.0.linux-amd64.tar.gz>` and check with `echo "$PROMTOOL_TARBALL_SHA256  $dist.tar.gz" | sha256sum --check --strict`, as the Kyverno step does. Keep the release `sha256sums.txt` cross-check if wanted.

### R3. Nit: the key is still reachable through OPA's ad-hoc query API, and the supervisor's key file overrides a child's own keys
- **Ad-hoc query API:** `POST /v1/query` with `opa.runtime()` (or `opa.runtime().env.DEPOT_LOG_PSEUDONYM_KEY`) on the ext_authz OPA returns the key. The decision log records that result, because only the input is masked. The API listens on 127.0.0.1 inside the pod, so reaching it needs exec, which already exposes the environment. Accepted as residual.
  - **Fix (optional):** add a `system.authz` policy (OPA's `--authorization=basic`) that allows only the health and data paths the probes and ext_authz use. Or accept the residual and note it in the README.
- **Key file precedence:** if the supervisor sets `FIELDCRYPT_KEYS_FILE` and a listed child's own config sets `FIELDCRYPT_KEYS`, the child gets both. `fieldcrypt.FromEnvWith` reads the file first (`platform/go/fieldcrypt/fieldcrypt.go:87-94`), so the supervisor's keys win over the child's own, contrary to "its own configuration still wins".
  - **Fix:** in `childEnv`, skip all inherited `FIELDCRYPT_*` for a child whose own `Env` sets any `FIELDCRYPT_*`. Add a test for that case.

### R4. Nit: `DepotAuthzEnvoyCheckErrors` fires in dev whenever someone uses the API
- **Where:** `deploy/observability/charts/observability/templates/alerts.yaml`, the `DepotAuthzEnvoyCheckErrors` rule.
- **Failure scenario:** in dev the OPA is never ready, so every request through Envoy counts as an ext_authz error. N7 gated only the OPA-availability alert.
- **Fix:** gate this alert on the same value, or on a separate `authz.envoyAlerts` value.

## Tests run

| Command | Result |
|---|---|
| `opa test --timeout 600s policies/authz` | PASS 322/322 (3m00s) |
| `python3 deploy/scripts/test_argocd_apps.py` | 50 tests OK (including the new PrometheusRule test) |
| `python3 deploy/scripts/test_gateway.py` (kshim kubectl, real `opa eval`) | 65 tests OK |
| `python3 deploy/scripts/prometheus_rules.py --out <scratch>` | 2 rule files: default 7 alerts, dev 6 alerts; structure checked |
| `go test -count=1 ./...` (tools/depot-server), plus `-v` on the childEnv, allow-list and subject-key tests | ok; all PASS |
| `go test -count=1 -timeout 45m ./authztest/ ./cmd/devgateway/` (services/identity) | ok; `TestNoAuthzDocumentHoldsThePseudonymKey` PASS |
| `golangci-lint run ./...` (tools/depot-server), `./authztest/...` (services/identity) | 0 issues |
| N1 test mutations (label removed; back to a ConfigMap) | both fail as intended |

**Not run:**
- `promtool` (not installed, as instructed); the CI step was checked by reading it.
- Live Envoy and Prometheus (#213).
