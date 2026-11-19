import { readFileSync } from "node:fs";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AGENT_IDS } from "../src/agents/catalog.js";
import { ApiError } from "../src/api/principal.js";
import {
  actionRoute,
  buildBriefing,
  decodeCursor,
  encodeCursor,
  type BriefingContent,
  type BriefingFacts,
  type BriefingStore,
  type PendingFact,
  type StoredBriefing,
} from "../src/domain/briefing.js";
import { createBriefingService } from "../src/runtime/briefing.js";
import { routeLabel } from "../src/server.js";
import { headers, MemoryStore, OTHER, serve, USER } from "./surface-fakes.js";

const NOW = new Date("2026-11-19T10:00:00.000Z");
const ID = "00000000-0000-7000-8000-0000000000e1";
const BASE_PATH = "/api/v1/agents/briefings";
const GENERATE = "agents.invoke";
const READ = "agents.run.read";

const pending = (over: Partial<PendingFact> = {}): PendingFact => ({
  agentId: "ap_coding",
  actionId: "00000000-0000-7000-8000-00000000a001",
  tool: "depot.ap.invoice.propose_coding",
  amount: "100.00",
  currency: "USD",
  selfApprovalLimit: "500.00",
  selfApprovalCurrency: "USD",
  requestedAt: "2026-11-19T09:00:00.000Z",
  expiryHours: 72,
  ...over,
});

const facts = (over: Partial<BriefingFacts> = {}): BriefingFacts => ({ pending: [], decided: [], runs: [], ...over });

class FakeStore implements BriefingStore {
  readonly rows = new Map<string, StoredBriefing & { userId: string }>();
  refuse: ApiError | undefined;

  async create(_t: string, userId: string): Promise<string> {
    if (this.refuse !== undefined) throw this.refuse;
    const id = `00000000-0000-7000-8000-0000000000f${this.rows.size}`;
    this.rows.set(id, { id, userId, status: "RUNNING", narrative: false, createdAt: NOW.toISOString(), completedAt: null, readAt: null, content: null });
    return id;
  }
  async finish(_t: string, id: string, content: BriefingContent): Promise<void> {
    this.rows.set(id, { ...this.rows.get(id)!, status: "READY", content });
  }
  async fail(_t: string, id: string): Promise<void> {
    this.rows.set(id, { ...this.rows.get(id)!, status: "FAILED" });
  }
  async get(_t: string, userId: string, id: string) {
    const row = this.rows.get(id);
    return row?.userId === userId ? row : undefined;
  }
  async list(_t: string, userId: string) {
    return { items: [...this.rows.values()].filter((r) => r.userId === userId), nextCursor: null };
  }
  async markRead(_t: string, userId: string, id: string) {
    const row = await this.get(_t, userId, id);
    if (row !== undefined) row.readAt = NOW.toISOString();
    return row;
  }
}

