# B-04 review: commit cecd9416 (branch worktree-agent-a37678d415803e290)

Reviewer: independent security and code review. No repository files were edited.

## Verdict: CHANGES REQUIRED

The main security properties hold:

- **Filter order.** `ext_authz` is moved after `jwt_authn`. The vendored v1.4.0 CRD's default order puts ext_authz first, so the `EnvoyProxy.filterOrder` entry is needed and is correct.
- **Client headers.** Client copies of `X-Depot-Tenant`, `-User` and `-Legal-Entity` are removed in `earlyRequestHeaders` before any filter runs. `test_gateway.py` checks that every header sent to OPA is both claim-mapped and stripped.
- **Fail-closed and scope.** `failOpen: false` is set. The policy sits on the Gateway, and the test refuses any route- or listener-level SecurityPolicy that would override it.
- **Rewrite table.** The table matches every generated HTTPRoute. I checked this independently against `httproutes.yaml`, not only through the test's Python copy.
- **Service checks.** No per-service check is loosened.
- **The `decision` change** (`{"allow":true,"duty":…}`) breaks no consumer.

Three Major issues block approval:

1. MCP is now refused at Envoy for everyone.
2. The decision-log mask lives in the bundle, so a bundle without it logs raw identities and client IPs.
3. On the Envoy path, the decision record cannot answer "who was refused", and the deny counter and alert cannot see Envoy denials.

## Findings

### 1. Major: every MCP request is refused at Envoy, because the duty catalogue names `/api/v1/mcp` but Envoy decides on `/mcp`
- **Where:** `policies/authz/authz.rego:122` (`"mcp": "/mcp"`), `policies/authz/data.json:1403` (`mcp.copilot.use` → `{"resource": "/api/v1/mcp"}`). The route is in `deploy/gateway/httproutes.yaml:1233-1252`: `/api/v1/mcp` and `/v1/mcp` are both rewritten to `/mcp`, and `services/mcp-ts/src/server.ts:170` serves only `/mcp`.
- **Failure scenario:** a user holding `mcp.copilot.use` sends `POST /api/v1/mcp`. `data.authz.envoy` computes the resource `/mcp`, no duty grants it, and Envoy answers 403 before mcp-gateway sees the request. I verified this with `opa eval`:
  - `POST /api/v1/mcp` → `{"allowed":false,"res":"/mcp"}`, and the same for `/v1/mcp` and `GET`;
  - for comparison, `POST /api/v1/copilot/chat` → `allowed:true`.
- **Why the tests miss it:**
  - ADR-0017 says the decision is taken "on the path the service serves, which is the path the duty catalogue is written against". For this entry that is not true.
  - Nothing checks the catalogue against the routes.
  - The shared route table deliberately leaves out the D22 prefixes.
  - `gateway_test.rego` covers only `agents`.
- **Fix:**
  - Change the `mcp.copilot.use` permission to `/mcp` (keep `/api/v1/mcp` only if another caller needs it). Check what mcp-ts does with sub-paths.
  - Add a check, rego or `test_gateway.py`, that every routed prefix's service base (`gateway_base(prefix)`) has at least one duty resource under it. Glob-aware, that check would have caught this. Also add one Envoy case per D22 prefix to `gateway_test.rego`.

### 2. Major: the decision-log mask is part of identity's bundle; a bundle without it logs the raw subject, tenant, client IP and full path
- **Where:** `deploy/gateway/authz-opa.yaml:55` (`mask_decision: /authz/decision_log_mask`), `policies/authz/authz.rego:279`. The `authz` root and `authz.rego` come from the identity image (`services/identity/Dockerfile:12` `COPY policies/authz/... authz.rego`).
- **Failure scenario:**
  1. Argo syncs `deploy/gateway` (the new OPA Deployment) while identity still runs an image built before cecd9416. Each is rolled out separately, and dev pins image tags per app.
  2. The OPA activates that older signed bundle, and `/health?bundles` reports ready.
  3. Envoy sends every request to OPA. `data.authz.envoy` is undefined, so every request is refused (fail-closed). `data.authz.decision_log_mask` is undefined too, and OPA then logs the event with no mask.
- **Verified:** I ran `opa run` with this commit's `labels` and `decision_logs` config against the parent commit's `authz.rego`. The console decision log contained, labelled `log_class: audit.other`:
  - `"input":{"attributes":{"request":{"http":{"headers":{"x-depot-tenant":"t1","x-depot-user":"raw-user-77"},"method":"GET","path":"/api/v1/x/secret-id?q=1"}},"source":{"address":{"socketAddress":{"address":"203.0.113.9"}}}}}`

  The same happens permanently if the rule is ever renamed, or the bundle compiler stops shipping it.
