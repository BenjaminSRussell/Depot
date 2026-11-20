import { buildActionRecord, type ActionRecord, type Decision } from "../domain/audit.js";
import { canCallTool, selfApprovalBlocked } from "../domain/authz.js";
import { assertPromptWithinCap, assertRunWithinCap, estimateTokens, outputBudgetForCall, type BudgetState } from "../domain/budget.js";
import { DEFAULT_APPROVAL_EXPIRY_H } from "../domain/approval.js";
import type { Money } from "../domain/money.js";
import { decideDisposition, isApprovalExpired, validatePolicy, type ModePolicy } from "../domain/policy.js";
import { assertNoPii, Pseudonymizer, scanPii } from "../domain/redaction.js";
import type { AgentVersion } from "../domain/registry.js";
import { AgentError } from "../errors.js";
import { AGENT_INVOKE_DUTY } from "../agents/catalog.js";
import { createHash } from "node:crypto";
import { ProviderError, proposeTool } from "../providers/wire.js";
import type { ApprovalStore, RecordedDecision } from "../api/decision.js";
import type { Principal } from "../api/principal.js";
import type { NewApproval } from "../store/approvals.js";
import type { RunCredential, RunStateStore } from "../store/runstate.js";
import { guardActionRecord, riskScoreOf, RUN_TIMEOUT_MS, type RiskPort, type AuditPort, type BudgetHold, type BudgetPort, type KillSwitchPort, type ModelProvider, type ModelResponse, type ModelUsage, type RunRequest, type RunResult, type ToolPort } from "./engine.js";
import type { McpConnector, McpSession } from "./mcp.js";
import { createFactRisk } from "./risk.js";
import type { RunStatus } from "../domain/run.js";
import type { GuardResult } from "./egressGuard.js";
import { checkRunOutput, createOutputGuard, OUTPUT_GUARD_CODE, OUTPUT_GUARD_MESSAGE, OUTPUT_GUARD_TOOL, outputBlockedResult, presentForCaller, type OutputGuard } from "./outputGuard.js";

/**
 * The agent run as resumable steps (FR-47.4, FR-47.8). The in-process engine (engine.ts) runs one run in one
 * promise and dies with its pod. Here each phase is a function over durable state (`agent_run_state`), so a
 * Temporal workflow (services/workflow-ts agentRunWorkflow) can call them as activities and wait for the approval
 * as a signal with a timer, across pod restarts. Every step is idempotent: a step retried after a lost response
 * reads the stored state and answers as it did. The phases and the rules in them are the engine's.
 */

/** Fixed at start (the prompt can hold PII, so it stays in this row and never enters Temporal history). */
export interface RunInput {
  agentId: string;
  agentVersion: number;
  invokerUserId: string;
  duties: string[];
  prompt: string;
  policy: ModePolicy;
  selfApprovalLimit: Money;
  runTokenCap: number;
  approvalExpiryHours?: number;
}

export type StepPhase = "created" | "begun" | "called" | "awaiting" | "approved" | "committing" | "committed" | "done";

export interface StepState {
  phase: StepPhase;
  seq: number;
  guardRows?: number;
  hold?: BudgetHold;
  tokensIn: number;
  tokensOut: number;
  estimated: boolean;
  mayHaveSpent: boolean;
  called: boolean;
  charged?: BudgetState;
  displayText: string;
  outputBlocked?: string;
  /** The model's answer, kept (with its usage) as soon as it came back, so a retried `plan` never calls the model again. */
  pendingUsage?: ModelUsage;
  proposal?: { tool: string; argsRedacted: Record<string, unknown>; inputRedacted: string; confidence: number; amount: Money; riskScore: number; disposition: "PROPOSE" | "AUTO"; approvalDuty?: string };
  /** sha256 of the model's proposal (tool, arguments, confidence); the approval request carries it and the commit checks it. */
  proposalHash?: string;
  /** How many approval requests this run has opened beyond the first (a re-approval after the approver's session expired). */
  approvalAttempt?: number;
  /** Set once the tool call succeeded, so a retry after that finishes the audit instead of calling the tool or failing the run. */
  committedRef?: string | null;
  actionId?: string;
  requestedAtMs?: number;
  expiryHours?: number;
  decision?: { decision: Decision; approver: string | null };
  outcome?: { status: RunStatus; code?: string; reason?: string };
}

