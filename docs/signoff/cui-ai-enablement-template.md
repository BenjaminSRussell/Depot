# CUI deployment AI enablement sign-off record

Required before `AGENTS_AI_ENABLED` is set to `true` in an isolated CUI deployment (DEPOT-310), and again after any
provider, model family or endpoint change in that deployment. Copy this file to
`docs/signoff/cui-ai-enablement-<deployment>-<date>.md`, fill it in and link it from the change request. The generated
`deploy/cloud-profiles/values/cui.yaml` keeps `aiEnabled: false`; this record is what authorizes overriding it.

## Deployment

| Field | Value |
|---|---|
| Deployment name (`global.deploymentName`) | |
| Deployment profile (must be `cui`) | |
| Model endpoint host, region and model id | |
| Endpoint approval record (runbook, "Endpoint approval") | |
| Release or change id | |

## Evidence

| Field | Value |
|---|---|
| DEPOT-308 suite run id, run inside this deployment | |
| DEPOT-308 result (pass required) | |
| Red-team session record (`docs/signoff/redteam-template.md` copy) | |
| Red-team result | |
| `test_cui_boundary.py` run for this commit | |
| Audit chain verification after the run (head hash) | |

## Approval

| Field | Value |
|---|---|
| Security lead | |
| Deployment owner | |
| Date | |

## Change

| Field | Value |
|---|---|
| Change reference that sets `global.aiEnabled: true` (or `AGENTS_AI_ENABLED=true`) | |
| Rollback: reference that sets it back to `false` | |
