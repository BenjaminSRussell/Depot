import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { buildActionRecord } from "../src/domain/audit.js";
import { Pseudonymizer } from "../src/domain/redaction.js";
import { PgApprovalStore } from "../src/store/approvals.js";
import { PgRunStateStore, tokenExpiryMs } from "../src/store/runstate.js";
import { INVOKER, makeRequest, TENANT, usd } from "./fakes.js";
import { fixture, ownerUrl, type Fixture } from "./pgHelper.js";

const OTHER = "00000000-0000-7000-8000-0000000000bb";
const iban = "DE89370400440532013000";

describe.runIf(ownerUrl)("resumable run state in Postgres", () => {
  let fx: Fixture;
  let state: PgRunStateStore;
  let n = 0;

  beforeAll(async () => {
    fx = await fixture();
    state = new PgRunStateStore(fx.store);
  });
  afterAll(async () => {
    await fx.close();
  });

  const action = (runId: string, seq: number, why: string) =>
    buildActionRecord({ runId, seq, toolName: "t", inputRedacted: "{}", outputRedacted: "{}", approverUserId: null, decision: "DENIED", decidedAt: "2026-10-21T00:00:00.000Z", reason: why });

  it("stores the input, the step state and the pseudonym map, and a new reader gets them back so values restore", async () => {
    const pseudo = new Pseudonymizer(TENANT, 86_400_000, () => Date.UTC(2026, 9, 21));
    const redacted = pseudo.redact(`pay ${iban}`);
    await state.create(TENANT, "r-state", { prompt: `pay ${iban}` }, { phase: "created" });
    await state.save(TENANT, "r-state", { phase: "awaiting" }, pseudo.snapshot());

    const row = await state.load<{ prompt: string }, { phase: string }>(TENANT, "r-state");
    expect(row?.state.phase).toBe("awaiting");
    const resumed = new Pseudonymizer(TENANT, 86_400_000, () => Date.UTC(2026, 9, 21));
    resumed.load(row!.pseudonyms);
    expect(resumed.restore(redacted)).toContain(iban);
    expect(resumed.redact(`again ${iban}`)).toContain("[IBAN-1]"); // the same value keeps its pseudonym
  });

  it("is tenant-isolated by row-level security, not by the queries' own tenant filter", async () => {
    await state.create(TENANT, "r-iso", {}, {});
    await state.putCredential(TENANT, "r-iso", { kind: "starter", userId: INVOKER, duties: ["a"], token: "t" }, Date.now() + 60_000);
    // Queries with no tenant filter at all, as the application role (passage_app when configured): another tenant sees nothing.
    const seen = await fx.store.withTenant(OTHER, async (c) => [
      (await c.query("select * from agent_run_state")).rowCount,
      (await c.query("select * from agent_run_credential")).rowCount,
    ]);
    expect(seen).toEqual([0, 0]);
    await expect(
      fx.store.withTenant(OTHER, (c) => c.query("insert into agent_run_credential (tenant_id, run_id, kind, user_id, duties, token, expires_at) values ($1, 'r-iso', 'approver', 'u', '', 't', now())", [TENANT])),
    ).rejects.toThrow(/row-level security/);
    await expect(fx.store.withTenant(OTHER, (c) => c.query("delete from agent_run_state"))).resolves.toMatchObject({ rowCount: 0 });
    expect(await state.load(TENANT, "r-iso")).toBeDefined();
  });

  it("scrub empties the prompt and the pseudonym map, discard removes the run, sweep removes expired credentials and finished state past retention", async () => {
    const pseudo = new Pseudonymizer(TENANT, 86_400_000, () => Date.UTC(2026, 9, 21));
    pseudo.redact(`pay ${iban}`);
    await state.create(TENANT, "r-scrub", { prompt: `pay ${iban}`, other: 1 }, { phase: "begun" });
    await state.save(TENANT, "r-scrub", { phase: "begun" }, pseudo.snapshot());
    await state.scrub(TENANT, "r-scrub");
    const row = (await state.load<{ prompt: string; other: number }, unknown>(TENANT, "r-scrub"))!;
    expect(row.input).toEqual({ prompt: "", other: 1 });
    expect(row.pseudonyms.entries).toEqual([]);

    await state.putCredential(TENANT, "r-scrub", { kind: "starter", userId: INVOKER, duties: [], token: "t" }, Date.now() - 1000);
    await state.create(TENANT, "r-old", {}, { phase: "done" });
    await state.create(TENANT, "r-live", {}, { phase: "awaiting" });
    await fx.owner.query("update agent_run_state set updated_at = now() - interval '2 days' where tenant_id = $1 and run_id in ('r-old', 'r-live')", [TENANT]);
    expect(await state.sweep(TENANT, 24 * 3_600_000)).toBe(2); // the expired credential and the old finished state
    expect(await state.load(TENANT, "r-old")).toBeUndefined();
    expect(await state.load(TENANT, "r-live")).toBeDefined(); // a run still waiting keeps its state
    await state.discard(TENANT, "r-live");
    expect(await state.load(TENANT, "r-live")).toBeUndefined();
  });

  it("reserve is idempotent per run and a finish that already landed is success", async () => {
    n += 1;
    const runId = `r-hold-${n}`;
    await fx.store.startRun(makeRequest({ runId }));
    const ask = { tenantId: TENANT, agentId: "ap_coding", runId, month: "2026-10", tokensIn: 1000, tokensOut: 1000, ttlMs: 60_000 };
    const first = await fx.store.reserve(ask);
    expect(await fx.store.reserve(ask)).toEqual(first);
    const holds = await fx.owner.query("select count(*)::int as n from budget_hold where run_id = $1", [runId]);
    expect(holds.rows[0].n).toBe(1);
    const result = { runId, status: "FAILED" as const, actions: [], tokensIn: 0, tokensOut: 0, estimated: false, budgetState: "OK" as const, displayText: "" };
    await fx.store.finishRun(makeRequest({ runId }), result);
    await expect(fx.store.finishRun(makeRequest({ runId }), result)).resolves.toBeUndefined();
    await expect(fx.store.finishRun(makeRequest({ runId: "never-started" }), { ...result, runId: "never-started" })).rejects.toThrow(/not open/);
  });

  it("a credential is read only while unexpired, per kind, and is purged with the run", async () => {
    await state.create(TENANT, "r-cred", {}, {});
    await state.putCredential(TENANT, "r-cred", { kind: "starter", userId: INVOKER, duties: ["a", "b"], token: "tok" }, Date.now() + 60_000);
    await state.putCredential(TENANT, "r-cred", { kind: "approver", userId: OTHER, duties: [], token: "old" }, Date.now() - 1000);
    expect(await state.credential(TENANT, "r-cred", "starter")).toEqual({ kind: "starter", userId: INVOKER, duties: ["a", "b"], token: "tok" });
    expect(await state.credential(TENANT, "r-cred", "approver")).toBeUndefined();
    await state.putCredential(TENANT, "r-cred", { kind: "approver", userId: OTHER, duties: [], token: "new" }, Date.now() + 60_000);
    expect((await state.credential(TENANT, "r-cred", "approver"))?.token).toBe("new");
    await state.purgeCredentials(TENANT, "r-cred");
    expect(await state.credential(TENANT, "r-cred", "starter")).toBeUndefined();
  });

  it("recordAction is idempotent per seq and finishRun keeps rows already recorded", async () => {
    n += 1;
    const runId = `r-act-${n}`;
    await fx.store.startRun(makeRequest({ runId }));
    await fx.store.recordAction(TENANT, action(runId, 1, "first"));
    await fx.store.recordAction(TENANT, action(runId, 1, "retry"));
    await fx.store.finishRun(makeRequest({ runId }), { runId, status: "FAILED", actions: [action(runId, 1, "again")], tokensIn: 0, tokensOut: 0, estimated: false, budgetState: "OK", displayText: "" });
    const rows = await fx.store.loadActions(TENANT, runId);
    expect(rows).toHaveLength(1);
    expect(rows[0]?.reason).toBe("first");
  });

  it("opening an approval twice for the same run and seq returns the first request", async () => {
    n += 1;
    const runId = `r-open-${n}`;
    await fx.store.startRun(makeRequest({ runId }));
    const approvals = new PgApprovalStore(fx.store);
    const base = { tenantId: TENANT, runId, seq: 1, tool: "t", invokerUserId: INVOKER, approvalDuty: "d", amount: usd("1.00"), selfApprovalLimit: usd("5.00"), proposalJson: "{}" };
    const first = await approvals.open(base);
    expect(await approvals.open(base)).toBe(first);
  });

  it("reads the expiry from a token's exp claim", () => {
    const body = Buffer.from(JSON.stringify({ exp: 1_800_000_000 })).toString("base64url");
    expect(tokenExpiryMs(`h.${body}.s`)).toBe(1_800_000_000_000);
    expect(tokenExpiryMs("garbage")).toBeUndefined();
  });
});
