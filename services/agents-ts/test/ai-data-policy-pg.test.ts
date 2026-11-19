import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { handleGetAiDataPolicy, handlePutAiDataPolicy } from "../src/api/aiDataPolicy.js";
import type { Principal } from "../src/api/principal.js";
import { PgAiDataPolicyStore } from "../src/store/aiDataPolicy.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";

const USER = "00000000-0000-7000-8000-0000000000e1";
const NOW = () => new Date("2026-11-18T10:00:00.000Z");
const WRITER_DUTIES = ["aipolicy.read", "aipolicy.write", "tenant.administer"];

let tenantSeq = 0;
const nextTenant = (): string => `00000000-0000-7000-8000-${(0x700 + ++tenantSeq).toString(16).padStart(12, "0")}`;
const admin = (tenantId: string): Principal => ({ userId: USER, tenantId, duties: new Set(WRITER_DUTIES) });

describe.runIf(ownerUrl)("tenant AI data policy in Postgres", () => {
  let fx: Fixture;
  let policies: PgAiDataPolicyStore;

  beforeAll(async () => {
    fx = await fixture();
    policies = new PgAiDataPolicyStore(fx.store);
  });
  afterAll(async () => {
    await fx.close();
  });

  const put = (tenantId: string, body: unknown, ifMatch: string) =>
    handlePutAiDataPolicy({ principal: admin(tenantId), ifMatch, body, store: policies, now: NOW });

  const count = async (table: string, tenantId: string): Promise<number> =>
    Number((await fx.owner.query(`select count(*) from ${table} where tenant_id = $1`, [tenantId])).rows[0].count);

  it("gives a tenant with no row the default at version 1, P3 and CUI denied everywhere", async () => {
    const policy = await handleGetAiDataPolicy({ principal: admin(nextTenant()), store: policies });
    expect(policy.version).toBe(1);
    for (const cells of Object.values(policy.modules)) expect(cells).toMatchObject({ P2: false, P3: false, CUI: false });
  });

  it("stores version 2 with before, after, actor and reason, and one outbox event", async () => {
    const t = nextTenant();
    const out = await put(t, { modules: { ap: { P2: true } }, reason: "pilot" }, '"1"');
    expect(out.version).toBe(2);
    const row = (await fx.owner.query("select * from ai_data_policy_change where tenant_id = $1", [t])).rows[0];
    expect(row).toMatchObject({ version: 2, by_user: USER, loosening: true, reason: "pilot", legal_entity_id: null });
    expect(row.before_modules.ap.P2).toBe(false);
    expect(row.after_modules.ap.P2).toBe(true);
    const events = (await fx.owner.query("select type, payload from outbox where tenant_id = $1", [t])).rows;
    expect(events).toHaveLength(1);
    expect(events[0].type).toBe("dev.depot.agent.aipolicy.changed.v1");
    expect(events[0].payload.data).toEqual({
      tenant_id: t,
      legal_entity_id: null,
      version: 2,
      loosening: true,
      by_user: USER,
      changed_at: "2026-11-18T10:00:00.000Z",
    });
    expect((await policies.read(t)).modules.ap.P2).toBe(true);
  });

  it("stores nothing and publishes nothing for a replayed patch", async () => {
    const t = nextTenant();
    await put(t, { modules: { ap: { P2: true } } }, '"1"');
    const again = await put(t, { modules: { ap: { P2: true } } }, '"2"');
    expect(again.version).toBe(2);
    expect(await count("ai_data_policy_change", t)).toBe(1);
    expect(await count("outbox", t)).toBe(1);
  });

  it("marks tightening as not loosening", async () => {
    const t = nextTenant();
    await put(t, { modules: { ap: { P2: true } } }, '"1"');
    await put(t, { modules: { ap: { P2: false } } }, '"2"');
    const rows = (await fx.owner.query("select version, loosening from ai_data_policy_change where tenant_id = $1 order by version", [t])).rows;
    expect(rows.map((r) => r.loosening)).toEqual([true, false]);
  });

  it("refuses a stale If-Match with 412 and a future one too", async () => {
    const t = nextTenant();
    await put(t, { modules: { ap: { P2: true } } }, '"1"');
    await expect(put(t, { modules: { ar: { P2: true } } }, '"1"')).rejects.toMatchObject({ status: 412, code: "AGT-0412" });
    await expect(put(t, { modules: { ar: { P2: true } } }, '"7"')).rejects.toMatchObject({ status: 412 });
    expect(await count("ai_data_policy_change", t)).toBe(1);
  });

  it("lets one of two concurrent writers on the same version win and answers the other 412", async () => {
    const t = nextTenant();
    const results = await Promise.allSettled([put(t, { modules: { ap: { P2: true } } }, '"1"'), put(t, { modules: { ar: { P2: true } } }, '"1"')]);
    expect(results.filter((r) => r.status === "fulfilled")).toHaveLength(1);
    const lost = results.find((r) => r.status === "rejected") as PromiseRejectedResult;
    expect(lost.reason).toMatchObject({ status: 412 });
    expect(await count("ai_data_policy_change", t)).toBe(1);
    expect(await count("outbox", t)).toBe(1);
  });

  it("refuses allowing P3 with nothing stored", async () => {
    const t = nextTenant();
    await expect(put(t, { modules: { ap: { P3: true } } }, '"1"')).rejects.toMatchObject({ code: "AGT-0217" });
    expect(await count("ai_data_policy_change", t)).toBe(0);
    expect(await count("outbox", t)).toBe(0);
  });

  it("keeps one tenant's policy and rows from another", async () => {
    const a = nextTenant();
    const b = nextTenant();
    await put(a, { modules: { ap: { P2: true } } }, '"1"');
    await put(b, { modules: { ar: { P2: true } } }, '"1"');
    expect((await policies.read(a)).modules).toMatchObject({ ap: { P2: true }, ar: { P2: false } });
    expect((await policies.read(b)).modules).toMatchObject({ ap: { P2: false }, ar: { P2: true } });
    const seenByA = await fx.store.withTenant(a, async (c) => (await c.query("select tenant_id from ai_data_policy_change")).rows);
    expect(seenByA.every((r) => r.tenant_id === a)).toBe(true);
    expect(seenByA).toHaveLength(1);
  });
});
