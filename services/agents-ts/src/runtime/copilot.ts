import { randomUUID } from "node:crypto";
import { ApiError, asObject, type Principal } from "../api/principal.js";
import { AGENT_INVOKE_DUTY, INJECTION_GUARD } from "../agents/catalog.js";
import { buildActionRecord, type ActionRecord, type Decision } from "../domain/audit.js";
import { assertPromptWithinCap, estimateTokens, outputBudgetForCall } from "../domain/budget.js";
import {
  CopilotAnswerCache,
  COPILOT_MODES,
  gateCopilotTools,
  missingDutyMessage,
  type CopilotMode,
} from "../domain/copilot.js";
import { gateCitations, type Citation } from "../domain/knowledge.js";
import { exclusionNotice, NO_ROUTE_MESSAGE, permittedOutput, recordsOf } from "../domain/modelRoute.js";
import { assertNoPii, Pseudonymizer } from "../domain/redaction.js";
import { AgentError } from "../errors.js";
import { StreamRestorer } from "../providers/stream.js";
import { ProviderError, proposeTool } from "../providers/wire.js";
import type { ModelEndpoint } from "../cloudprofile/index.js";
import type { AuditPort, BudgetPort, KillSwitchPort, ModelUsage, RunRequest, RunResult } from "./engine.js";
import type { AiPlan, AiRouter } from "./aiRouter.js";
import { createEgressGuard, EGRESS_GUARD_TOOL, GUARD_REFUSAL_MESSAGE, highestClassOf, type DataClass, type EgressGuard, type GuardResult } from "./egressGuard.js";
import type { McpConnector } from "./mcp.js";
import type { KnowledgeService } from "./knowledge.js";

/** The duty to use the copilot (seeded in policies/authz/data.json). */
export const COPILOT_DUTY = "mcp.copilot.use";
export const COPILOT_AGENT_ID = "copilot";
export const MAX_MESSAGE_CHARS = 4000;
export const KNOWLEDGE_SEARCH_TOOL = "depot.knowledge.search";
/** Searching the knowledge index is part of using the copilot: the route is seeded under the same duty (policies/authz/data.json). */
export const KNOWLEDGE_READ_DUTY = COPILOT_DUTY;

/** Request and response of POST /api/v1/copilot/chat; the shape apps/web/src/modules/copilot/api/client.ts validates. */
export interface ChatRequest {
  mode: CopilotMode;
  message: string;
  recordRef?: string;
}

export interface ChatCitation {
  sourceType: "RECORD" | "METRIC" | "ARTICLE" | "DOC" | "POLICY";
  sourceRef: string;
  label: string;
}

export interface ChatToolCall {
  tool: string;
  status: "OK" | "DENIED" | "ERROR";
  code?: string;
}

export interface ChatResponse {
  answer: string;
  citations: ChatCitation[];
  toolCalls: ChatToolCall[];
  refusal: { code: string; message: string } | null;
  excluded?: { count: number; kinds: string[]; reason: "NO_APPROVED_ROUTE" };
  dataClass?: DataClass;
  /** Always true: generated text is labelled as such in the UI (FR-47.12). */
  generated: true;
}

/** What a caller of `chat` can listen for while the answer is written. */
export interface ChatHooks {
  /**
   * The answer as the model writes it, with pseudonyms already restored (FR-47.3). Called only when the provider
   * can stream and no answer is served from the cache; the finished ChatResponse stays the authoritative answer.
   * Nothing is sent for a chat whose knowledge results fail the citation gate (AGT-0212): that answer is refused.
   */
  onToken?: (text: string) => void;
  /** Aborts the model call when the caller is gone. */
  signal?: AbortSignal;
}

export interface CopilotStore extends AuditPort, BudgetPort {}

export interface CopilotConfig {
  /** Token cap of one chat turn, input and output (FR-47.11). */
  tokenCap: number;
  /** Most model turns of one chat (read-tool calls feed further turns). */
  maxTurns: number;
  pseudonymTtlMs: number;
}

export interface CopilotDeps {
  router: AiRouter;
  mcp: McpConnector;
  store: CopilotStore;
  killSwitch: KillSwitchPort;
  cache: CopilotAnswerCache;
  config: CopilotConfig;
  modelId: string;
  /** The knowledge index. When set, `depot.knowledge.search` is a read tool of the copilot to everyone who may use the copilot. */
  knowledge?: KnowledgeService;
  guard?: EgressGuard;
  now?: () => Date;
}

