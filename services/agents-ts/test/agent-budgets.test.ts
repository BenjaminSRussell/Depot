import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createAgentBudgetService, parseCapBody, type AgentBudgetStore, type TokenCap, type TokenPair } from "../src/api/agentBudgets.js";
import { budgetStatus, costUsd, type TokenBudget } from "../src/domain/budget.js";
import { routeLabel } from "../src/server.js";
import { MemoryStore, OTHER, serve, T1, USER, headers } from "./surface-fakes.js";

const NOW = new Date("2026-11-18T10:00:00Z");
const READ = "agentbudget.read";
const WRITE = "agentbudget.write";

class MemoryBudgets implements AgentBudgetStore {
  readonly tenants = new Map<string, TokenBudget>();
  readonly caps = new Map<string, Map<string, TokenCap>>();
  readonly usage = new Map<string, Map<string, TokenPair>>();
  readonly changes: Array<{ tenantId: string; agentId: string; cap: TokenCap; by: string }> = [];
  fail = false;

  async snapshot(tenantId: string): Promise<TokenBudget> {
    if (this.fail) throw new Error("connect ECONNREFUSED 10.0.0.1:5432 secret-host");
    return this.tenants.get(tenantId) ?? { limitIn: 1_000, limitOut: 1_000, usedIn: 0, usedOut: 0, softPct: 80 };
  }
  async agentCaps(tenantId: string) {
    return this.caps.get(tenantId) ?? new Map<string, TokenCap>();
  }
  async agentUsage(tenantId: string) {
    return this.usage.get(tenantId) ?? new Map<string, TokenPair>();
  }
  async setAgentCap(tenantId: string, agentId: string, cap: TokenCap, by: string) {
    this.caps.set(tenantId, new Map(this.caps.get(tenantId)).set(agentId, cap));
    this.changes.push({ tenantId, agentId, cap, by });
  }
}

describe("budget thresholds and cost", () => {
  const at = (usedIn: number): TokenBudget => ({ limitIn: 100, limitOut: 100, usedIn, usedOut: 0, softPct: 80 });
  it.each([
    [79, "OK"],
    [80, "SOFT"],
    [99, "SOFT"],
    [100, "HARD"],
  ])("%i of 100 tokens is %s", (used, state) => {
    expect(budgetStatus(at(used)).state).toBe(state);
  });

  it("a zero cap with any use is HARD", () => {
    expect(budgetStatus({ limitIn: 0, limitOut: 0, usedIn: 1, usedOut: 0, softPct: 80 }).state).toBe("HARD");
  });

  it("costs tokens at 2 dollars per million in and 10 per million out", () => {
    expect(costUsd(0, 0)).toBe("0.000000");
    expect(costUsd(1_000_000, 1_000_000)).toBe("12.000000");
    expect(costUsd(1, 1)).toBe("0.000012");
  });
});

describe("metric label", () => {
  it.each([
    ["/api/v1/agents/budgets", "budgets"],
    ["/api/v1/agents/budgets/ap_coding", "budgets"],
    ["/api/v1/agents/budgets/AP", "other"],
    ["/api/v1/agents/budgets/a/b", "other"],
  ])("%s is labelled %s", (path, label) => {
    expect(routeLabel(path)).toBe(label);
  });
});

describe("cap validation", () => {
  it("accepts two non-negative integers", () => {
    expect(parseCapBody({ limitIn: 0, limitOut: 5 })).toEqual({ limitIn: 0, limitOut: 5 });
  });

  it.each([
    ["a string", { limitIn: "abc", limitOut: 1 }, "limitIn"],
    ["a numeric string", { limitIn: "1000", limitOut: 1 }, "limitIn"],
    ["a negative", { limitIn: 1, limitOut: -1 }, "limitOut"],
    ["a fraction", { limitIn: 1.5, limitOut: 1 }, "limitIn"],
    ["null", { limitIn: null, limitOut: 1 }, "limitIn"],
    ["an object", { limitIn: {}, limitOut: 1 }, "limitIn"],
    ["a missing field", { limitIn: 1 }, "limitOut"],
    ["above the safe range", { limitIn: Number.MAX_SAFE_INTEGER + 1, limitOut: 1 }, "limitIn"],
  ])("rejects %s with 422 naming the field", (_name, body, field) => {
    expect(() => parseCapBody(body)).toThrowError(expect.objectContaining({ status: 422, code: "AGT-0422", message: expect.stringContaining(field) }));
  });
});

