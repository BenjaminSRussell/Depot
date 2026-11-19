import { cpSync, mkdtempSync, readdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import pg from "pg";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { buildActionRecord, type ActionRecord } from "../src/domain/audit.js";
import { scanPii } from "../src/domain/redaction.js";
import { PgActionStore } from "../src/store/actions.js";
import { PgApprovalStore } from "../src/store/approvals.js";
import { MIGRATIONS_DIR, migrate } from "../src/store/pg.js";
import { INVOKER, makeRequest, TENANT, usd } from "./fakes.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";

const OTHER_TENANT = "00000000-0000-7000-8000-0000000000bb";
const AT = "2026-11-18T12:00:00.000Z";

describe.runIf(ownerUrl)("action decline in Postgres", () => {
  let fx: Fixture;
  let actions: PgActionStore;
  let n = 0;

  beforeAll(async () => {
    fx = await fixture();
    actions = new PgActionStore(fx.store);
  });
  afterAll(async () => {
    await fx.close();
  });

  const seed = async (decision: ActionRecord["decision"] = "AUTO") => {
    n += 1;
    const runId = `run-decline-${n}`;
    await fx.store.startRun(makeRequest({ runId }));
    await fx.store.recordAction(
      TENANT,
      buildActionRecord({ runId, seq: 1, toolName: "depot.ap.invoice.propose_coding", inputRedacted: "{}", outputRedacted: "{}", approverUserId: null, decision, decidedAt: AT }),
    );
    const [row] = await fx.store.loadActions(TENANT, runId);
    return { runId, actionId: row!.actionId! };
  };

  const events = async (actionId: string) =>
    (await fx.owner.query<{ type: string; payload: { data: Record<string, string> } }>("select type, payload from outbox where aggregate_id = $1", [actionId])).rows;

  it("declines an AUTO action, stores reason, actor and time, and writes one outbox event without the reason", async () => {
    const { runId, actionId } = await seed();
    const stored = await actions.decline(TENANT, runId, actionId, { by: INVOKER, reason: "wrong vendor", at: AT });
    expect(stored?.declined).toEqual({ by: INVOKER, reason: "wrong vendor", at: AT });
    expect((await fx.store.loadActions(TENANT, runId))[0]?.declined?.reason).toBe("wrong vendor");
    const rows = await events(actionId);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.type).toBe("dev.depot.agent.action.declined.v1");
    expect(JSON.stringify(rows[0]?.payload)).not.toContain("wrong vendor");
  });

  it("replays without a second write or event", async () => {
    const { runId, actionId } = await seed();
    await actions.decline(TENANT, runId, actionId, { by: INVOKER, reason: null, at: AT });
    const again = await actions.decline(TENANT, runId, actionId, { by: "someone-else", reason: "later", at: "2026-11-19T00:00:00.000Z" });
    expect(again?.declined).toEqual({ by: INVOKER, reason: null, at: AT });
    expect(await events(actionId)).toHaveLength(1);
  });

  it("lets the first of two concurrent declines win, with one event", async () => {
    const { runId, actionId } = await seed();
    const [a, b] = await Promise.all([
      actions.decline(TENANT, runId, actionId, { by: "user-a", reason: null, at: AT }),
      actions.decline(TENANT, runId, actionId, { by: "user-b", reason: null, at: AT }),
    ]);
    expect(a?.declined).toEqual(b?.declined);
    expect(await events(actionId)).toHaveLength(1);
  });

  it("never finds or declines another tenant's action", async () => {
    const { runId, actionId } = await seed();
    expect(await actions.find(OTHER_TENANT, runId, actionId)).toBeUndefined();
    expect(await actions.decline(OTHER_TENANT, runId, actionId, { by: INVOKER, reason: null, at: AT })).toBeUndefined();
    expect((await actions.find(TENANT, runId, actionId))?.declined).toBeUndefined();
    expect(await events(actionId)).toHaveLength(0);
  });

  it("declines nothing but an AUTO action", async () => {
    const { runId, actionId } = await seed("DENIED");
    const stored = await actions.decline(TENANT, runId, actionId, { by: INVOKER, reason: null, at: AT });
    expect(stored?.declined).toBeUndefined();
    expect(await events(actionId)).toHaveLength(0);
  });

  it("lets the application role change only the decline columns", async () => {
    const { runId, actionId } = await seed();
    await expect(
      fx.store.withTenant(TENANT, (c) => c.query("update agent_action set decision = 'REJECTED' where tenant_id = $1 and run_id = $2 and action_id = $3", [TENANT, runId, actionId])),
    ).rejects.toThrow(/permission denied/);
    await expect(
      fx.store.withTenant(TENANT, (c) => c.query("update agent_action set declined_at = now() where tenant_id = $1 and run_id = $2 and action_id = $3", [TENANT, runId, actionId])),
    ).rejects.toThrow(/agent_action_decline_check/);
  });

  it("gives a gated action the id of its approval request and an ungated one a generated id", async () => {
    n += 1;
    const runId = `run-gated-${n}`;
    await fx.store.startRun(makeRequest({ runId }));
    const requestId = await new PgApprovalStore(fx.store).open({
      tenantId: TENANT, runId, seq: 1, tool: "depot.ap.invoice.propose_coding", invokerUserId: INVOKER, approvalDuty: "ap.invoice.approve", amount: usd("1.00"), selfApprovalLimit: usd("5.00"), proposalJson: "{}",
    });
    const record = (seq: number) =>
      buildActionRecord({ runId, seq, toolName: "depot.ap.invoice.propose_coding", inputRedacted: "{}", outputRedacted: "{}", approverUserId: null, decision: "AUTO", decidedAt: AT });
    await fx.store.recordAction(TENANT, record(1));
    await fx.store.recordAction(TENANT, record(2));
    const rows = await fx.store.loadActions(TENANT, runId);
    expect(rows[0]?.actionId).toBe(requestId);
    expect(rows[1]?.actionId).toMatch(/^[0-9a-f-]{36}$/);
    expect(rows[1]?.actionId).not.toBe(requestId);
  });
});

