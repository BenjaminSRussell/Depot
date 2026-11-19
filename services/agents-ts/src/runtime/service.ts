import { randomUUID } from "node:crypto";
import { ApiError, requireDuty, type Principal } from "../api/principal.js";
import { assertCanStartRun, type TokenBudget } from "../domain/budget.js";
import type { ActionRecord } from "../domain/audit.js";
import { type ActionView, toActionView } from "../domain/reversibility.js";
import type { Money } from "../domain/money.js";
import type { AgentVersion } from "../domain/registry.js";
import { AGENT_INVOKE_DUTY } from "../agents/catalog.js";
import { AgentError } from "../errors.js";
import type { StoredRun } from "../store/pg.js";
import type { PendingApproval } from "../store/approvals.js";
import { createPersistedApprovalPort, type ApprovalInbox } from "./approvals.js";
import {
  runAgent,
  type AuditPort,
  type BudgetPort,
  type KillSwitchPort,
  type ModelProvider,
  type RunDeps,
  type RunResult,
} from "./engine.js";
import type { McpConnector } from "./mcp.js";
import type { RunInput, RunSteps } from "./steps.js";
import type { WorkflowGateway } from "./workflowGateway.js";
import { STATE_RETENTION_MS, tokenExpiryMs, type RunStateStore } from "../store/runstate.js";
import { WorkflowRefused } from "./workflowGateway.js";
import type { DecisionResult } from "../api/decision.js";
import { createFactRisk } from "./risk.js";

/** What the run service reads from the database beyond the engine's audit and budget ports. */
export interface RunStore extends AuditPort, BudgetPort {
  getRun(tenantId: string, runId: string): Promise<StoredRun | undefined>;
  loadActions(tenantId: string, runId: string): Promise<ActionRecord[]>;
  budgetSnapshot(tenantId: string, month: string): Promise<TokenBudget>;
}

export interface RunServiceConfig {
  /** Per-run token cap (FR-47.11), input and output. */
  runTokenCap: number;
  /** Above this an approver who started the run may not approve it (9.47.12, wave-48 SoD). */
  selfApprovalLimit: Money;
  /** `agent.approval_expiry_h`; absent means the BR-47.3 default of 72 hours. */
  approvalExpiryHours?: number;
  approvalPollMs?: number;
  pseudonymTtlMs: number;
}

export interface RunServiceDeps {
  agents: readonly AgentVersion[];
  /** True when this exact definition's newest eval run passed (FR-47.10; production: PgVersionStore.evalPassed). */
  evalPassed: (agent: AgentVersion) => boolean | Promise<boolean>;
  /** The provider for a model id; each agent version pins its model (FR-47.10). */
  providerFor: (modelId: string) => ModelProvider;
  mcp: McpConnector;
  store: RunStore;
  inbox: ApprovalInbox & { pendingForRun(tenantId: string, runId: string): Promise<PendingApproval[]> };
  killSwitch: KillSwitchPort;
  config: RunServiceConfig;
  now?: () => Date;
  sleep?: (ms: number) => Promise<void>;
  /** Runs the background work; tests pass a function that collects the promise. */
  spawn?: (work: Promise<unknown>) => void;
  newRunId?: () => string;
  /**
   * Set when runs execute as workflows (AGENTS_RUN_ENGINE=temporal): `start` stores the run and starts its workflow
   * instead of spawning the in-process engine, and approval decisions wake the workflow. Absent: the in-process engine.
   */
  workflow?: { steps: RunSteps; state: RunStateStore; gateway: WorkflowGateway };
}

export interface StartedRun {
  runId: string;
  agentId: string;
  agentVersion: number;
  status: "RUNNING";
}

export interface RunView {
  run: StoredRun;
  pendingApprovals: PendingApproval[];
  actions: ActionView[];
}

/**
 * Starts and reads agent runs for the HTTP surface (PRD 9.47.5). A start is refused before any state
 * exists when the agent is unknown, the caller lacks `agents.invoke` or the agent's own duties
 * (AGT-0203), the kill switch is on (AGT-0201), the version has no passing evaluation (AGT-0204) or
 * the month's budget is exhausted (AGT-0202). Then the run executes in the background with the
 * caller's token, and the answer is 202 with the run id; the run view shows its progress.
 */
