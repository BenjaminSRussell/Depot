# Runbook: the isolated CUI deployment

DEPOT-310. CUI is served by a separate deployment (ADR-0018): its own stack, its own accounts or partition, its own model
endpoint, and no credential held by the standard deployment for it or the reverse. This runbook covers what the repository
builds today (the `cui` cloud profile, the egress policies, the sub-processor registry and the AI sign-off gate) and the
operating procedures. The AWS GovCloud accounts, network, cluster, Vault transit keys, object-lock storage, disaster recovery
and the live probe-pod tests are follow-ups (`docs/ops/NOT-IMPLEMENTED.md`); steps that need them are marked **follow-up**.

## Deployment topology

Inside the boundary, in one cluster of the `cui` stack: agents-ts (`agent-runtime` chart) with its database, the audit
store with its hash chain and daily anchor (`services/audit`), compliance, observability (Prometheus, Grafana, Alertmanager
with the in-boundary webhook receiver), and the accredited model endpoint, Amazon Bedrock in AWS GovCloud (US) reached over
PrivateLink. `model-serving` (Presidio, embedding, reranker) is not built: the egress policy already selects its pods by the
label `app.kubernetes.io/name: model-serving` so the chart that ships it inherits the boundary.

Off or denied:

- AI for every tenant: `AGENTS_AI_ENABLED` is `false` until the sign-off record exists (see Enabling AI).
- Every non-AI sub-processor (next section).
- Any telemetry exporter, paging service (`pagerDuty: false`) or public registry (`registry.requireMirror: true`).
- Any model host other than Bedrock. `llmProviders` is `["bedrock"]` with `enforceProviders: true`. The profile matcher
  `bedrock-runtime.*.amazonaws.com` also matches commercial regions, so the endpoint is pinned to
  `bedrock-runtime.us-gov-west-1.amazonaws.com` in `deploy/networkpolicy/cui-egress/values.yaml` (`allowHosts`) and in the
  endpoint attestation below.

The profile's Azure endpoint table is a copy of `gcc-high`: the offline renderer needs an Azure Government substrate to
verify against. Those hosts stay on the cui allowlist although no Azure service is in an AWS boundary, which is wider than
intended. The AWS follow-up replaces the table; until then do not treat the allowlist as the final boundary.

## Sub-processors in the CUI deployment

All off at launch, by product-owner decision. Source of truth: `deploy/cloud-profiles/subprocessors.json`
(`enabled.cui` is `false` for every entry). `test_cui_boundary.py` fails when any entry is not exactly `false`, when an entry
is malformed, when a sub-processor host is on the cui allowlist or when one appears in any chart rendered under `cui`.

| Id | Integration | Manual alternative |
|---|---|---|
| `slack-teams` | Slack and Microsoft Teams notifications | Read the in-app notification; no message is sent |
| `email-smtp` | Email and SMTP, including collections email | Print or download the letter and send it by your own channel |
| `ocr-invoice-capture` | OCR and invoice capture | Key the invoice by hand |
| `tax-provider` | Tax calculation provider | Enter the tax manually |
| `carriers` | Carrier rating and shipping | Enter the rate and tracking by hand |
| `payment-processor` | Payment processor for the customer portal | Take payment outside the portal and record it |
| `payroll-provider` | Payroll provider | Run payroll by the documented manual process |
| `card-feed` | Card feed import | Enter card transactions by hand or import a file |
| `e-invoice-gateways` | E-invoice gateways | Issue the invoice as a document and submit it by your own channel |

The per-screen "unavailable" states with the manual alternative are a follow-up (`docs/ops/NOT-IMPLEMENTED.md`); this change
does not alter any screen, so use the manual alternatives above when an integration is off.

## Provisioning

1. Render the profile files (generated, never hand-edited): `python3 deploy/cloud-profiles/cloudprofile.py sync`; check
   with `sync --check` and `check`.
2. Stack name: `deploy/cloud-profiles/tofu/cui.tfvars.json` sets `name` to `depot-cui`, so the stack never shares a name with
   the standard one. **Follow-up:** the AWS GovCloud modules that consume it.
