# Architecture v1.0: response to the independent stage-2 review

- Review: independent review of commit 9d8507bc (stage 2, Architecture), findings R-01 to R-18, gate recommendation
  "PASS WITH CONDITIONS". The review is held with the run's evidence by the SDLC coordinator; each finding is restated
  briefly below.
- This response: the revision commit "Architecture v1.0: resolve independent review R-01..R-18 (SDLC run 1, stage 2)".
- Method: every finding was checked against the code and `deploy/` before the documents were changed (grep, YAML and
  realm JSON reads). Where the check found more or less than the review, the row says so under "Verification".
- Scope: documentation only. `docs/sdlc/run-1/architecture-delta.md` is kept unchanged; `docs/architecture.compiled`
  is recompiled in stage 3.

## Decision awaiting owner sign-off

| Item | Status | Where |
|---|---|---|
| Multi-entity legal-entity model (R-03): the `le` claim lists the legal entities a user is entitled to; the shell's `X-Depot-LegalEntity` selects and narrows to one of them; a selection outside the list, or without a claim, is refused with 403 by the kernel and every gateway; Envoy forwards the selection | **Proposed (recommended; pending owner confirmation).** Not accepted and not built. Until confirmed, the as-built single-entity behaviour stands and D33 records its user impact | ADR-0006 rule 4; architecture §7.3 step 2, frontend §5.2, Known gaps D33 |

The owners are asked to confirm or reject this model. The alternative (one entity per token, re-authenticate on each
company switch) is described and not recommended in ADR-0006 rule 4.

## Findings