describe("buildBriefing", () => {
  it.each(AGENT_IDS)("tags every item of %s with its source agent and links each recommendation to its proposal", (agentId) => {
    const p = pending({ agentId });
    const out = buildBriefing(
      facts({
        pending: [p],
        decided: [{ agentId, actionId: "d1", tool: p.tool, decision: "APPROVED", decidedAt: "2026-11-19T08:00:00.000Z" }],
        runs: [{ agentId, runId: "r1", status: "FAILED", createdAt: "2026-11-19T07:00:00.000Z" }],
      }),
      NOW,
    );
    expect([...out.changes, ...out.risks, ...out.recommendedActions].every((i) => i.agentId === agentId)).toBe(true);
    expect(out.recommendedActions).toEqual([expect.objectContaining({ actionId: p.actionId, route: actionRoute(p.actionId) })]);
    expect(out.counts[agentId]).toEqual({ pending: 1, decided: 1, committedRuns: 0, failedRuns: 1 });
    expect(out.risks.map((r) => r.factId)).toEqual(["r1"]);
  });

  it("flags a pending item that expires within 24 hours and one above the self-approval limit", () => {
    const soon = pending({ actionId: "soon", requestedAt: "2026-11-17T09:00:00.000Z", expiryHours: 72 });
    const large = pending({ actionId: "large", amount: "900.00" });
    const out = buildBriefing(facts({ pending: [soon, large] }), NOW);
    expect(out.risks.map((r) => r.factId)).toEqual(["soon", "large"]);
  });

  it("does not compare amounts in different currencies", () => {
    const out = buildBriefing(facts({ pending: [pending({ amount: "900.00", currency: "EUR" })] }), NOW);
    expect(out.risks).toEqual([]);
  });

  it("drops a disabled agent's items and count and states it is disabled, leaving other agents untouched", () => {
    const input = facts({
      pending: [pending({ agentId: "ap_coding" }), pending({ agentId: "cash_app", actionId: "keep" })],
      runs: [{ agentId: "ap_coding", runId: "r1", status: "FAILED", createdAt: "2026-11-19T07:00:00.000Z" }],
    });
    const out = buildBriefing(input, NOW, new Set(["ap_coding", "not_an_agent"]));
    expect([...out.changes, ...out.risks, ...out.recommendedActions].map((i) => i.agentId)).toEqual(["cash_app"]);
    expect(Object.keys(out.counts)).toEqual(AGENT_IDS.filter((id) => id !== "ap_coding"));
    expect(out.notes).toEqual(["ap_coding is disabled by your administrator"]);
    expect(buildBriefing(input, NOW, new Set(["not_an_agent"]))).toEqual(buildBriefing(input, NOW));
  });

  it("is an empty briefing with zero counts when there are no facts", () => {
    const out = buildBriefing(facts(), NOW);
    expect([out.changes, out.risks, out.recommendedActions, out.notes]).toEqual([[], [], [], []]);
    expect(Object.values(out.counts).every((c) => c.pending + c.decided + c.committedRuns + c.failedRuns === 0)).toBe(true);
  });
});

describe("cursor", () => {
  it("round-trips and refuses malformed values", () => {
    const cursor = ["2026-11-19 10:00:00.123456+00", ID] as const;
    expect(decodeCursor(encodeCursor(cursor))).toEqual(cursor);
    for (const bad of ["", "!!!", encodeCursor(["x", ID]), encodeCursor(["2026-11-19 10:00:00+00", "nope"]), Buffer.from("{}").toString("base64url")]) {
      expect(decodeCursor(bad)).toBeUndefined();
    }
  });
});

