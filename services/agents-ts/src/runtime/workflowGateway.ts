import type { Principal } from "../api/principal.js";
import type { ServiceSigner } from "../api/serviceSigner.js";

/** workflow-ts as this service uses it: start a run's workflow, and wake it when an approval was decided (FR-47.4). */
export interface WorkflowGateway {
  start(caller: Principal, runId: string): Promise<void>;
  /** A wake-up only: the workflow reads the stored decision, so a lost or repeated signal changes nothing. */
  approvalDecided(caller: Principal, runId: string, actionId: string): Promise<void>;
}

/** Thrown when workflow-ts answered with a refusal: the request was seen and not started (as against a timeout, which is unknown). */
export class WorkflowRefused extends Error {}

/**
 * agents calls workflow-ts under its own signed service token (grant agents.run.signal, tenant-level, the run's user as actor), the
 * way workflow-ts calls agents. workflow-ts trusts the X-Depot-* headers on its edge routes, so no pod that merely reaches it can
 * claim an identity there; these two routes take only a verified agents token. The wake is authorized as the agents service, not as
 * the approver, so approvers need no workflow duty.
 */
export function createWorkflowGateway(cfg: { baseUrl: string; signer: ServiceSigner; fetchFn?: typeof fetch; timeoutMs?: number }): WorkflowGateway {
  const fetchFn = cfg.fetchFn ?? fetch;
  const base = cfg.baseUrl.replace(/\/$/, "");
  const post = async (caller: Principal, path: string, body: Record<string, unknown>): Promise<void> => {
    const res = await fetchFn(`${base}${path}`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "x-depot-service-token": cfg.signer.mintForWorkflow({ tenantId: caller.tenantId, actor: caller.userId }),
      },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(cfg.timeoutMs ?? 10_000),
    });
    if (!res.ok) throw new WorkflowRefused(`the workflow service answered ${res.status}`);
  };
  return {
    start: (caller, runId) => post(caller, `/v1/workflows/agent-runs/${encodeURIComponent(runId)}:start`, {}),
    approvalDecided: (caller, runId, actionId) => post(caller, `/v1/workflows/agent-runs/${encodeURIComponent(runId)}:approval-decided`, { actionId }),
  };
}
