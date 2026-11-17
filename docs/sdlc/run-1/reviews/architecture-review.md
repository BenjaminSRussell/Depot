# Stage 2 (Architecture) independent review: commit 9d8507bc

- Under review: `docs/architecture.md` v1.0 "as built", ADR-0004 (now Accepted), ADR-0006 to ADR-0015, `docs/sdlc/run-1/architecture-delta.md`.
- Reviewer: independent; did not write the change. Read-only on the repository (working tree at e41a5c0e, which contains 9d8507bc).
- Method: read the commit and the delta; checked as-built claims against `services/`, `platform/go/`, `deploy/`, `apps/`, `tools/`, `.github/workflows/` with grep and small parse scripts (OpenAPI and AsyncAPI parsed with PyYAML, realm JSON parsed with Python). Nothing was run against a live cluster.

## 1. Claim checks

Verdicts: **OK** = matches code; **Wrong** = contradicted by code; **Imprecise** = substantially right but misleading as worded.

| # | Claim | Location | Verdict | Evidence |
|---|---|---|---|---|
| 1 | 71 service directories: 58 Go, 7 TS, 6 Python | Status; §3.2; ADR-0008 | OK | `ls services` = 71 dirs plus README; 7 `-ts`, 6 `-py`; every other dir has `go.mod` |
| 2 | 69 charts; `dispatch-py`, `routing-py` have none | §3.2; ADR-0008 | OK | `services/*/charts` present for 69 dirs |
| 3 | 69 services have an Argo CD application in dev and stage | Status | OK | `deploy/envs/{dev,stage}/apps/services.yaml` list 65 each, plus hand-written fx, identity, org, receiving |
| 4 | Prod deploys only `hello-ledger` and `kyverno` | Status; §7.20; D4 | OK | `deploy/envs/prod/apps/` holds those two files |
| 5 | Stage has no Kafka, Temporal, Postgres, Keycloak or gateway apps | §7.20; D4 | OK | `deploy/envs/stage/apps/`: fx, hello-ledger, identity, kyverno, org, receiving, services |
| 6 | "63 of the 65 dev service releases share namespace `hello-ledger`" | Status corrections; ADR-0006 Context; Security & Auth ("63 services") | Imprecise | True of the ApplicationSet only. fx, identity, org and receiving Applications also deploy to `hello-ledger`, so **67 of 69** service charts share it |
| 7 | 13 of 69 charts ship a NetworkPolicy (list given) | D1; ADR-0006 | OK | grep `kind: NetworkPolicy` in `services/*/charts/*/templates`: agents-ts, carrier-gateway-ts, customer-portal-ts, edi, fx, identity, integration-hub-ts, mcp-ts, migration, org, receivables, supplier-portal-ts, workflow-ts |
| 8 | fx's policy admits every pod in the namespace by default; no default-deny anywhere | ADR-0006 | OK | `services/fx/charts/fx/templates/networkpolicy.yaml` falls back to `podSelector: {}`; no default-deny policy in `deploy/` or charts |
| 9 | Envoy strips client `X-Depot-*` in `earlyRequestHeaders` | ADR-0006; §7.3 | OK | `deploy/gateway/clienttrafficpolicy.yaml` removes Tenant, Legal-Entity, User, Duties, Tier, Auth-Time, LegalEntity, Service-Token, Gateway-Secret |
| 10 | Envoy maps `tid`, `le`, `sub`, `duties`, `tier`, `auth_time` to headers | §7.3; ADR-0006 | OK | `deploy/gateway/securitypolicy.yaml` `claimToHeaders` (ADR-0006 omits `auth_time` from its list but rule 5 relies on it) |
| 11 | Envoy validates Keycloak tokens of realm `depot` ("one issuer", ADR-0007) | §7.1 Validation; ADR-0006; ADR-0007 Consequences | **Wrong** | SecurityPolicy issuer is `https://idp.dev.depot.dev/realms/dev` with `localJWKS` from a static placeholder key (`deploy/gateway/README.md:60`: "nothing can mint accepted tokens until #80"). `agent-runtime` values use `realms/dev` (dev) and `realms/stage` (stage). The realm file is `depot` |
| 12 | No ext_authz in Kubernetes | §7.2; ADR-0006 | OK | no `extAuth`/`ext_authz` in `deploy/gateway`, `deploy/envoy-gateway` |
| 13 | Realm `depot`, access token 300 s, SSO idle 30 min, SSO max 10 h, refresh rotation reuse 0 | §7.1; ADR-0009 | OK | `realm-depot.json`: 300, 1800, 36000, `revokeRefreshToken` true, `refreshTokenMaxReuse` 0 |
| 14 | Clients `depot-web` (public, PKCE S256), `depot-services`, `depot-identity-deprovision`, two portal clients | ADR-0007; frontend §8.1 | OK | parsed `clients` in `realm-depot.json` |
| 15 | "All tokens carry `aud: depot-api`" | §7.1 Claims; frontend §8.1; ADR-0007 | **Wrong** | `audience-depot-api` mapper only on `depot-web` and `depot-services`; portal clients and `depot-identity-deprovision` have none |
| 16 | Shell tokens in memory; `sessionStorage` only for the PKCE pending state | ADR-0009 | OK | `apps/web/src/shell/auth/auth.ts:3,24,41` (`depot.oidc.pending`) |
| 17 | 56 services enforce `authz.RequireDuty` | §7.2; ADR-0006 | OK | 56 with `RequireDuty`/`RequireAnyDuty` (55 with `RequireDuty` alone) |
| 18 | "Every OpenAPI operation declares its duty in `x-depot-duty` (97.6%)" | ADR-0006 | Imprecise | 3,116 of 3,194 operations = 97.6%; "every" contradicts the figure |
| 19 | OPA only in identity (`RequireOPA…`), devgateway and `depot-server`; decision logs off | §7.2; ADR-0006 | OK | `services/identity/opa.go`, `scim_api.go`, `cmd/devgateway/gateway.go`, `tools/depot-server/gateway.go`; `deploy/policies/opa/README.md:12` |
| 20 | Header names `X-Depot-LegalEntity`, `X-Depot-LegalEntity-Scope`, duty `org.switcher.all-scope`, `DEPOT_GATEWAY_SECRET` | §7.3; ADR-0006 | OK | `platform/go/tenant/tenant.go:104-150,194` |
| 21 | The shell's `X-Depot-LegalEntity` narrows the token's legal entities to one; it can narrow but never widen | §7.3 step 2; ADR-0006 rule 4; frontend §5.2; D23 backlog | **Wrong** | `tenant.requestLegalEntity` (`tenant.go:232-245`) rejects with 400 any value that differs from `X-Depot-Legal-Entity` (test `TestMiddlewareBothLegalEntityHeaders`); devgateway and `depot-server` return 403 unless the selection equals the claim exactly (`gateway.go:54`, `:71`). The `le` mapper is a single string (`multivalued: false`). Nothing narrows |
| 22 | Envoy strips the selection; devgateway and `depot-server` forward it | §7.3; D23 | OK | `clienttrafficpolicy.yaml` removes `X-Depot-LegalEntity`; both gateways set `HeaderWebLegalEntity` |
| 23 | `depot-server` sets a per-start gateway secret that the kernel then requires | §7.21; ADR-0006 | OK | `tools/depot-server/app.go:70-74`, `keys.go:77`; `tenant.go:292` |
| 24 | Service token: context `depot.service-token.v1.`, max 60 s, skew 5 s, 4 KiB, `service_token_replay` | ADR-0010 | OK | `platform/go/tenant/service.go:29,40,49-52,60`; `dbx.go:314` |
| 25 | Outbox tables in 58 services, 37 with no connector, 21 connectors, no `cdc` connector | Status; §7.6; D2 | OK | migration/DDL grep = 58; 21 `KafkaConnector` (`outbox-*`) in `deploy/kafka/connect.yaml`; set difference = 37 |
| 26 | Relayed / not relayed per context | §3.2 | OK | set difference matches every row (37 not relayed) |
| 27 | 358 topics published in 56 AsyncAPI specs | Status; events §4.2 | OK | parsed `send` operations → 358 distinct channel addresses |
| 28 | Consumers in 17 Go services plus workflow-ts and integration-hub-ts; "each has a `KafkaUser`" / "one SCRAM KafkaUser per consumer service" | §7.6; events §4.1 | **Wrong** (second half) | 17 Go consumers confirmed. KafkaUsers exist only for billing, edi, intercompany, inventory, payables, receivables, receiving, revrec, scheduling, search, tax, prediction, workflow-ts. **None** for finreport, fulfillment, pricing, production, purchasing, shopfloor, integration-hub-ts, though their charts name users such as `fulfillment-consumer` and Kafka uses `scram-sha-512` |
| 29 | Retention 7 d; 10 attempts, backoff capped at 2 min; DLQ `<topic>.dlq` | §7.6; D24 | OK | `connect.yaml:417`; `platform/go/eventing/consumer.go:21-23` |
| 30 | fieldcrypt imported by compliance, hr, migration, party, payables, receiving, sales (not metadata) | Status corrections; §7.16 | OK | grep imports of `platform/fieldcrypt`; metadata only generates code that names it |
| 31 | ADR-0011 lists metadata among fieldcrypt users | ADR-0011 Context | **Wrong** | contradicts claim 30 |
| 32 | Frozen labels `passage.fieldcrypt.v1`, `passage.fieldcrypt.mac.v1`, `passage.log.pseudonym.v1\0` | §7.16; ADR-0011 | OK | `fieldcrypt.go:32-33`; `logpseudo.go:48` |
| 33 | Per-tenant keys by HKDF from shared master key, so no tenant key can be destroyed; Azure Key Vault is the only wrapper; no pgcrypto | Status; ADR-0011 | OK | `fieldcrypt.go:178,254` (`hkdf.Key(..., master, tenantID, ...)`); only `fieldcrypt/azurekv`; no `pgp_sym_encrypt` in migrations |
| 34 | Valkey `8.1.9-alpine`, one replica, no persistence or auth, NetworkPolicy; tiers 600/3000/12000; portal `sessionStore.url` unset everywhere | ADR-0014; §7.17 | OK | `deploy/envoy-gateway/ratelimit-valkey.yaml`; `deploy/gateway/backendtrafficpolicy.yaml:71,81,91`; stage portal values comment |
| 35 | Portal sessions "in Valkey (ADR-0014)" | ADR-0009 Context | Imprecise | in memory today (ADR-0014 itself says no environment sets the URL) |
| 36 | Search: own DB with `pg_trgm`, Kafka consumer with its own KafkaUser | ADR-0013 | OK | `services/search/migrations/0001_init.sql:10`; `deploy/kafka/search-consumer.yaml` (no `events/asyncapi.yaml` exists yet; backlog item covers it) |
| 37 | Numbering: `POST /v1/numbering/sequences/{code}/next`, no `platform/go/sequence`, called by hr and party, no DB provisioned | ADR-0015; §7.18 | OK | `services/numbering/README.md:21`; `ls platform/go`; `deploy/envs/dev/apps/postgres.yaml` has no `depot_numbering` |
| 38 | Party calls numbering "with the forwarded user headers" | ADR-0015; ADR-0006 | Imprecise | `services/party/internal/numbering/client.go:76-91` signs a service token when a signer is configured (`cmd/party/main.go:84`); header forwarding is the fallback |
| 39 | Inventory numbers its own journals (`0042_journal_number.sql`) | ADR-0015 | OK | file exists |
| 40 | Windows: `tools/depot-server` (serve, migrate, doctor), `deploy/windows/*.wxs`, `windows-msi.yml`; ADR-0004 Accepted | §7.21; ADR-0004 | OK | files present; `main.go:31-42` |
| 41 | `ledger.Post` called by 19 services through `ledgerclient` | Overview; §3.3 | OK | 19 importers of `platform/ledgerclient` |
| 42 | Postings come from receiving, payables, … "each through `ledgerclient`"; "19 services call `ledger.Post`" | §3.4; ADR-0008 Consequences | **Wrong** | receiving (`internal/domain/ports.go`) and payables (`internal/ledger/ledger.go`) call `/v1/ledger/journal-entries` with their own clients: 21 callers |
| 43 | HPA in 3 charts (inventory, pricing, production); no PDBs; no Rollouts | §7.20 | OK | grep in chart templates |
| 44 | No migration Job; `MIGRATE_DATABASE_URL` read only by identity and org; on-start migration for a few dev services | ADR-0012; D28 | OK | no `argocd.argoproj.io/hook`; grep; `migrateOnStart: true` only for fx, hello-ledger, identity, org, receiving, transport |
| 45 | FORCE RLS in all 59 services with migrations; 955 `_tenant_access`, 470 `_legal_entity_scope` | §7.18; ADR-0012 | OK | 59/59 directories; 956 and 470 `CREATE POLICY` matches (off by one, immaterial) |
| 46 | Dev shared Postgres creates 21 databases | Status corrections | OK | 21 `CREATE DATABASE` names in `deploy/envs/dev/apps/postgres.yaml` |
| 47 | Envoy rewrites `/api/v1/<svc>` to `/v1/<svc>` through "the service's HTTPRoute"; `/v1/<svc>` passes through for partners | §3.2 route table; ADR-0008 rule 5; §7.4 | **Wrong** | `deploy/gateway/httproutes.yaml` has **17** HTTPRoutes (analytics, ar, controlling, data, fx, inbox, ledger, ops, org, party, planning, predictions, pricing, receiving, sales, search, workflows). The shell calls about 80 `/api/v1/` prefixes (inventory, procurement, payables, production, warehouse, hr, treasury, …) that have no route |
| 48 | Webhooks signed with `Depot-Signature` | §7.7 | OK | `services/integration-hub-ts/src/webhook.ts:15,24` |
| 49 | React 19.3, Vite 8, Go 1.26, Kafka 4.0, Node 24 | §4; frontend §1 | OK | `apps/web/package.json:19,36`; `go.mod`; `deploy/kafka/kafka.yaml:36`; `workflow-ts/Dockerfile` |
| 50 | No OpenTelemetry in services; the web shell emits `traceparent` | Status; Observability | OK | no `opentelemetry` in any `go.mod`/`package.json`; `apps/web/src/shell/telemetry.ts:25,78` |
| 51 | Audit fed over HTTP by engineering only; PHI read log written by hr; hash chains only in audit and shopfloor; no `depot.ctx` | §7.16; D3 | OK | only `services/engineering/internal/api/audit.go` calls `/v1/audit/changes`; `platform/go/phiaudit` used by hr; no `pg_logical_emit_message` |
| 52 | agents-ts is "agent runtime (Temporal)" | Overview diagram | **Wrong** | `services/agents-ts/package.json` depends only on `pg`; `workflow-ts` is the only `@temporalio` user |
| 53 | Repository topology CI/CD shape: `platform-ci.yml`, `services-ci.yml`, `web-ci.yml`, `pwa-ci.yml`, `charts-ci.yml`, `data-ci.yml`, root `release.yml` | Repository Topology (line 8161) | **Wrong** | workflows are `ci, gate, ci-negative, windows-msi, pages, record-release, cleanup-branches`; no `release.yml`. §2 states the correct list |
| 54 | 59 in-tree shell modules | frontend §1; Repository Topology | OK | `ls apps/web/src/modules` = 59 |