describe("budgets API", () => {
  const budgets = new MemoryBudgets();
  let api: Awaited<ReturnType<typeof serve>>;
  beforeAll(async () => {
    api = await serve({ approvals: new MemoryStore(), budgets: createAgentBudgetService({ store: budgets, now: () => NOW }) });
  });
  afterAll(() => api.close());

  const get = (duties: string, tenant = T1) => fetch(`${api.base}/api/v1/agents/budgets`, { headers: headers(duties, USER, tenant) });
  const put = (duties: string, agent: string, body: unknown) =>
    fetch(`${api.base}/api/v1/agents/budgets/${agent}`, { method: "PUT", headers: headers(duties), body: typeof body === "string" ? body : JSON.stringify(body) });

  it("lists the tenant and every agent with cap, use, cost, percent and status", async () => {
    budgets.tenants.set(T1, { limitIn: 1_000, limitOut: 1_000, usedIn: 800, usedOut: 0, softPct: 80 });
    budgets.usage.set(T1, new Map([["ap_coding", { tokensIn: 100, tokensOut: 50 }]]));
    const res = await get(READ);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { month: string; tenant: Record<string, unknown>; agents: Array<Record<string, unknown>> };
    expect(body.month).toBe("2026-11");
    expect(body.tenant).toMatchObject({ scope: "TENANT", agentId: null, cap: { tokensIn: 1_000, tokensOut: 1_000 }, percentConsumed: 80, status: "SOFT", costUsed: "0.001600" });
    expect(body.agents.map((a) => a["agentId"])).toEqual(["ap_coding", "cash_app", "mrp_triage", "collections", "anomaly"]);
    expect(body.agents[0]).toMatchObject({ scope: "AGENT", tokensUsed: { tokensIn: 100, tokensOut: 50 }, status: "OK", percentConsumed: 10, costUsed: "0.000700" });
  });

  it("reports HARD at 100 percent and OK again once the cap is raised", async () => {
    budgets.tenants.set(T1, { limitIn: 1_000, limitOut: 1_000, usedIn: 1_000, usedOut: 0, softPct: 80 });
    expect(((await (await get(READ)).json()) as { tenant: { status: string } }).tenant.status).toBe("HARD");
    budgets.tenants.set(T1, { limitIn: 5_000, limitOut: 1_000, usedIn: 1_000, usedOut: 0, softPct: 80 });
    expect(((await (await get(READ)).json()) as { tenant: { status: string } }).tenant.status).toBe("OK");
  });

  it("answers 401 without a token", async () => {
    const res = await fetch(`${api.base}/api/v1/agents/budgets`);
    expect(res.status).toBe(401);
    expect(await res.json()).toMatchObject({ code: "AGT-0401" });
  });

  it.each([["no duty", ""], ["only agentbudget.write", WRITE], ["an unrelated duty", "agents.run.read"]])("refuses the list to a caller with %s as a 403 problem", async (_name, duties) => {
    const res = await get(duties);
    expect(res.status).toBe(403);
    expect(res.headers.get("content-type")).toBe("application/problem+json");
    expect(await res.json()).toMatchObject({ status: 403, code: "AGT-0203", instance: "/api/v1/agents/budgets" });
  });

  it("sets a cap, the next list shows it and the change is recorded with the caller", async () => {
    const res = await put(WRITE, "cash_app", { limitIn: 200, limitOut: 100 });
    expect(res.status).toBe(200);
    expect(await res.json()).toMatchObject({ agentId: "cash_app", cap: { tokensIn: 200, tokensOut: 100 } });
    const list = (await (await get(READ)).json()) as { agents: Array<{ agentId: string; cap: unknown }> };
    expect(list.agents.find((a) => a.agentId === "cash_app")?.cap).toEqual({ tokensIn: 200, tokensOut: 100 });
    expect(budgets.changes.at(-1)).toEqual({ tenantId: T1, agentId: "cash_app", cap: { limitIn: 200, limitOut: 100 }, by: USER });
  });

  it("an agent with a cap of its own is judged against it, not the tenant's", async () => {
    budgets.usage.set(T1, new Map([["anomaly", { tokensIn: 90, tokensOut: 0 }]]));
    await put(WRITE, "anomaly", { limitIn: 100, limitOut: 100 });
    const list = (await (await get(READ)).json()) as { agents: Array<{ agentId: string; status: string }> };
    expect(list.agents.find((a) => a.agentId === "anomaly")?.status).toBe("SOFT");
  });

  it.each([
    ["no duty", "", 403],
    ["only agentbudget.read", READ, 403],
  ])("refuses a cap to a caller with %s and stores nothing", async (_name, duties, status) => {
    const before = budgets.changes.length;
    expect((await put(duties, "ap_coding", { limitIn: 1, limitOut: 1 })).status).toBe(status);
    expect(budgets.changes).toHaveLength(before);
  });

  it("answers 401 to a cap without a token", async () => {
    const res = await fetch(`${api.base}/api/v1/agents/budgets/ap_coding`, { method: "PUT", body: JSON.stringify({ limitIn: 1, limitOut: 1 }) });
    expect(res.status).toBe(401);
  });

  it.each(["nope", "AP_CODING", "ap-coding"])("answers 404 for the agent %s and stores nothing", async (agent) => {
    const before = budgets.changes.length;
    const res = await put(WRITE, agent, { limitIn: 1, limitOut: 1 });
    expect(res.status).toBe(404);
    expect(await res.json()).toMatchObject({ code: "AGT-0404" });
    expect(budgets.changes).toHaveLength(before);
  });

  it.each([
    ["a non-numeric limitIn", { limitIn: "abc", limitOut: 1 }, "limitIn"],
    ["a negative limitOut", { limitIn: 1, limitOut: -1 }, "limitOut"],
    ["a fraction", { limitIn: 1.5, limitOut: 1 }, "limitIn"],
    ["a null", { limitIn: 1, limitOut: null }, "limitOut"],
    ["a missing limitIn", { limitOut: 1 }, "limitIn"],
    ["an overflowing number", '{"limitIn": 1e999, "limitOut": 1}', "limitIn"],
  ])("answers 422 naming the field for %s", async (_name, body, field) => {
    const before = budgets.changes.length;
    const res = await put(WRITE, "ap_coding", body);
    expect(res.status).toBe(422);
    expect(await res.json()).toMatchObject({ code: "AGT-0422", detail: expect.stringContaining(field) });
    expect(budgets.changes).toHaveLength(before);
  });

  it.each([
    ["an unknown field", { limitIn: 1, limitOut: 1, limit: 2 }],
    ["an array", [1, 2]],
    ["invalid JSON", "{nope"],
    ["an empty body", ""],
  ])("answers 400 for %s", async (_name, body) => {
    const res = await put(WRITE, "ap_coding", body);
    expect(res.status).toBe(400);
    expect(await res.json()).toMatchObject({ code: "AGT-0400" });
  });

  it("answers 413 to a body over 64 KiB", async () => {
    const res = await put(WRITE, "ap_coding", { limitIn: 1, limitOut: 1, pad: "x".repeat(70_000) });
    expect(res.status).toBe(413);
  });

  it.each([
    ["POST", "/api/v1/agents/budgets", "GET"],
    ["DELETE", "/api/v1/agents/budgets", "GET"],
    ["GET", "/api/v1/agents/budgets/ap_coding", "PUT"],
    ["POST", "/api/v1/agents/budgets/ap_coding", "PUT"],
  ])("answers 405 with allow for %s %s", async (method, path, allow) => {
    const res = await fetch(`${api.base}${path}`, { method, headers: headers(`${READ},${WRITE}`) });
    expect(res.status).toBe(405);
    expect(res.headers.get("allow")).toBe(allow);
  });

  it("shows each tenant only its own budgets and caps", async () => {
    budgets.tenants.set(OTHER, { limitIn: 10, limitOut: 10, usedIn: 10, usedOut: 0, softPct: 80 });
    const mine = (await (await get(READ)).json()) as { tenant: { cap: unknown }; agents: Array<{ agentId: string; cap: unknown }> };
    const theirs = (await (await get(READ, OTHER)).json()) as { tenant: { cap: unknown; status: string }; agents: Array<{ agentId: string; cap: unknown }> };
    expect(theirs.tenant).toMatchObject({ cap: { tokensIn: 10, tokensOut: 10 }, status: "HARD" });
    expect(theirs.agents.find((a) => a.agentId === "cash_app")?.cap).toEqual({ tokensIn: 10, tokensOut: 10 });
    expect(mine.agents.find((a) => a.agentId === "cash_app")?.cap).toEqual({ tokensIn: 200, tokensOut: 100 });
  });

  it("answers 500 without internals when the database is unavailable", async () => {
    budgets.fail = true;
    try {
      const res = await get(READ);
      expect(res.status).toBe(500);
      const text = await res.text();
      expect(JSON.parse(text)).toMatchObject({ code: "AGT-0500", detail: "unexpected error" });
      expect(text).not.toContain("secret-host");
    } finally {
      budgets.fail = false;
    }
  });
});

describe("budgets service not wired", () => {
  it("answers 404 on both routes", async () => {
    const api = await serve({ approvals: new MemoryStore() });
    try {
      expect((await fetch(`${api.base}/api/v1/agents/budgets`, { headers: headers(READ) })).status).toBe(404);
      expect((await fetch(`${api.base}/api/v1/agents/budgets/ap_coding`, { method: "PUT", headers: headers(WRITE), body: "{}" })).status).toBe(404);
    } finally {
      await api.close();
    }
  });
});
