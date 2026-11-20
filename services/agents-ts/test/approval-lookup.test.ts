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