| ID | Sev. | Finding (short) | Verification | Resolution (what changed, where) |
|---|---|---|---|---|
| R-01 | Major | Route table and ADR-0008 rule 5 claim every service is routed by Envoy; only 17 HTTPRoutes exist | Confirmed. `deploy/gateway/httproutes.yaml` has 17 HTTPRoutes for 15 backend services (`inbox` → batch, `data` → metadata, `controlling` → costacct, `predictions` and `ar` → prediction-py). `apps/web/src` uses about 80 distinct `/api/v1/<prefix>` values; 15 of them are routed (`workflows` and `ar` are routed but not called by the shell) | §3.2 route table lists the routed prefixes and backends, the unrouted remainder, and the single-host gateways; ADR-0008 rule 5 rewritten, plus a backlog bullet; ADR-0006 Context and backlog; §7.4 note; §7.20; overview diagram edge; status summary. **New gap D31 (High, platform)** with a CI exit criterion |
| R-02 | Major | Envoy validates a placeholder issuer and static key, not realm `depot`; agent-runtime uses `realms/dev`/`realms/stage`; agents-ts self-verification unlisted | Confirmed: issuer `https://idp.dev.depot.dev/realms/dev`, `localJWKS` ConfigMap `depot-dev-jwks` (no private key, `deploy/gateway/README.md`); `deploy/envs/{dev,stage}/values/agent-runtime.yaml`; realm `depot`. Also found: the portal BFFs already use `realms/depot`; `services/agents-ts/src/api/auth.ts` verifies RS256, issuer, audience and expiry and requires the headers to agree | §7.1 Validation states the as-built wiring; ADR-0006 Context adds "The issuer is not wired" and rule 1 lists agents-ts (and the portal BFFs, identity) as explicit exceptions; ADR-0007 Context and Consequences ("one issuer" is the decision, not yet the deploy state) plus backlog; status summary; overview diagram. **New gap D32 (High, security)** |
| R-03 | Major | Rule 4 says the shell's header narrows the token's entities; the code accepts it only when equal to the claim; company switching impossible | Confirmed: `tenant.requestLegalEntity` returns 400 on any difference (`TestMiddlewareBothLegalEntityHeaders`), devgateway and `depot-server` 403 unless equal, Envoy strips the header, `le` mapper `multivalued: false`. Two details the review did not state: the kernel parser already accepts a comma list of up to 100 entities in `X-Depot-Legal-Entity`; and with no `le` claim the kernel takes the selection as the scope, which only the gateways prevent (a D1-class exposure for direct pod access) | ADR-0006 rule 4 split into As built and **Proposed (recommended; pending owner confirmation)**; §7.3 step 2; §7.1 claims; frontend §5.2 and frontend Q6; ADR-0007 and ADR-0004 backlog bullets. **New gap D33 (High, security and frontend)** replaces the legal-entity part of D23, with the end-to-end acceptance test (multi-entity user switches company through Envoy, devgateway and `depot-server`; 403 outside the list; refusal of a selection without a claim). D23 now covers only the OPA difference |
| R-04 | Major | "Each consumer has a KafkaUser" is false; seven consumers have none on a SCRAM cluster | Confirmed for finreport, fulfillment, pricing, production, purchasing, shopfloor and integration-hub-ts. Refinements: `prediction-py` is a twentieth consumer (it has a user); the cluster also has a plain 9092 listener without authentication, but `authorization: simple` gives anonymous clients no ACLs, so the effect is the same; and **no environment enables any consumer** (every chart defaults to `consumer.enabled: false` or empty `brokers`, and no environment values override it), which the review did not report | §7.6 consume side and events §4.1 rewritten; status summary; Auditability table A5. **New gap D34 (High, eventing)** covers both the missing users and the disabled consumers |
| R-05 | Major | ADR-0011 accepts no crypto-shred and one master key per installation without stated safety conditions; D15 filed as documentation-only | Confirmed, with corrections to the review's facts: eight charts mount the master keys (the seven Go users plus integration-hub-ts's TypeScript port, `src/crypt.ts`), not seven; Key Vault wrapping is supported in the charts through Azure workload identity (`deploy/tofu/cluster/modules/aks`, key `fieldcrypt-kek`) but enabled in no environment; hr already has an opt-in re-encryption job (`hr reseal-pii`) | ADR-0011 status "Accepted with conditions"; new risk paragraph and **Conditions** 1–4 (KMS wrapping mandatory outside dev with custody and rotation; key holders minimized and reviewed; erasure reaching search, Kafka, lakehouse, logs and backups with retention tied to the SLA; counsel sign-off on Q17 before EU personal data); Context corrected (metadata removed, TS port, wrapping state, reseal job); backlog bullets tied to conditions. §7.16 erasure note. **New gap D35 (High, security)**; D15 removed from the documentation-only list |
| R-06 | Major | Authorization decisions not logged; no consolidated auditability and end-user impact statement | Confirmed: OPA `decision_logs` off (`deploy/policies/opa/README.md`); `authz.RequireDuty` writes only a 403 problem; the kernel access log (`httpx.logRequests`) has method, path, status, bytes, duration and request id, no actor, tenant or duty | New section **"Auditability and end-user impact (v1.0)"** after Known gaps: A1–A7 and U1–U9, each mapped to a D-id, an ADR or an explicit deferral (covers the 300 s revocation window, company switching, actor and trace loss across async hops, audit ingestion D3, idempotency and If-Match coverage). §7.2 decision-logging bullet; ADR-0006 Consequences and backlog. **New gap D36 (High, security)** |
| R-07 | Minor | 63 of 65 understates namespace sharing; 67 of 69 | Confirmed (63 ApplicationSet entries plus fx, identity, org, receiving Applications) | Status corrections, D1, ADR-0006 Context, Security & Auth now say 67 of 69 (all but batch and metadata) |
| R-08 | Minor | "All tokens carry `aud: depot-api`" is false | Confirmed: audience mapper only on `depot-web` and `depot-services` | §7.1 Claims, frontend §8.1, ADR-0007 Context; ADR-0007's realm test scoped to `depot-web` and `depot-services` (D16) |
| R-09 | Minor | ADR-0011 lists metadata as a fieldcrypt user | Confirmed | ADR-0011 Context: metadata removed; "generates fieldcrypt-sealed fields for other services" |
| R-10 | Minor | `ledger.Post` has 21 callers; receiving and payables do not use `ledgerclient` | Confirmed (`services/receiving/internal/domain/ports.go`, `services/payables/internal/ledger/ledger.go`) | Overview note, §3.3, §3.4, §6, ADR-0008 Consequences, Auditability U8 say 21 (19 via `ledgerclient`); status corrections; D9 extended and ADR-0008 backlog bullet to move both onto `ledgerclient` |
| R-11 | Minor | Party uses a service token with header fallback; portal sessions are in memory | Confirmed (`services/party/internal/numbering/client.go`, `cmd/party/main.go`) | ADR-0015 Context and backlog (remove the fallback, D1); ADR-0009 Context (in memory; Valkey only when `sessionStore.url` is set, which no environment does) |
| R-12 | Minor | Overview diagram shows agents-ts on Temporal | Confirmed (agents-ts depends only on `pg`) | Agents node relabelled; the edge now reads "starts and wakes agent-run workflows through the workflow-ts API (service token)" |
| R-13 | Minor | Repository Topology names non-existent CI workflows and `release.yml`; `tools/entitygen` missing | Confirmed | CI/CD shape starts with the as-built workflow list and marks the rest *Target (v0.x)*; `entitygen` added to the tools line |
| R-14 | Minor | Frontend §2.1 and Testing carry stale v0.x content without markers | Confirmed | Frontend §2.1 marked *Target layout (v0.x), not as built* with the as-built summary; Testing marked *Not audited for v1.0* with Atlas superseded by ADR-0012; reading guide lists the unaudited sections and the new "Proposed" marker |
| R-15 | Minor | ADR-0014 has no Alternatives; backlog bullets without D-ids | Confirmed | ADR-0014 "Alternatives rejected" added (per-pod limits, per-service limits, the Redis product, sticky sessions, sessions in Postgres). Every `Backlog:` bullet in ADR-0004 and ADR-0006 to ADR-0015 now cites a D-id: ADR-0004 → D42, D23, D33; ADR-0006 → D31, D32, D33, D36, D41; ADR-0007 → D16, D32, D33; ADR-0009 → D7, D29, D38; ADR-0011 → D35, D20, D43. D16 is now a Known-gaps row |
| R-16 | Minor | Delta findings neither in Known gaps nor deferred | Confirmed | Added as gaps: **D37** Temporal frontend without mTLS or authorizer (Med); **D38** no image for the portal SPAs (Med); **D39** `If-Match` in 35 of 64 specs and mixed custom-method styles (Low); **D40** `X-Depot-Api-Build` and `RateLimit-*` not set by the kernel (Low). A "Deferred, not a v1.0 gap" list records the items deliberately not tracked (composition layer, TS stack D25, egress topics Q5, agent delegation Q8) with reasons |
| R-17 | Minor | D28 says nothing migrates "outside dev"; in dev too, most services have no path | Confirmed, and the review's own count was short: on-start migration is set for fx, identity, org, receiving and transport in **dev and stage**, pricing by chart default, and `hello-ledger` in all three environments | D28 reworded (no migration path for the other 50-odd services in any environment, dev included); §7.18, backend Q16, ADR-0012 Context corrected |
| R-18 | Minor | Known gaps lack owner and exit criterion; D30 before D29; "Every … (97.6%)" | Confirmed | Known gaps table gains Owner (role) and Exit criterion columns and is ordered by severity, then id; ADR-0006 now says 3,116 of 3,194 operations (97.6%) |