const MODE_PROMPTS: Record<CopilotMode, string> = {
  ask: "Answer the user's question about their Depot data. Use the read tools to fetch what you need and state only what the tool results show.",
  explain: "Explain the record the user names: what it is, how it got into its state and what usually happens next. Use the read tools; state only what they show.",
  draft: "Draft the document text the user asks for. The draft is for a person to review and is never sent or posted by you.",
  summarize: "Summarize the workflow or case history the user names. Use the read tools; state only what they show.",
};

const READ_TOOL = /\.(get|list|search)$/;

/** Validates the POST body: a known mode, a message of 1 to 4000 characters and an optional record reference. */
export function parseChatRequest(raw: unknown): ChatRequest {
  const body = asObject(raw, ["mode", "message", "recordRef"]);
  const { mode, message, recordRef } = body;
  if (typeof mode !== "string" || !(COPILOT_MODES as readonly string[]).includes(mode)) {
    throw new ApiError(400, "AGT-0400", `mode must be one of ${COPILOT_MODES.join(", ")}`);
  }
  if (typeof message !== "string" || message.trim() === "" || message.length > MAX_MESSAGE_CHARS) {
    throw new ApiError(400, "AGT-0400", `message is required, at most ${MAX_MESSAGE_CHARS} characters`);
  }
  if (recordRef !== undefined && (typeof recordRef !== "string" || recordRef === "" || recordRef.length > 200)) {
    throw new ApiError(400, "AGT-0400", "recordRef must be a string of 1 to 200 characters");
  }
  return { mode: mode as CopilotMode, message, ...(recordRef !== undefined ? { recordRef } : {}) };
}

function systemPrompt(mode: CopilotMode, tools: readonly string[]): string {
  return `${MODE_PROMPTS[mode]} Tools you may call: ${tools.join(", ")}. To call one, propose it with its arguments; you will receive its result and may call another. When you have enough, answer in plain language without proposing a tool. ${INJECTION_GUARD}`;
}

export const TYPED_REPLACED_NOTICE = "I replaced sensitive numbers in your message with placeholders before sending it to the AI model.";

const clip = (s: string, n: number): string => (s.length > n ? `${s.slice(0, n)}...` : s);

/**
 * The copilot chat backend (FR-47.3, BR-47.11). One chat turn: the caller's own tool view (the
 * gateway lists only tools whose duty the caller holds), the model asked in up to `maxTurns` rounds
 * with read tools only, every prompt redacted before the provider sees it, a budget hold settled with
 * the tokens used, one audited run (`agent_id` copilot) with an action row per tool call, and answers
 * cached per tenant, user, duty set and mode for 5 minutes. A tool the model names outside the
 * caller's allowed set is answered AGT-0203 and never called.
 */
