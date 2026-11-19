import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { defaultCatalog } from "../src/agents/catalog.js";
import { createAgentDefinitionService } from "../src/api/agentDefinitions.js";
import { createBriefingService } from "../src/runtime/briefing.js";
import { PgAgentDefinitions } from "../src/store/agentDefinitions.js";
import { PgApprovalStore } from "../src/store/approvals.js";
import { PgBriefingFacts, PgBriefingStore } from "../src/store/briefing.js";
import { apAgent, makeRequest, usd } from "./fakes.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";
import { headers, MemoryStore, serve, T1 } from "./surface-fakes.js";

const TENANT_B = "00000000-0000-7000-8000-0000000000b2";
const USER = "00000000-0000-7000-8000-0000000000d1";
const DEFINITIONS = "/api/v1/agents/definitions";
const OPERATOR_DUTIES = "agents.admin,agents.invoke,agents.run.read,ap.dis.approve";

describe.runIf(ownerUrl)("agent definitions in Postgres", () => {
  let fx: Fixture;
  let store: PgAgentDefinitions;
  let srv: Awaited<ReturnType<typeof serve>>;
  let n = 0;

  beforeAll(async () => {
    fx = await fixture();
    store = new PgAgentDefinitions(fx.store);
    const disabledAgents = async (tenantId: string) => new Set((await store.disabledAgents(tenantId)).keys());
    srv = await serve({
      approvals: new MemoryStore(),
      definitions: createAgentDefinitionService({ agents: defaultCatalog("claude-sonnet-5-5"), store }),
      briefings: createBriefingService({ store: new PgBriefingStore(fx.store), facts: new PgBriefingFacts(fx.store), disabledAgents }),
    });
  });
  afterAll(async () => {
    await srv.close();
    await fx.close();
  });

  const call = (method: string, path: string, body?: unknown, user = USER, tenant = T1) =>
    fetch(`${srv.base}${path}`, { method, headers: headers(OPERATOR_DUTIES, user, tenant), ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
  const changes = async (tenantId: string) =>
    (await fx.owner.query("select agent_id, disabled, changed_by, reason, changed_at is not null as timed from agent_disabled_change where tenant_id = $1 order by change_seq", [tenantId])).rows;
  const seedPending = async (agentId: "ap_coding" | "cash_app") => {
    n += 1;
    const runId = `run-disabled-${n}`;
    await fx.store.startRun(makeRequest({ runId, tenantId: T1, invokerUserId: "someone-else", agent: { ...apAgent, agentId } }));
    return new PgApprovalStore(fx.store).open({
      tenantId: T1,
      runId,
      seq: 1,
      tool: "depot.ap.invoice.propose_coding",
      invokerUserId: "someone-else",
      approvalDuty: "ap.dis.approve",
      amount: usd("100.00"),
      selfApprovalLimit: usd("500.00"),
      proposalJson: "{}",
    });
  };

  it("disables an agent, lists it disabled and generates a briefing without its items that states it is disabled", async () => {
    const apAction = await seedPending("ap_coding");
    const cashAction = await seedPending("cash_app");
    const disabled = await call("POST", `${DEFINITIONS}/ap_coding:disable`, { reason: "audit hold" });
    expect(await disabled.json()).toMatchObject({ agentId: "ap_coding", enabled: false, disabledBy: USER, reason: "audit hold" });
    const list = (await (await call("GET", DEFINITIONS)).json()) as { items: Array<{ agentId: string; enabled: boolean }> };
    expect(list.items.filter((i) => !i.enabled).map((i) => i.agentId)).toEqual(["ap_coding"]);

    const { id } = (await (await call("POST", "/api/v1/agents/briefings:generate")).json()) as { id: string };
    let status = "";
    await vi.waitFor(async () => {
      status = ((await (await call("GET", `/api/v1/agents/briefings/${id}`)).json()) as { status: string }).status;
      expect(status).toBe("READY");
    });
    const briefing = (await (await call("GET", `/api/v1/agents/briefings/${id}`)).json()) as { recommendedActions: Array<{ actionId: string; agentId: string }>; notes: string[] };
    expect(briefing.recommendedActions.map((a) => a.actionId)).toEqual([cashAction]);
    expect(JSON.stringify(briefing)).not.toContain(apAction);
    expect(briefing.notes).toEqual(["ap_coding is disabled by your administrator"]);
  });

  it("writes one audit row with actor and time per real change and none for a repeat or a no-op enable", async () => {
    await call("POST", `${DEFINITIONS}/anomaly:enable`);
    await call("POST", `${DEFINITIONS}/anomaly:disable`);
    await call("POST", `${DEFINITIONS}/anomaly:disable`);
    await call("POST", `${DEFINITIONS}/anomaly:enable`);
    await call("POST", `${DEFINITIONS}/anomaly:enable`);
    expect((await changes(T1)).filter((r) => r.agent_id === "anomaly")).toEqual([
      { agent_id: "anomaly", disabled: true, changed_by: USER, reason: null, timed: true },
      { agent_id: "anomaly", disabled: false, changed_by: USER, reason: null, timed: true },
    ]);
  });

  it("keeps one tenant's disabled agents and change rows invisible to another under row-level security", async () => {
    await call("POST", `${DEFINITIONS}/mrp_triage:disable`);
    const other = (await (await call("GET", DEFINITIONS, undefined, USER, TENANT_B)).json()) as { items: Array<{ enabled: boolean }> };
    expect(other.items.every((i) => i.enabled)).toBe(true);
    expect((await store.disabledAgents(TENANT_B)).size).toBe(0);
    for (const table of ["agent_disabled", "agent_disabled_change"]) {
      expect((await fx.store.withTenant(TENANT_B, (c) => c.query(`select 1 from ${table} where tenant_id = $1`, [T1]))).rowCount).toBe(0);
    }
    expect(await changes(TENANT_B)).toEqual([]);
  });

  it("serializes concurrent disables into one flag row and one change row", async () => {
    await Promise.all([1, 2, 3, 4].map(() => call("POST", `${DEFINITIONS}/collections:disable`)));
    expect((await fx.owner.query("select count(*)::int as n from agent_disabled where tenant_id = $1 and agent_id = 'collections'", [T1])).rows[0].n).toBe(1);
    expect((await changes(T1)).filter((r) => r.agent_id === "collections")).toHaveLength(1);
  });
});
