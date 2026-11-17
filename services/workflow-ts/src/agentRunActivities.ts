import { Context } from "@temporalio/activity";
import { callFailure, mintOrReject } from "./serviceCall.js";
import type { ServiceTokenSigner } from "./serviceToken.js";

/** The run a step belongs to. Only ids travel through Temporal: the prompt, tokens and pseudonym map stay in the agents service. */
export interface AgentRunRef {
  tenantId: string;
  runId: string;
  /** The user who started the run: the actor of the service token. */
  actor: string;
}

/** What a run step answers (services/agents-ts runtime/steps.ts StepOutcome). */
export type AgentStepOutcome =
  | { next: "plan" }
  | { next: "await_approval"; actionId: string; expiresAtMs: number }
  | { next: "commit" }
  | { next: "done"; status: string; code?: string; reason?: string };

export interface AgentRunActivities {
  /** Reserves the budget hold and opens the run; the kill switch is checked here, before any step. */
  agentBegin(ref: AgentRunRef): Promise<AgentStepOutcome>;
  /** Kill-switch check, the model call and the proposal's gates; opens the approval request. */
  agentPlan(ref: AgentRunRef): Promise<AgentStepOutcome>;
  /** Reads the stored decision; with `expire` an undecided request is rejected as expired. */
  agentResolve(ref: AgentRunRef & { expire: boolean }): Promise<AgentStepOutcome>;
  /** Kill-switch check, then the tool call. */
  agentCommit(ref: AgentRunRef): Promise<AgentStepOutcome>;
  /** Ends a run the workflow gave up on, FAILED. */
  agentAbort(ref: AgentRunRef & { reason: string }): Promise<AgentStepOutcome>;
}

export function createAgentRunActivities(baseUrl: string, signer: ServiceTokenSigner, fetchImpl: typeof fetch = fetch): AgentRunActivities {
  const call = async (step: string, ref: AgentRunRef, body: Record<string, unknown> = {}): Promise<AgentStepOutcome> => {
    const response = await fetchImpl(`${baseUrl}/api/v1/agents/runs/${encodeURIComponent(ref.runId)}/steps/${step}`, {
      method: "POST",
      headers: {
        "X-Depot-Service-Token": mintOrReject(() => signer.mintForAgents({ tenantId: ref.tenantId, actor: ref.actor })),
        "Content-Type": "application/json",
      },
      body: JSON.stringify(body),
      signal: Context.current().cancellationSignal,
    });
    if (!response.ok) throw await callFailure(`agent run ${step}`, response);
    return (await response.json()) as AgentStepOutcome;
  };
  return {
    agentBegin: (ref) => call("begin", ref),
    agentPlan: (ref) => call("plan", ref),
    agentResolve: (ref) => call("resolve", ref, { expire: ref.expire }),
    agentCommit: (ref) => call("commit", ref),
    agentAbort: (ref) => call("abort", ref, { reason: ref.reason }),
  };
}