export function createCopilotService(deps: CopilotDeps) {
  const now = deps.now ?? (() => new Date());
  const guard = deps.guard ?? createEgressGuard();
  return {
    async chat(caller: Principal, token: string, req: ChatRequest, hooks: ChatHooks = {}): Promise<ChatResponse> {
      if (deps.killSwitch.isHalted(caller.tenantId)) throw new AgentError("AGT-0201", "agent runs are halted by the kill switch");
      const prompt = req.recordRef === undefined ? req.message : `${req.message}\n[record: ${req.recordRef}]`;
      const asker = { tenantId: caller.tenantId, userId: caller.userId, duties: caller.duties };
      const cached = deps.cache.get(asker, req.mode, prompt);
      if (cached !== undefined) return JSON.parse(cached) as ChatResponse;

      const runId = randomUUID();
      const session = await deps.mcp.connect(caller, token, runId);
      const candidates = session.tools.filter((t) => t.approvalMode === "none" && READ_TOOL.test(t.name));
      if (deps.knowledge !== undefined && !candidates.some((t) => t.name === KNOWLEDGE_SEARCH_TOOL)) {
        candidates.push({ name: KNOWLEDGE_SEARCH_TOOL, requiredDuty: KNOWLEDGE_READ_DUTY, approvalMode: "none" } as (typeof candidates)[number]);
      }
      const gate = gateCopilotTools(caller.duties, candidates.map((t) => ({ name: t.name, requiredDuty: t.requiredDuty })));
      if (gate.allowed.length === 0) {
        const first = gate.denied[0];
        return refusal(first !== undefined ? "AGT-0203" : "AGT-0212", first?.message ?? "No tool is available to answer this.");
      }

      // TODO: DEPOT-306 createAiPolicyGuard.check is still unwired
      let plan: AiPlan;
      try {
        plan = await deps.router.plan({ caller, modelId: deps.modelId, refs: [] });
      } catch (err) {
        if (err instanceof AgentError && err.code === "AGT-0218") return refusal("AGT-0218", NO_ROUTE_MESSAGE);
        throw err;
      }

      const month = now().toISOString().slice(0, 7);
      const hold = await deps.store.reserve({
        tenantId: caller.tenantId,
        agentId: COPILOT_AGENT_ID,
        runId,
        month,
        tokensIn: deps.config.tokenCap,
        tokensOut: deps.config.tokenCap,
        ttlMs: 10 * 60_000,
      });
      const runReq = auditRequest(runId, caller, deps.modelId, deps.config.tokenCap);
      try {
        await deps.store.startRun(runReq);
      } catch (err) {
        await deps.store.settle(hold, { tokensIn: 0, tokensOut: 0 }).catch(() => undefined);
        throw err;
      }

      const pseudo = new Pseudonymizer(caller.tenantId, deps.config.pseudonymTtlMs, () => now().getTime());
      const actions: ActionRecord[] = [];
      const toolCalls: ChatToolCall[] = [];
      const citations: ChatCitation[] = [];
      const knowledge: Citation[] = [];
      const usage: ModelUsage = { tokensIn: 0, tokensOut: 0 };
      let answer = "";
      let refused: ChatResponse["refusal"] = null;
      let settleSpend = true;
      let provider = plan.provider;
      let routeId = plan.routeId;
      let dataClass: DataClass = "P0";
      let typedReplaced = 0;
      let withheldCount = 0;
      const withheldKinds = new Set<string>();
      const system = systemPrompt(req.mode, gate.allowed);
      const results: string[] = [];
      const sink = hooks.onToken;
      const restorer = new StreamRestorer((text) => pseudo.restore(text));
      // Text is shown as it is written unless a knowledge result already makes the answer unanswerable.
      const streamable = (): boolean => knowledge.length === 0 || gateCitations(knowledge).answerable;
      const onText = (delta: string): void => {
        if (sink === undefined || !streamable()) return;
        const text = restorer.push(delta);
        if (text !== "") sink(text);
      };
      const row = (tool: string, input: unknown, output: unknown, decision: Decision, why?: string): void => {
        actions.push(
          buildActionRecord({
            runId,
            seq: actions.length + 1,
            toolName: tool,
            inputRedacted: pseudo.redact(JSON.stringify(input) ?? "null"),
            outputRedacted: pseudo.redact(JSON.stringify(output) ?? "null"),
            approverUserId: null,
            decision,
            decidedAt: now().toISOString(),
            ...(why !== undefined ? { reason: why } : {}),
          }),
        );
      };

      const guardRow = (r: GuardResult): void => {
        actions.push(
          buildActionRecord({
            runId,
            seq: actions.length + 1,
            toolName: EGRESS_GUARD_TOOL,
            inputRedacted: "{}",
            outputRedacted: JSON.stringify({ decision: r.decision, highestClass: r.highestClass }),
            approverUserId: null,
            decision: r.decision === "BLOCKED" ? "DENIED" : "AUTO",
            decidedAt: now().toISOString(),
            guardResult: r,
            ...(r.errorCode !== undefined ? { reason: r.errorCode } : {}),
          }),
        );
      };

      try {
        for (let turn = 0; turn < deps.config.maxTurns; turn += 1) {
          const verdict = await guard.inspect({ tenantId: caller.tenantId, callId: randomUUID(), routeId, pseudo, system, typed: prompt, results });
          guardRow(verdict.result);
          dataClass = highestClassOf([dataClass, verdict.result.highestClass]);
          if (verdict.result.decision === "BLOCKED") {
            refused = { code: "AGT-0219", message: GUARD_REFUSAL_MESSAGE };
            answer = refused.message;
            break;
          }
          typedReplaced = Math.max(typedReplaced, verdict.typedReplaced);
          const context = verdict.user;
          assertNoPii(context);
          const toolsText = JSON.stringify(proposeTool(gate.allowed));
          assertPromptWithinCap([system, context, toolsText], deps.config.tokenCap - usage.tokensIn);
          const maxOutputTokens = outputBudgetForCall(
            deps.config.tokenCap,
            usage.tokensIn + usage.tokensOut,
            estimateTokens(system) + estimateTokens(context) + estimateTokens(toolsText),
          );
          let response;
          try {
            const call = { system, user: context, allowedTools: gate.allowed, maxOutputTokens, ...(hooks.signal !== undefined ? { signal: hooks.signal } : {}) };
            response = sink !== undefined && provider.stream !== undefined ? await provider.stream(call, onText) : await provider.complete(call);
            if (sink !== undefined && streamable()) {
              const tail = restorer.flush();
              if (tail !== "") sink(tail);
            }
          } catch (err) {
            if (err instanceof ProviderError && err.unbilled) settleSpend = false;
            throw err;
          }
          const used = response.usage ?? { tokensIn: estimateTokens(system) + estimateTokens(context), tokensOut: estimateTokens(response.text) };
          usage.tokensIn += used.tokensIn;
          usage.tokensOut += used.tokensOut;
          answer = response.text;
          const p = response.proposal;
          if (p === undefined) break;
          if (!gate.allowed.includes(p.tool)) {
            // BR-47.11: a tool outside the caller's allowed set is refused with AGT-0203, never called.
            const duty = session.describe(p.tool)?.requiredDuty ?? AGENT_INVOKE_DUTY;
            toolCalls.push({ tool: p.tool, status: "DENIED", code: "AGT-0203" });
            row(p.tool, p.args, { error: "AGT-0203" }, "DENIED");
            refused = { code: "AGT-0203", message: missingDutyMessage(p.tool, duty) };
            answer = refused.message;
            break;
          }
          const args = JSON.parse(pseudo.restore(JSON.stringify(p.args))) as Record<string, unknown>;
          const out =
            p.tool === KNOWLEDGE_SEARCH_TOOL && deps.knowledge !== undefined
              ? await searchKnowledge(deps.knowledge, caller.tenantId, args)
              : await session.call(p.tool, args);
          if (!out.ok) {
            const denied = out.code === "AGT-0203";
            toolCalls.push({ tool: p.tool, status: denied ? "DENIED" : "ERROR", code: out.code });
            row(p.tool, p.args, { error: out.code }, denied ? "DENIED" : "AUTO");
            results.push(`Tool ${p.tool} failed with ${out.code}.`);
            if (denied) {
              refused = { code: "AGT-0203", message: out.message };
              answer = out.message;
              break;
            }
            continue;
          }
          toolCalls.push({ tool: p.tool, status: "OK" });
          const found = p.tool === KNOWLEDGE_SEARCH_TOOL ? [] : recordsOf(p.tool, out.output);
          const next = found.length === 0 ? undefined : await deps.router.plan({ caller, modelId: deps.modelId, refs: found });
          answer = "";
          const excludedHere = next?.excluded ?? [];
          const shown = excludedHere.length === 0 ? out.output : permittedOutput(out.output, excludedHere);
          if (excludedHere.length > 0) {
            withheldCount += excludedHere.length;
            for (const r of excludedHere) withheldKinds.add(r.entityType === "" ? "unidentified" : r.entityType);
          }
          if (shown === undefined) {
            row(p.tool, p.args, { withheld: "NO_APPROVED_ROUTE" }, "AUTO", "read");
            results.push(`Result of ${p.tool} was withheld: its data has no approved AI route.`);
            continue;
          }
          row(p.tool, p.args, shown, "AUTO", "read");
          if (next !== undefined) {
            provider = next.provider;
            routeId = next.routeId;
          }
          if (p.tool === KNOWLEDGE_SEARCH_TOOL) knowledge.push(...knowledgeCitations(out.output));
          else citations.push({ sourceType: "RECORD", sourceRef: out.ref ?? p.tool, label: p.tool });
          results.push(`Result of ${p.tool}: ${clip(JSON.stringify(shown) ?? "null", 4000)}`);
        }
        if (refused === null && knowledge.length > 0) {
          const g = gateCitations(knowledge);
          if (!g.answerable) {
            refused = { code: g.code, message: g.message };
            answer = g.message;
          } else {
            for (const c of g.citations) citations.push({ sourceType: c.sourceType, sourceRef: c.sourceRef, label: c.section });
          }
        }
        if (answer === "" && refused === null) answer = "I could not complete the answer within the tool-call limit.";
        const notice = withheldCount > 0 ? `\n\n${exclusionNotice([...withheldKinds], withheldCount)}` : "";
        const replacedNotice = typedReplaced > 0 ? `\n\n${TYPED_REPLACED_NOTICE}` : "";
        const display = (refused !== null ? answer : pseudo.restore(answer)) + notice + replacedNotice;
        const result = finished(runId, "COMMITTED", actions, usage, display, provider.endpoint);
        await deps.store.finishRun(runReq, result);
        const out: ChatResponse = {
          answer: display,
          citations,
          toolCalls,
          refusal: refused,
          dataClass,
          ...(withheldCount > 0 ? { excluded: { count: withheldCount, kinds: [...withheldKinds], reason: "NO_APPROVED_ROUTE" as const } } : {}),
          generated: true,
        };
        if (refused === null && withheldCount === 0) deps.cache.put(asker, req.mode, prompt, JSON.stringify(out));
        return out;
      } catch (err) {
        await deps.store
          .finishRun(runReq, { ...finished(runId, "FAILED", actions, usage, "", provider.endpoint), ...(err instanceof AgentError ? { code: err.code } : {}), reason: err instanceof Error ? err.message : String(err) })
          .catch(() => undefined);
        throw err;
      } finally {
        // A call that may have been billed with no usage known stays held for the sweep (as in the run engine).
        if (settleSpend) await deps.store.settle(hold, usage).catch(() => undefined);
        else await deps.store.settle(hold, { tokensIn: 0, tokensOut: 0 }).catch(() => undefined);
      }
    },
  };
}