3. Deploy every chart with `-f deploy/cloud-profiles/values/cui.yaml` and `global.imageRegistry` set to the in-boundary
   mirror. That sets `AGENTS_BOUNDARY=cui`, `COMPLIANCE_BOUNDARY=cui` (the need-to-know switch then defaults ON) and
   `AGENTS_AI_ENABLED=false`.
4. Install `deploy/networkpolicy/cui-egress` with the same values file and fill `privateEndpointCidrs` with the PrivateLink
   endpoint ranges. Label every namespace that holds a boundary workload `depot.dev/boundary=cui`. Policies select pods by
   label, not hostname, so the hostnames in `allowHosts` document what the ranges front; the render test checks them.
5. agents-ts refuses `AGENTS_BOUNDARY=cui` at startup (`assertBoundarySupported`) until run routing lands in DEPOT-303 slice 2,
   so the assistant cannot run in the deployment before then even after sign-off.

## Key rotation

**Follow-up:** the Vault transit key hierarchy is not built. Until then rotate what exists: the service signing keys
(`DEPOT_SERVICE_SIGNING_KEY` and the peers' `serviceIdentity.publicKeys`, ADR-0010), the database passwords
(`docs/ops/runbooks/database-password-rotation.md`) and the model endpoint credentials. Rotate inside the boundary only, one
key at a time, and record the date and the operator in the change record. After each rotation run the audit chain
verification (see Incident response).

## Endpoint approval

An endpoint is approved for the deployment only with attestation evidence kept in the change record:

1. The endpoint host, region and model id, and that the host is the GovCloud PrivateLink name (not a commercial region).
2. The provider's authorization evidence for the boundary (FedRAMP High) and the date it was checked.
3. A render of the egress policy showing the host under `allowHosts` and the passing `test_cui_boundary.py` run.
4. The two approvers (security lead and the deployment owner) and the date.

An unapproved or outside host must leave the deployment with AI off: do not set `AGENTS_AI_ENABLED=true`.

## Model or provider change

Any change of provider, model family or endpoint: update the endpoint approval, then rerun the DEPOT-308 suite against
the deployment (`evals/README.md`, "Leakage and red-team gate"; `npm run redteam` in `services/agents-ts` runs the CI
gate), hold the manual red-team session (`docs/signoff/redteam-template.md`) and record a new sign-off
(`docs/signoff/cui-ai-enablement-template.md`). Until the record exists set `aiEnabled: false` again for the environment.

## Enabling AI

1. Complete the sign-off record from `docs/signoff/cui-ai-enablement-template.md` with the DEPOT-308 run id and the red-team
   result, and link it from the change request.
2. Only then set `global.aiEnabled: true` in the environment's values (the generated `cui.yaml` keeps `false`) so the agent
   chart passes `AGENTS_AI_ENABLED=true`.
3. Until then the availability read answers `copilotEnabled: false` with `signoffPending: true` and the web shows "The
   assistant is not available in this deployment until sign-off"; pages and records keep working. The kill switch,
   budgets and the classic-navigation fallback are unchanged.

## Incident response

1. Pause the assistant: turn on the kill switch (`POST /api/v1/agents/kill-switch`, duty `agents.admin`).
2. Verify the audit chain: `GET /v1/audit/reads:verify` and `GET /v1/audit/records:verify` (`services/audit/README.md`); a
   break follows `docs/ops/runbooks/audit-chain-verify-failure.md`.
3. Export the evidence: `GET /v1/audit/reads:export?format=ndjson` streams the AI data access rows and reports the chain
   verdict in its headers; keep the head hash with the incident record.
4. Do not copy exported rows out of the boundary. Reviewers work inside it.
5. Set `AGENTS_AI_ENABLED=false` and re-enable only through a new sign-off record.

## Open questions

Not decided by this change: which disabled sub-processors get an in-boundary replacement later (tax calculation and
e-invoicing may be legally required for some customers); the CMMC and FedRAMP authorization route; and whether ITAR or
export-control limits on personnel access (US persons) apply to operations.
