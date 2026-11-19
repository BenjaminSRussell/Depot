import { createHash } from "node:crypto";
import { AGENT_INVOKE_DUTY, defaultCatalog } from "../../src/agents/catalog.js";
import type { Principal } from "../../src/api/principal.js";
import { CopilotAnswerCache } from "../../src/domain/copilot.js";
import { NO_ROUTE_MESSAGE, type ModelRoute } from "../../src/domain/modelRoute.js";
import type { BriefingContent, BriefingStore, StoredBriefing } from "../../src/domain/briefing.js";
import { classifyByRules } from "../../src/domain/promptClass.js";
import { AgentError } from "../../src/errors.js";
import { createActService } from "../../src/runtime/act.js";
import { createAiRouter, type AiRouter } from "../../src/runtime/aiRouter.js";
import { createBriefingService } from "../../src/runtime/briefing.js";
import { COPILOT_DUTY, createCopilotService } from "../../src/runtime/copilot.js";
import type { DesignationClient } from "../../src/runtime/cuiDesignation.js";
import { createOutputGuard } from "../../src/runtime/outputGuard.js";
import { createRunService } from "../../src/runtime/service.js";
import { allowAiAccess, testGuard } from "../fakes.js";
import { FakeMcp, MemoryStore, tool } from "../surface-fakes.js";
import { expandedMessage, expandedTools, type RedTeamCase } from "./cases.js";
import { OFFLIST_TOOL, RoutedProvider, SETTLE_TOOL, type Captured, type Responder } from "./providers.js";
import type { Observed, Profile } from "./score.js";

export type ResponderFor = (c: RedTeamCase, scope: string) => Responder;

const TOKEN = "redteam-token";
const NOW = new Date("2026-11-19T10:00:00.000Z");
const DUTIES = new Set([COPILOT_DUTY, AGENT_INVOKE_DUTY, "redteam.read", "ar.payment.read", "ar.invoice.read", "ar.payment.apply_proposal"]);
const PRIOR: Principal = { userId: "00000000-0000-7000-8000-0000000000b1", tenantId: "00000000-0000-7000-8000-0000000000a1", duties: DUTIES };
const LATER_SAME_TENANT: Principal = { userId: "00000000-0000-7000-8000-0000000000b2", tenantId: PRIOR.tenantId, duties: DUTIES };
const LATER_OTHER_TENANT: Principal = { userId: "00000000-0000-7000-8000-0000000000b3", tenantId: "00000000-0000-7000-8000-0000000000a2", duties: DUTIES };

const ATTESTATION = { accreditation: "TEST-HIGH", evidenceRef: "evidence/test", approvedBy: "test-security-lead", approvedAt: "2026-11-19" };
export const PROFILE_ROUTES: Readonly<Record<Profile, readonly ModelRoute[]>> = {
  standard: [{ routeId: "std-1", adapter: "vllm", endpointId: "std-endpoint" }],
  cui: [
    { routeId: "ext-1", adapter: "azure", endpointId: "ext-endpoint" },
    { routeId: "cui-1", adapter: "bedrock", endpointId: "cui-endpoint", region: "us-gov-west-1", attestation: ATTESTATION },
  ],
};

const designation: DesignationClient = {
  resolve: async (_caller, refs) => refs.map((r) => ({ ...r, class: r.recordId === "" ? ("UNKNOWN" as const) : r.recordId.startsWith("cui-") ? ("CUI" as const) : ("NONE" as const), source: "test" })),
};

const settleSchema = { type: "object", required: ["paymentId", "invoiceId"], properties: { paymentId: { type: "string" }, invoiceId: { type: "string" } } };

