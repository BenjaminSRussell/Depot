import { ActivityFailure, condition, defineQuery, defineSignal, proxyActivities, setHandler } from "@temporalio/workflow";
import type { AgentRunActivities, AgentRunRef, AgentStepOutcome } from "./agentRunActivities.js";

/**
 * An AI agent run (FR-47.4, FR-47.8). The run's logic lives in services/agents-ts (runtime/steps.ts); this workflow is
 * its durable driver: begin, plan (the model call), then, when the proposal needs a human, wait for the approval as a
 * signal with a timer for expiry, then commit. Each step is an activity that calls agents with a service token and is
 * idempotent, so a worker or an agents pod restarting at any point, even days into the approval wait, resumes the run
 * where it was. The signal is only a wake-up: the decision itself is read from the agents service, so a lost, early or
 * repeated signal cannot change the outcome. Only ids are workflow input; the prompt, tokens and pseudonym map stay in
 * the agents database.
 */

export interface AgentRunInput {
  tenantId: string;
  runId: string;
  /** The user who started the run. */
  actor: string;
}

export interface AgentRunState {
  runId: string;
  tenantId: string;
  status: "starting" | "planning" | "awaiting_approval" | "committing" | "done";
  actionId?: string;
  outcome?: { status: string; code?: string; reason?: string };
}

export interface AgentApprovalWake {
  actionId: string;
}

export const agentApprovalDecidedSignal = defineSignal<[wake: AgentApprovalWake]>("agent-approval-decided");
export const agentRunStateQuery = defineQuery<AgentRunState>("agent-run-state");

export function agentRunWorkflowId(runId: string): string {
  return `agent-run:${runId}`;
}

/** While waiting, the stored decision is re-read this often, so a signal that never arrived delays a run by at most this long. */
export const AGENT_APPROVAL_RECHECK_MS = 60 * 60 * 1000;

const activities = proxyActivities<AgentRunActivities>({
  startToCloseTimeout: "5 minutes",
  retry: { initialInterval: "2 seconds", maximumInterval: "1 minute", maximumAttempts: 6, nonRetryableErrorTypes: ["ServiceCallRejected"] },
});

export function validateAgentRunInput(input: AgentRunInput): void {
  if (!input.tenantId || !input.runId || !input.actor) throw new TypeError("tenantId, runId and actor are required");
}

export async function agentRunWorkflow(input: AgentRunInput): Promise<AgentRunState> {
  validateAgentRunInput(input);
  const ref: AgentRunRef = { tenantId: input.tenantId, runId: input.runId, actor: input.actor };
  const state: AgentRunState = { runId: input.runId, tenantId: input.tenantId, status: "starting" };
  let woken = false;
  setHandler(agentRunStateQuery, () => ({ ...state }));
  setHandler(agentApprovalDecidedSignal, (wake) => {
    if (state.actionId === undefined || wake.actionId === state.actionId) woken = true;
  });

  /** A step that fails for good (retries spent, or refused) ends the run FAILED instead of leaving it RUNNING in the database. */
  const step = async (run: () => Promise<AgentStepOutcome>): Promise<AgentStepOutcome> => {
    try {
      return await run();
    } catch (error) {
      if (!(error instanceof ActivityFailure)) throw error;
      return activities.agentAbort({ ...ref, reason: (error.cause?.message ?? error.message).slice(0, 300) });
    }
  };

  let out = await step(() => activities.agentBegin(ref));
  while (out.next !== "done") {
    if (out.next === "plan") {
      state.status = "planning";
      out = await step(() => activities.agentPlan(ref));
    } else if (out.next === "await_approval") {
      state.status = "awaiting_approval";
      state.actionId = out.actionId;
      const deadline = out.expiresAtMs;
      for (;;) {
        const left = deadline - Date.now();
        if (left > 0 && !woken) await condition(() => woken, Math.min(left, AGENT_APPROVAL_RECHECK_MS));
        woken = false;
        out = await step(() => activities.agentResolve({ ...ref, expire: Date.now() >= deadline }));
        if (out.next !== "await_approval") break;
      }
    } else {
      state.status = "committing";
      out = await step(() => activities.agentCommit(ref));
    }
  }
  state.status = "done";
  state.outcome = { status: out.status, ...(out.code !== undefined ? { code: out.code } : {}), ...(out.reason !== undefined ? { reason: out.reason } : {}) };
  return { ...state };
}
