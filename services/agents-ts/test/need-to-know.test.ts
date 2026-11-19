import { afterEach, describe, expect, it } from "vitest";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import { defaultPolicy } from "../src/domain/aiDataPolicy.js";
import type { Principal } from "../src/api/principal.js";
import { createAiPolicyGuard } from "../src/runtime/aiPolicyGuard.js";
import { createCopilotService } from "../src/runtime/copilot.js";
import type { ModelResponse } from "../src/runtime/engine.js";
import { createEgressGuard, EGRESS_GUARD_TOOL } from "../src/runtime/egressGuard.js";
import { createNeedToKnowClient } from "../src/runtime/needToKnow.js";
import type { AiRouter } from "../src/runtime/aiRouter.js";
import { allowAiAccess, ScriptedProvider, testGuard } from "./fakes.js";
import { FakeMcp, headers, MemoryStore, serve, tool } from "./surface-fakes.js";

const GET = "depot.ap.invoice.get";
const T1 = "11111111-1111-4111-8111-111111111111";
const LE = "22222222-2222-4222-8222-222222222222";
const caller = (duties: string[] = []): Principal => ({ tenantId: T1, userId: "u1", legalEntityId: LE, duties: new Set(duties) }) as Principal;
const json = (body: unknown, status = 200): Response => new Response(JSON.stringify(body), { status });

describe("need-to-know client", () => {
  const client = (fetchFn: typeof fetch, now = () => 0) => createNeedToKnowClient({ baseUrl: "http://compliance", authHeaders: () => ({}), fetchFn, now });

  it("reads the switch per legal entity and caches it for 60 seconds", async () => {
    let calls = 0;
    let clock = 0;
    const c = client(async (url) => {
      calls += 1;
      expect(String(url)).toContain(`legalEntityId=${LE}`);
      return json({ enabled: false, source: "TENANT" });
    }, () => clock);
    expect(await c.enforced(caller())).toBe(false);
    expect(await c.enforced(caller())).toBe(false);
    expect(calls).toBe(1);
    clock = 61_000;
    await c.enforced(caller());
    expect(calls).toBe(2);
  });

  it("is ON when unreadable, refused, unset or the caller names no legal entity, and OFF for cui.view", async () => {
    expect(await client(async () => json({}, 500)).enforced(caller())).toBe(true);
    expect(await client(async () => json({ enabled: "no" })).enforced(caller())).toBe(true);
    expect(await client(async () => Promise.reject(new Error("down"))).enforced(caller())).toBe(true);
    expect(await createNeedToKnowClient({ baseUrl: undefined, authHeaders: () => ({}) }).enforced(caller())).toBe(true);
    expect(await client(async () => json({ enabled: false })).enforced({ ...caller(), legalEntityId: undefined } as Principal)).toBe(true);
    expect(await client(async () => json({ enabled: true })).enforced(caller(["cui.view"]))).toBe(false);
  });
});

describe("guard admission order", () => {
  const policy = (p1: boolean) => createAiPolicyGuard({ read: async () => ({ ...defaultPolicy(), modules: { ap: { ...defaultPolicy().modules.ap, P1: p1 }, ar: defaultPolicy().modules.ar } }) });

  it("uses the default cell for a module outside ap and ar and the tenant policy inside", async () => {
    const guard = createEgressGuard({ policy: policy(false) });
    expect(await guard.admit({ tenantId: T1, tool: "depot.knowledge.search", text: "{}" })).toMatchObject({ pass: true, policyCell: "default", class: "P1" });
    expect(await guard.admit({ tenantId: T1, tool: GET, text: '{"id":"x"}' })).toMatchObject({ pass: true, policyCell: "tenant", class: "P2" });
  });

  it("raises the class from detected content and blocks when the policy cannot be read", async () => {
    const guard = createEgressGuard({ policy: createAiPolicyGuard({ read: async () => Promise.reject(new Error("db down")) }) });
    expect(await guard.admit({ tenantId: T1, tool: "depot.ar.invoice.list", text: "DE89370400440532013000" })).toMatchObject({ pass: true, class: "P3" });
    const unreadable = createEgressGuard({ policy: createAiPolicyGuard({ read: async () => Promise.reject(new Error("db down")) }) });
    expect(await unreadable.admit({ tenantId: T1, tool: "depot.knowledge.search", text: "x" })).toMatchObject({ pass: true });
  });});

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  await close?.();
  close = undefined;
});

const chatWithCui = async (duties: string, enforced: boolean) => {
  const store = new MemoryStore();
  const provider = new ScriptedProvider([
    { text: "", proposal: { tool: GET, args: {}, confidence: 1, amount: { amount: "0", currency: "USD" } }, usage: { tokensIn: 10, tokensOut: 1 } } as ModelResponse,
    { text: "Done.", usage: { tokensIn: 10, tokensOut: 1 } },
  ]);
  const router: AiRouter = { plan: async ({ refs }) => ({ routeId: "cui", provider, keep: [...refs], excluded: [], restricted: [...refs], classes: refs.map(() => "CUI" as const) }) };
  const copilot = createCopilotService({
    guard: testGuard(),
    aiAccess: allowAiAccess(),
    router,
    needToKnow: { enforced: async () => enforced },
    mcp: new FakeMcp([tool(GET, "ap.invoice.read")], { [GET]: { ok: true, output: { id: "CUI-INV-9", note: "secret-cui-note" }, ref: "CUI-INV-9" } }),
    store,
    killSwitch: { isHalted: () => false },
    cache: new CopilotAnswerCache(),
    config: { tokenCap: 20_000, maxTurns: 3, pseudonymTtlMs: 60_000 },
    modelId: "m",
  });
  const srv = await serve({ approvals: store, copilot });
  close = srv.close;
  const res = await fetch(`${srv.base}/api/v1/copilot/chat`, { method: "POST", headers: headers(duties), body: JSON.stringify({ mode: "ask", message: "show it" }) });
  const rows = [...store.actionRows.values()].flat();
  return { body: (await res.json()) as Record<string, unknown>, provider, rows };
};

describe("need-to-know in the copilot", () => {
  it("drops CUI-designated records with reason NEED_TO_KNOW and leaves no trace for the user", async () => {
    const { body, provider, rows } = await chatWithCui("mcp.copilot.use,ap.invoice.read", true);
    for (const seen of provider.seen) expect(seen.user).not.toContain("CUI-INV-9");
    expect(JSON.stringify(provider.seen)).not.toContain("secret-cui-note");
    expect(body["excluded"]).toBeUndefined();
    expect(String(body["answer"])).not.toMatch(/withheld|left out|need/i);
    const guardRows = rows.filter((r) => r.toolName === EGRESS_GUARD_TOOL);
    expect(guardRows.at(-1)?.guardResult?.excluded).toEqual({ NEED_TO_KNOW: 1 });
  });

  it("passes the records when the switch is OFF", async () => {
    const { provider, rows } = await chatWithCui("mcp.copilot.use,ap.invoice.read,cui.view", false);
    expect(JSON.stringify(provider.seen)).toContain("secret-cui-note");
    expect(rows.filter((r) => r.toolName === EGRESS_GUARD_TOOL).at(-1)?.guardResult?.excluded).toEqual({});
  });
});