describe("briefing routes", () => {
  let close: (() => Promise<void>) | undefined;
  afterEach(async () => {
    await close?.();
    close = undefined;
  });

  const setup = async (over: { facts?: () => Promise<BriefingFacts>; wired?: boolean } = {}) => {
    const store = new FakeStore();
    const read = vi.fn(over.facts ?? (async () => facts({ pending: [pending()] })));
    const logs: string[] = [];
    const briefings = createBriefingService({ store, facts: { read }, disabledAgents: async () => new Set(), now: () => NOW, log: (_l, message) => void logs.push(message) });
    const srv = await serve({ approvals: new MemoryStore(), ...(over.wired === false ? {} : { briefings }) });
    close = srv.close;
    return { base: srv.base, store, read, logs };
  };
  const call = (base: string, method: string, path: string, duties: string, user = USER) => fetch(`${base}${path}`, { method, headers: headers(duties, user) });

  it("generates a briefing in the background and stores it READY", async () => {
    const { base, store } = await setup();
    const res = await call(base, "POST", `${BASE_PATH}:generate`, GENERATE);
    expect(res.status).toBe(202);
    expect(res.headers.get("cache-control")).toBe("no-store");
    const { id } = (await res.json()) as { id: string };
    await vi.waitFor(() => expect(store.rows.get(id)?.status).toBe("READY"));
    const got = await call(base, "GET", `${BASE_PATH}/${id}`, READ);
    expect(((await got.json()) as { recommendedActions: unknown[] }).recommendedActions).toHaveLength(1);
  });

  it("ends the row FAILED, logs, and never leaves it RUNNING when the facts read fails", async () => {
    const { base, store, logs } = await setup({ facts: async () => Promise.reject(new Error("db down")) });
    const { id } = (await (await call(base, "POST", `${BASE_PATH}:generate`, GENERATE)).json()) as { id: string };
    await vi.waitFor(() => expect(store.rows.get(id)?.status).toBe("FAILED"));
    expect(logs).toEqual(["briefing build failed"]);
  });

  it("answers 429 with Retry-After and no build, and 409 with the in-flight id and no build", async () => {
    const { base, store, read } = await setup();
    store.refuse = new ApiError(429, "AGT-0230", "slow down", { headers: { "retry-after": "120" } });
    const limited = await call(base, "POST", `${BASE_PATH}:generate`, GENERATE);
    expect([limited.status, limited.headers.get("retry-after")]).toEqual([429, "120"]);
    expect(await limited.json()).toMatchObject({ code: "AGT-0230", title: "Too Many Requests" });
    store.refuse = new ApiError(409, "AGT-0231", "in flight", { members: { briefingId: ID } });
    const inFlight = await call(base, "POST", `${BASE_PATH}:generate`, GENERATE);
    expect(inFlight.status).toBe(409);
    expect(await inFlight.json()).toMatchObject({ code: "AGT-0231", briefingId: ID });
    expect(read).not.toHaveBeenCalled();
  });

  it.each([
    ["POST", `${BASE_PATH}:generate`, READ],
    ["GET", BASE_PATH, GENERATE],
    ["GET", `${BASE_PATH}/${ID}`, GENERATE],
    ["POST", `${BASE_PATH}/${ID}:mark-read`, GENERATE],
  ])("refuses %s %s without the route's duty", async (method, path, duties) => {
    const { base, read } = await setup();
    const res = await call(base, method, path, duties);
    expect(res.status).toBe(403);
    expect(await res.json()).toMatchObject({ code: "AGT-0203" });
    expect(read).not.toHaveBeenCalled();
  });

  it("answers 401 without a bearer token, 405 on the wrong method, and 404 when no service is wired", async () => {
    const { base } = await setup();
    const { authorization: _a, ...anonymous } = headers(GENERATE);
    expect((await fetch(`${base}${BASE_PATH}:generate`, { method: "POST", headers: anonymous })).status).toBe(401);
    const wrong = await call(base, "GET", `${BASE_PATH}:generate`, GENERATE);
    expect([wrong.status, wrong.headers.get("allow")]).toEqual([405, "POST"]);
    expect((await call(base, "GET", `${BASE_PATH}/not-a-uuid`, READ)).status).toBe(404);
    const bare = await setup({ wired: false });
    expect((await call(bare.base, "POST", `${BASE_PATH}:generate`, GENERATE)).status).toBe(404);
  });

  it.each(["0", "101", "abc", "1.5", "", "-1", "1e2", "cursor"])("refuses list limit or cursor %j with 400", async (value) => {
    const { base } = await setup();
    const query = value === "cursor" ? "cursor=garbage" : `limit=${value}`;
    expect((await call(base, "GET", `${BASE_PATH}?${query}`, READ)).status).toBe(400);
  });

  it("lists only the caller's briefings with nextScheduledAt null and marks read for the owner only", async () => {
    const { base, store } = await setup();
    const mine = await store.create("t", USER);
    await store.create("t", OTHER);
    const list = (await (await call(base, "GET", BASE_PATH, READ)).json()) as { items: Array<{ id: string }>; nextScheduledAt: null };
    expect(list.items.map((i) => i.id)).toEqual([mine]);
    expect(list.nextScheduledAt).toBeNull();
    expect((await call(base, "POST", `${BASE_PATH}/${mine}:mark-read`, READ, OTHER)).status).toBe(404);
    expect(store.rows.get(mine)?.readAt).toBeNull();
    const owner = await call(base, "POST", `${BASE_PATH}/${mine}:mark-read`, READ);
    expect(((await owner.json()) as { readAt: string }).readAt).toBe(NOW.toISOString());
  });
});

describe("briefing modules", () => {
  it("name no tool, so creating a briefing cannot commit, post or send", () => {
    for (const file of ["domain/briefing.ts", "runtime/briefing.ts", "store/briefing.ts", "api/briefings.ts"]) {
      expect(readFileSync(new URL(`../src/${file}`, import.meta.url), "utf8")).not.toMatch(/depot\.[a-z]+\.[a-z_.]+/);
    }
  });

  it("labels briefing paths for metrics", () => {
    expect([routeLabel(BASE_PATH), routeLabel(`${BASE_PATH}:generate`), routeLabel(`${BASE_PATH}/${ID}:mark-read`)]).toEqual(["briefings", "briefings", "briefings"]);
  });
});