export const initialStepState = (): StepState => ({ phase: "created", seq: 0, tokensIn: 0, tokensOut: 0, estimated: false, mayHaveSpent: false, called: false, displayText: "" });

/** What a step tells the workflow to do next. */
export type StepOutcome =
  | { next: "plan" }
  | { next: "await_approval"; actionId: string; expiresAtMs: number }
  | { next: "commit" }
  | { next: "done"; status: RunStatus; code?: string; reason?: string };

export interface StepAudit extends AuditPort, BudgetPort {
  recordAction(tenantId: string, action: ActionRecord): Promise<void>;
}

export interface StepsDeps {
  agents: readonly AgentVersion[];
  providerFor: (modelId: string) => ModelProvider;
  mcp: McpConnector;
  audit: StepAudit;
  state: RunStateStore;
  inbox: ApprovalStore & { open(a: NewApproval): Promise<string> };
  killSwitch: KillSwitchPort;
  now?: () => Date;
  /** The system-side risk facts for a run's tools; default reads them from the owning domains (risk.ts). Tests inject fixed facts. */
  riskFor?: (tools: ToolPort, input: RunInput) => RiskPort;
  /** Pseudonym mappings live at least this long: an approval can wait up to `approvalExpiryHours`, far past the in-process 24 h. */
  pseudonymTtlMs: number;
  outputGuard?: OutputGuard;
}

/** Binds an approval to exactly what the model proposed. */
export const proposalHashOf = (p: { tool: string; args: Record<string, unknown>; confidence: number }): string =>
  createHash("sha256").update(JSON.stringify({ tool: p.tool, args: p.args, confidence: p.confidence })).digest("hex");

const UNAUDITABLE_INPUT = '{"withheld":"AGT-0210"}';
const MAX_SETTLE_ATTEMPTS = 3;
const isMetered = (u: ModelUsage): boolean => Number.isSafeInteger(u.tokensIn) && u.tokensIn >= 0 && Number.isSafeInteger(u.tokensOut) && u.tokensOut >= 0;

export class StepRefusal extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
  }
}

const defaultRisk = (tools: ToolPort, input: RunInput): RiskPort => createFactRisk({ tools, invokerUserId: input.invokerUserId, selfApprovalLimit: input.selfApprovalLimit });

