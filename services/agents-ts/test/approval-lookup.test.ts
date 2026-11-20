import { afterEach, describe, expect, it } from "vitest";
import { approvalStateOf } from "../src/api/decision.js";
import { headers, MemoryStore, OTHER, serve, T1 } from "./surface-fakes.js";

const DUTY = "ap.invoice.propose";
const HOUR_MS = 3_600_000;
const NOW = Date.parse("2026-10-21T12:00:00.000Z");
const UNKNOWN = "00000000-0000-7000-8000-00000000ffff";

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  await close?.();
  close = undefined;
});

const setup = async (over: { ageHours?: number; expiryHours?: number; decided?: Parameters<MemoryStore["record"]>[3] } = {}) => {
  const store = new MemoryStore();
  const actionId = await store.open({
    tenantId: T1,
    runId: "00000000-0000-7000-8000-0000000000d1",
    seq: 1,
    tool: "depot.ar.settlement.propose",
    invokerUserId: OTHER,
    approvalDuty: DUTY,
    amount: { amount: "1200.50", currency: "USD" },
    selfApprovalLimit: { amount: "0", currency: "USD" },
    ...(over.expiryHours !== undefined ? { expiryHours: over.expiryHours } : {}),
    proposalJson: "{}",
  });
  const request = store.approvals.get(actionId);
  if (request === undefined) throw new Error("no request");
  request.requestedAtMs = NOW - (over.ageHours ?? 1) * HOUR_MS;
  if (over.decided !== undefined) request.decided = over.decided;
  const srv = await serve({ approvals: store, approvalReader: store, now: () => new Date(NOW) });
  close = srv.close;
  return { store, srv, actionId };
};

const get = (base: string, id: string, h: Record<string, string> = headers(DUTY)) => fetch(`${base}/api/v1/agents/approvals/${id}`, { headers: h });

describe("GET /api/v1/agents/approvals/{actionId}", () => {
  it("returns a pending request with the stored money text, the tool and its expiry", async () => {
    const { srv, actionId } = await setup();
    const res = await get(srv.base, actionId);
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual({
      runId: "00000000-0000-7000-8000-0000000000d1",
      actionId,
      tool: "depot.ar.settlement.propose",
      state: "PENDING",
      amount: { amount: "1200.50", currency: "USD" },
      requestedAt: new Date(NOW - HOUR_MS).toISOString(),
      expiresAt: new Date(NOW - HOUR_MS + 72 * HOUR_MS).toISOString(),
    });
  });

  it("returns EXPIRED for an undecided request past 72 hours, and honours a custom expiry, writing nothing", async () => {
    const aged = await setup({ ageHours: 73 });
    expect(((await (await get(aged.srv.base, aged.actionId)).json()) as { state: string }).state).toBe("EXPIRED");
    expect(aged.store.approvals.get(aged.actionId)?.decided).toBeUndefined();
    await aged.srv.close();
    const custom = await setup({ ageHours: 5, expiryHours: 4 });
    expect(((await (await get(custom.srv.base, custom.actionId)).json()) as { state: string }).state).toBe("EXPIRED");
  });

  it("returns the stored decision, and a stored EXPIRED reason wins over REJECTED", async () => {
    const decidedAt = new Date(NOW).toISOString();
    const approved = await setup({ decided: { decision: "APPROVED", approverUserId: OTHER, decidedAt } });
    expect(await (await get(approved.srv.base, approved.actionId)).json()).toMatchObject({ state: "APPROVED", decided: { decision: "APPROVED", decidedAt } });
    await approved.srv.close();
    const rejected = await setup({ decided: { decision: "REJECTED", approverUserId: OTHER, decidedAt } });
    expect(((await (await get(rejected.srv.base, rejected.actionId)).json()) as { state: string }).state).toBe("REJECTED");
    await rejected.srv.close();
    const expired = await setup({ decided: { decision: "REJECTED", reason: "EXPIRED", code: "AGT-0208", approverUserId: OTHER, decidedAt } });
    const body = (await (await get(expired.srv.base, expired.actionId)).json()) as { state: string; decided: Record<string, unknown> };
    expect(body.state).toBe("EXPIRED");
    expect(body.decided).not.toHaveProperty("approverUserId");
  });

  it("treats a request requested in the future as pending", () => {
    expect(approvalStateOf({ requestedAtMs: NOW + HOUR_MS, invokerUserId: OTHER, approvalDuty: DUTY, amount: { amount: "1", currency: "USD" }, selfApprovalLimit: { amount: "0", currency: "USD" } }, NOW)).toBe("PENDING");
  });

  it("answers an unknown id, another tenant's id and a missing approval duty with the same 404", async () => {
    const { srv, actionId } = await setup();
    const unknown = await get(srv.base, UNKNOWN);
    const otherTenant = await get(srv.base, actionId, headers(DUTY, OTHER, "00000000-0000-7000-8000-000000000002"));
    const noDuty = await get(srv.base, actionId, headers("agents.run.read"));
    expect(unknown.status).toBe(404);
    const reference = (await unknown.json()) as { code: string; detail: string };
    expect(reference.code).toBe("AGT-0404");
    for (const res of [otherTenant, noDuty]) {
      expect(res.status).toBe(404);
      const body = (await res.json()) as { code: string; detail: string };
      expect({ code: body.code, detail: body.detail }).toEqual({ code: reference.code, detail: reference.detail });
    }
  });

  it("answers 401 without a token and 404 for an id that is not a UUID", async () => {
    const { srv, actionId } = await setup();
    expect((await fetch(`${srv.base}/api/v1/agents/approvals/${actionId}`)).status).toBe(401);
    expect((await get(srv.base, "not-a-uuid")).status).toBe(404);
  });

  it("answers 405 with allow GET for other methods", async () => {
    const { srv, actionId } = await setup();
    for (const method of ["POST", "PUT", "DELETE"]) {
      const res = await fetch(`${srv.base}/api/v1/agents/approvals/${actionId}`, { method, headers: headers(DUTY), ...(method === "DELETE" ? {} : { body: "{}" }) });
      expect(res.status, method).toBe(405);
      expect(res.headers.get("allow")).toBe("GET");
    }
  });

  it("does not change the request and never calls record", async () => {
    const { store, srv, actionId } = await setup();
    let records = 0;
    const record = store.record.bind(store);
    store.record = async (...args) => {
      records += 1;
      return record(...args);
    };
    const before = structuredClone(store.approvals.get(actionId));
    await get(srv.base, actionId);
    expect(records).toBe(0);
    expect(store.approvals.get(actionId)).toEqual(before);
  });

  it("answers 404 no such route when the reader is not wired", async () => {
    const store = new MemoryStore();
    const srv = await serve({ approvals: store });
    close = srv.close;
    expect((await get(srv.base, UNKNOWN)).status).toBe(404);
  });
});

