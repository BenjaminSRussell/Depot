import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { createAgentBudgetService, type BudgetList } from "../src/api/agentBudgets.js";
import { assertCanStartRun } from "../src/domain/budget.js";
import { PgAgentBudgets } from "../src/store/agentBudgets.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";
import type { Principal } from "../src/api/principal.js";

const MONTH = "2026-10";
const tenant = (n: number): string => `00000000-0000-7000-8000-${String(n).padStart(12, "0")}`;
const principal = (n: number, duties: string[]): Principal => ({ userId: `user-${n}`, tenantId: tenant(n), duties: new Set(duties) });
let holdN = 0;

describe.runIf(ownerUrl)("agent budgets in Postgres", () => {
  let db: Fixture;
  let budgets: PgAgentBudgets;
  const service = () => createAgentBudgetService({ store: budgets, now: () => new Date(`${MONTH}-15T12:00:00Z`) });
  const list = (n: number): Promise<BudgetList> => service().list(principal(n, ["agentbudget.read"]));
  const reserve = (n: number, tokens: number) =>
    db.store.reserve({ tenantId: tenant(n), agentId: "ap_coding", runId: `run-${(holdN += 1)}`, month: MONTH, tokensIn: tokens, tokensOut: tokens, ttlMs: 60_000 });
  const changes = async (n: number) =>
    (await db.owner.query("select agent_id, limit_in::int, limit_out::int, previous_limit_in::int, previous_limit_out::int, changed_by from agent_budget_change where tenant_id = $1 order by change_seq", [tenant(n)])).rows;

  beforeAll(async () => {
    db = await fixture();
    budgets = new PgAgentBudgets(db.store);
  });
  afterAll(() => db.close());

  it("reports a month with no budget row at the defaults, unused, without writing one", async () => {
    const out = await list(40);
    expect(out.tenant).toMatchObject({ cap: { tokensIn: 50_000_000, tokensOut: 10_000_000 }, tokensUsed: { tokensIn: 0, tokensOut: 0 }, status: "OK" });
    expect((await db.owner.query("select count(*)::int as n from token_budget where tenant_id = $1", [tenant(40)])).rows[0].n).toBe(0);
  });

  it("sums finished runs per agent for the month and ignores other months and agents", async () => {
    const t = tenant(41);
    await db.owner.query(
      `insert into agent_run (tenant_id, run_id, agent_id, agent_version, invoker_user_id, status, tokens_in, tokens_out, estimated, created_at) values
         ($1, 'a1', 'ap_coding', 1, 'u', 'COMMITTED', 100, 10, false, '2026-10-02T00:00:00Z'),
         ($1, 'a2', 'ap_coding', 1, 'u', 'COMMITTED', 50, 5, false, '2026-10-20T00:00:00Z'),
         ($1, 'a3', 'ap_coding', 1, 'u', 'COMMITTED', 999, 999, false, '2026-09-30T23:00:00Z'),
         ($1, 'a4', 'anomaly', 1, 'u', 'COMMITTED', 7, 1, false, '2026-10-05T00:00:00Z')`,
      [t],
    );
    const out = await list(41);
    expect(out.agents.find((a) => a.agentId === "ap_coding")?.tokensUsed).toEqual({ tokensIn: 150, tokensOut: 15 });
    expect(out.agents.find((a) => a.agentId === "anomaly")?.tokensUsed).toEqual({ tokensIn: 7, tokensOut: 1 });
    expect(out.agents.find((a) => a.agentId === "cash_app")?.tokensUsed).toEqual({ tokensIn: 0, tokensOut: 0 });
  });

  it("stores a cap, shows it on the next read and audits each change with the previous cap and the caller", async () => {
    const writer = principal(42, ["agentbudget.write"]);
    await service().setCap(writer, "cash_app", { limitIn: 500, limitOut: 400 });
    await service().setCap(writer, "cash_app", { limitIn: 900, limitOut: 800 });
    expect((await list(42)).agents.find((a) => a.agentId === "cash_app")?.cap).toEqual({ tokensIn: 900, tokensOut: 800 });
    expect(await changes(42)).toEqual([
      { agent_id: "cash_app", limit_in: 500, limit_out: 400, previous_limit_in: null, previous_limit_out: null, changed_by: "user-42" },
      { agent_id: "cash_app", limit_in: 900, limit_out: 800, previous_limit_in: 500, previous_limit_out: 400, changed_by: "user-42" },
    ]);
  });

  it("keeps one tenant's caps and change rows invisible to another under row-level security", async () => {
    await service().setCap(principal(43, ["agentbudget.write"]), "anomaly", { limitIn: 5, limitOut: 5 });
    expect((await list(44)).agents.find((a) => a.agentId === "anomaly")?.cap).toEqual({ tokensIn: 50_000_000, tokensOut: 10_000_000 });
    const seen = await db.store.withTenant(tenant(44), (c) => c.query("select 1 from agent_budget_change where tenant_id = $1", [tenant(43)]));
    expect(seen.rowCount).toBe(0);
    await expect(db.store.withTenant(tenant(44), (c) => c.query("update agent_budget_change set limit_in = 0"))).rejects.toThrow();
  });

  it("serializes concurrent caps so every change records the cap it replaced", async () => {
    const writer = principal(45, ["agentbudget.write"]);
    await Promise.all([1, 2, 3, 4].map((n) => service().setCap(writer, "mrp_triage", { limitIn: n, limitOut: n })));
    const rows = await changes(45);
    expect(rows).toHaveLength(4);
    expect(rows[0]).toMatchObject({ previous_limit_in: null });
    rows.slice(1).forEach((row, i) => expect(row.previous_limit_in).toBe(rows[i]?.limit_in));
  });

  it("goes SOFT then HARD with one outbox event per crossing and none on a repeat, and HARD is the state reserve refuses with AGT-0202", async () => {
    const n = 46;
    const t = tenant(n);
    await db.store.setBudget(t, MONTH, { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    const events = async () => (await db.owner.query("select payload->'data'->>'state' as state from outbox where tenant_id = $1 and type like '%budget.threshold%' order by created_at, id", [t])).rows.map((r) => r.state);

    await db.store.settle(await reserve(n, 790), { tokensIn: 790, tokensOut: 0 });
    expect((await list(n)).tenant).toMatchObject({ status: "OK", percentConsumed: 79 });
    expect(await events()).toEqual([]);

    await db.store.settle(await reserve(n, 10), { tokensIn: 10, tokensOut: 0 });
    expect((await list(n)).tenant).toMatchObject({ status: "SOFT", percentConsumed: 80 });
    await db.store.settle(await reserve(n, 10), { tokensIn: 10, tokensOut: 0 });
    expect(await events()).toEqual(["SOFT"]);

    await db.store.settle(await reserve(n, 190), { tokensIn: 190, tokensOut: 0 });
    expect((await list(n)).tenant).toMatchObject({ status: "HARD", percentConsumed: 100 });
    expect(await events()).toEqual(["SOFT", "HARD"]);
    await expect(reserve(n, 1)).rejects.toMatchObject({ code: "AGT-0202" });
    expect(() => assertCanStartRun({ limitIn: 1_000, limitOut: 1_000, usedIn: 1_000, usedOut: 0, softPct: 80 })).toThrowError(expect.objectContaining({ code: "AGT-0202" }));
    expect(await events()).toEqual(["SOFT", "HARD"]);
  });

  it("returns to OK after the cap is raised and alerts again on the next crossing", async () => {
    const n = 47;
    const t = tenant(n);
    const states = async () => (await db.owner.query("select payload->'data'->>'state' as state from outbox where tenant_id = $1 and type like '%budget.threshold%' order by created_at, id", [t])).rows.map((r) => r.state);
    await db.store.setBudget(t, MONTH, { limitIn: 1_000, limitOut: 1_000, softPct: 80 });
    await db.store.settle(await reserve(n, 1_000), { tokensIn: 1_000, tokensOut: 0 });
    expect((await list(n)).tenant.status).toBe("HARD");
    await db.store.setBudget(t, MONTH, { limitIn: 10_000, limitOut: 10_000, softPct: 80 });
    expect((await list(n)).tenant.status).toBe("OK");
    await db.store.settle(await reserve(n, 8_000), { tokensIn: 8_000, tokensOut: 0 });
    expect((await list(n)).tenant.status).toBe("SOFT");
    await db.store.settle(await reserve(n, 1_000), { tokensIn: 1_000, tokensOut: 0 });
    expect(await states()).toEqual(["HARD", "SOFT", "HARD"]);
  });
});
