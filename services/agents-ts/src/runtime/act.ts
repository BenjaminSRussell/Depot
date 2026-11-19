import { randomUUID } from "node:crypto";
import { ApiError, type Principal } from "../api/principal.js";
import { AGENT_INVOKE_DUTY } from "../agents/catalog.js";
import { navigateAlternative, resolveActTool, type ActEntry } from "../domain/actMap.js";
import { approvalExpiresAtMs } from "../domain/approval.js";
import { buildActionRecord } from "../domain/audit.js";
import { canCallTool } from "../domain/authz.js";
import { missingDutyMessage } from "../domain/copilot.js";
import type { Money } from "../domain/money.js";
import { NO_ROUTE_MESSAGE } from "../domain/modelRoute.js";
import type { RouteResult } from "../domain/promptClass.js";
import { Pseudonymizer } from "../domain/redaction.js";
import { schemaViolations, unresolvedIds } from "../domain/schemaCheck.js";
import { AgentError } from "../errors.js";
import type { LogFn } from "../logging/pseudonymize.js";
import { auditRequest, finished, type ChatRequest, type CopilotService, type CopilotStore } from "./copilot.js";
import { GUARD_REFUSAL_MESSAGE } from "./egressGuard.js";
import type { McpConnector } from "./mcp.js";
import { checkRunOutput, OUTPUT_GUARD_MESSAGE, type OutputGuard } from "./outputGuard.js";
import type { OpenedProposal, RunService } from "./service.js";

export const ACT_MAX_TURNS = 8;

export interface ActProposal {
  tool: string;
  agent: string;
  summary: string;
  diff: Array<{ field: string; to: unknown }>;
  amount: Money;
  confidence: number;
  rationale: string;
  expiresAt: string;
}

interface Refusal {
  code: string;
  message: string;
}

export type ActBody =
  | { runId: string; approvalId: string; proposal: ActProposal; generated: true }
  | { clarifyingQuestion: string; generated: true }
  | { refusal: Refusal; alternative?: { kind: "navigate"; route: RouteResult }; generated: true }
  | { runId: string; agentId: string; agentVersion: number; status: "RUNNING"; refusal: Refusal; generated: true };

export interface ActResult {
  status: 200 | 202;
  body: ActBody;
}

export interface ActDeps {
  copilot: CopilotService;
  runs: RunService;
  mcp: McpConnector;
  store: CopilotStore;
  outputGuard: OutputGuard;
  modelId: string;
  tokenCap: number;
  pseudonymTtlMs: number;
  now?: () => Date;
  log?: LogFn;
}

const REFUSALS: Readonly<Record<string, string>> = {
  "AGT-0201": "The assistant is paused for your organization.",
  "AGT-0202": "The assistant's usage limit has been reached.",
  "AGT-0204": "This assistant is not approved to run yet.",
  "AGT-0218": NO_ROUTE_MESSAGE,
  "AGT-0219": GUARD_REFUSAL_MESSAGE,
  "AGT-0220": OUTPUT_GUARD_MESSAGE,
  "AGT-0232": GUARD_REFUSAL_MESSAGE,
};
const REFUSAL_FALLBACK = "The assistant could not prepare that request right now.";
const NO_PROPOSAL_MESSAGE = "The proposal could not be prepared in time. Check the run before you try again.";
const NEEDS_DETAIL = "I need a little more detail to prepare that. Which record do you mean, and what should change?";

const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

function parseExtraction(answer: string): { params: Record<string, unknown> } | { clarifyingQuestion: string } | undefined {
  let raw: unknown;
  try {
    raw = JSON.parse(answer);
  } catch {
    return undefined;
  }
  if (!isRecord(raw)) return undefined;
  const { params, clarifyingQuestion } = raw;
  if (typeof clarifyingQuestion === "string" && clarifyingQuestion.trim() !== "" && params === undefined) return { clarifyingQuestion };
  if (isRecord(params) && clarifyingQuestion === undefined) return { params };
  return undefined;
}

const firstSentence = (text: string): string => /^.*?[.!?](?=\s|$)/s.exec(text.trim())?.[0] ?? text.trim();

const ok = (body: ActBody): ActResult => ({ status: 200, body });
const refuse = (code: string, message: string, alternative?: { kind: "navigate"; route: RouteResult }): ActResult =>
  ok({ refusal: { code, message }, ...(alternative !== undefined ? { alternative } : {}), generated: true });
const clarify = (clarifyingQuestion: string): ActResult => ok({ clarifyingQuestion, generated: true });

function startRefusal(err: unknown): ActResult {
  if (err instanceof AgentError) return refuse(err.code, REFUSALS[err.code] ?? REFUSAL_FALLBACK);
  if (err instanceof ApiError && err.status === 404) return refuse("AGT-0404", "That assistant is not available.");
  throw err;
}