export function createRunService(deps: RunServiceDeps) {
  const now = deps.now ?? (() => new Date());
  const spawn =
    deps.spawn ??
    ((work: Promise<unknown>): void => {
      work.catch((err: unknown) => {
        process.stderr.write(`${JSON.stringify({ level: "error", message: "agent run failed", service: "agents-ts", error: String(err) })}\n`);
      });
    });
  const newRunId = deps.newRunId ?? randomUUID;
  const prod = new Map(deps.agents.map((a) => [a.agentId, a]));

  return {
    agentIds(): string[] {
      return [...prod.keys()];
    },

    async start(caller: Principal, token: string, agentId: string, prompt: string): Promise<StartedRun> {
      const agent = prod.get(agentId);
      if (agent === undefined) throw new ApiError(404, "AGT-0404", `no agent ${agentId}`);
      requireDuty(caller, AGENT_INVOKE_DUTY, `agent ${agentId}`);
      for (const duty of agent.requiredDuties) requireDuty(caller, duty, `agent ${agentId}`);
      if (deps.killSwitch.isHalted(caller.tenantId)) throw new AgentError("AGT-0201", "agent runs are halted by the kill switch");
      if (!(await deps.evalPassed(agent))) {
        throw new AgentError("AGT-0204", `agent ${agent.agentId} v${agent.version} has no passing evaluation`);
      }
      assertCanStartRun(await deps.store.budgetSnapshot(caller.tenantId, now().toISOString().slice(0, 7)));

      const runId = newRunId();
      const wf = deps.workflow;
      if (wf !== undefined) {
        // v1 has no autonomous posting (9.47.1): every agent runs in suggest mode. The prompt can hold PII and the token is a
        // credential, so both go to the run's own tables and only the run id travels to Temporal.
        const input: RunInput = {
          agentId: agent.agentId,
          agentVersion: agent.version,
          invokerUserId: caller.userId,
          duties: [...caller.duties],
          prompt,
          policy: { mode: "SUGGEST", autoThreshold: 1, autoAmountLimit: { amount: "0", currency: deps.config.selfApprovalLimit.currency } },
          selfApprovalLimit: deps.config.selfApprovalLimit,
          runTokenCap: deps.config.runTokenCap,
          ...(deps.config.approvalExpiryHours !== undefined ? { approvalExpiryHours: deps.config.approvalExpiryHours } : {}),
        };
        // Opportunistic retention for this tenant: expired credentials and finished runs' state past 24 h.
        await wf.state.sweep(caller.tenantId, STATE_RETENTION_MS).catch(() => 0);
        await wf.steps.create(caller.tenantId, runId, input, { kind: "starter", userId: caller.userId, duties: input.duties, token }, tokenExpiryMs(token) ?? now().getTime() + 5 * 60_000);
        // The workflow id comes from the run id, so starting again is the same run. A refusal is final; a timeout or a dropped
        // connection is unknown (the workflow may have started), so it is tried again before the start is given up.
        let started = false;
        for (let attempt = 0; attempt < 3 && !started; attempt += 1) {
          try {
            await wf.gateway.start(caller, runId);
            started = true;
          } catch (err) {
            if (err instanceof WorkflowRefused) break;
          }
        }
        if (!started) {
          await wf.state.discard(caller.tenantId, runId).catch(() => undefined);
          throw new ApiError(502, "AGT-0214", "the workflow service is unavailable");
        }
        return { runId, agentId: agent.agentId, agentVersion: agent.version, status: "RUNNING" };
      }
      let session;
      try {
        session = await deps.mcp.connect(caller, token, runId);
      } catch {
        throw new ApiError(502, "AGT-0214", "the tool gateway is unavailable");
      }
      const nowIso = (): string => now().toISOString();
      const nowMs = (): number => now().getTime();
      const cfg = deps.config;
      const runDeps: RunDeps = {
        provider: deps.providerFor(agent.modelId),
        tools: session,
        approvals: createPersistedApprovalPort({
          inbox: deps.inbox,
          tenantId: caller.tenantId,
          invokerUserId: caller.userId,
          approvalDuty: (tool) => session.describe(tool)?.requiredDuty ?? AGENT_INVOKE_DUTY,
          selfApprovalLimit: cfg.selfApprovalLimit,
          ...(cfg.approvalExpiryHours !== undefined ? { expiryHours: cfg.approvalExpiryHours } : {}),
          nowMs,
          now: nowIso,
          ...(cfg.approvalPollMs !== undefined ? { pollMs: cfg.approvalPollMs } : {}),
          ...(deps.sleep !== undefined ? { sleep: deps.sleep } : {}),
        }),
        // System-side risk facts (amount against the limits, counterparty, segregation of duties and the payment prediction) are read
        // from the owning domains through the run's own tool session; every proposal still goes to a human (suggest mode).
        risk: createFactRisk({ tools: session, invokerUserId: caller.userId, selfApprovalLimit: cfg.selfApprovalLimit }),
        killSwitch: deps.killSwitch,
        now: nowIso,
        nowMs,
        pseudonymTtlMs: cfg.pseudonymTtlMs,
        audit: deps.store,
        budgets: deps.store,
      };
      spawn(
        runAgent(runDeps, {
          runId,
          tenantId: caller.tenantId,
          invokerUserId: caller.userId,
          userDuties: caller.duties,
          agent,
          evalPassed: true,
          // v1 has no autonomous posting (9.47.1): every agent runs in suggest mode.
          policy: { mode: "SUGGEST", autoThreshold: 1, autoAmountLimit: { amount: "0", currency: cfg.selfApprovalLimit.currency } },
          selfApprovalLimit: cfg.selfApprovalLimit,
          runTokenCap: cfg.runTokenCap,
          prompt,
        }),
      );
      return { runId, agentId: agent.agentId, agentVersion: agent.version, status: "RUNNING" };
    },

    /**
     * After the decision endpoint recorded a decision (workflow mode): keeps the approver's token for the commit call, which
     * runs as the approver, and wakes the run's workflow. Best effort: the decision is already stored, and the workflow also
     * re-reads it on a timer, so a failure here only delays the run.
     */
    async decided(caller: Principal, token: string, result: DecisionResult): Promise<void> {
      const wf = deps.workflow;
      if (wf === undefined) return;
      try {
        // The approver's token is kept only for a decision this user gave, on the request the run is waiting on: a replay by
        // anyone else, or a decision for a run that has ended, stores nothing.
        if (result.decision === "APPROVED" && result.approverUserId === caller.userId) {
          const row = await wf.state.load<unknown, { phase?: string; actionId?: string }>(caller.tenantId, result.runId);
          if (row?.state.phase === "awaiting" && row.state.actionId === result.actionId) {
            await wf.state.putCredential(caller.tenantId, result.runId, { kind: "approver", userId: caller.userId, duties: [...caller.duties], token }, tokenExpiryMs(token) ?? now().getTime() + 5 * 60_000);
          }
        }
      } catch (err) {
        process.stderr.write(`${JSON.stringify({ level: "warn", message: "approver session not kept for the run", service: "agents-ts", runId: result.runId, error: String(err) })}\n`);
      }
      try {
        await wf.gateway.approvalDecided(caller, result.runId, result.actionId);
      } catch (err) {
        // The run does not depend on the wake: it re-reads the stored decision on its timer.
        process.stderr.write(`${JSON.stringify({ level: "warn", message: "approval decision not delivered to the workflow", service: "agents-ts", runId: result.runId, error: String(err) })}\n`);
      }
    },

    async view(caller: Principal, runId: string): Promise<RunView> {
      const run = await deps.store.getRun(caller.tenantId, runId);
      if (run === undefined) throw new ApiError(404, "AGT-0404", "no such run");
      const [pendingApprovals, actions] = await Promise.all([deps.inbox.pendingForRun(caller.tenantId, runId), deps.store.loadActions(caller.tenantId, runId)]);
      return { run, pendingApprovals, actions: actions.map(toActionView) };
    },

    async actions(caller: Principal, runId: string): Promise<ActionView[]> {
      if ((await deps.store.getRun(caller.tenantId, runId)) === undefined) throw new ApiError(404, "AGT-0404", "no such run");
      return (await deps.store.loadActions(caller.tenantId, runId)).map(toActionView);
    },
  };
}

export type RunService = ReturnType<typeof createRunService>;
export type { RunResult };
