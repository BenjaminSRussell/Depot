import { afterEach, describe, expect, it } from "vitest";
import { defaultCatalog } from "../src/agents/catalog.js";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import { createCopilotService } from "../src/runtime/copilot.js";
import { createRunService } from "../src/runtime/service.js";
import { openRouter, proposal, ScriptedProvider, testGuard, allowAiAccess } from "./fakes.js";
import { FakeMcp, headers, MemoryStore, serve, tool } from "./surface-fakes.js";

const PROPOSE = "depot.ap.invoice.propose_coding";
const CHAT_DUTIES = "mcp.copilot.use";
const RUN_ID = "00000000-0000-7000-8000-0000000000a1";
const ACTION_ID = "00000000-0000-7000-8000-0000000000b1";
const CANARY = "CANARY-prompt-text-8821";

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  await close?.();
  close = undefined;
});

const setup = async (opts: { copilot?: boolean; runs?: boolean } = {}) => {
  const store = new MemoryStore();
  const mcp = new FakeMcp([tool(PROPOSE, "ap.invoice.propose", "suggest")]);
  const provider = new ScriptedProvider([proposal(0.9, "1200", PROPOSE, { invoiceId: "INV-7" }), { text: "ok", usage: { tokensIn: 1, tokensOut: 1 } }]);
  const runs = createRunService({
    agents: defaultCatalog("claude-sonnet-5-5"),
    evalPassed: async () => true,
    providerFor: () => provider,
    mcp,
    store,
    inbox: store,
    killSwitch: { isHalted: () => false },
    disabledAgents: async () => new Set(),
    config: { runTokenCap: 50_000, selfApprovalLimit: { amount: "0", currency: "USD" }, pseudonymTtlMs: 60_000, approvalPollMs: 1 },
    sleep: () => new Promise((r) => setTimeout(r, 2)),
    spawn: (p) => void p.catch(() => undefined),
  });
  const copilot = createCopilotService({
    guard: testGuard(),
    aiAccess: allowAiAccess(),
    router: openRouter(new ScriptedProvider([{ text: "Cash is steady.", usage: { tokensIn: 10, tokensOut: 5 } }])),
    mcp,
    store,
    killSwitch: { isHalted: () => false },
    cache: new CopilotAnswerCache(),
    config: { tokenCap: 20_000, maxTurns: 3, pseudonymTtlMs: 60_000 },
    modelId: "claude-haiku-5-5",
  });
  const srv = await serve({ approvals: store, ...(opts.runs === false ? {} : { runs }), ...(opts.copilot === false ? {} : { copilot }) });
  close = srv.close;
  return srv.base;
};

const expectNoStore = (res: Response) => expect(res.headers.get("cache-control")).toBe("no-store");
const get = (base: string, path: string, duties = "agents.run.read") => fetch(`${base}${path}`, { headers: headers(duties) });
const post = (base: string, path: string, duties: string, body: unknown, accept = "application/json") =>
  fetch(`${base}${path}`, { method: "POST", headers: { ...headers(duties), accept }, body: JSON.stringify(body) });