Of the 54 checks, 41 are OK, 4 Imprecise and 9 Wrong. The headline numbers (services, charts, NetworkPolicies, outbox and connector counts, topics, fieldcrypt users, frozen labels, Valkey, search, numbering, Windows) hold. The errors cluster in the Kubernetes request path: routing (#47), the JWT issuer (#11), legal-entity narrowing (#21) and Kafka credentials (#28).

## 2. Findings

| ID | Severity | Location | Problem | Required change |
|---|---|---|---|---|
| R-01 | Major | §3.2 route table (line 447-448); ADR-0008 Decision 5; §7.4; overview | The doc says every service is reachable as `/api/v1/<svc>` and `/v1/<svc>` through its HTTPRoute. Only 17 services have an HTTPRoute. Most of what the web shell calls (inventory, procurement, payables, production, warehouse, hr, treasury, …) would 404 at Envoy in Kubernetes. Neither the delta nor the Known gaps record it. | State the 17 routed services. Add a Known gap (new D-id): "Envoy routes 17 of 69 services; the shell's other modules have no route", with a CI check that every chart with an API has an HTTPRoute and a BackendTrafficPolicy targetRef. Add it to ADR-0008's backlog. |
| R-02 | Major | §7.1 "Validation (as built)"; ADR-0006 Context; ADR-0007 Consequences ("one issuer"); Status summary | Envoy's SecurityPolicy validates against the placeholder issuer `…/realms/dev` and a static placeholder JWKS ("nothing can mint accepted tokens until #80"). agent-runtime is configured for `realms/dev` and `realms/stage`. The Keycloak realm is `depot`. So the identity chain that ADR-0006 and ADR-0007 accept is not wired in any environment, and ADR-0007's "one issuer" is not what the deploy files say. agents-ts also verifies JWTs itself, an unlisted exception to ADR-0006 rule 1. | Qualify the as-built validation text: "configured against a placeholder issuer and key; not connected to realm `depot` (#80)". Add a Known gap (new D-id) and backlog bullets in ADR-0006 and ADR-0007: switch to `remoteJWKS` and the `depot` realm issuer per environment, align agent-runtime, and test that a token from `realm-depot.json` passes the gateway. Note the agents-ts exception in ADR-0006. |
| R-03 | Major | ADR-0006 rule 4; §7.3 step 2; frontend §5.2; D23 gap text and ADR-0006 D23 backlog bullet | The accepted decision says the shell's `X-Depot-LegalEntity` narrows the token's entities. The code accepts the header only when it equals the claim (kernel 400 otherwise, single-host gateways 403), and `le` is one string attribute. Nothing narrows in any deployment, and the backlog test ("can only narrow the token's `le` claim") would fail against today's kernel. The end-user effect is not stated: a user entitled to several legal entities cannot switch company in the shell, either on Windows or in Kubernetes. | Correct rule 4 and §7.3 to the built semantics. Record the user-visible impact in Known gaps (split D23: an enforcement-model gap and a "company switcher cannot narrow scope" gap). Then decide one of two: implement narrowing (intersect the selection with the claim in `requestLegalEntity` and the gateways, and forward it at Envoy), or make the token carry exactly one entity and have switching re-authenticate. Record the choice in ADR-0006. |
| R-04 | Major | §7.6 consume side; events §4.1 as-built note | "Each consumer has a `KafkaUser`" is false. finreport, fulfillment, pricing, production, purchasing, shopfloor and integration-hub-ts consume Kafka but have no SCRAM user, so they cannot connect to the cluster as deployed. Flows such as sales order → fulfillment, planned order → production, journal → finreport and price-cache invalidation cannot run even where the producer is relayed. This is not among the Known gaps. | Correct both sentences. Add a Known gap (new D-id, or extend D2): a KafkaUser and ACLs for every consumer group, with a CI check pairing each `eventing.Consumer`/kafkajs consumer with a KafkaUser. |
| R-05 | Major | ADR-0011 Decision and Consequences; Known gaps intro (D15 classed as documentation-only) | ADR-0011 accepts a risky combination: no crypto-shred, and one master key per installation that derives every tenant's key, held as an env var or file by seven services, with KMS wrapping "optional". It does not state the conditions that keep this safe. Because D15 is filed as documentation-only, none of those conditions is tracked in Known gaps, including the counsel sign-off. | Add to ADR-0011 a "Conditions" list in the style of ADR-0006 rule 6 and ADR-0009: (a) KMS-wrapped master keys required outside dev, with named custody and a rotation and re-encryption runbook; (b) pseudonymization reaches every copy (search index, Kafka with 7-day retention stated, lakehouse, logs, backups with a bounded retention that the erasure SLA cites); (c) counsel sign-off on pseudonymization as erasure (Q17) before personal data of EU subjects is loaded. Put the unmet conditions in Known gaps with a D-id (D15 is no longer documentation-only), and fix the metadata entry (R-09). |
| R-06 | Major | Known gaps; §7.2; ADR-0006 Consequences and Backlog; no summary section | Auditability and end-user impact are scattered and partly untracked. (1) Authorization decisions are not logged anywhere: OPA decision logs are off, and Kubernetes makes no OPA decision at all. ADR-0006 adds only a denial metric, which is not an audit record. No D-item exists. (2) There is no consolidated statement for sign-off readers. Delta §6 (auditability gaps 1–7, end-user impact 1–7) has no counterpart in the doc. Permission-revocation latency (up to 300 s, stale navigation) is stated only inside ADR-0006 and §7.1. D23's user impact is missing (see R-03). | Add a short "Auditability and end-user impact (v1.0)" subsection after Known gaps. Map each delta §6 item to a D-id or ADR. Name: no CDC audit trail (D3); actor and trace lost across async hops (D3, D8, D19); forged-header identity (D1); 300 s revocation (ADR-0006); company switcher (R-03); multi-call pages and no live updates (D29). Add a Known gap for authorization-decision logging (a structured allow/deny record per request in `authz.Require`, with masked subject), not only the metric. |
| R-07 | Minor | Status corrections; ADR-0006 Context; Security & Auth | The namespace count understates the exposure: 67 of 69 service charts run in `hello-ledger`, not 63. | Correct to "67 of 69 (all but batch and metadata)". |
| R-08 | Minor | §7.1 Claims issued; frontend §8.1; ADR-0007 Context and backlog | "All tokens carry `aud: depot-api`" is false: portal and deprovision clients have no audience mapper. ADR-0007's proposed realm test ("fails if a client lacks the depot-api audience mapper") would fail today. | Say which clients carry the audience. Either scope the test to `depot-web` and `depot-services`, or add the mapper where it is intended. |
| R-09 | Minor | ADR-0011 Context | Lists metadata as a fieldcrypt user. The doc's own correction says otherwise. | Remove metadata, or say "metadata generates fieldcrypt-sealed fields for other services". |
| R-10 | Minor | §3.4 as-built note; ADR-0008 Consequences; delta-derived "19 services" in end-user notes | receiving and payables post to the ledger with their own clients, not `ledgerclient`, so 21 services call `ledger.Post`. | Correct the count and the "each through `ledgerclient`" wording. Consider a backlog item to move both onto `ledgerclient`, since D9 key uniformity depends on it. |
| R-11 | Minor | ADR-0015 Context; ADR-0006 Context; ADR-0009 Context | Party signs a service token when a key is configured and forwards headers only as a fallback. Portal sessions are in memory, not "in Valkey". | Reword: "service token, with header forwarding as fallback when no signing key is set"; "sessions in memory, Valkey when `sessionStore.url` is set (not set in any environment)". |
| R-12 | Minor | Overview diagram (line 150, `Agents --> Temporal`) | agents-ts has no Temporal SDK; agent runs are a `workflow-ts` workflow. | Fix the label and edge, or mark them as target. |
| R-13 | Minor | Repository Topology, "CI/CD shape" (line 8161) and tools list | Names six CI workflows and a `release.yml` that do not exist, with no marker. `tools/entitygen` is missing from the list. | Replace with the as-built workflow list (as in §2), or mark it *Target*. |
| R-14 | Minor | Frontend §2.1 topology diagram; Testing section (T10 "atlas migrate lint", M-07 Atlas lint, CI order) | Stale v0.x content (web-bff and device-bff cookie sessions, OPA sidecar, realm per tenant, Atlas) has no marker. The reading guide says unmarked text "is design that the code follows, or detail that was not audited", and it names only the data model and AI sections as unaudited. | Add a *Target layout (v0.x)* marker to frontend §2.1, and a section-level note on Testing ("not audited for v1.0; Atlas references superseded by ADR-0012"). Extend the reading guide's list of unaudited sections. |
| R-15 | Minor | ADR-0014; Backlog bullets in ADR-0004 (3), ADR-0006 (2), ADR-0007 (all 3), ADR-0009 (2), ADR-0011 (1) | ADR-0014 has no "Alternatives rejected" section (for example in-process or per-pod limits, Redis, the portal sticky sessions it replaces). Twelve backlog bullets cite no D-id, against the stated convention that each becomes a traceable item. | Add Alternatives to ADR-0014. Give every backlog bullet a D-id, either an existing one or a new one listed in Known gaps (for example D16 for the ADR-0007 bullets). |
| R-16 | Minor | Known gaps | Delta findings that are neither in Known gaps nor explicitly deferred: no mTLS or authorizer on the Temporal frontend (delta §3 row 11; label-based NetworkPolicy only, while `workflow-ts` can mint `payment-run.execute` tokens); no image for the portal SPAs (§6.2 item 7); `If-Match` on 35 of 64 specs and mixed `:verb` / `/verb` custom methods (§6.2 item 5); `X-Depot-Api-Build` and `RateLimit-*` not set (C24). | Add each to Known gaps, or list it under "deferred, not a v1.0 gap" with a reason. |
| R-17 | Minor | D28; ADR-0012 | D28 says nothing applies migrations "outside dev". In dev, too, only six releases migrate on start. ledger, inventory, sales and about 60 others have no migration path in any environment. | Reword D28 to cover dev. |
| R-18 | Minor | Known gaps table; ADR-0006 | Readability for sign-off: no owner, target wave or acceptance test per gap; D30 is listed before D29; "Every OpenAPI operation … (97.6%)" contradicts itself. | Add Owner and Exit-criterion columns, or a pointer to the backlog where they live. Order by severity, then id. Fix the wording. |

What the change does well, for balance: the status and reading guide, the marker scheme and the Known gaps table are clear and mostly accurate. §3.2, §7.1–7.6, §7.16, §7.18, §8 and §9 were consistently converted. Every write-ADR deviation in the delta has an ADR: D1→0006, D5→0008, D7→0009, D14→0010, D15→0011, D16→0007, D18→0012, D20→0013, D21→0015, D23→0004 accepted and 0006 rule 7, D26→0014. Every fix-code deviation appears in Known gaps: D1–D4, D6–D13, D17–D19, D21–D24, plus D28–D30. The as-built "Corrections to the stage-2 delta" list is a good audit practice. ADR-0006 and ADR-0009 state explicit safety conditions, and their unmet conditions are D1 and D7 in Known gaps. ADR-0015's fiscal-number condition is D21.

## 3. ADR quality summary

| ADR | Context / Decision / Alternatives / Consequences | Matches code | Risk conditions stated and tracked | Notes |
|---|---|---|---|---|
| 0004 | yes (amended status and backlog) | yes | D23 cited | 3 backlog bullets without a D-id |
| 0006 | yes | mostly | yes (rule 6 → D1; rule 7 → D23) | rule 4 wrong (R-03); issuer placeholder unmentioned (R-02); no decision-logging item (R-06) |
| 0007 | yes | mostly | partly (`tid` must not be user-editable is a backlog item) | aud claim (R-08); issuer mismatch (R-02); no D-ids |
| 0008 | yes | mostly | n/a | routing claim (R-01); 19 vs 21 (R-10) |
| 0009 | yes | yes | yes (4 conditions, triggers → D7) | portal Valkey wording (R-11) |
| 0010 | yes | yes | yes (replay store fail-closed, key rotation) | good |
| 0011 | yes | mostly | **no** (R-05) | metadata (R-09) |
| 0012 | yes | yes | yes (prod topology left open → D18, D28) | good |
| 0013 | yes | yes | yes (D2 dependency, erasure) | good |
| 0014 | **no Alternatives** | yes | yes (session store auth and TLS backlog) | R-15 |
| 0015 | yes | mostly | yes (condition → D21) | party wording (R-11) |

## 4. Gate recommendation

**PASS WITH CONDITIONS.** There are no Blockers and six Majors (R-01 to R-06).

Conditions before the architecture is signed off as v1.0, and before stage 3 recompiles `docs/architecture.compiled` from it:

1. R-01, R-02 and R-04: correct the Kubernetes request-path claims (routes, JWT issuer, Kafka credentials) and add the three missing Known gaps. Left uncorrected, the doc presents a Kubernetes path as built that cannot carry a request or an event end to end. In that case this reviewer would treat them as Blockers at the next gate.
2. R-03: correct ADR-0006 rule 4 and §7.3 to the built legal-entity semantics, record the company-switcher impact, and decide the intended model.
3. R-05: add safety conditions to ADR-0011 and track the unmet ones (including counsel sign-off) in Known gaps.
4. R-06: add the auditability and end-user impact summary and a Known gap for authorization-decision logging.

Minors R-07 to R-18 should be fixed in the same revision; none blocks.

---

## Re-review: commit 6403857a (and compile commit 0662309d)

- Scope: I checked each resolution in the files themselves, not in the response table
  (`docs/sdlc/run-1/architecture-review-response.md`): `docs/architecture.md` (Known gaps, the new "Auditability and
  end-user impact (v1.0)" section, §3.2, §7.1, §7.3, §7.6, events §4.1, frontend §2.1, Testing, Repository Topology),
  and ADR-0004, 0006, 0007, 0008, 0009, 0011, 0012, 0014 and 0015. Facts were re-checked against `deploy/` and the code.
- `python3 scripts/compile_architecture.py --check` passes: "architecture.compiled is up to date: docs/architecture.md
  sha256 b3b55b2cab82… (8378 lines) …; 285 cited paths exist".

### Majors

| ID | Verdict | Evidence in the files | Code re-check |
|---|---|---|---|
| R-01 | **Resolved** | §3.2 route table lists the 17 routes, their 15 backends, the unrouted remainder (404 at Envoy, D31) and the single-host gateways as their own row. ADR-0008 rule 5 rewritten, with a backlog bullet (D31). D31 (High, platform) has a CI exit criterion. Auditability U3 | Parsed `deploy/gateway/httproutes.yaml`: 17 HTTPRoutes; backends org, fx, receiving, search, workflows, batch (`inbox`), ledger, pricing, sales, party, analytics, planning, metadata (`data`), costacct (`controlling`), ops, prediction (`predictions`, `ar`) = 15. Matches |
| R-02 | **Resolved** | §7.1 Validation now says the SecurityPolicy is configured against the placeholder issuer and key and is not connected to realm `depot` (D32). It names agent-runtime's `realms/dev` / `realms/stage`, agents-ts self-verification and the portals' `realms/depot`. ADR-0006 Context adds "The issuer is not wired", and rule 1 lists the exceptions. ADR-0007 Context says the deploy files do not point at the realm, and its Consequences say "one issuer" holds only on single-host installs until D32 closes; backlog D32. D32 is High, security | `securitypolicy.yaml`, `deploy/envs/{dev,stage}/values/agent-runtime.yaml`, portal values: as stated |
| R-03 | **Resolved** (model awaits owners) | ADR-0006 rule 4 is split into *As built* (one string, kernel 400, gateways 403, Envoy strips, no-claim case, nobody can switch company) and **"Proposed (recommended; pending owner confirmation)"** (an `le` list, membership check, 403 outside the list or without a claim, Envoy forwards). The text says "Until the owners confirm, the as-built behaviour stands". The same wording appears in the reading guide's "Awaiting owner sign-off" row, the new **Proposed** marker definition, §7.1 claims ("Target (D33, Proposed)") and U2. No passage presents the model as accepted or built. D33 (High, security and frontend) replaces the legal-entity part of D23 (D23 is now OPA only) and has an end-to-end acceptance test | Realm `le` mapper is `user.attribute: legal_entity_id`, `multivalued: false`; `tenant.requestLegalEntity` and the devgateway and `depot-server` checks behave as described |
| R-04 | **Resolved** | §7.6 consume side (13 have a `KafkaUser`, 7 do not) and events §4.1 corrected. D34 (High, eventing) also records that no environment enables any consumer, which I had not reported; checked as true. A5 maps it | `deploy/kafka/*-consumer.yaml` users as stated. Consumer `enabled: false` in every chart sampled (billing, edi, finreport, fulfillment, intercompany, inventory, payables, prediction, production, purchasing), and no `deploy/envs/*/values` file overrides it |
| R-05 | **Resolved** | ADR-0011 status is "Accepted with conditions". It gains a risk paragraph and a **Conditions** section: (1) KMS wrapping mandatory outside dev, with custody and a rotation drill; (2) master-key holders minimized and reviewed; (3) erasure reaches search, Kafka (7 d stated), lakehouse, logs and backups, with backup retention tied to the SLA and erasure re-applied on restore; (4) counsel sign-off on Q17 before EU personal data is loaded. It says "no stage or prod environment holds real personal data" until the conditions are met. Metadata is removed from the users. D35 (High, security) is in Known gaps, and D15 is off the documentation-only list | `FIELDCRYPT_KEYS[_FILE]` env entries in chart templates: exactly compliance, hr, integration-hub-ts, migration, party, payables, receiving, sales (8); `integration-hub-ts/src/crypt.ts` exists; `fieldcrypt-kek` in `deploy/tofu/cluster/modules/aks/main.tf:222`; no `deploy/envs` file sets the workload-identity client id; hr `reseal` CronJob present. One note checked: with `LOG_PSEUDONYMIZE`/eu-data-boundary on and no keys, `logpseudo.Install` redacts rather than needing the key in every chart, so condition 2 does not conflict with log pseudonymization |
| R-06 | **Resolved** | New section "Auditability and end-user impact (v1.0)": A1–A7 and U1–U9, each mapped to a D-id, an ADR or an explicit deferral. It covers authorization logging (D36), the CDC audit trail (D3), actor and trace loss across async hops (D3, D8, D19), forged-header provenance (D1), the 300 s revocation window (ADR-0006 rule 5, D41), company switching (D33), unrouted modules and the unwired issuer (D31, D32), and erasure coverage (D35). D36 (High, security) asks for a structured allow/deny record from `authz.Require` and the three gateways, not only the metric | n/a (doc structure) |

### The question the author raised: a token with no `le` claim

**Code.** `tenant.requestLegalEntity` (`platform/go/tenant/tenant.go:241-243`) returns the shell's
`X-Depot-LegalEntity` as the scope when `X-Depot-Legal-Entity` is empty. Reachability today:

- **Through Envoy:** not reachable. `ClientTrafficPolicy` strips `X-Depot-LegalEntity`, and a token without `le` gets no `X-Depot-Legal-Entity`, so the scope is empty.
- **Through devgateway and `depot-server`:** not reachable. A non-empty selection that differs from the (empty) claim is refused with 403 (`gateway.go:54` and `:71`).
- **By calling a pod directly:** reachable. That is the D1 exposure, where an attacker can set `X-Depot-Legal-Entity` itself, so the no-claim path adds nothing to D1 today.

**Captured?** Yes, in three places:

- the D33 gap text ("With no `le` claim, the kernel takes the selection as the scope, which only the gateways prevent");
- D33's exit criterion ("a request with no `le` claim and a selection is refused");
- ADR-0006 rule 4 (*As built*, and the *Proposed* model's "a selection with no claim is refused with 403"), with the D33 backlog bullet.

It is not repeated in D1, which is acceptable because D1 already covers direct pod access.

**Severity.** High, through D33, is right. On its own it is subsumed by D1, which is High. The risk becomes live only if Envoy starts forwarding the selection before the kernel refuses no-claim selections. Tokens without `le` exist legitimately: tenant administrators, per §7.3 rule 3a and identity's `withAdminTx`.

**One residual (Minor, RR-01):** the backlog bullet bundles the kernel check with Envoy forwarding but states no ordering. Add to D33's exit criterion or the ADR-0006 bullet:

- "the kernel and single-host gateways refuse a selection without a claim before (or in the same release as) Envoy forwards it";
- "tenant-level administration with no `le` claim and no selection keeps working".

### Minors (spot-checked)

| ID | Verdict | Check |
|---|---|---|
| R-07 | Resolved | "67 of the 69 service charts" in the status corrections, D1, ADR-0006 and Security & Auth; no "63 services" left |
| R-08 | Resolved | §7.1 says only `depot-web` and `depot-services` carry `aud`; ADR-0007 test scoped (D16) |
| R-09 | Resolved | ADR-0011 Context: "metadata does not encrypt" |
| R-10 | Resolved | 21 callers (19 through `ledgerclient`) in the overview, §3.3, §3.4, ADR-0008, U8; D9 extended |
| R-11 | Resolved | ADR-0015 (service token, header fallback), ADR-0009 (sessions in memory) |
| R-12 | Resolved | Agents node relabelled; the edge to Temporal goes through the workflow-ts API. Verified in `services/agents-ts/src/runtime/workflowGateway.ts:36-37` (`/v1/workflows/agent-runs/{id}:start`) |
| R-13 | Resolved | Repository Topology starts with the seven real workflows; the rest is marked *Target (v0.x)* |
| R-14 | Resolved | Frontend §2.1 "*Target layout (v0.x), not as built*" (line 5237); Testing "*Not audited for v1.0*" (line 6393); the reading guide lists the unaudited sections |
| R-15 | Resolved | ADR-0014 has "Alternatives rejected"; `grep '^- Backlog' docs/adr/00*.md` finds no bullet without a D-id |
| R-16 | Resolved | D37–D40 added; a "Deferred, not a v1.0 gap" list gives reasons |
| R-17 | Resolved | D28 now covers dev; the on-start set (fx, identity, org, receiving and transport in dev and stage; pricing by chart default; hello-ledger in all three) checked against `deploy/envs/*` and `services/pricing/charts/pricing/values.yaml:15` |
| R-18 | Resolved | Owner and Exit-criterion columns; ordered by severity then id; "3,116 of 3,194 operations (97.6%)" |

New minor observations (non-blocking):

- **RR-01:** ordering guard for the no-claim selection, described above.
- **RR-02:** the new **Proposed** marker shares its word with older, unrelated "Proposed" cells (events OQ-5 and OQ-12; frontend shared-dependency table rows 6–10). Consider writing those as "proposed (v0.x)", or using a distinct marker label, so a search for the marker finds only owner-pending decisions.

### Final gate decision

**PASS.** All six Majors (R-01 to R-06) are resolved in the files, with the corrections verified against `deploy/`
and the code. All twelve Minors are resolved. There are no Blockers and no open Majors. The compiled architecture
check passes.

Recorded with the sign-off, not as gate conditions:

- The multi-entity legal-entity model (ADR-0006 rule 4) is **Proposed, pending owner confirmation**. D33 tracks it as a High gap with an acceptance test.
- RR-01 and RR-02 can be done in the next documentation pass.
- The v1.0 document openly records many High gaps: D1–D4, D6–D8, D31–D36. Passing this gate means the architecture describes the system accurately and traceably. It does not mean the system is ready for stage or prod users. ADR-0006 rule 6 and ADR-0011's conditions keep stage and prod closed to real users and personal data until those gaps are closed.