class MemoryBriefings implements BriefingStore {
  readonly rows = new Map<string, StoredBriefing & { userId: string }>();
  async create(_tenant: string, userId: string): Promise<string> {
    const id = `00000000-0000-7000-8000-0000000000f${this.rows.size}`;
    this.rows.set(id, { id, userId, status: "RUNNING", narrative: false, createdAt: NOW.toISOString(), completedAt: null, readAt: null, content: null });
    return id;
  }
  async finish(_tenant: string, id: string, content: BriefingContent): Promise<void> {
    this.rows.set(id, { ...this.rows.get(id)!, status: "READY", content });
  }
  async fail(_tenant: string, id: string): Promise<void> {
    this.rows.set(id, { ...this.rows.get(id)!, status: "FAILED" });
  }
  async get(_tenant: string, userId: string, id: string): Promise<StoredBriefing | undefined> {
    const row = this.rows.get(id);
    return row?.userId === userId ? row : undefined;
  }
  async list(_tenant: string, userId: string) {
    return { items: [...this.rows.values()].filter((r) => r.userId === userId), nextCursor: null };
  }
  async markRead(_tenant: string, userId: string, id: string) {
    return this.get(_tenant, userId, id);
  }
}

class TenantFreeStore extends MemoryStore {
  override async getRun(_tenantId: string, runId: string) {
    return this.runs.get(runId);
  }
}

export const actionIdOf = (c: RedTeamCase): string => `00000000-0000-7000-8000-${createHash("sha256").update(c.id).digest("hex").slice(0, 12)}`;

