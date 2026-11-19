import { afterEach, describe, expect, it } from "vitest";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import { AgentError } from "../src/errors.js";
import { providerRejected, providerUnavailable } from "../src/providers/wire.js";
import { createCopilotService } from "../src/runtime/copilot.js";
import type { createEgressGuard } from "../src/runtime/egressGuard.js";
import { GUARD_REFUSAL_MESSAGE } from "../src/runtime/egressGuard.js";
import type { ModelProvider, ModelResponse } from "../src/runtime/engine.js";
import type { AiRouter } from "../src/runtime/aiRouter.js";
import { createOutputGuard, OUTPUT_GUARD_MESSAGE } from "../src/runtime/outputGuard.js";
import { openRouter, ScriptedProvider, testGuard } from "./fakes.js";
import { FakeMcp, headers, MemoryStore, serve, tool } from "./surface-fakes.js";

const DUTIES = "mcp.copilot.use";
const CONVERSATION = "00000000-0000-7000-8000-0000000000d1";
const MESSAGES = `/api/v1/agents/copilot/conversations/${CONVERSATION}/messages`;
const verdict = (text: string): ModelResponse => ({ text, usage: { tokensIn: 50, tokensOut: 10 } });

class BudgetStopStore extends MemoryStore {
  override async reserve(): Promise<never> {
    throw new AgentError("AGT-0202", "token budget exhausted for this month");
  }
}

interface Options {
  provider?: ModelProvider;
  router?: AiRouter;
  killed?: boolean;
  store?: MemoryStore;
  guard?: ReturnType<typeof createEgressGuard>;
  outputGuard?: ReturnType<typeof createOutputGuard>;
}

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  await close?.();
  close = undefined;
});

const setup = async (script: ModelResponse[], options: Options = {}) => {
  const store = options.store ?? new MemoryStore();
  const provider = new ScriptedProvider(script);
  const mcp = new FakeMcp([tool("depot.ap.invoice.get", "ap.invoice.read")]);
  const copilot = createCopilotService({
    router: options.router ?? openRouter(options.provider ?? provider),
    mcp,
    store,
    killSwitch: { isHalted: () => options.killed ?? false },
    cache: new CopilotAnswerCache(),
    config: { tokenCap: 20_000, maxTurns: 3, pseudonymTtlMs: 60_000 },
    modelId: "claude-haiku-5-5",
    guard: options.guard ?? testGuard(),
    ...(options.outputGuard !== undefined ? { outputGuard: options.outputGuard } : {}),
  });
  const srv = await serve({ approvals: store, copilot });
  close = srv.close;
  return { store, provider, mcp, base: srv.base };
};

const route = (base: string, message: unknown, duties = DUTIES, accept = "application/json") =>
  fetch(`${base}${MESSAGES}`, { method: "POST", headers: { ...headers(duties), accept }, body: JSON.stringify({ mode: "route", message }) });

describe("conversation endpoints", () => {
  it("creates a conversation id for a caller with the copilot duty and refuses one without", async () => {
    const s = await setup([]);
    const created = await fetch(`${s.base}/api/v1/agents/copilot/conversations`, { method: "POST", headers: headers(DUTIES) });
    expect(created.status).toBe(201);
    expect(created.headers.get("cache-control")).toBe("no-store");
    expect(((await created.json()) as { conversationId: string }).conversationId).toMatch(/^[0-9a-f-]{36}$/);
    const denied = await fetch(`${s.base}/api/v1/agents/copilot/conversations`, { method: "POST", headers: headers("ap.vendor.read") });
    expect(denied.status).toBe(403);
    expect(((await denied.json()) as { code: string }).code).toBe("AGT-0203");
  });

  it("rejects a message body that is unknown, mistyped or empty and a caller without the duty", async () => {
    const s = await setup([]);
    const post = (body: unknown, duties = DUTIES) => fetch(`${s.base}${MESSAGES}`, { method: "POST", headers: headers(duties), body: JSON.stringify(body) });
    for (const body of [{ mode: "route", message: "x", extra: 1 }, { mode: "nope", message: "x" }, { mode: "route", message: "" }, { mode: "route", message: 5 }, { mode: "route", message: "x".repeat(4001) }, [1], "route"]) {
      expect((await post(body)).status).toBe(400);
    }
    expect((await post({ mode: "route", message: "x" }, "ap.vendor.read")).status).toBe(403);
    expect(s.provider.seen).toHaveLength(0);
  });
});

