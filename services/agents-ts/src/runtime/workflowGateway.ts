import type { Principal } from "../api/principal.js";

/** workflow-ts as this service uses it: start a run's workflow, and wake it when an approval was decided (FR-47.4). */
export interface WorkflowGateway {
  start(caller: Principal, runId: string): Promise<void>;
  /** A wake-up only: the workflow reads the stored decision, so a lost or repeated signal changes nothing. */
  approvalDecided(caller: Principal, runId: string, actionId: string): Promise<void>;
}

/** The caller's edge identity travels as on every service-to-service call behind the edge; workflow-ts checks the duty. */
export function createWorkflowGateway(cfg: { baseUrl: string; fetchFn?: typeof fetch; timeoutMs?: number }): WorkflowGateway {
  const fetchFn = cfg.fetchFn ?? fetch;
  const base = cfg.baseUrl.replace(/\/$/, "");
  const post = async (caller: Principal, path: string, body: Record<string, unknown>): Promise<void> => {
    const res = await fetchFn(`${base}${path}`, {
      method: "POST",
      headers: {
        "content-type": "application/json",
        "x-passage-tenant": caller.tenantId,
        "x-passage-user": caller.userId,
        "x-passage-duties": [...caller.duties].join(","),
      },
      body: JSON.stringify(body),
      signal: AbortSignal.timeout(cfg.timeoutMs ?? 10_000),
    });
    if (!res.ok) throw new Error(`the workflow service answered ${res.status}`);
  };
  return {
    start: (caller, runId) => post(caller, `/v1/workflows/agent-runs/${encodeURIComponent(runId)}:start`, {}),
    approvalDecided: (caller, runId, actionId) => post(caller, `/v1/workflows/agent-runs/${encodeURIComponent(runId)}:approval-decided`, { actionId }),
  };
}
