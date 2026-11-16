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

  it("is tenant-isolated", async () => {
    await state.create(TENANT, "r-iso", {}, {});
    expect(await state.load(OTHER, "r-iso")).toBeUndefined();
    await state.putCredential(TENANT, "r-iso", { kind: "starter", userId: INVOKER, duties: ["a"], token: "t" }, Date.now() + 60_000);
    expect(await state.credential(OTHER, "r-iso", "starter")).toBeUndefined();
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