describe.runIf(ownerUrl)("guard result on agent_action", () => {
  it("round-trips the guard result and stores no value", async () => {
    const fx = await fixture();
    try {
      const runId = "run-guard-1";
      const guardResult = {
        callId: "call-1",
        highestClass: "P3" as const,
        routeId: "route-1",
        policyVersion: 1,
        counts: { IBAN: 1 },
        excluded: {},
        decision: "REDACTED" as const,
      };
      await fx.store.startRun(makeRequest({ runId }));
      await fx.store.recordAction(TENANT, buildActionRecord({ runId, seq: 1, toolName: "egress.guard", inputRedacted: "{}", outputRedacted: "{}", approverUserId: null, decision: "AUTO", decidedAt: AT, guardResult }));
      await fx.store.recordAction(TENANT, buildActionRecord({ runId, seq: 2, toolName: "depot.ap.invoice.get", inputRedacted: "{}", outputRedacted: "{}", approverUserId: null, decision: "AUTO", decidedAt: AT }));
      const [guarded, plain] = await fx.store.loadActions(TENANT, runId);
      expect(guarded?.guardResult).toEqual(guardResult);
      expect(plain?.guardResult).toBeUndefined();
      const stored = await fx.owner.query<{ guard_result: unknown }>("select guard_result from agent_action where run_id = $1 and guard_result is not null", [runId]);
      expect(scanPii(JSON.stringify(stored.rows))).toEqual([]);
    } finally {
      await fx.close();
    }
  });
});

describe.runIf(ownerUrl)("migration 0010 on existing rows", () => {
  it("keeps the approval request's action id on a gated action that existed before it", async () => {
    const dir = mkdtempSync(join(tmpdir(), "agents-migrations-"));
    const real = fileURLToPath(MIGRATIONS_DIR);
    for (const name of readdirSync(real).filter((f) => f < "0010")) cpSync(join(real, name), join(dir, name));
    const schema = `agents_mig_${Date.now().toString(36)}`;
    const admin = new pg.Pool({ connectionString: ownerUrl, max: 1 });
    await admin.query(`create schema ${schema}`);
    const owner = new pg.Pool({ connectionString: ownerUrl, options: `-c search_path=${schema}`, max: 2 });
    try {
      await migrate(owner, pathToFileURL(`${dir}/`));
      const tenant = TENANT;
      const c = await owner.connect();
      try {
        await c.query("select set_config('depot.tenant_id', $1, false)", [tenant]);
        await c.query("insert into agent_run (tenant_id, run_id, agent_id, agent_version, invoker_user_id, status, tokens_in, tokens_out, estimated) values ($1, 'r1', 'ap_coding', 1, 'u', 'RUNNING', 0, 0, false)", [tenant]);
        await c.query(
          `insert into approval_request (tenant_id, action_id, run_id, seq, tool_name, invoker_user_id, approval_duty, amount, currency, self_approval_limit, self_approval_currency, proposal_json)
           values ($1, '00000000-0000-7000-8000-0000000000e1', 'r1', 1, 't', 'u', 'd', '1', 'USD', '1', 'USD', '{}')`,
          [tenant],
        );
        const base = `insert into agent_action (tenant_id, run_id, seq, tool_name, input_json, output_hash, output_json, output_truncated, decision, decided_at) values ($1, 'r1', $2, 't', '{}', '${"0".repeat(64)}', '{}', false, 'AUTO', now())`;
        await c.query(base, [tenant, 1]);
        await c.query(base, [tenant, 2]);
        await migrate(owner);
        const rows = (await c.query<{ seq: number; action_id: string }>("select seq, action_id from agent_action order by seq")).rows;
        expect(rows[0]?.action_id).toBe("00000000-0000-7000-8000-0000000000e1");
        expect(rows[1]?.action_id).toMatch(/^[0-9a-f-]{36}$/);
        expect(rows[1]?.action_id).not.toBe(rows[0]?.action_id);
      } finally {
        c.release();
      }
    } finally {
      await owner.end();
      await admin.query(`drop schema ${schema} cascade`);
      await admin.end();
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