describe("mode=route", () => {
  it("answers a rule-decided prompt with no provider call, no gateway session and a hold settled with zero", async () => {
    const s = await setup([]);
    const res = await route(s.base, "show me overdue invoices over $10k", DUTIES, "text/event-stream");
    expect(res.headers.get("content-type")).toContain("application/json");
    expect(await res.json()).toEqual({ kind: "navigate", confidence: 1, generated: true });
    expect(s.provider.seen).toHaveLength(0);
    expect(s.mcp.connects).toHaveLength(0);
    expect(s.store.reserved).toHaveLength(1);
    expect(s.store.settled).toEqual([[0, 0]]);
    expect([...s.store.runs.values()]).toEqual([expect.objectContaining({ agentId: "copilot", status: "COMMITTED" })]);
  });

  it("does not cache a route answer, so every call is counted against the budget", async () => {
    const s = await setup([]);
    await route(s.base, "what is the total?");
    await route(s.base, "what is the total?");
    expect(s.store.reserved).toHaveLength(2);
  });

  it("asks the model with an empty tool set when no rule matches and keeps a confident answer", async () => {
    const s = await setup([verdict('{"kind":"ask","confidence":0.9,"runnerUp":{"kind":"act","confidence":0.2}}')]);
    expect(await (await route(s.base, "invoices from Acme last quarter")).json()).toEqual({ kind: "ask", confidence: 0.9, generated: true });
    expect(s.provider.seen[0]?.allowedTools).toEqual([]);
    expect(s.provider.seen[0]?.system).not.toContain("Tools you may call");
    expect(s.mcp.calls).toHaveLength(0);
    expect(s.mcp.connects).toHaveLength(0);
  });

  it.each([
    ["low confidence", '{"kind":"ask","confidence":0.4}'],
    ["close runner-up", '{"kind":"ask","confidence":0.8,"runnerUp":{"kind":"navigate","confidence":0.7}}'],
    ["non-JSON text", "I think it is a question"],
    ["clarify as the kind", '{"kind":"clarify","confidence":0.99}'],
    ["string confidence", '{"kind":"ask","confidence":"0.9"}'],
    ["confidence above one", '{"kind":"ask","confidence":1.5}'],
  ])("returns clarify with two candidate kinds for %s", async (_name, text) => {
    const s = await setup([verdict(text)]);
    const body = (await (await route(s.base, "hmm")).json()) as { kind: string; candidates: string[]; generated: boolean };
    expect(body.kind).toBe("clarify");
    expect(body.candidates).toHaveLength(2);
    expect(body.generated).toBe(true);
  });

  it("ignores a tool the model names and never calls it", async () => {
    const named: ModelResponse = { text: "", proposal: { tool: "depot.ap.invoice.get", args: {}, confidence: 1, amount: { amount: "0", currency: "USD" } } };
    const s = await setup([named]);
    expect(((await (await route(s.base, "hmm")).json()) as { kind: string }).kind).toBe("clarify");
    expect(s.mcp.calls).toHaveLength(0);
  });

  it("answers 200 {refusal} with no kind for a halted tenant, even when a rule could decide", async () => {
    const s = await setup([], { killed: true });
    const body = (await (await route(s.base, "show me invoices")).json()) as Record<string, unknown>;
    expect(body).toMatchObject({ refusal: { code: "AGT-0201" }, generated: true });
    expect(body).not.toHaveProperty("kind");
    expect(s.provider.seen).toHaveLength(0);
  });

  it("answers 200 {refusal} AGT-0202 with no kind when the budget is stopped", async () => {
    const s = await setup([], { store: new BudgetStopStore() });
    const res = await route(s.base, "show me invoices");
    const body = (await res.json()) as Record<string, unknown>;
    expect(res.status).toBe(200);
    expect(body).toMatchObject({ refusal: { code: "AGT-0202" } });
    expect(body).not.toHaveProperty("kind");
  });

  it("answers 200 {refusal} AGT-0206 with a fixed message when the provider is down or rejects", async () => {
    for (const err of [providerUnavailable("upstream 503 secret-detail"), providerRejected("proposal names a tool outside the allow-list secret-detail")]) {
      const failing: ModelProvider = { complete: async () => { throw err; } };
      const s = await setup([], { provider: failing });
      const res = await route(s.base, "hmm");
      const text = await res.text();
      expect(res.status).toBe(200);
      expect(JSON.parse(text)).toMatchObject({ refusal: { code: "AGT-0206" } });
      expect(text).not.toContain("secret-detail");
      expect(text).not.toContain('"kind"');
      await close?.();
    }
  });

  it("answers the guard refusal with no kind and no provider call when the egress guard blocks", async () => {
    const guard = testGuard({ analyzer: { scan: () => { throw new Error("analyzer down"); } } });
    const s = await setup([verdict('{"kind":"ask","confidence":0.9}')], { guard });
    const body = (await (await route(s.base, "hmm")).json()) as Record<string, unknown>;
    expect(body).toMatchObject({ refusal: { code: "AGT-0219", message: GUARD_REFUSAL_MESSAGE } });
    expect(body).not.toHaveProperty("kind");
    expect(s.provider.seen).toHaveLength(0);
  });

  it("answers AGT-0220 with no kind when the output guard fails the model text", async () => {
    const outputGuard = createOutputGuard({ analyzer: { scan: () => { throw new Error("analyzer down"); } } });
    const s = await setup([verdict('{"kind":"ask","confidence":0.9}')], { outputGuard });
    const body = (await (await route(s.base, "hmm")).json()) as Record<string, unknown>;
    expect(body).toMatchObject({ refusal: { code: "AGT-0220", message: OUTPUT_GUARD_MESSAGE } });
    expect(body).not.toHaveProperty("kind");
  });

  it("answers AGT-0218 with no kind when the tenant has no approved AI route", async () => {
    const router: AiRouter = { plan: async () => { throw new AgentError("AGT-0218", "no approved route"); } };
    const s = await setup([], { router });
    const body = (await (await route(s.base, "hmm")).json()) as Record<string, unknown>;
    expect(body).toMatchObject({ refusal: { code: "AGT-0218" } });
    expect(body).not.toHaveProperty("kind");
    expect(s.provider.seen).toHaveLength(0);
  });
});