export function createActService(deps: ActDeps) {
  const now = deps.now ?? (() => new Date());

  async function recordDenied(caller: Principal, tool: string): Promise<void> {
    const runId = randomUUID();
    const runReq = auditRequest(runId, caller, deps.modelId, deps.tokenCap);
    try {
      const action = buildActionRecord({ runId, seq: 1, toolName: tool, inputRedacted: "{}", outputRedacted: JSON.stringify({ error: "AGT-0203" }), approverUserId: null, decision: "DENIED", decidedAt: now().toISOString(), reason: "AGT-0203" });
      await deps.store.startRun(runReq);
      await deps.store.finishRun(runReq, finished(runId, "FAILED", [action], { tokensIn: 0, tokensOut: 0 }, ""));
    } catch (err) {
      deps.log?.("error", "act denied audit write failed", { tenantId: caller.tenantId, tool, error: err instanceof Error ? err.name : "unknown" });
      throw err;
    }
  }

  async function deny(caller: Principal, tool: string, duty: string): Promise<ActResult> {
    await recordDenied(caller, tool);
    return refuse("AGT-0203", missingDutyMessage(tool, duty));
  }

  async function proposalOf(caller: Principal, entry: ActEntry, opened: OpenedProposal): Promise<ActProposal> {
    const { req, requestedAtMs, expiryHours } = opened;
    const diff = Object.entries(req.args).map(([field, to]) => ({ field, to }));
    const verdict = await checkRunOutput(deps.outputGuard, {
      tenantId: caller.tenantId,
      prompt: "",
      response: { text: req.rationale, proposal: { args: diff } },
      pseudo: new Pseudonymizer(caller.tenantId, deps.pseudonymTtlMs, () => now().getTime()),
      duties: caller.duties,
    });
    if (!verdict.ok) deps.log?.("warn", "act proposal text withheld by the output guard", { tenantId: caller.tenantId, tool: entry.tool, reason: verdict.reason });
    return {
      tool: entry.tool,
      agent: entry.agent,
      summary: verdict.ok ? firstSentence(req.rationale) : OUTPUT_GUARD_MESSAGE,
      diff: verdict.ok ? diff : [],
      amount: req.amount,
      confidence: req.confidence,
      rationale: verdict.ok ? req.rationale : OUTPUT_GUARD_MESSAGE,
      expiresAt: new Date(approvalExpiresAtMs(requestedAtMs, expiryHours)).toISOString(),
    };
  }

  return {
    async act(caller: Principal, token: string, req: ChatRequest): Promise<ActResult> {
      const found = resolveActTool(req.message);
      if (found.status === "none") return refuse("AGT-0233", "I cannot make that change for you. You can open the screen and do it there.", navigateAlternative(req.message));
      if (found.status === "ambiguous") return clarify(`Which of these do you want: ${found.entries.map((e) => e.label).join(", or ")}?`);
      const { entry } = found;

      const agent = deps.runs.agent(entry.agent);
      if (agent === undefined) return refuse("AGT-0404", "That assistant is not available.");
      const missingAgentDuty = [AGENT_INVOKE_DUTY, ...agent.requiredDuties].find((d) => !canCallTool(caller.duties, d));
      if (missingAgentDuty !== undefined) return deny(caller, entry.tool, missingAgentDuty);

      let session;
      try {
        session = await deps.mcp.connect(caller, token);
      } catch (err) {
        deps.log?.("error", "act could not reach the tool gateway", { tenantId: caller.tenantId, tool: entry.tool, error: err instanceof Error ? err.name : "unknown" });
        throw new ApiError(502, "AGT-0214", "the tool gateway is unavailable");
      }
      const info = session.tools.find((t) => t.name === entry.tool);
      if (info === undefined || info.approvalMode === "none") return refuse("AGT-0233", "I cannot prepare that change: the tool for it is not available to you.", navigateAlternative(req.message));
      if (!canCallTool(caller.duties, info.requiredDuty)) return deny(caller, entry.tool, info.requiredDuty);

      try {
        await deps.runs.assertStartable(caller, entry.agent);
      } catch (err) {
        return startRefusal(err);
      }

      let reply;
      try {
        reply = await deps.copilot.chat(caller, token, req, { act: { tool: entry.tool, inputSchema: info.inputSchema, maxTurns: ACT_MAX_TURNS } });
      } catch (err) {
        if (err instanceof AgentError) return startRefusal(err);
        throw err;
      }
      if (reply.refusal !== null) return refuse(reply.refusal.code, reply.refusal.message);
      if (reply.excluded !== undefined) return refuse("AGT-0218", NO_ROUTE_MESSAGE);

      const extracted = parseExtraction(reply.answer);
      if (extracted === undefined) {
        deps.log?.("warn", "act extraction was not usable", { tenantId: caller.tenantId, tool: entry.tool });
        return clarify(NEEDS_DETAIL);
      }
      if ("clarifyingQuestion" in extracted) return clarify(extracted.clarifyingQuestion);
      const violations = schemaViolations(info.inputSchema ?? {}, extracted.params);
      const unresolved = unresolvedIds(extracted.params, `${req.message}\n${req.recordRef ?? ""}`);
      if (violations.length > 0 || unresolved.length > 0) {
        deps.log?.("warn", "act parameters were incomplete", { tenantId: caller.tenantId, tool: entry.tool, violations: violations.length, unresolved: unresolved.length });
        return clarify(unresolved.length > 0 ? `I could not find ${unresolved.map((p) => p.replace(/^\$\./, "")).join(", ")} in your request. Which record do you mean?` : NEEDS_DETAIL);
      }

      const prompt = `${req.message}${req.recordRef === undefined ? "" : `\n[record: ${req.recordRef}]`}\nParameters: ${JSON.stringify(extracted.params)}`;
      let launched;
      try {
        launched = await deps.runs.startForProposal(caller, token, entry.agent, prompt, entry.tool);
      } catch (err) {
        return startRefusal(err);
      }
      const { started, opened, endedWith } = launched;
      if (opened === undefined) {
        deps.log?.("warn", "act proposal did not appear", { tenantId: caller.tenantId, tool: entry.tool, runId: started.runId, ...(endedWith !== undefined ? { endedWith } : {}) });
        if (endedWith !== undefined) return refuse("AGT-0234", NO_PROPOSAL_MESSAGE);
        return { status: 202, body: { ...started, refusal: { code: "AGT-0234", message: NO_PROPOSAL_MESSAGE }, generated: true } };
      }
      return ok({ runId: started.runId, approvalId: opened.actionId, proposal: await proposalOf(caller, entry, opened), generated: true });
    },
  };
}

export type ActService = ReturnType<typeof createActService>;