export async function runCase(profile: Profile, c: RedTeamCase, responderFor: ResponderFor, options: { leaky?: boolean } = {}): Promise<Observed> {
  if (c.path === "route" && classifyByRules(expandedMessage(c)).status === "decided") throw new Error(`route case ${c.id} is decided by the rules, so no model is called`);
  const sink: Captured[] = [];
  const state = { phase: 0 };
  const responder = responderFor(c, `${profile}:${c.id}`);
  const providerAt = (routeId: string): RoutedProvider => new RoutedProvider(sink, () => state.phase, routeId, responder);
  const inner = createAiRouter({ boundary: profile, routes: PROFILE_ROUTES[profile], providerFor: () => providerAt("unplanned"), designation: () => designation });
  const router: AiRouter = {
    plan: async (input) => {
      const planned = await inner.plan(input);
      const routed = { ...planned, provider: providerAt(planned.routeId) };
      return options.leaky === true ? { ...routed, keep: [...input.refs], excluded: [], restricted: [] } : routed;
    },
  };

  const answers: Record<string, { ok: true; output: unknown }> = Object.fromEntries(Object.entries(expandedTools(c)).map(([name, output]) => [name, { ok: true as const, output }]));
  const gateway = [
    ...Object.keys(c.tools ?? {}).map((name) => tool(name, "redteam.read")),
    tool(SETTLE_TOOL, "ar.payment.apply_proposal", "approve_before_commit", settleSchema),
    tool(OFFLIST_TOOL, "ar.invoice.post", "approve_before_commit"),
  ];
  const mcp = new FakeMcp(gateway, answers);
  const store = new TenantFreeStore();
  const killSwitch = { isHalted: () => false };
  const outputGuard = createOutputGuard();
  const agents = defaultCatalog("claude-sonnet-5-5");
  const spawned: Array<Promise<unknown>> = [];
  const runs = createRunService({
    agents,
    evalPassed: async () => true,
    providerFor: () => {
      if (profile === "cui") throw new AgentError("AGT-0218", NO_ROUTE_MESSAGE);
      return providerAt("std-1");
    },
    mcp,
    store,
    inbox: store,
    killSwitch,
    disabledAgents: async () => new Set(),
    config: { runTokenCap: 50_000, selfApprovalLimit: { amount: "0", currency: "USD" }, pseudonymTtlMs: 60_000, approvalPollMs: 1 },
    sleep: () => new Promise((r) => setTimeout(r, 2)),
    spawn: (p) => spawned.push(p.catch(() => undefined)),
  });
  const copilot = createCopilotService({
    router,
    mcp,
    store,
    killSwitch,
    cache: new CopilotAnswerCache(),
    config: { tokenCap: 20_000, maxTurns: 4, pseudonymTtlMs: 60_000 },
    modelId: "claude-haiku-5-5",
    guard: testGuard(),
    aiAccess: allowAiAccess(),
    outputGuard,
  });
  const act = createActService({ copilot, runs, mcp, store, outputGuard, modelId: "claude-haiku-5-5", tokenCap: 20_000, pseudonymTtlMs: 60_000 });
  const briefings = new MemoryBriefings();
  const actionId = actionIdOf(c);
  const briefing = createBriefingService({
    store: briefings,
    facts: {
      read: async (_tenant, userId) => ({
        pending:
          userId === PRIOR.userId
            ? [{ agentId: "ap_coding", actionId, tool: "depot.ap.invoice.propose_coding", amount: "100", currency: "USD", selfApprovalLimit: "500", selfApprovalCurrency: "USD", requestedAt: NOW.toISOString(), expiryHours: 72 }]
            : [],
        decided: [],
        runs: [],
      }),
    },
    disabledAgents: async () => new Set(),
    now: () => NOW,
  });

  const outputs: Observed["outputs"] = [];
  const refusals: string[] = [];
  let routeKind: string | undefined;
  const calls: string[] = [];
  const say = (text: string): void => void outputs.push({ phase: state.phase, text });

  const generated = async (caller: Principal): Promise<string> => {
    const { id } = await briefing.generate(caller);
    for (let tick = 0; tick < 50 && briefings.rows.get(id)?.status === "RUNNING"; tick += 1) await new Promise((r) => setImmediate(r));
    return JSON.stringify((await briefing.get(caller, id))?.content ?? null);
  };

  const phases = [{ caller: PRIOR, message: expandedMessage(c) }];
  if (c.priorUser !== undefined) phases.push({ caller: c.priorUser === "otherTenant" ? LATER_OTHER_TENANT : LATER_SAME_TENANT, message: expandedMessage(c, c.replay ?? c.message) });

  for (const [phase, { caller, message }] of phases.entries()) {
    state.phase = phase;
    if (phase === 1) for (const name of Object.keys(answers)) delete answers[name];
    try {
      if (c.path === "ask" || c.path === "route") {
        const r = await copilot.chat(caller, TOKEN, { mode: c.path, message });
        say(r.answer);
        if (r.refusal !== null) refusals.push(r.refusal.code);
        if (r.route !== undefined) routeKind = r.route.kind;
      } else if (c.path === "act") {
        const { body } = await act.act(caller, TOKEN, { mode: "act", message, recordRef: "PAY-1" });
        say(JSON.stringify(body));
        if ("refusal" in body) refusals.push(body.refusal.code);
        if ("proposal" in body) calls.push(body.proposal.tool);
      } else if (c.path === "briefing") {
        if (phase === 0) say(await generated(caller));
        else {
          say(JSON.stringify((await briefing.get(caller, [...briefings.rows.keys()][0] ?? "")) ?? null));
          say(JSON.stringify((await briefing.list(caller, 10, undefined)).items));
          say(await generated(caller));
        }
      }
    } catch (err) {
      if (!(err instanceof AgentError)) throw err;
      refusals.push(err.code);
    }
  }

  for (const a of store.approvals.values()) await store.record(a.tenantId, a.runId, a.actionId, { decision: "REJECTED", approverUserId: "x", decidedAt: NOW.toISOString() });
  await Promise.race([Promise.all(spawned), new Promise((r) => setTimeout(r, 100))]);

  calls.push(...mcp.calls.map((call) => call.name));
  for (const call of mcp.calls) say(JSON.stringify(call.args));
  const allowed = new Set([...Object.keys(c.tools ?? {}), ...(agents.find((a) => a.agentId === "cash_app")?.allowedTools ?? [])]);
  return { requests: sink, outputs, calls, allowed, refusals, ...(routeKind !== undefined ? { routeKind } : {}), ...(c.path === "briefing" ? { extraForbidden: [actionId] } : {}) };
}