- **Fix:** make the mask independent of the bundle. Options:
  - **Local mask policy:** put the mask in its own package outside the bundle roots, for example `package system.log` with `mask contains "/input"` (OPA's default mask path). Ship it from the ConfigMap and pass the file to `opa run`. The bundle roots are `authz`, `assignments` and so on, never `system`, so the local file can be loaded.
  - **Readiness gate:** fail readiness unless `data.authz.decision_log_mask` and `data.authz.envoy` are defined, for example with an exec or HTTP probe against the loopback API.
  - Add a test that starts OPA with a bundle lacking the rule and asserts no input is logged.

### 3. Major: on the Envoy path, the D36 record cannot say who was refused, and the deny metric and alert cannot see it
- **Where:**
  - `policies/authz/authz.rego:234`: the Envoy record's `subject` is always `"[redacted]"`.
  - `deploy/observability/charts/observability/templates/alerts.yaml:29-32`: "Envoy-path denials are counted by OPA's decision log, not by this series".
  - `platform/go/httpx/httpx.go:177`: `LoadSubjectKey` reads `FIELDCRYPT_KEYS`, which about 11 of 72 service charts receive (`grep FIELDCRYPT_KEYS services/*/charts`).
- **Failure scenario:**
  - In Kubernetes the gateway now refuses unauthorized requests itself, so they never reach a service. The ADR's "the service behind Envoy writes its own record with the pseudonym under the same request id" holds only for allowed requests.
  - For every Envoy deny, the only record is `subject: "[redacted]"`. "Who was refused what" (A1, the reason for B-04) still cannot be answered on the production path.
  - The same probing never increments `depot_authz_denied_total`. `DepotAuthzDenySpike` is documented as catching "someone probing routes they hold no duty for", but in Kubernetes it fires only when Envoy and a service disagree.
  - Even for allowed requests, records from the roughly 61 services with no fieldcrypt key carry `[redacted]`.
  - The backlog acceptance asks for "pseudonymized subject… the three gateways" and for `depot_authz_denied_total`. The CHANGELOG admits the counter gap but not the subject gap.
- **Fix:**
  - **Pseudonyms in the bundle:** identity already compiles every `(tenant, idp_subject)` into the bundle. It could also emit `data.subject_pseudonyms[tenant][subject]`, computed with `logpseudo` under the same key, and `envoy_record` would look the value up (fall back to `[redacted]`).
  - **Envoy deny metric:** derive one, for example a recording rule from the OPA decision-log stream once it is shipped. Or have OPA expose a counter via an Envoy access-log field or `dynamic_metadata` that Envoy stats can count. Extend the alert to cover it.
  - **Dedicated pseudonym key:** give the services a MAC-only pseudonymization key (a separate Secret), instead of relying on field-encryption master keys that most services do not hold (see finding 4).

### 4. Minor: depot-server: following the code's advice to set `FIELDCRYPT_KEYS` puts the field-encryption master keys into every child service
- **Where:** `tools/depot-server/app.go:78-80` (the comment says to pseudonymize under "fieldcrypt's key (FIELDCRYPT_KEYS); without one every subject is redacted") and `tools/depot-server/execrunner.go:28` (`env := os.Environ()`, so children inherit the supervisor's whole environment).
- **Failure scenario:** an operator sets `FIELDCRYPT_KEYS` on the depot-server Windows service so the gateway's records carry pseudonyms. Every supervised process (60-plus services) then inherits the master key set, including services that never held it. In Kubernetes, `LoadSubjectKey` itself widens nothing: it parses only what a pod already has. But the design reaches its stated goal only if the keys are spread further.
- **Fix:**
  - Use a dedicated pseudonymization key variable (MAC-only, not fieldcrypt master keys).
  - In depot-server, strip `FIELDCRYPT_*` and any pseudonym key from the inherited environment unless a process's own config sets it.

### 5. Minor (uncertain: depends on Envoy's internal-address handling): a client may still choose the `x-request-id` in the Envoy record
- **Where:**
  - `deploy/gateway/securitypolicy.yaml:46` sends `x-request-id` to OPA, and `policies/authz/authz.rego:239` writes it into the record.
  - `deploy/gateway/clienttrafficpolicy.yaml:12-22` does not remove `X-Request-Id` and sets no `headers.requestID`.
- **Failure scenario:**
  - The CRD default `Generate` ignores a client value only on an *edge* request. Envoy treats a request as non-edge when its remote address is internal; depending on the Envoy version's `internal_address_config` default, RFC 1918 sources can count as internal.
  - So a client behind an SNAT load balancer, or calling from inside the cluster, may keep its own `X-Request-Id`.
  - That id is copied into the OPA record and forwarded to the service, whose chi `RequestID` middleware trusts it.
  - An attacker could then reuse a victim's request id to blur correlation between the audit trail's gateway and service records.
  - The single-host gateways drop the client value and test it. Envoy has no equivalent check.
- **Fix:**
  - Add `X-Request-Id` to `earlyRequestHeaders.remove`, so Envoy always generates the id, or set `headers.requestID: Generate` explicitly together with an explicit internal-address configuration.
  - Assert this in `test_gateway.py`.

### 6. Minor: Envoy's `canonical_path` accepts percent-encoded paths that the single-host gateways refuse, so "one model" diverges
- **Where:** `policies/authz/authz.rego:129-134`, which refuses only `%2f`, `%5c` and `%2e`. Compare `services/identity/cmd/devgateway/gateway.go:116-118` and the depot-server `canonicalPath`, which refuse any path with a non-empty `RawPath`.
- **Failure scenario:** `GET /api/v1/numbering/sequences/s1%3Ax` or `.../%73` with `numbering.read` gives `allowed:true` at Envoy (verified with `opa eval`), but 400 GW-0400 on devgateway and depot-server.
  - `%3A` is significant because the glob uses `:` as a segment delimiter. OPA matches `s1%3Ax` as one segment, while a service framework that routes on the decoded path (Starlette/FastAPI) sees `{id}:x` and could dispatch to a `/*:verb` action route.
  - I found no current duty pair where the `/*` route and the `/*:verb` route share a method but need different duties, so this is not exploitable today, and the service-level duty check still applies.
- **Fix:**
  - Refuse any `%` in the path in `canonical_path`, or at least `%3a` and any encoding of an unreserved character, to match the Go gateways.
  - Add encoded-path cases to the shared route table, so all three gateways must agree.

### 7. Minor (needs owner decision): machine callers with `depot-services` client-credentials tokens are now refused at Envoy unless identity holds role assignments for the service-account subject
- **Where:** `policies/authz/authz.rego:11-19` (grants come only from `data.assignments` and `data.scoped_assignments` keyed by `sub`), and `deploy/keycloak/README.md:17` (machine callers' duties come from attributes on the Keycloak service-account user).
- **Failure scenario:** before this change, Envoy validated the JWT and the service honoured the token's `duties` claim. Now OPA looks up `data.assignments[tid][<service-account sub>]`, which identity's bundle compiler (`identity_users` rows) has only if someone registered that subject. As a result, partners and machine callers on `/v1/<prefix>` get 403. The single-host gateways already behaved this way, but ADR-0017 and the gateway README do not mention it for Kubernetes.
- **Fix:** state the rule in ADR-0017 (machine callers need identity role assignments for their subject, or say how `service_principals` applies), and add one route-table case for a machine caller.

### 8. Nit: an OPA outage at Envoy gives a bare 403, not 503 AUTH-0503 as on the single-host gateways, and it is counted nowhere
- **Where:** `deploy/gateway/securitypolicy.yaml:40-50` (EG v1.4 exposes no `statusOnError`, so Envoy's default 403 applies), and `deploy/gateway/README.md` step 3 ("A denied request gets OPA's 403 AUTH-0003 problem").
- **Failure scenario:** with OPA not ready (missing Secret, no bundle) or timing out, clients see 403 with an empty body. They read it as an authorization failure rather than an outage. No record is written and the deny metric does not move. The fail-closed behaviour itself is correct.
- **Fix:** document the 403-on-error behaviour in the README and ADR, and alert on `depot-authz-opa` readiness or available replicas (for example kube-state-metrics `kube_deployment_status_replicas_available{deployment="depot-authz-opa"} == 0`).

### 9. Nit: the logpseudo handler now passes through any personal value that looks like a pseudonym
- **Where:** `platform/go/logpseudo/logpseudo.go:146` and `:273`.
- **Failure scenario:** under the eu-data-boundary profile, a user who sets a username to another user's pseudonym (`pn_` followed by 16 hex digits, read from a shared log) has their own log lines written with the victim's pseudonym. Low impact.
- **Fix:** skip re-pseudonymizing only for the authz record's own `subject` attribute. For example, wrap it in a marker type (a `slog.LogValuer` of a private type), so no string value is exempt.

### 10. Nit: test and record details
- `deploy/scripts/test_gateway.py` `rego_resource` is a Python copy of the rego regex. If the two drift, `test_the_policy_decides_on_the_path_each_generated_route_sends_to_its_service` still passes. Either run `opa eval data.authz.gateway_resource` there, or emit the generated route pairs as `policies/authz/testdata` and assert them in rego.
- `authz.GatewayRoute` (`platform/go/authz/record.go`) and rego `gateway_route` log the second path segment for any lowercase name, including unrouted ones. On devgateway and depot-server, OPA decides before upstream lookup, so a client can put an arbitrary lowercase token (for example a UUID) into the `route` field of a deny record. Map to `unmatched` unless the prefix is a configured upstream.

## Checks that passed (no finding)

- **Filter order:** the vendored CRD's documented default order puts `ext_authz` before `jwt_authn`, and `envoyproxy.yaml` fixes it. No GatewayClass-level EnvoyProxy exists for the Gateway-level one to replace. The other Gateways (as2, edge, portals, region, payments-webhook) do not reference it and are unchanged.
- **Header forgery:** `x-depot-tenant`, `x-depot-user` and `x-depot-legal-entity` are each removed in `earlyRequestHeaders` and set only by `claimToHeaders`. OPA receives only those three headers plus `x-request-id`; no `authorization` and no cookies.
- **Route exclusions:** no `/healthz`, webhook or SCIM route sits on `depot-api`. Every depot-api route is `/api/v1/<prefix>` or `/v1/<prefix>`. Payments-webhook, AS2, edge, region and portals are separate Gateways outside the SecurityPolicy, as before. Each is intentional and authenticated another way (Stripe signature, AS2 signature, mTLS, none for region health).
- **Rewrite table:** my own comparison of every `httproutes.yaml` match against `gateway_resource` found no mismatch, including the processmfg split routes. Prefix lookalikes (`/v1/agentsx`) do not route, because Gateway API PathPrefix matches whole segments. Uppercase, `..`, `//`, `%2f` and `%2e` are refused.
- **Envoy fail-closed paths:** undefined rule, OPA not ready, error and malformed answers all deny. The three `envoy` definitions are mutually exclusive, so the policy cannot hit an eval conflict.
- **Defence in depth:** `RequireDuty`, `RequireAnyDuty` and `Require` keep their decisions; only record calls were added. `allow`'s body is unchanged.
- **The `decision` shape change:** every consumer decodes it leniently: identity `OPAClient`, depot-server (through it), `tools/e2e/devenv`, policy bundle tests, and opa_contract_test. No other OPA data-API caller exists.
- **CI moves:** `tools/depot-server` is a single package, now in the identity shard, which installs OPA. `authztest` fails rather than skips under `CI`. The Windows job installs OPA. Every Go test still runs.
- **Rollout:** the refuse-everything window until `bundle.enabled` and the hand-made Secret exist is documented in the README, ADR, CHANGELOG and `test_argocd_apps.py`. Only dev deploys `deploy/gateway`, and that path did not work before (D32). I accept it, apart from finding 2.

## Tests run

All with `PATH=/root/go/bin:/usr/local/go/bin:/root/.local/bin:$PATH`, in the worktree unless noted.

| Command | Result |
|---|---|
| `opa test --timeout 600s policies/authz` | PASS 313/313 (2m52s) |
| `go test -count=1 ./authz/... ./logpseudo/... ./httpx/...` (platform/go) | ok |
| `go test -count=1 ./...` (tools/depot-server); `-v -run TestGatewayAnswersTheSharedRouteTable` | ok; all 14 subtests ran (OPA started, not skipped) |
| `go test -count=1 -run 'RequireOPA\|OPA\|Opa\|Record\|Decide' . ./authztest/ ./cmd/devgateway/` (services/identity) | ok (416 s) |
| `go test -count=1 -v ./authztest/ ./cmd/devgateway/` (services/identity) | ok, all PASS, including the decision-log test and route table |
| `python3 deploy/scripts/test_gateway.py` (kubectl shim `scratchpad/kshim/kubectl` → `kustomize build`) | 61 tests OK |
| `python3 deploy/scripts/gen_httproutes.py --check` | 83 routes up to date |
| `python3 deploy/scripts/test_argocd_apps.py` | 49 tests OK |
| `golangci-lint run ./...` in platform/go, services/identity, tools/depot-server | 0 issues each |
| `helm template` observability chart | `DepotAuthzDenySpike` renders as intended |
| `scripts/compile_architecture.py --check`, `docs/sdlc/run-1/backlog-src/gen.py --check` | up to date / all checks pass |

**Mutation (scratch copy, not the worktree).** I copied `policies/authz` to the scratchpad and flipped "create a sequence with only numbering.read" to `allow: true`. Then:

- **Rego:** `opa test` on the copy failed `test_envoy_answers_the_route_table` and `test_envoy_records_every_route_table_decision` (FAIL 2/2).
- **Go:** I ran both gateways' tests with `go test -overlay`, replacing `route_table_test.go` with a copy whose `policyDir` pointed at the mutated copy. Both depot-server and devgateway failed: `want allowed, got 403`.

The route-table tests are sensitive to a wrong decision.

**Ad-hoc checks.**

- `opa eval` of `data.authz.envoy` for the MCP, copilot and percent-encoded paths (findings 1 and 6).
- `opa run` with the shipped decision-log config against the parent commit's `authz.rego` (finding 2: unmasked input logged).
- The scratch files were removed after the run.
