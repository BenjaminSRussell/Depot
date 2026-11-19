import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { decodeCursor, type Cursor } from "../src/domain/briefing.js";
import { createBriefingService } from "../src/runtime/briefing.js";
import { PgApprovalStore } from "../src/store/approvals.js";
import { PgBriefingFacts, PgBriefingStore } from "../src/store/briefing.js";
import { apAgent, makeRequest, usd } from "./fakes.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";

const TENANT_A = "00000000-0000-7000-8000-0000000000a1";
const TENANT_B = "00000000-0000-7000-8000-0000000000b1";
const USER_A = "user-a";
const USER_B = "user-b";
const DUTIES_A = ["agents.invoke", "agents.run.read", "ap.a.approve"];

describe.runIf(ownerUrl)("briefings in Postgres", () => {
  let fx: Fixture;
  let approvals: PgApprovalStore;
  let store: PgBriefingStore;
  let facts: PgBriefingFacts;
  let n = 0;

  beforeAll(async () => {
    fx = await fixture();
    approvals = new PgApprovalStore(fx.store);
    store = new PgBriefingStore(fx.store);
    facts = new PgBriefingFacts(fx.store);
  });
  afterAll(async () => {
    await fx.close();
  });

  const count = async (table: string): Promise<number> => Number((await fx.owner.query(`select count(*) as n from ${table}`)).rows[0].n);

  const seed = async (over: { tenantId?: string; user: string; duty: string; agentId?: string }) => {
    n += 1;
    const runId = `run-briefing-${n}`;
    const tenantId = over.tenantId ?? TENANT_A;
    await fx.store.startRun(makeRequest({ runId, tenantId, invokerUserId: over.user, agent: { ...apAgent, agentId: (over.agentId ?? "ap_coding") as typeof apAgent.agentId } }));
    const actionId = await approvals.open({
      tenantId,
      runId,
      seq: 1,
      tool: "depot.ap.invoice.propose_coding",
      invokerUserId: over.user,
      approvalDuty: over.duty,
      amount: usd("100.00"),
      selfApprovalLimit: usd("500.00"),
      proposalJson: "{}",
    });
    return { runId, actionId };
  };

  const readFacts = (user: string, duties: string[], tenantId = TENANT_A) => facts.read(tenantId, user, duties, new Date());

  it("keeps user B's proposals, decisions and runs out of user A's facts (two-persona contract)", async () => {
    const mine = await seed({ user: USER_A, duty: "ap.a.approve" });
    const theirs = await seed({ user: USER_B, duty: "ap.b.approve" });
    const decided = await seed({ user: USER_B, duty: "ap.b.approve" });
    await approvals.record(TENANT_A, decided.runId, decided.actionId, { decision: "APPROVED", approverUserId: USER_B, decidedAt: new Date().toISOString() });
    await fx.owner.query("update agent_run set status = 'FAILED' where run_id = $1", [theirs.runId]);
    const asA = await readFacts(USER_A, DUTIES_A);
    expect(asA.pending.map((p) => p.actionId)).toContain(mine.actionId);
    expect(JSON.stringify(asA)).not.toMatch(new RegExp(`${theirs.actionId}|${decided.actionId}|${theirs.runId}`));
    const asB = await readFacts(USER_B, ["ap.b.approve"]);
    expect(asB.pending.map((p) => p.actionId)).toEqual(expect.arrayContaining([theirs.actionId]));
    expect(JSON.stringify(asB)).not.toContain(mine.actionId);
  });

  it("includes an item the user may decide through the approval duty, and excludes copilot runs and other tenants", async () => {
    const viaDuty = await seed({ user: USER_B, duty: "ap.a.approve" });
    const copilot = await seed({ user: USER_A, duty: "ap.a.approve", agentId: "copilot" });
    const foreign = await seed({ tenantId: TENANT_B, user: USER_A, duty: "ap.a.approve" });
    const asA = (await readFacts(USER_A, DUTIES_A)).pending.map((p) => p.actionId);
    expect(asA).toContain(viaDuty.actionId);
    expect(asA).not.toContain(copilot.actionId);
    expect(asA).not.toContain(foreign.actionId);
  });

  it("generates a READY briefing whose actions exist and changes no approval or action row", async () => {
    const mine = await seed({ user: "user-gen", duty: "ap.gen.approve" });
    const before = [await count("approval_request"), await count("agent_action")];
    const briefings = createBriefingService({ store, facts });
    const principal = { userId: "user-gen", tenantId: TENANT_A, duties: new Set(["agents.invoke"]) };
    const { id } = await briefings.generate(principal);
    await vi.waitFor(async () => expect((await briefings.get(principal, id))?.status).toBe("READY"));
    const stored = await briefings.get(principal, id);
    expect(stored?.narrative).toBe(false);
    expect(stored?.content?.recommendedActions.map((a) => a.actionId)).toEqual([mine.actionId]);
    expect(stored?.content?.recommendedActions[0]?.route).toBe(`/assistant/proposals/${mine.actionId}`);
    expect([await count("approval_request"), await count("agent_action")]).toEqual(before);
  });

  it("answers 429 with Retry-After on the 4th generation in an hour, per user and tenant", async () => {
    for (let i = 0; i < 3; i += 1) {
      await store.fail(TENANT_A, await store.create(TENANT_A, "user-rate"));
    }
    await expect(store.create(TENANT_A, "user-rate")).rejects.toMatchObject({ status: 429, code: "AGT-0230", headers: { "retry-after": expect.stringMatching(/^[1-9][0-9]*$/) } });
    expect(await store.create(TENANT_A, "user-rate-other")).toEqual(expect.any(String));
    expect(await store.create(TENANT_B, "user-rate")).toEqual(expect.any(String));
  });

  it("answers 409 with the in-flight id and lets another user of the tenant start", async () => {
    const first = await store.create(TENANT_A, "user-flight");
    await expect(store.create(TENANT_A, "user-flight")).rejects.toMatchObject({ status: 409, code: "AGT-0231", members: { briefingId: first } });
    expect(await store.create(TENANT_A, "user-flight-other")).toEqual(expect.any(String));
  });

  it("sweeps a RUNNING row older than ten minutes and accepts a new generation", async () => {
    const stale = await store.create(TENANT_A, "user-stale");
    await fx.owner.query("update agent_briefing set created_at = now() - interval '11 minutes' where id = $1", [stale]);
    const fresh = await store.create(TENANT_A, "user-stale");
    expect((await store.get(TENANT_A, "user-stale", stale))?.status).toBe("FAILED");
    expect((await store.get(TENANT_A, "user-stale", fresh))?.status).toBe("RUNNING");
  });

  it("lists nothing for a user with no briefings, and pages equal-second rows newest first without gaps", async () => {
    expect(await store.list(TENANT_A, "user-empty", 20, undefined)).toEqual({ items: [], nextCursor: null });
    const ids: string[] = [];
    for (let i = 0; i < 5; i += 1) {
      const id = await store.create(TENANT_A, "user-page");
      await store.fail(TENANT_A, id);
      await fx.owner.query("update agent_briefing set created_at = date_trunc('second', now()) - interval '2 hours' where id = $1", [id]);
      ids.push(id);
    }
    const seen: string[] = [];
    let cursor: Cursor | undefined;
    for (let guard = 0; guard < 5; guard += 1) {
      const page = await store.list(TENANT_A, "user-page", 2, cursor);
      seen.push(...page.items.map((b) => b.id));
      if (page.nextCursor === null) break;
      cursor = decodeCursor(page.nextCursor);
    }
    expect([...seen].sort()).toEqual([...ids].sort());
    expect(seen).toEqual([...seen].sort().reverse());
    expect((await store.list(TENANT_A, "user-other", 20, undefined)).items).toEqual([]);
  });

  it("hides another user's or tenant's briefing and marks read for the owner only, keeping the first read_at", async () => {
    const id = await store.create(TENANT_A, "user-read");
    expect(await store.get(TENANT_A, "user-other", id)).toBeUndefined();
    expect(await store.get(TENANT_B, "user-read", id)).toBeUndefined();
    expect(await store.markRead(TENANT_A, "user-other", id)).toBeUndefined();
    expect((await store.get(TENANT_A, "user-read", id))?.readAt).toBeNull();
    const first = await store.markRead(TENANT_A, "user-read", id);
    expect(first?.readAt).toEqual(expect.any(String));
    await new Promise((r) => setTimeout(r, 5));
    expect((await store.markRead(TENANT_A, "user-read", id))?.readAt).toBe(first?.readAt);
    expect((await store.get(TENANT_A, "user-read", id))?.readAt).toBe(first?.readAt);
  });
});