/** The copilot's own knowledge search: the caller's tenant, a query of at most 1000 characters, ten chunks at most. */
async function searchKnowledge(knowledge: KnowledgeService, tenantId: string, args: Record<string, unknown>): Promise<{ ok: true; output: unknown; ref: string } | { ok: false; code: string; message: string }> {
  const query = typeof args["query"] === "string" ? args["query"] : typeof args["q"] === "string" ? args["q"] : "";
  if (query.trim() === "" || query.length > 1000) return { ok: false, code: "AGT-0214", message: "the knowledge search needs a query of 1 to 1000 characters" };
  try {
    return { ok: true, output: await knowledge.search(tenantId, query, 10), ref: KNOWLEDGE_SEARCH_TOOL };
  } catch {
    return { ok: false, code: "AGT-0214", message: "the knowledge search failed" };
  }
}

function refusal(code: string, message: string): ChatResponse {
  return { answer: message, citations: [], toolCalls: [], refusal: { code, message }, generated: true };
}

function knowledgeCitations(output: unknown): Citation[] {
  const chunks = typeof output === "object" && output !== null && Array.isArray((output as { chunks?: unknown }).chunks) ? (output as { chunks: unknown[] }).chunks : [];
  const out: Citation[] = [];
  for (const c of chunks) {
    if (typeof c !== "object" || c === null) continue;
    const r = c as Record<string, unknown>;
    if (typeof r.chunkId !== "string" || typeof r.sourceRef !== "string" || typeof r.section !== "string") continue;
    if (r.sourceType !== "ARTICLE" && r.sourceType !== "DOC" && r.sourceType !== "POLICY") continue;
    out.push({
      chunkId: r.chunkId,
      sourceType: r.sourceType,
      sourceRef: r.sourceRef,
      section: r.section,
      fusedScore: typeof r.fusedScore === "number" ? r.fusedScore : 0,
      ...(typeof r.similarity === "number" ? { similarity: r.similarity } : {}),
    });
  }
  return out;
}

function auditRequest(runId: string, caller: Principal, modelId: string, cap: number): RunRequest {
  return {
    runId,
    tenantId: caller.tenantId,
    invokerUserId: caller.userId,
    userDuties: caller.duties,
    agent: { agentId: COPILOT_AGENT_ID, version: 1, modelId, systemPrompt: "", allowedTools: [], requiredDuties: [COPILOT_DUTY] },
    evalPassed: true,
    policy: { mode: "SUGGEST", autoThreshold: 1, autoAmountLimit: { amount: "0", currency: "USD" } },
    selfApprovalLimit: { amount: "0", currency: "USD" },
    runTokenCap: cap,
    // The prompt is not stored; the audit row keeps only the run and its tool calls.
    prompt: "",
  };
}

function finished(runId: string, status: "COMMITTED" | "FAILED", actions: ActionRecord[], usage: ModelUsage, displayText: string, endpoint?: ModelEndpoint): RunResult {
  return { runId, status, actions, tokensIn: usage.tokensIn, tokensOut: usage.tokensOut, estimated: false, budgetState: "OK", displayText, ...(endpoint !== undefined ? { modelEndpoint: endpoint } : {}) };
}

export type CopilotService = ReturnType<typeof createCopilotService>;