## Further corrections included in this revision (stage-3 compile)

| Item | Verification | Resolution |
|---|---|---|
| Events §2.5 said "No enricher SMT" | Wrong: all 21 connectors in `deploy/kafka/connect.yaml` chain `transforms: outbox,enrich` (budgeting adds a filter) with `deploy/kafka-connect/enricher`, which fills `specversion`, `datacontenttype`, default `source`, `dataschema` (template `https://schemas.depot.dev/{type}.json`), `tenantid`, `legalentityid` and `ce_*` headers | Events §2.5 and the §2 envelope note rewritten; D19 now says `dataschema` is set by the enricher on relayed topics (not by `outbox.Enqueue`), and that `traceparent` and `sequence` are missing |
| D9 posting keys | Confirmed: receiving uses `post:receiving:…` (service name, not `post:procurement:`); inventory's event-driven postings use the prefix `inventory-postings:` | D9 and §3.4 list inventory and the receiving segment; the exit criterion asks to define the key's second segment first |

## Rebuttals

None of the eighteen findings is rebutted. Four are refined above where the code shows more than the review stated:
R-03 (no-claim selection, existing list parser), R-04 (no consumer enabled anywhere; plain listener), R-05 (eight key
holders, wrapping supported but unused, hr re-encryption job) and R-17 (stage also migrates on start; pricing).

## New Known gaps

| D-id | Severity | Owner | Gap |
|---|---|---|---|
| D31 | High | platform | Envoy routes 15 services (17 HTTPRoutes); most shell modules unrouted |
| D32 | High | security | Envoy issuer and key are placeholders; agent-runtime on other realm names |
| D33 | High | security, frontend | Multi-entity users cannot switch company; model Proposed, pending owner confirmation |
| D34 | High | eventing | Seven consumers without `KafkaUser`; no consumer enabled in any environment |
| D35 | High | security | ADR-0011 conditions unmet (KMS wrapping, key holders, erasure coverage, counsel sign-off) |
| D36 | High | security | Authorization decisions not logged |
| D37 | Med | platform | Temporal frontend without mTLS or authorizer |
| D38 | Med | frontend | No image or chart for the portal SPAs |
| D41 | Med | security | 300 s revocation window undocumented; no session-revocation action |
| D42 | Med | platform | Windows single-host install unproven; certificate store and outbox relay undecided |
| D39 | Low | platform | `If-Match` coverage and custom-method style uneven |
| D40 | Low | platform | `X-Depot-Api-Build` and `RateLimit-*` not set by the kernel |
| D43 | Low | security | No CI guard for the frozen `passage.*` labels |

D16 (realm guard rails, Med, security) moves from documentation-only to Known gaps.

## Gate conditions

| Condition | Status |
|---|---|
| 1. R-01, R-02, R-04: correct the Kubernetes request-path claims and add the three gaps | Done: D31, D32, D34 |
| 2. R-03: correct rule 4 and §7.3, record the company-switcher impact, decide the intended model | Corrected and recorded (D33); model **Proposed, awaiting owner sign-off** |
| 3. R-05: ADR-0011 conditions and tracking | Done: Conditions 1–4, D35 |
| 4. R-06: auditability and end-user impact summary, decision-logging gap | Done: new section, D36 |
