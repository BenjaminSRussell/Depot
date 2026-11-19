import { parseDecision, parseReason } from "../api/decision.js";
import { ApiError, asObject, requireDuty, type Principal } from "../api/principal.js";
import { RUN_READ_DUTY } from "../api/runs.js";
import type { ActionRecord } from "../domain/audit.js";
import { assertNoPii } from "../domain/redaction.js";
import { NOT_REVERSIBLE_CODE, reversibilityOf } from "../domain/reversibility.js";
import type { ActionStore } from "../store/actions.js";
import type { StoredRun } from "../store/pg.js";

/** Lets someone other than the run's invoker decline or undo its actions. */
export const ACTION_DECIDE_DUTY = "agentapproval.decide";

export interface ActionDeclineResult {
  runId: string;
  actionId: string;
  decision: "REJECTED";
  declinedBy: string;
  declinedAt: string;
  reason: string | null;
}

export interface ActionServiceDeps {
  runs: { getRun(tenantId: string, runId: string): Promise<StoredRun | undefined> };
  actions: ActionStore;
  now?: () => Date;
}

/**
 * Declines and undoes the actions of a run. Both need `agents.run.read` and either the run's invoker or the
 * `agentapproval.decide` duty; another tenant's run or action is never found. No tool is called here, so the domain
 * record is never touched.
 */
export function createActionService(deps: ActionServiceDeps) {
  const now = deps.now ?? (() => new Date());

  async function load(principal: Principal, runId: string, actionId: string): Promise<ActionRecord> {
    requireDuty(principal, RUN_READ_DUTY, "agent runs");
    const run = await deps.runs.getRun(principal.tenantId, runId);
    if (run === undefined) throw new ApiError(404, "AGT-0404", "no such run");
    if (run.invokerUserId !== principal.userId) requireDuty(principal, ACTION_DECIDE_DUTY, "another user's run");
    const action = await deps.actions.find(principal.tenantId, runId, actionId);
    if (action === undefined) throw new ApiError(404, "AGT-0404", "no such action");
    return action;
  }

  return {
    /** Declines an ungated (AUTO) action. Idempotent: a repeat returns the stored decline and writes nothing. */
    async decline(principal: Principal, runId: string, actionId: string, body: unknown): Promise<ActionDeclineResult> {
      const { decision, reason } = parseDecision(body);
      if (decision !== "REJECTED") throw new ApiError(400, "AGT-0400", "an action without an approval gate can only be declined (REJECTED)");
      if (reason !== null) assertNoPii(reason);
      const action = await load(principal, runId, actionId);
      if (action.decision !== "AUTO") throw new ApiError(409, "AGT-0409", `this action was already ${action.decision.toLowerCase()}`);
      const stored = await deps.actions.decline(principal.tenantId, runId, actionId, { by: principal.userId, reason, at: now().toISOString() });
      if (stored?.declined === undefined) throw new ApiError(409, "AGT-0409", "this action cannot be declined");
      return { runId, actionId, decision: "REJECTED", declinedBy: stored.declined.by, declinedAt: stored.declined.at, reason: stored.declined.reason };
    },

    /** Every action is refused in this slice: the registry marks none reversible yet (domain/reversibility.ts). */
    async undo(principal: Principal, runId: string, actionId: string, body: unknown): Promise<never> {
      const reason = parseReason(asObject(body, ["reason"]).reason);
      if (reason !== null) assertNoPii(reason);
      const action = await load(principal, runId, actionId);
      throw new ApiError(409, NOT_REVERSIBLE_CODE, reversibilityOf(action).reversibleReason);
    },
  };
}

export type ActionService = ReturnType<typeof createActionService>;