describe("Cache-Control: no-store on assistant responses", () => {
  it("covers a run read and an unknown run", async () => {
    const base = await setup();
    const unknown = await get(base, `/api/v1/agents/runs/${RUN_ID}`);
    expect(unknown.status).toBe(404);
    expectNoStore(unknown);
    expectNoStore(await get(base, `/api/v1/agents/runs/${RUN_ID}/actions`));
    expectNoStore(await fetch(`${base}/api/v1/agents/runs/${RUN_ID}`, { method: "POST", headers: headers("agents.run.read") }));
  });

  it("covers a started run and a refused start", async () => {
    const base = await setup();
    const started = await post(base, "/api/v1/agents/ap_coding/runs", "agents.invoke,ap.invoice.read,ap.vendor.read,ledger.account.read,tax.code.read,ap.invoice.propose", { prompt: "Code invoice INV-7" });
    expect(started.status).toBe(202);
    expectNoStore(started);
    const refused = await post(base, "/api/v1/agents/ap_coding/runs", "agents.invoke", { prompt: "Code invoice INV-7" });
    expect(refused.status).toBe(403);
    expectNoStore(refused);
  });

  it("covers a decision refused without credentials and without the duty", async () => {
    const base = await setup();
    const path = `/api/v1/agents/runs/${RUN_ID}/actions/${ACTION_ID}/decision`;
    const { authorization: _a, ...anonymous } = headers("ap.invoice.propose");
    const noAuth = await fetch(`${base}${path}`, { method: "POST", headers: anonymous, body: JSON.stringify({ decision: "APPROVED" }) });
    expect(noAuth.status).toBe(401);
    expectNoStore(noAuth);
    const noDuty = await post(base, path, "ap.vendor.read", { decision: "APPROVED" });
    expect([403, 404]).toContain(noDuty.status);
    expectNoStore(noDuty);
  });

  it("covers the chat answer as JSON and as an event stream, and its refusals", async () => {
    const base = await setup();
    const json = await post(base, "/api/v1/copilot/chat", CHAT_DUTIES, { mode: "ask", message: "how is cash" });
    expect(json.status).toBe(200);
    expectNoStore(json);
    const stream = await post(base, "/api/v1/copilot/chat", CHAT_DUTIES, { mode: "ask", message: "how is cash" }, "text/event-stream");
    expect(stream.headers.get("content-type")).toContain("text/event-stream");
    expectNoStore(stream);
    await stream.text();
    const noDuty = await post(base, "/api/v1/copilot/chat", "ap.vendor.read", { mode: "ask", message: "how is cash" });
    expect(noDuty.status).toBe(403);
    expectNoStore(noDuty);
    expectNoStore(await fetch(`${base}/api/v1/copilot/chat`, { headers: headers(CHAT_DUTIES) }));
  });

  it("covers the conversation endpoints and their refusals", async () => {
    const base = await setup();
    const created = await post(base, "/api/v1/agents/copilot/conversations", CHAT_DUTIES, {});
    expect(created.status).toBe(201);
    expectNoStore(created);
    const { conversationId } = (await created.json()) as { conversationId: string };
    const messages = `/api/v1/agents/copilot/conversations/${conversationId}/messages`;
    const routed = await post(base, messages, CHAT_DUTIES, { mode: "route", message: "show me overdue invoices" }, "text/event-stream");
    expect(routed.headers.get("content-type")).toContain("application/json");
    expectNoStore(routed);
    expect((await post(base, "/api/v1/agents/copilot/conversations", "ap.vendor.read", {})).status).toBe(403);
    expect((await post(base, "/api/v1/agents/copilot/conversations/not-a-uuid/messages", CHAT_DUTIES, { mode: "route", message: "x" })).status).toBe(404);
    expect((await fetch(`${base}/api/v1/agents/copilot/conversations`, { headers: headers(CHAT_DUTIES) })).status).toBe(405);
  });

  it("covers chat when no copilot service is wired", async () => {
    const base = await setup({ copilot: false });
    const bare = await post(base, `/api/v1/agents/copilot/conversations/${RUN_ID}/messages`, CHAT_DUTIES, { mode: "route", message: "x" });
    expect(bare.status).toBe(404);
    expect((await post(base, "/api/v1/agents/copilot/conversations", CHAT_DUTIES, {})).status).toBe(404);
    const res = await post(base, "/api/v1/copilot/chat", CHAT_DUTIES, { mode: "ask", message: "how is cash" });
    expect(res.status).toBe(404);
    expectNoStore(res);
  });

  it.each(["briefings", "approvals"])("covers the unserved %s path, with and without credentials, and never echoes the query", async (family) => {
    const base = await setup();
    const path = `/api/v1/agents/copilot/${family}?q=${CANARY}`;
    for (const h of [headers(CHAT_DUTIES), {}]) {
      const res = await fetch(`${base}${path}`, { headers: h });
      expect(res.status).toBe(404);
      expectNoStore(res);
      expect(await res.text()).not.toContain(CANARY);
    }
  });
});