export function createRunSteps(deps: StepsDeps) {
  const outputGuard = deps.outputGuard ?? createOutputGuard();
  const now = deps.now ?? (() => new Date());
  const agentOf = (input: RunInput): AgentVersion => {
    const a = deps.agents.find((x) => x.agentId === input.agentId && x.version === input.agentVersion) ?? deps.agents.find((x) => x.agentId === input.agentId);
    if (a === undefined) throw new StepRefusal(404, "AGT-0404", `no agent ${input.agentId}`);
    return a;
  };

  interface Ctx {
    tenantId: string;
    runId: string;
    input: RunInput;
    state: StepState;
    agent: AgentVersion;
    pseudo: Pseudonymizer;
    req: RunRequest;
  }

  async function load(tenantId: string, runId: string): Promise<Ctx> {
    const row = await deps.state.load<RunInput, StepState>(tenantId, runId);
    if (row === undefined) throw new StepRefusal(404, "AGT-0404", "no such run");
    const agent = agentOf(row.input);
    const pseudo = new Pseudonymizer(tenantId, deps.pseudonymTtlMs, () => now().getTime());
    pseudo.load(row.pseudonyms);
    const req: RunRequest = {
      runId,
      tenantId,
      invokerUserId: row.input.invokerUserId,
      userDuties: new Set(row.input.duties),
      agent,
      evalPassed: true,
      policy: row.input.policy,
      selfApprovalLimit: row.input.selfApprovalLimit,
      runTokenCap: row.input.runTokenCap,
      prompt: row.input.prompt,
    };
    return { tenantId, runId, input: row.input, state: row.state, agent, pseudo, req };
  }

  const save = (c: Ctx): Promise<void> => deps.state.save(c.tenantId, c.runId, c.state, c.pseudo.snapshot());
  const logError = (c: Ctx, message: string, error: string): void => {
    let safe = error;
    for (const f of scanPii(error)) safe = safe.split(f.value).join(`[${f.category}]`);
    process.stderr.write(`${JSON.stringify({ level: "error", message, service: "agents-ts", tenantId: c.tenantId, runId: c.runId, error: safe })}\n`);
  };

  async function sessionFor(c: Ctx, kinds: RunCredential["kind"][]): Promise<{ session: McpSession; cred: RunCredential } | undefined> {
    for (const kind of kinds) {
      const cred = await deps.state.credential(c.tenantId, c.runId, kind);
      if (cred === undefined) continue;
      const caller: Principal = { tenantId: c.tenantId, userId: cred.userId, duties: new Set(cred.duties) };
      return { session: await deps.mcp.connect(caller, cred.token, c.runId), cred };
    }
    return undefined;
  }

  /** One action row per step, idempotent per seq (the store keeps the first write). */
  async function record(c: Ctx, tool: string, inputRedacted: string, output: unknown, decision: Decision, approver: string | null, ref: string | null = null, why?: string): Promise<void> {
    const action = buildActionRecord({
      runId: c.runId,
      seq: c.state.seq + 1,
      toolName: tool,
      inputRedacted,
      outputRedacted: c.pseudo.redact(JSON.stringify(output) ?? "null"),
      approverUserId: approver,
      decision,
      decidedAt: now().toISOString(),
      committedEntityRef: ref,
      ...(why !== undefined ? { reason: why } : {}),
    });
    await deps.audit.recordAction(c.tenantId, action);
    c.state.seq += 1;
  }

  /** Every way out of a run after a proposal leaves exactly one action row (AC-47.3). */
  async function closeOut(c: Ctx, output: unknown, why: string, ref: string | null = null): Promise<void> {
    const p = c.state.proposal;
    if (p === undefined || c.state.seq > (c.state.guardRows ?? 0)) return;
    const d = c.state.decision ?? { decision: "DENIED" as Decision, approver: null };
    try {
      await record(c, p.tool, p.inputRedacted, output, d.decision, d.approver, ref, why);
    } catch (err) {
      if (!(err instanceof AgentError && err.code === "AGT-0210")) throw err;
    }
  }

  async function settle(c: Ctx, usage: ModelUsage): Promise<BudgetState> {
    const hold = c.state.hold;
    if (hold === undefined) throw new Error("run has no budget hold");
    let last: unknown;
    for (let i = 0; i < MAX_SETTLE_ATTEMPTS; i += 1) {
      try {
        return await deps.audit.settle(hold, usage);
      } catch (err) {
        last = err;
      }
    }
    throw last;
  }

  /** Ends the run: settles what the hold still covers, writes the final status, drops the credentials. */
  async function finalize(c: Ctx, status: RunStatus, code?: string, reason?: string): Promise<StepOutcome> {
    if (c.state.outcome !== undefined) return doneOf(c.state.outcome);
    let budgetState: BudgetState | "UNKNOWN" = c.state.charged ?? "UNKNOWN";
    if (c.state.charged === undefined && c.state.hold !== undefined && !c.state.mayHaveSpent) {
      budgetState = await settle(c, { tokensIn: 0, tokensOut: 0 }).catch(() => {
        logError(c, "hold release failed; hold left for the full-cap sweep", "settle failed");
        return "UNKNOWN" as const;
      });
    }
    const endpoint = c.state.called ? deps.providerFor(c.agent.modelId).endpoint : undefined;
    const result: RunResult = {
      runId: c.runId,
      status,
      ...(code !== undefined ? { code } : {}),
      ...(reason !== undefined ? { reason } : {}),
      actions: [],
      tokensIn: c.state.tokensIn,
      tokensOut: c.state.tokensOut,
      estimated: c.state.estimated,
      budgetState,
      displayText: "",
      ...(endpoint !== undefined ? { modelEndpoint: endpoint } : {}),
    };
    try {
      await deps.audit.finishRun(c.req, result);
    } catch (err) {
      if (status !== "COMMITTED") throw err;
      const message = err instanceof Error ? err.message : String(err);
      logError(c, "audit write failed after commit", message);
      await deps.audit.finishRun(c.req, result, message).catch(() => undefined);
    }
    const outcome = { status, ...(code !== undefined ? { code } : {}), ...(reason !== undefined ? { reason } : {}) };
    // The tokens and the real values go first and the "done" mark last, so a retry after a failure in between repeats this and ends.
    await deps.state.purgeCredentials(c.tenantId, c.runId);
    await deps.state.scrub(c.tenantId, c.runId);
    c.state.outcome = outcome;
    c.state.phase = "done";
    // The model's answer with real values restored, the prompt and the pseudonym map are not kept past the run (BR-47.7).
    c.state.displayText = "";
    await deps.state.save(c.tenantId, c.runId, c.state, { entries: [], counters: {} });
    return doneOf(c.state.outcome);
  }

  const doneOf = (o: NonNullable<StepState["outcome"]>): StepOutcome => ({ next: "done", status: o.status, ...(o.code !== undefined ? { code: o.code } : {}), ...(o.reason !== undefined ? { reason: o.reason } : {}) });

  /** Opens the approval request for the run's proposal (bound to it by hash) and waits on it. Idempotent per attempt. */
  async function openApproval(c: Ctx): Promise<void> {
    const { state: s, input } = c;
    const p = s.proposal!;
    const expiryHours = input.approvalExpiryHours ?? DEFAULT_APPROVAL_EXPIRY_H;
    const actionId = await deps.inbox.open({
      tenantId: c.tenantId,
      runId: c.runId,
      // One request per attempt: a re-approval after the approver's session expired is a new request, never a reopened one.
      seq: s.seq + 1 + (s.approvalAttempt ?? 0),
      tool: p.tool,
      invokerUserId: input.invokerUserId,
      approvalDuty: p.approvalDuty ?? AGENT_INVOKE_DUTY,
      amount: p.amount,
      selfApprovalLimit: input.selfApprovalLimit,
      ...(input.approvalExpiryHours !== undefined ? { expiryHours: input.approvalExpiryHours } : {}),
      // The inbox row stays PII-free: the tool, the confidence and the amount only.
      proposalJson: JSON.stringify({ tool: p.tool, confidence: p.confidence, amount: p.amount }),
      proposalHash: s.proposalHash ?? "",
      confidence: p.confidence,
      riskScore: p.riskScore,
    });
    s.actionId = actionId;
    s.requestedAtMs = now().getTime();
    s.expiryHours = expiryHours;
    s.phase = "awaiting";
    await save(c);
  }

  /**
   * The platform has no delegated service credential the tools accept, so a commit needs the approver's own unexpired token.
   * When that token is gone (the approval came and the run was not resumed within the token's life) the run is put back to
   * awaiting a fresh approval, with a new request, instead of failing; the approver is asked again.
   */
  async function reapprove(c: Ctx): Promise<StepOutcome> {
    c.state.approvalAttempt = (c.state.approvalAttempt ?? 0) + 1;
    c.state.decision = undefined;
    await openApproval(c);
    return resumeOf(c);
  }

  async function approverSession(c: Ctx): Promise<McpSession | undefined> {
    const approver = c.state.decision?.approver;
    const cred = await deps.state.credential(c.tenantId, c.runId, "approver");
    if (cred === undefined || approver === null || approver === undefined || cred.userId !== approver) return undefined;
    return deps.mcp.connect({ tenantId: c.tenantId, userId: cred.userId, duties: new Set(cred.duties) }, cred.token, c.runId);
  }

  async function killed(c: Ctx, why: string): Promise<StepOutcome> {
    await closeOut(c, { killed: why }, "KILLED");
    return finalize(c, "KILLED", undefined, why);
  }

  /** A refusal that the engine would have turned into a FAILED run (an AgentError or a provider error), after closing the proposal's row. */
  async function failed(c: Ctx, err: unknown): Promise<StepOutcome> {
    await closeOut(c, { error: "port_error" }, "run failed before the action was recorded");
    return finalize(c, "FAILED", err instanceof AgentError ? err.code : undefined, err instanceof Error ? err.message : String(err));
  }

  const resumeOf = (c: Ctx): StepOutcome => {
    const s = c.state;
    if (s.outcome !== undefined) return doneOf(s.outcome);
    if (s.phase === "awaiting") return { next: "await_approval", actionId: s.actionId ?? "", expiresAtMs: (s.requestedAtMs ?? 0) + (s.expiryHours ?? DEFAULT_APPROVAL_EXPIRY_H) * 3_600_000 };
    if (s.phase === "approved" || s.phase === "committed") return { next: "commit" };
    if (s.phase === "begun") return { next: "plan" };
    return { next: "plan" };
  };

  return {
    /** Stores a new run; the caller (the HTTP start) has already checked duties, evaluation, kill switch and budget. */
    async create(tenantId: string, runId: string, input: RunInput, starter: RunCredential, expiresAtMs: number): Promise<void> {
      await deps.state.create(tenantId, runId, input, initialStepState());
      await deps.state.putCredential(tenantId, runId, starter, expiresAtMs);
    },

    /** Reserves the budget hold and opens the run RUNNING, before any model call. Retried: finds what it already did. */
    async begin(tenantId: string, runId: string): Promise<StepOutcome> {
      const c = await load(tenantId, runId);
      if (c.state.phase !== "created") return resumeOf(c);
      const cap = c.input.runTokenCap;
      if (!Number.isSafeInteger(cap) || cap <= 0) throw new Error(`runTokenCap must be a positive safe integer, got ${cap}`);
      try {
        if (deps.killSwitch.isHalted(tenantId)) throw new AgentError("AGT-0201", "agent runs are halted by the kill switch");
        if (c.input.policy.mode === "OFF") throw new Error(`agent ${c.agent.agentId} is OFF for this legal entity`);
        const policyErrors = validatePolicy(c.input.policy);
        if (policyErrors.length > 0) throw new Error(`invalid agent policy: ${policyErrors.join("; ")}`);
      } catch (err) {
        // Nothing is held or opened yet: a run row is still needed so the failure is visible and audited.
        await deps.audit.startRun(c.req).catch(() => undefined);
        return failed(c, err);
      }
      if (c.state.hold === undefined) {
        // `reserve` is idempotent per run (one hold per run id), and the hold is saved before anything else can fail.
        c.state.hold = await deps.audit.reserve({
          tenantId,
          agentId: c.agent.agentId,
          runId,
          month: now().toISOString().slice(0, 7),
          tokensIn: cap,
          tokensOut: cap,
          ttlMs: RUN_TIMEOUT_MS,
        });
        await save(c);
      }
      if ((await deps.audit.runStatus(c.req)) === undefined) await deps.audit.startRun(c.req);
      c.state.phase = "begun";
      await save(c);
      return { next: "plan" };
    },

    /** Kill-switch check, the one model call (pseudonymized), the proposal's gates and risk, then the approval request or the commit. */
    async plan(tenantId: string, runId: string): Promise<StepOutcome> {
      const c = await load(tenantId, runId);
      if (c.state.phase !== "begun" && c.state.phase !== "called") return resumeOf(c);
      const { state: s, input, agent } = c;
      try {
        if (s.phase === "begun") {
          if (deps.killSwitch.isHalted(tenantId)) return await killed(c, "kill switch activated");
          const prompt = c.pseudo.redact(input.prompt);
          assertNoPii(prompt);
          const toolsText = agent.allowedTools.length > 0 ? JSON.stringify(proposeTool(agent.allowedTools)) : "";
          assertPromptWithinCap([agent.systemPrompt, prompt, toolsText], input.runTokenCap - s.tokensIn);
          const promptTokens = estimateTokens(prompt) + estimateTokens(agent.systemPrompt) + estimateTokens(toolsText);
          const maxOutputTokens = outputBudgetForCall(input.runTokenCap, s.tokensIn + s.tokensOut, promptTokens);
          s.mayHaveSpent = true;
          s.called = true;
          await save(c);
          const provider = deps.providerFor(agent.modelId);
          let response: ModelResponse;
          let guardResult: GuardResult | undefined;
          const recordGuard = async (): Promise<void> => {
            if (guardResult === undefined) return;
            await deps.audit.recordAction(c.tenantId, guardActionRecord(c.runId, s.seq + 1, guardResult, now().toISOString()));
            s.seq += 1;
            s.guardRows = (s.guardRows ?? 0) + 1;
          };
          try {
            response = await provider.complete({ system: agent.systemPrompt, user: prompt, allowedTools: agent.allowedTools, maxOutputTokens, guard: { tenantId: c.tenantId, report: (r) => { guardResult = r; } } });
            await recordGuard();
          } catch (err) {
            await recordGuard();
            if ((err instanceof ProviderError && err.unbilled) || (err instanceof AgentError && (err.code === "AGT-0215" || err.code === "AGT-0219"))) s.mayHaveSpent = false;
            throw err;
          }
          const metered = response.usage !== undefined && isMetered(response.usage);
          s.estimated = !metered;
          const usage: ModelUsage =
            response.usage !== undefined && metered
              ? response.usage
              : response.usage === undefined
                ? { tokensIn: promptTokens, tokensOut: estimateTokens(response.text) }
                : { tokensIn: input.runTokenCap, tokensOut: input.runTokenCap };
          s.tokensIn += usage.tokensIn;
          s.tokensOut += usage.tokensOut;
          const verdict = await checkRunOutput(outputGuard, { tenantId: c.tenantId, prompt, response, pseudo: c.pseudo, duties: new Set(input.duties) });
          if (!verdict.ok) {
            await deps.audit.recordAction(c.tenantId, guardActionRecord(c.runId, s.seq + 1, outputBlockedResult(c.runId, verdict), now().toISOString(), OUTPUT_GUARD_TOOL));
            s.seq += 1;
            s.guardRows = (s.guardRows ?? 0) + 1;
            s.outputBlocked = verdict.reason;
            s.displayText = OUTPUT_GUARD_MESSAGE;
          } else {
            s.displayText = presentForCaller(outputGuard, c.pseudo, verdict.text);
          }
          const p0 = verdict.ok ? response.proposal : undefined;
          if (p0) {
            s.proposal = { tool: p0.tool, argsRedacted: p0.args, inputRedacted: c.pseudo.redact(JSON.stringify(p0.args)), confidence: p0.confidence, amount: p0.amount, riskScore: 0, disposition: "PROPOSE" };
            s.proposalHash = proposalHashOf({ tool: p0.tool, args: p0.args, confidence: p0.confidence });
          }
          // Everything the model said is kept before anything else can fail: a retry resumes from here and never calls the model again.
          s.pendingUsage = usage;
          s.phase = "called";
          await save(c);
        }
        if (s.charged === undefined && s.pendingUsage !== undefined) {
          // Settling twice is safe: the hold is gone after the first, and the second charges nothing.
          s.charged = await settle(c, s.pendingUsage);
          await save(c);
        }
        assertRunWithinCap(s.tokensIn + s.tokensOut, input.runTokenCap);
        const p = s.proposal;
        if (!p) return await finalize(c, "FAILED", s.outputBlocked !== undefined ? OUTPUT_GUARD_CODE : undefined, s.outputBlocked ?? "model returned no proposal");
        const inputRedacted = p.inputRedacted;
        if (deps.killSwitch.isHalted(tenantId)) return await killed(c, "kill switch activated");

        const got = await sessionFor(c, ["starter"]);
        if (got === undefined) throw new AgentError("AGT-0214", "the caller's session is no longer available to read the proposal's facts");
        const tools: ToolPort = got.session;
        const desc = tools.describe(p.tool);
        const deny = async (output: unknown, code: string | undefined, why: string): Promise<StepOutcome> => {
          await record(c, p.tool, inputRedacted, output, "DENIED", null);
          return finalize(c, "FAILED", code, why);
        };
        if (!agent.allowedTools.includes(p.tool) || !desc) return await deny({ error: "not_allowed" }, undefined, `tool ${p.tool} is not on the allow-list of ${agent.agentId}`);
        if (!canCallTool(new Set(input.duties), desc.requiredDuty)) return await deny({ error: "AGT-0203" }, "AGT-0203", `you do not hold duty ${desc.requiredDuty} required by ${p.tool}`);
        if (desc.approvalMode === "none") return await deny({ error: "read_only" }, undefined, `tool ${p.tool} is read-only and cannot be proposed`);

        const args = JSON.parse(c.pseudo.restore(JSON.stringify(p.argsRedacted))) as Record<string, unknown>;
        const risk = await (deps.riskFor ?? defaultRisk)(tools, input).assess(p.tool, args);
        const amount = risk.amount ?? p.amount;
        const disposition =
          desc.approvalMode === "approve_before_commit"
            ? "PROPOSE"
            : decideDisposition(input.policy, { confidence: p.confidence, amount, vendorHighRisk: risk.vendorHighRisk, sodConflict: risk.sodConflict });
        if (disposition === "BLOCKED") return await finalize(c, "FAILED", undefined, `agent ${agent.agentId} is OFF for this legal entity`);
        s.proposal = { ...p, amount, riskScore: riskScoreOf(risk, p.confidence), disposition, approvalDuty: desc.requiredDuty };

        if (disposition === "AUTO") {
          s.decision = { decision: "AUTO", approver: null };
          s.phase = "approved";
          await save(c);
          return { next: "commit" };
        }
        if (deps.killSwitch.isHalted(tenantId)) return await killed(c, "kill switch activated");
        await openApproval(c);
        return resumeOf(c);
      } catch (err) {
        if (err instanceof AgentError || err instanceof ProviderError) return failed(c, err);
        throw err;
      }
    },

    /**
     * Reads the approval's stored decision, the source of truth: the signal only wakes the workflow. With `expire`
     * (the timer fired) an undecided request is recorded REJECTED/EXPIRED, first writer wins against a human.
     */
    async resolve(tenantId: string, runId: string, expire: boolean): Promise<StepOutcome> {
      const c = await load(tenantId, runId);
      if (c.state.phase !== "awaiting") return resumeOf(c);
      const { state: s } = c;
      const actionId = s.actionId ?? "";
      const found = await deps.inbox.find(tenantId, runId, actionId);
      if (found === undefined) throw new Error(`approval ${actionId} disappeared`);
      let decided: RecordedDecision | undefined = found.decided;
      if (decided === undefined) {
        const expired = isApprovalExpired(s.requestedAtMs ?? 0, now().getTime(), s.expiryHours ?? DEFAULT_APPROVAL_EXPIRY_H);
        if (!expire && !expired) return resumeOf(c);
        decided = await deps.inbox.record(tenantId, runId, actionId, { decision: "REJECTED", reason: "EXPIRED", code: "AGT-0208", approverUserId: "system:expiry", decidedAt: now().toISOString() });
      }
      const p = s.proposal!;
      const approver = decided.reason === "EXPIRED" ? null : decided.approverUserId;
      s.decision = { decision: decided.decision, approver };
      try {
        if (decided.decision === "REJECTED") {
          await record(c, p.tool, p.inputRedacted, { rejected: decided.reason ?? "REJECTED" }, "REJECTED", approver);
          return await finalize(c, "REJECTED", decided.reason === "EXPIRED" ? "AGT-0208" : undefined, decided.reason ?? "approver rejected");
        }
        if (selfApprovalBlocked({ approverUserId: approver, invokerUserId: c.input.invokerUserId, amount: p.amount, selfApprovalLimit: c.input.selfApprovalLimit })) {
          await record(c, p.tool, p.inputRedacted, { rejected: "AGT-0209" }, "REJECTED", approver);
          return await finalize(c, "REJECTED", "AGT-0209", "you cannot approve an action from a run you started above the self-approval limit");
        }
      } catch (err) {
        if (err instanceof AgentError) return failed(c, err);
        throw err;
      }
      if ((await deps.state.credential(tenantId, runId, "approver"))?.userId !== approver) return reapprove(c);
      s.phase = "approved";
      await save(c);
      return { next: "commit" };
    },

    /** Kill-switch check, the PII gate, then the commit call as the approver (else the starter). Never repeated after a restart mid-call. */
    async commit(tenantId: string, runId: string): Promise<StepOutcome> {
      const c = await load(tenantId, runId);
      const { state: s } = c;
      if (s.phase === "committing") {
        // The call may or may not have posted: repeating it could double-post, so the run fails visibly instead.
        await closeOut(c, { error: "commit_outcome_unknown" }, "commit interrupted by a restart");
        return finalize(c, "FAILED", undefined, "the commit call was interrupted; check the target record before retrying");
      }
      if (s.phase === "committed") {
        await closeOut(c, { committed: s.committedRef ?? null }, "committed; tool output not recorded", s.committedRef ?? null);
        return finalize(c, "COMMITTED");
      }
      if (s.phase !== "approved") return resumeOf(c);
      const p = s.proposal!;
      const d = s.decision ?? { decision: "AUTO" as Decision, approver: null };
      try {
        if (deps.killSwitch.isHalted(tenantId)) return await killed(c, "kill switch activated before commit");
        if (d.decision !== "AUTO") {
          // What is posted is what was approved: the request carries the hash of the model's proposal.
          const found = await deps.inbox.find(tenantId, runId, s.actionId ?? "");
          if (found?.proposalHash !== undefined && found.proposalHash !== s.proposalHash) {
            return await finalize(c, "FAILED", undefined, "the proposal to commit is not the one that was approved");
          }
        }
        try {
          assertNoPii(p.inputRedacted);
        } catch (err) {
          if (!(err instanceof AgentError && err.code === "AGT-0210")) throw err;
          await record(c, p.tool, UNAUDITABLE_INPUT, { error: "AGT-0210" }, "DENIED", d.approver);
          return await finalize(c, "FAILED", "AGT-0210", err.message);
        }
        let session: McpSession | undefined;
        if (d.decision === "AUTO") session = (await sessionFor(c, ["starter"]))?.session;
        else session = await approverSession(c);
        if (session === undefined) {
          if (d.decision === "AUTO") throw new AgentError("AGT-0214", "the caller's session expired before the commit");
          // The approver's token has expired: ask again with a new request rather than failing an approved run.
          return await reapprove(c);
        }
        s.phase = "committing";
        await save(c);
        const args = JSON.parse(c.pseudo.restore(JSON.stringify(p.argsRedacted))) as Record<string, unknown>;
        const out = await session.call(p.tool, args);
        if (!out.ok) {
          await record(c, p.tool, p.inputRedacted, { error: out.code }, d.decision, d.approver);
          return await finalize(c, "FAILED", out.code, out.message);
        }
        s.committedRef = out.ref ?? null;
        s.phase = "committed";
        await save(c);
        try {
          await record(c, p.tool, p.inputRedacted, out.output, d.decision, d.approver, out.ref ?? null);
        } catch (err) {
          // Committed but the row failed (the output held PII, or the write): the fallback row carries the reference only.
          await closeOut(c, { committed: out.ref ?? null }, "committed; tool output not recorded", out.ref ?? null);
          logError(c, "committed action row not written", String(err));
        }
        return await finalize(c, "COMMITTED");
      } catch (err) {
        if (err instanceof AgentError) return failed(c, err);
        throw err;
      }
    },

    /** The workflow's last resort after a step exhausted its retries: ends the run FAILED so it is never left RUNNING. */
    async abort(tenantId: string, runId: string, reason: string): Promise<StepOutcome> {
      const c = await load(tenantId, runId);
      if (c.state.outcome !== undefined) return doneOf(c.state.outcome);
      if (c.state.phase === "created") await deps.audit.startRun(c.req).catch(() => undefined);
      // An open approval request is closed with the run, so an approver cannot approve a run that has ended.
      if (c.state.phase === "awaiting" && c.state.actionId !== undefined) {
        try {
          await deps.inbox.record(tenantId, runId, c.state.actionId, { decision: "REJECTED", approverUserId: "system:abort", decidedAt: now().toISOString() });
        } catch (err) {
          logError(c, "approval request not closed on abort", String(err));
        }
      }
      if (c.state.phase === "committed") return this.commit(tenantId, runId);
      await closeOut(c, { error: "workflow_aborted" }, "run aborted by the workflow");
      return finalize(c, "FAILED", undefined, reason);
    },

    /** The step handler checks the service token's actor is the user who started the run. */
    async assertActor(tenantId: string, runId: string, actor: string): Promise<void> {
      const row = await deps.state.load<RunInput, StepState>(tenantId, runId);
      if (row === undefined) throw new StepRefusal(404, "AGT-0404", "no such run");
      if (row.input.invokerUserId.toLowerCase() !== actor.toLowerCase()) throw new StepRefusal(403, "AGT-0403", "the token's actor did not start this run");
    },
  };
}

export type RunSteps = ReturnType<typeof createRunSteps>;
