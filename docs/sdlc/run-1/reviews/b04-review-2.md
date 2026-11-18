# B-04 re-review: fix commit c62753bb (branch claude/confident-gauss-ncr4l9, on top of e6078b2e)

Reviewer: independent re-review of c62753bb only. No repository files were edited. Scratch copies, the test database and the schema cache the run generated were all removed afterwards.

Base check: on the B-04 files, e6078b2e matches the cecd9416 I reviewed in round 1. `git diff cecd9416 e6078b2e` touches only `legal_entities_api.go` and `role_assignments_api.go`, which come from merges.

## Verdict: CHANGES REQUIRED

All ten round-1 findings are fixed in the code, and the fixes are well tested. The rego reproduction of logpseudo matches Go. The local mask and readiness rule hold against a bundle that lacks them. The `%` refusal is consistent at all three gateways. I mutated each new safeguard and each mutation made a test fail.

One new Major blocks approval. Every `depot.authz` alert lives in a plain ConfigMap that no Prometheus loads, so none of them, old or new, can fire in any environment. This also defeats the alert half of round-1 finding 3. The mechanism predates B-04 (the chart's SLO and B-05b alerts sit in the same ConfigMap), and I missed it in round 1. The fix is small.

## Round-1 findings: status

| # | Round-1 finding | Status |
|---|---|---|
| 1 | MCP refused at Envoy | **Fixed.** `data.json:1403` now has `{"resource": "/mcp", "methods": ["POST"]}`; mcp-ts answers only POST on `/mcp` (`server.ts:170-174`). `gateway_test.rego` has `d22_cases`: allow with the duty and refuse without it, on both public paths, for all seven D22 prefixes, plus a check that every rewritten base has a permission. `test_gateway.py::test_every_routed_service_base_has_a_duty_permission_under_it` covers every routed base. |
| 2 | Mask in the bundle | **Fixed.** See "Local mask and readiness" below. |
| 3 | Envoy record has no subject; denies not counted or alerted | **Subject: fixed** (rego pseudonym, below). **Counting: fixed** (`envoy-podmonitor.yaml`). **Alerting: not effective** (new finding N1). Distributing the dedicated key to the service charts is a documented follow-up (N4). |
| 4 | depot-server spreads fieldcrypt keys | **Fixed.** `childEnv` (`tools/depot-server/execrunner.go:58`) drops `FIELDCRYPT_*`, `DEPOT_LOG_PSEUDONYM_KEY` and `_FILE` (case-insensitive) unless the child's own config sets them. `subjectKeyEnv` stops the gateway from reading fieldcrypt keys. Both are tested in `execrunner_env_test.go`. There is an operational side effect (N2). |
| 5 | Client-chosen x-request-id at Envoy | **Fixed in config.** `clienttrafficpolicy.yaml:27-28` removes `X-Request-Id` early and sets `requestID: Generate`, and `test_gateway.py` asserts both. Live behaviour is not verified (N5). |
| 6 | Percent-encoding divergence | **Fixed.** `authz.rego:135` refuses any `%`. Both Go gateways refuse `RawPath != ""` or any `%` in `EscapedPath()` (`devgateway/gateway.go:121`, `depot-server/gateway.go:211`), which also catches the default-canonical `%20` and `%C3%A9` they used to pass. Encoded cases (`%3A`, `%73`, `%20`) are in the shared route table with expected status 400, and all three suites pass them. |
| 7 | Machine callers refused | **Fixed (documented).** ADR-0017 states the rule, and the route table gained a `token_duties_only` case that every gateway refuses. |
| 8 | Bare 403 on an OPA outage | **Documented**, and alerts were added; but see N1. |
| 9 | Pseudonym look-alike passthrough | **Fixed.** `SubjectAttr` marks its value with the unexported type `pseudonymized` (a `LogValuer`). The handler passes only that type; `pseudonymRE` is gone. |
| 10 | Python copy of the regex; route field leaks | **Fixed.** `test_gateway.py` now calls `opa eval` against `policies/authz` (`opa_query`, `rego_resources`), and CI installs OPA for that job (`ci.yml:563`). `authz.GatewayRouteOf` writes `unmatched` for any prefix the gateway does not route. |

## Points the coordinator asked about

**Rego logpseudo against Go (`authz.rego:283-314`).** Each step matches Go's `fieldcrypt.Keys.MACWithKey` and `scrubber.pseudonym`:

| Step | Go | Rego |
|---|---|---|
| HKDF-Extract | key = salt (tenant), message = IKM | `crypto.hmac.sha256(ikm, tenant)`, argument order (x, key) |
| HKDF-Expand, one block | HMAC(PRK, `passage.fieldcrypt.mac.v1` ‖ 0x01) | same |
| MAC | HMAC(OKM, `passage.log.pseudonym.v1\x00` ‖ lower(trim(value))) | same |
| Output | first 8 bytes, as hex | first 8 bytes, as hex |

- **Key handling:** `KeyFromEnv` uses `base64.StdEncoding` and requires 32 bytes; rego uses `base64.decode` and checks for 64 hex characters. They agree.
- **Case and whitespace:** rego's `lower` and `trim_space` are `strings.ToLower` and `strings.TrimSpace`, as in Go.
- **Edge values:** empty user gives `""`; empty tenant, no key or a bad key give `[redacted]`. The `log_subject` clauses are mutually exclusive.
- **Tested:** `TestRegoPseudonymMatchesGo` compares 24 vectors against Go, including non-ASCII values and keys whose bytes are not UTF-8. A fixed vector is also in `gateway_test.rego`.
- **Mutation:** I changed the HKDF info string in a scratch copy. All 24 vectors and the end-to-end record test failed.

**Local mask and readiness (`authz-opa.yaml`).**
- **Mask:** `mask.rego` (`package system.log`, `mask contains "/input"`) and `health.rego` are loaded as command-line policies, and `mask_decision: /system/log/mask` points at the local rule.
- **Bundle roots:** the bundle roots never claim `system`. `bundle_roots_test.go` checks this, and also checks that authztest uses the same roots. A bundle that shipped `package system.log` would sit outside its declared roots and OPA would refuse to activate it, so the bundle cannot override the mask.
- **Readiness:** `/health/ready` on the diagnostic port requires `plugins_ready`, the mask loaded, and `data.authz.envoy` answering.
- **Tests:** `TestExtAuthzMaskDoesNotDependOnTheBundle` serves a bundle with no mask and no envoy rule. No raw user, tenant, IP, path or token appears in the log, and the pod is not ready. Both directions mean something, because the first test needs a 200 from the same endpoint.
- **Mutations:**
  - narrowing the mask to the authorization header made both tests fail and logged the raw user;
  - deleting the envoy condition from `health.rego` made the readiness assertion fail.

**childEnv.** Verified as described in the table. Windows' hidden `=C:` variables (empty name) pass through unchanged, which is fine. Other inherited secrets, such as Azure credentials, are out of scope.

**`%` at all three gateways.** Verified, and the shared table enforces it. Note that a live Envoy may decode `%73` to `s` during path normalization before ext_authz runs. It would then allow that request instead of answering 400. That is harmless, because the service receives the same normalized path, but the table's claim that every gateway answers 400 holds for the rego, not necessarily for a live Envoy. See also N6.

**Alerts and PodMonitor.**
- **PodMonitor:** the selector, port (`metrics`, `/stats/prometheus`) and keep-relabel are reasonable. kube-prometheus-stack selects every PodMonitor. The series name `envoy_cluster_ext_authz_denied` and `_error` follows Envoy's documented `cluster.<target>.ext_authz.*` stats. Not verified against a live Envoy (#213), and it assumes Envoy Gateway sets no ext_authz `stat_prefix`.
- **Alert expressions:** I checked them by hand, since promtool is not installed and installing a PromQL parser was not permitted here. `sum(...) > x unless sum(...) <= k * sum(... offset 1h)` is valid PromQL and matches on the empty label set. `max(...) < 1 or absent(...)` is valid. `helm template` renders all four.
- **But the rules are never loaded:** N1.

**test_gateway.py against the real rego.** It runs `opa eval` for every public path of every generated route, plus a sub-path, and for encoded, relative and unrouted paths. It no longer reads the regex out of the file. 65 tests OK.

## New findings

### N1. Major: no Prometheus loads the `depot.authz` alerts, so none of them can fire
- **Where:** `deploy/observability/charts/observability/templates/alerts.yaml:1-2` (`kind: ConfigMap`) and `:32-79` (`DepotAuthzDenySpike`, `DepotAuthzEnvoyDenySpike`, `DepotAuthzEnvoyCheckErrors`, `DepotAuthzOPAUnavailable`).
- **Failure scenario:**
  - In dev, Prometheus is kube-prometheus-stack, which loads rules only from `PrometheusRule` objects (`deploy/envs/dev/apps/kube-prometheus-stack.yaml:71` `ruleSelectorNilUsesHelmValues: false`, and `deploy/observability/README.md`).
  - The observability chart renders this ConfigMap, with a `depot-slo.yaml` key and no `grafana_alert` label. It renders no Prometheus and no `rule_files`, and nothing references the ConfigMap (`grep` for `-alerts` across `deploy` finds nothing).
  - So a spike of Envoy refusals, an OPA outage (every API request refused with 403) or a service-level deny spike raises no alert anywhere.
  - Round-1 finding 3's alert half and finding 8's OPA-availability alert are therefore not effective.
  - This predates B-04: the chart's SLO alerts and the B-05b consumer alert share the ConfigMap.
- **Fix:**
  - Render the `depot.authz` group (ideally every group in the file) as a `monitoring.coreos.com/v1` `PrometheusRule` from the observability chart. The CRD is installed at wave -2 by `prometheus-operator-crds`.
  - Add a test (`test_argocd_apps.py` or a chart test) asserting each alert name appears in a rendered `PrometheusRule`.
  - While there, run `promtool check rules` on the rendered output in CI. promtool is not installed here, so the expressions are only hand-checked.

### N2. Minor: on Windows, services that need fieldcrypt keys no longer receive keys set on the depot-server process
- **Where:** `tools/depot-server/execrunner.go:58-95` (`childEnv`, `keyVariable`) and `tools/depot-server/config.go:282-288` (every `bin\*.exe` runs).
- **Failure scenario:**
  - An installation that set `FIELDCRYPT_KEYS` (or `_FILE`) on the depot-server Windows service is upgraded. Until this commit, inheriting that environment was the only path, and it is undocumented.
  - After the upgrade, hr exits at start (`services/hr/cmd/hr/main.go:133-140`: "FIELDCRYPT_KEYS or FIELDCRYPT_KEYS_FILE is required").
  - party can no longer store or read vendor bank accounts, and sales refuses one-time customer orders.
  - compliance, migration, payables and receiving also load fieldcrypt keys (`grep fieldcrypt.FromEnv services/*/cmd`).
  - The supervisor's own health then reports hr down. Neither the CHANGELOG nor `docs/ops/windows-install.md` tells the operator to move the keys into each service's `env:`.
- **Fix:**
  - Document the per-service setting in `docs/ops/windows-install.md` and the CHANGELOG upgrade notes: `services: - name: hr, env: {FIELDCRYPT_KEYS_FILE: ...}` for the seven services. Prefer a file path over inline keys in `config.yaml`.
  - Or give depot-server an explicit allow-list (for example `fieldcryptServices: [hr, party, sales, ...]`) that passes `FIELDCRYPT_*` to exactly those children.
  - Add a test that a listed service receives the keys and an unlisted one does not.

### N3. Nit: the pseudonymization key is an evaluable rule of the bundle
- **Where:** `policies/authz/authz.rego:314` (`log_pseudonym_key := trim_space(opa.runtime().env.DEPOT_LOG_PSEUDONYM_KEY)`).
- **Failure scenario:** `GET /v1/data/authz` (or `/v1/data/authz/log_pseudonym_key`) on any OPA that has the variable returns the raw key. The decision logger then writes that result to the pod log (only the input is masked). On the ext_authz OPA the REST API is loopback-only, so reaching it needs exec into the pod, which already exposes the environment. Low risk, but the key should not be a named document.
- **Fix:** inline the lookup inside `log_subject` (for example `log_pseudonym(trim_space(object.get(opa.runtime().env, "DEPOT_LOG_PSEUDONYM_KEY", "")), …)`), so no rule evaluates to the key. Add a rego test that `data.authz` contains no 44-character base64 value when the variable is set.

### N4. Nit (documented follow-up): Envoy and the fieldcrypt-keyed services can pseudonymize the same user differently
- **Where:** `platform/go/logpseudo/logpseudo.go` `KeyFromEnv`, which falls back to fieldcrypt's current master key. No service chart mounts `depot-log-pseudonym`.
- **Failure scenario:** with Secret `depot-log-pseudonym` created, the Envoy record carries `pn_A` for a user. hr, party, sales and the other fieldcrypt-keyed services fall back to fieldcrypt's current key and write `pn_B` for the same user. All other services write `[redacted]`. Cross-record correlation by subject fails until the key is wired into every chart. ADR-0017 records this as a follow-up.
- **Fix:** track the chart wiring as a backlog item tied to B-04's D36 exit. Until then, state in the gateway README that the Secret's bytes must equal fieldcrypt's current key for pseudonyms to match.

### N5. Nit (unverified, needs a live Envoy, #213): removing `X-Request-Id` early relies on Envoy applying early header mutations before it generates the request id
- **Where:** `deploy/gateway/clienttrafficpolicy.yaml:27`.
- **Failure scenario:** if Envoy generated the id before applying `earlyRequestHeaders`, it would delete its own id. Records would then carry `request_id: ""`, and each service would mint its own id, breaking gateway-to-service correlation. Security is not affected. My understanding of Envoy's HCM order (early mutation first) says this works.
- **Fix:** confirm on the #213 live check, and assert there that a non-empty `x-request-id` reaches OPA and the upstream.

### N6. Nit: refusing every `%` will also refuse legitimate encoded identifiers
- **Where:** `policies/authz/authz.rego:135`, `devgateway/gateway.go:121`, `depot-server/gateway.go:211`.
- **Failure scenario:**
  - The web shell builds about 1,150 paths with `encodeURIComponent`. Any identifier containing a space, a non-ASCII letter, `@` or another reserved character is now refused with 400 at every gateway. Before, Envoy forwarded these, and the Go gateways forwarded `%20` and `%C3%A9`.
  - I found no such identifier today: the codes I checked are pattern-restricted (planning `^[A-Z0-9][A-Z0-9_-]{1,19}$`, inventory unit and account codes) and the rest are UUIDs.
  - But nothing prevents a future free-text key.
- **Fix:** record the rule (path identifiers must be unreserved ASCII) in the API style guide or `gen_httproutes.py`'s checks, so a new route with a free-text path parameter fails review rather than in production.

### N7. Nit: `DepotAuthzOPAUnavailable` will fire permanently in dev
- **Where:** `alerts.yaml:71-79`.
- **Failure scenario:** `bundle.enabled` is off in dev, so the OPA is never ready and the alert is always active once N1 is fixed. Operators learn to ignore it.
- **Fix:** gate the group on a chart value (for example `authz.envoyAlerts: true`, set where the bundle is enabled), or add an inhibition.

## Tests run

All with `PATH=/root/go/bin:/usr/local/go/bin:/root/.local/bin:$PATH` in `/home/user/Passage` (branch claude/confident-gauss-ncr4l9, clean tree).

| Command | Result |
|---|---|
| `opa test --timeout 600s policies/authz` | PASS 321/321 (3m32s) |
| `go test -count=1 ./authz/... ./logpseudo/... ./httpx/... ./fieldcrypt/...` (platform/go) | ok |
| `go test -count=1 ./...` (tools/depot-server) | ok |
| `go test -count=1 -timeout 45m -v ./authztest/ ./cmd/devgateway/` (services/identity) | all PASS (including `TestExtAuthzDecisionLogKeepsTheRecordAndNoRawSubject`, `TestExtAuthzMaskDoesNotDependOnTheBundle`, `TestRegoPseudonymMatchesGo`, and the route table with the encoded and machine-caller cases) |
| `go test -count=1 -timeout 45m -run 'Bundle\|RequireOPA\|OPA\|Opa\|Record\|Sidecar\|Decide' -v .` (services/identity; `DEPOT_TEST_DATABASE_URL` and `DEPOT_TEST_APP_DATABASE_URL` on a database `rev_b04b_identity`, dropped afterwards) | ok (470 s): 21 tests PASS, 0 skipped, including `TestBundleRootsLeaveSystemToTheLocalPolicies`, every compiled-bundle and bundle-endpoint DB test, `TestOPAAcceptsTheSignedBundleAndRejectsTampering` and `TestSidecarConfigActivatesTheSignedBundle` |
| `python3 deploy/scripts/test_gateway.py` (kshim kubectl, real `opa eval`) | 65 tests OK |
| `python3 deploy/scripts/gen_httproutes.py --check` | 83 routes up to date |
| `python3 deploy/scripts/test_argocd_apps.py` | 49 tests OK |
| `make -C deploy validate-gateway-schema` | 113 valid, 0 invalid, 8 skipped (cache dir removed afterwards) |
| `golangci-lint run ./...` in platform/go, services/identity, tools/depot-server | 0 issues each |
| `helm template` observability chart | all four `depot.authz` alerts render; rendered as a ConfigMap (N1) |

**Mutations.** Each ran on a scratch copy, through `go test -overlay` repointing `decisionlog_test.go`'s paths; each made a test fail:

| Mutation | Failing tests |
|---|---|
| HKDF info `…mac.v1` → `…mac.v2` in `authz.rego` | `TestRegoPseudonymMatchesGo` (all 24 vectors), `TestExtAuthzDecisionLogKeepsTheRecordAndNoRawSubject` |
| `mask.rego` narrowed to the authorization header | `TestExtAuthzMaskDoesNotDependOnTheBundle` (raw user logged), `TestExtAuthzDecisionLogKeepsTheRecordAndNoRawSubject` (never ready) |
| envoy condition removed from `health.rego` | `TestExtAuthzMaskDoesNotDependOnTheBundle` ("ready with a bundle that cannot answer data.authz.envoy (200)") |

**Not verified:**
- promtool syntax (not installed; installing a PromQL parser was not permitted here);
- live Envoy behaviour: filter order, stats names, early-header ordering (#213).