describe("GET /api/v1/agents/approvals", () => {
  const open = (store: MemoryStore, runId: string, agentId: string, over: { duty?: string; ageHours?: number; expiryHours?: number } = {}) =>
    store
      .open({
        tenantId: T1,
        runId,
        seq: 1,
        tool: "depot.ar.settlement.propose",
        invokerUserId: OTHER,
        approvalDuty: over.duty ?? DUTY,
        amount: { amount: "10.00", currency: "USD" },
        selfApprovalLimit: { amount: "0", currency: "USD" },
        ...(over.expiryHours !== undefined ? { expiryHours: over.expiryHours } : {}),
        proposalJson: "{}",
      })
      .then((actionId) => {
        const request = store.approvals.get(actionId);
        if (request === undefined) throw new Error("no request");
        request.requestedAtMs = NOW - (over.ageHours ?? 1) * HOUR_MS;
        store.agentOfRun.set(runId, agentId);
        return actionId;
      });
  const list = (base: string, query = "", h: Record<string, string> = headers(DUTY)) => fetch(`${base}/api/v1/agents/approvals${query}`, { headers: h });
  const boot = async (store: MemoryStore, disabled: string[] = []) => {
    const srv = await serve({ approvals: store, approvalReader: store, disabledAgents: async () => new Set(disabled), now: () => new Date(NOW) });
    close = srv.close;
    return srv;
  };

  it("lists pending, unexpired requests the caller may decide, newest first, with agentId and expiresAt", async () => {
    const store = new MemoryStore();
    const older = await open(store, "00000000-0000-7000-8000-0000000000e1", "ap_coding", { ageHours: 5 });
    const newer = await open(store, "00000000-0000-7000-8000-0000000000e2", "cash_application", { ageHours: 1 });
    await open(store, "00000000-0000-7000-8000-0000000000e3", "ap_coding", { duty: "other.duty" });
    await open(store, "00000000-0000-7000-8000-0000000000e4", "ap_coding", { ageHours: 100 });
    const srv = await boot(store);
    const res = await list(srv.base);
    expect(res.status).toBe(200);
    const body = (await res.json()) as { items: Array<{ actionId: string; agentId: string; expiresAt: string; state: string }> };
    expect(body.items.map((i) => i.actionId)).toEqual([newer, older]);
    expect(body.items[0]).toMatchObject({ agentId: "cash_application", state: "PENDING", expiresAt: new Date(NOW - HOUR_MS + 72 * HOUR_MS).toISOString() });
  });

  it("leaves out requests of a disabled agent and honours limit", async () => {
    const store = new MemoryStore();
    await open(store, "00000000-0000-7000-8000-0000000000e1", "ap_coding");
    await open(store, "00000000-0000-7000-8000-0000000000e2", "cash_application");
    await open(store, "00000000-0000-7000-8000-0000000000e3", "cash_application");
    const srv = await boot(store, ["ap_coding"]);
    const all = (await (await list(srv.base)).json()) as { items: Array<{ agentId: string }> };
    expect(all.items.map((i) => i.agentId)).toEqual(["cash_application", "cash_application"]);
    const one = (await (await list(srv.base, "?limit=1")).json()) as { items: unknown[] };
    expect(one.items).toHaveLength(1);
  });

  it("answers 400 for a bad limit, 401 without a token, 405 for other methods and 404 when the reader is not wired", async () => {
    const srv = await boot(new MemoryStore());
    for (const bad of ["?limit=0", "?limit=201", "?limit=abc"]) expect((await list(srv.base, bad)).status, bad).toBe(400);
    expect((await fetch(`${srv.base}/api/v1/agents/approvals`)).status).toBe(401);
    const post = await fetch(`${srv.base}/api/v1/agents/approvals`, { method: "POST", headers: headers(DUTY), body: "{}" });
    expect(post.status).toBe(405);
    expect(post.headers.get("allow")).toBe("GET");
    await srv.close();
    const bare = await serve({ approvals: new MemoryStore() });
    close = bare.close;
    expect((await list(bare.base)).status).toBe(404);
  });
});
