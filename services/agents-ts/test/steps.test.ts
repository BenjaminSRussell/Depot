import { describe, expect, it } from "vitest";
import type { ApprovalRequest, RecordedDecision } from "../src/api/decision.js";
import type { ActionRecord } from "../src/domain/audit.js";
import type { McpConnector } from "../src/runtime/mcp.js";
import type { RunRequest } from "../src/runtime/engine.js";
import { createRunSteps, initialStepState, type RunInput, type StepAudit, type StepsDeps } from "../src/runtime/steps.js";
import type { RunCredential } from "../src/store/runstate.js";
import { MemState } from "./memstate.js";
import { apAgent, APPROVER, codingDescriptor, FakeTools, INVOKER, postDescriptor, proposal, REAL_IBAN, ScriptedProvider, TENANT, usd } from "./fakes.js";

const RUN = "run-1";
const CODING = "passage.ap.invoice.propose_coding";

/** Behaves like PgApprovalStore: one request per (run, seq) that keeps the FIRST proposal, ids per request, first decision wins. */
class MemInbox {
  decided: RecordedDecision | undefined;
  opened: Array<{ seq: number; approvalDuty: string; proposalJson: string; proposalHash?: string; actionId: string }> = [];
  requestedAtMs = Date.UTC(2026, 9, 21);
  async open(a: { seq: number; approvalDuty: string; proposalJson: string; proposalHash?: string }) {
    const ex = this.opened.find((o) => o.seq === a.seq);
    if (ex) return ex.actionId;
    this.decided = undefined;
    const actionId = `action-${this.opened.length + 1}`;
    this.opened.push({ ...a, actionId });
    return actionId;
  }
  async find(): Promise<ApprovalRequest | undefined> {
    const last = this.opened.at(-1);
    return { invokerUserId: INVOKER, requestedAtMs: this.requestedAtMs, approvalDuty: "APInvoicePost", amount: usd("1200"), selfApprovalLimit: usd("1000"), ...(last?.proposalHash ? { proposalHash: last.proposalHash } : {}), ...(this.decided ? { decided: this.decided } : {}) };
  }
  async record(_t: string, _r: string, _a: string, d: RecordedDecision) {
    this.decided ??= d;
    return this.decided;
  }
}

/** Fault injection: `fail.x = n` makes the next n calls of x throw, as a lost connection or a restart would. */
type Faults = Partial<Record<"connect" | "startRun" | "finishRunAfter" | "saveCalled" | "saveAwaiting" | "saveDone" | "reserveAfter", number>>;

function setup(over: Partial<{ provider: ScriptedProvider; tools: FakeTools; halted: { on: boolean }; nowMs: { ms: number } }> = {}) {
  const state = new MemState();
  const inbox = new MemInbox();
  const rows: ActionRecord[] = [];
  const finished: Array<{ status: string; code?: string | undefined }> = [];
  const fail: Faults = {};
  const trip = (k: keyof Faults): void => {
    if ((fail[k] ?? 0) > 0) {
      fail[k] = (fail[k] ?? 0) - 1;
      throw new Error(`injected failure: ${k}`);
    }
  };
  const origSave = state.save.bind(state);
  state.save = async (t, r, st, p) => {
    const phase = (st as { phase?: string }).phase;
    const out = await origSave(t, r, st, p);
    // The write lands and then the connection drops, the worst case for a retry.
    if (phase === "called") trip("saveCalled");
    if (phase === "awaiting") trip("saveAwaiting");
    if (phase === "done") trip("saveDone");
    return out;
  };
  // Like PgAgentStore: one hold per run, settle drops the hold and a second settle charges nothing.
  const holds = new Map<string, { holdId: string; tenantId: string; month: string }>();
  const live = new Set<string>();
  const charged = { in: 0, out: 0 };
  let reserves = 0;
  const finishedRuns = new Set<string>();
  const audit: StepAudit = {
    reserve: async (r) => {
      const ex = holds.get(r.runId);
      if (ex) return ex;
      reserves += 1;
      const hold = { holdId: `hold-${reserves}`, tenantId: r.tenantId, month: r.month };
      holds.set(r.runId, hold);
      live.add(hold.holdId);
      trip("reserveAfter");
      return hold;
    },
    settle: async (h, u) => {
      if (live.delete(h.holdId)) {
        charged.in += u.tokensIn;
        charged.out += u.tokensOut;
      }
      return "OK";
    },
    startRun: async () => {
      trip("startRun");
      started.add(RUN);
    },
    runStatus: async () => (finishedRuns.has(RUN) ? "COMMITTED" : started.has(RUN) ? "RUNNING" : undefined),
    finishRun: async (_req: RunRequest, r) => {
      if (!finishedRuns.has(RUN)) finished.push({ status: r.status, code: r.code });
      finishedRuns.add(RUN);
      trip("finishRunAfter");
    },
    recordAction: async (_t, a) => {
      if (!rows.some((x) => x.runId === a.runId && x.seq === a.seq)) rows.push(a);
    },
  };
  const started = new Set<string>();
  const provider = over.provider ?? new ScriptedProvider([proposal(0.8)]);
  const tools = over.tools ?? new FakeTools({ "passage.ap.invoice.post": postDescriptor, [CODING]: codingDescriptor });
  const halted = over.halted ?? { on: false };
  const clock = over.nowMs ?? state.clock;
  const connects: string[] = [];
  const mcp: McpConnector = {
    connect: async (caller) => {
      trip("connect");
      connects.push(caller.userId);
      return { tools: [], describe: (n) => tools.describe(n), call: (n, a) => tools.call(n, a) };
    },
  };
  // A new instance over the same stores is a pod restart: nothing but the stores survives.
  const pod = () =>
    createRunSteps({
      agents: [apAgent],
      providerFor: () => provider,
      mcp,
      audit,
      state,
      inbox: inbox as unknown as StepsDeps["inbox"],
      killSwitch: { isHalted: () => halted.on },
      now: () => new Date(clock.ms),
      riskFor: () => ({ assess: () => ({ vendorHighRisk: false, sodConflict: false }) }),
      pseudonymTtlMs: 10 * 86_400_000,
    });
  return { state, inbox, rows, finished, provider, tools, halted, clock, pod, fail, charged, live, connects, reserves: () => reserves };
}

const input = (over: Partial<RunInput> = {}): RunInput => ({
  agentId: apAgent.agentId,
  agentVersion: apAgent.version,
  invokerUserId: INVOKER,
  duties: ["APInvoicePost"],
  prompt: `Code invoice INV-7 from Acme. Remit to IBAN ${REAL_IBAN}, total 1200 USD.`,
  policy: { mode: "SUGGEST", autoThreshold: 1, autoAmountLimit: usd("0") },
  selfApprovalLimit: usd("1000"),
  runTokenCap: 400_000,
  ...over,
});
const starter = (): RunCredential => ({ kind: "starter", userId: INVOKER, duties: ["APInvoicePost"], token: "tok-starter" });
const approverCred = (): RunCredential => ({ kind: "approver", userId: APPROVER, duties: ["APInvoicePost"], token: "tok-approver" });
const far = Date.UTC(2030, 0, 1);
const approve = (s: ReturnType<typeof setup>, by = APPROVER) => (s.inbox.decided = { decision: "APPROVED", approverUserId: by, decidedAt: "2026-10-21T01:00:00.000Z" });

async function toAwaiting(s: ReturnType<typeof setup>) {
  await s.pod().create(TENANT, RUN, input(), starter(), far);
  expect(await s.pod().begin(TENANT, RUN)).toEqual({ next: "plan" });
  return s.pod().plan(TENANT, RUN);
}

describe("resumable run steps", () => {
  it("resumes after an approval on another pod: the real IBAN reaches the commit, the model never saw it", async () => {
    const s = setup();
    const out = await toAwaiting(s);
    expect(out).toMatchObject({ next: "await_approval", actionId: "action-1" });
    expect(s.provider.seen[0]?.user).not.toContain(REAL_IBAN);
    expect(s.inbox.opened[0]?.proposalJson).not.toContain("IBAN");
    expect(s.tools.calls).toHaveLength(0);

    // pods restart; the approver decides on one of them
    s.inbox.decided = { decision: "APPROVED", approverUserId: APPROVER, decidedAt: "2026-10-21T01:00:00.000Z" };
    await s.state.putCredential(TENANT, RUN, approverCred(), far);
    expect(await s.pod().resolve(TENANT, RUN, false)).toEqual({ next: "commit" });
    expect(await s.pod().commit(TENANT, RUN)).toEqual({ next: "done", status: "COMMITTED" });

    expect(s.tools.calls[0]?.args.remitTo).toBe(REAL_IBAN);
    expect(s.rows).toEqual([expect.objectContaining({ seq: 1, decision: "APPROVED", approverUserId: APPROVER, committedEntityRef: "posting-1" })]);
    expect(s.rows[0]?.inputJson).not.toContain(REAL_IBAN);
    expect(s.finished).toEqual([{ status: "COMMITTED", code: undefined }]);
    expect(s.state.creds.size).toBe(0);
  });

  it("every step is idempotent: a retried step answers from the stored state and posts once", async () => {
    const s = setup();
    const first = await toAwaiting(s);
    expect(await s.pod().plan(TENANT, RUN)).toEqual(first);
    expect(await s.pod().begin(TENANT, RUN)).toEqual(first);
    expect(s.provider.seen).toHaveLength(1);
    s.inbox.decided = { decision: "APPROVED", approverUserId: APPROVER, decidedAt: "2026-10-21T01:00:00.000Z" };
    await s.state.putCredential(TENANT, RUN, approverCred(), far);
    await s.pod().resolve(TENANT, RUN, false);
    const done = await s.pod().commit(TENANT, RUN);
    expect(await s.pod().commit(TENANT, RUN)).toEqual(done);
    expect(await s.pod().resolve(TENANT, RUN, false)).toEqual(done);
    expect(s.tools.calls).toHaveLength(1);
    expect(s.rows).toHaveLength(1);
  });

  it("an undecided request stays awaiting until the timer fires, then it is REJECTED/EXPIRED (BR-47.3)", async () => {
    const s = setup();
    await toAwaiting(s);
    expect(await s.pod().resolve(TENANT, RUN, false)).toMatchObject({ next: "await_approval" });
    expect(await s.pod().resolve(TENANT, RUN, true)).toEqual({ next: "done", status: "REJECTED", code: "AGT-0208", reason: "EXPIRED" });
    expect(s.tools.calls).toHaveLength(0);
    expect(s.rows[0]).toMatchObject({ decision: "REJECTED", approverUserId: null });
  });

  it("a human decision that beat the timer stands (first writer wins)", async () => {
    const s = setup();
    await toAwaiting(s);
    s.inbox.decided = { decision: "REJECTED", approverUserId: APPROVER, decidedAt: "2026-10-21T01:00:00.000Z" };
    expect(await s.pod().resolve(TENANT, RUN, true)).toMatchObject({ next: "done", status: "REJECTED", reason: "approver rejected" });
    expect(s.rows[0]).toMatchObject({ decision: "REJECTED", approverUserId: APPROVER });
  });

  it("reads the expiry by the clock too, so a lost timer cannot leave the run waiting", async () => {
    const s = setup();
    await toAwaiting(s);
    s.clock.ms += 73 * 3_600_000;
    expect(await s.pod().resolve(TENANT, RUN, false)).toMatchObject({ status: "REJECTED", code: "AGT-0208" });
  });

  it("self-approval above the limit is refused with AGT-0209 even though the decision was recorded", async () => {
    const s = setup();
    await toAwaiting(s);
    s.inbox.decided = { decision: "APPROVED", approverUserId: INVOKER, decidedAt: "2026-10-21T01:00:00.000Z" };
    expect(await s.pod().resolve(TENANT, RUN, false)).toMatchObject({ status: "REJECTED", code: "AGT-0209" });
    expect(s.tools.calls).toHaveLength(0);
  });

  it("the kill switch stops a run before the model call and again before the commit", async () => {
    const s = setup();
    await s.pod().create(TENANT, RUN, input(), starter(), far);
    await s.pod().begin(TENANT, RUN);
    s.halted.on = true;
    expect(await s.pod().plan(TENANT, RUN)).toMatchObject({ status: "KILLED" });
    expect(s.provider.seen).toHaveLength(0);

    const t = setup();
    await toAwaiting(t);
    t.inbox.decided = { decision: "APPROVED", approverUserId: APPROVER, decidedAt: "2026-10-21T01:00:00.000Z" };
    await t.state.putCredential(TENANT, RUN, approverCred(), far);
    await t.pod().resolve(TENANT, RUN, false);
    t.halted.on = true;
    expect(await t.pod().commit(TENANT, RUN)).toMatchObject({ status: "KILLED" });
    expect(t.tools.calls).toHaveLength(0);
    expect(t.rows).toEqual([expect.objectContaining({ decision: "APPROVED", reason: "KILLED", committedEntityRef: null })]);
  });

  it("the commit runs as the approver, never as the starter: the invoker's replay of the decision cannot take the approver's place", async () => {
    const s = setup();
    await toAwaiting(s);
    approve(s);
    await s.state.putCredential(TENANT, RUN, approverCred(), far);
    expect(await s.pod().resolve(TENANT, RUN, false)).toEqual({ next: "commit" });
    await s.pod().commit(TENANT, RUN);
    expect(s.connects.at(-1)).toBe(APPROVER);
  });

  it("an approver credential of another user than the one who decided is not used", async () => {
    const s = setup();
    await toAwaiting(s);
    approve(s, APPROVER);
    await s.state.putCredential(TENANT, RUN, { kind: "approver", userId: INVOKER, duties: ["APInvoicePost"], token: "tok-invoker" }, far);
    expect(await s.pod().resolve(TENANT, RUN, false)).toMatchObject({ next: "await_approval", actionId: "action-2" });
    expect(s.tools.calls).toHaveLength(0);
  });

  it("when the approver's session has expired the run is put back to awaiting a new approval, not failed; the second approval commits", async () => {
    const s = setup();
    await toAwaiting(s);
    approve(s);
    // No unexpired approver session when the run resumes (the approval came, the token life ran out before the run was resumed).
    expect(await s.pod().resolve(TENANT, RUN, false)).toMatchObject({ next: "await_approval", actionId: "action-2" });
    expect(s.finished).toEqual([]);
    expect(s.inbox.opened.map((o) => o.seq)).toEqual([1, 2]);
    expect(s.tools.calls).toHaveLength(0);
    // Approved again, now with a live session.
    approve(s);
    await s.state.putCredential(TENANT, RUN, approverCred(), far);
    expect(await s.pod().resolve(TENANT, RUN, false)).toEqual({ next: "commit" });
    expect(await s.pod().commit(TENANT, RUN)).toMatchObject({ status: "COMMITTED" });
    expect(s.tools.calls).toHaveLength(1);
  });

  it("a credential that expires between resolve and commit also sends the run back to approval", async () => {
    const s = setup();
    await toAwaiting(s);
    approve(s);
    await s.state.putCredential(TENANT, RUN, approverCred(), Date.UTC(2026, 9, 21, 0, 5));
    await s.pod().resolve(TENANT, RUN, false);
    s.clock.ms += 10 * 60_000;
    expect(await s.pod().commit(TENANT, RUN)).toMatchObject({ next: "await_approval", actionId: "action-2" });
    expect(s.tools.calls).toHaveLength(0);
  });

  it("the commit refuses a proposal that is not the one the approval was opened for", async () => {
    const s = setup();
    await toAwaiting(s);
    approve(s);
    await s.state.putCredential(TENANT, RUN, approverCred(), far);
    await s.pod().resolve(TENANT, RUN, false);
    (s.state.rows.get(`${TENANT}/${RUN}`)!.state as { proposalHash: string }).proposalHash = "tampered";
    expect(await s.pod().commit(TENANT, RUN)).toMatchObject({ status: "FAILED", reason: expect.stringContaining("not the one") });
    expect(s.tools.calls).toHaveLength(0);
  });

  it("a commit interrupted by a restart is never repeated: the run fails and says so", async () => {
    const s = setup();
    await toAwaiting(s);
    s.inbox.decided = { decision: "APPROVED", approverUserId: APPROVER, decidedAt: "2026-10-21T01:00:00.000Z" };
    await s.pod().resolve(TENANT, RUN, false);
    const row = s.state.rows.get(`${TENANT}/${RUN}`)!;
    (row.state as { phase: string }).phase = "committing"; // the pod died inside the tool call
    expect(await s.pod().commit(TENANT, RUN)).toMatchObject({ status: "FAILED" });
    expect(s.tools.calls).toHaveLength(0);
    expect(s.rows).toHaveLength(1);
  });

  it("a tool failure after approval ends FAILED with the tool's code and one row", async () => {
    const tools = new FakeTools({ "passage.ap.invoice.post": postDescriptor }, { ok: false, code: "AGT-0214", message: "posting refused" });
    const s = setup({ tools });
    await toAwaiting(s);
    s.inbox.decided = { decision: "APPROVED", approverUserId: APPROVER, decidedAt: "2026-10-21T01:00:00.000Z" };
    await s.state.putCredential(TENANT, RUN, approverCred(), far);
    await s.pod().resolve(TENANT, RUN, false);
    expect(await s.pod().commit(TENANT, RUN)).toMatchObject({ status: "FAILED", code: "AGT-0214" });
    expect(s.rows).toHaveLength(1);
  });

  it("a suggest-mode tool under AUTO policy commits without a gate", async () => {
    const s = setup({ provider: new ScriptedProvider([proposal(0.97, "1200", CODING)]) });
    await s.pod().create(TENANT, RUN, input({ policy: { mode: "AUTO", autoThreshold: 0.95, autoAmountLimit: usd("5000") } }), starter(), far);
    await s.pod().begin(TENANT, RUN);
    expect(await s.pod().plan(TENANT, RUN)).toEqual({ next: "commit" });
    expect(await s.pod().commit(TENANT, RUN)).toMatchObject({ status: "COMMITTED" });
    expect(s.inbox.opened).toHaveLength(0);
    expect(s.rows[0]).toMatchObject({ decision: "AUTO" });
  });

  it("a disallowed tool fails the run with a DENIED row; abort ends a run the workflow gave up on", async () => {
    const s = setup({ provider: new ScriptedProvider([proposal(0.8, "1200", "passage.other.tool")]) });
    await toAwaiting(s);
    expect(s.rows[0]).toMatchObject({ decision: "DENIED" });
    expect(s.finished[0]?.status).toBe("FAILED");

    const t = setup();
    await toAwaiting(t);
    expect(await t.pod().abort(TENANT, RUN, "workflow gave up")).toMatchObject({ status: "FAILED", reason: "workflow gave up" });
    expect(t.rows).toEqual([expect.objectContaining({ decision: "DENIED", reason: "run aborted by the workflow" })]);
    expect(await t.pod().abort(TENANT, RUN, "again")).toMatchObject({ status: "FAILED", reason: "workflow gave up" });
  });

  it("the initial state is a plain JSON value", () => {
    expect(JSON.parse(JSON.stringify(initialStepState()))).toEqual(initialStepState());
  });
});

const withUsage = (p: ReturnType<typeof proposal>, n: number) => ({ ...p, usage: { tokensIn: n, tokensOut: n } });

describe("resumable run steps survive a failure partway through a step", () => {
  it("plan: a gateway error after the model answered is retried without calling the model again, and the usage is charged once", async () => {
    const s = setup({ provider: new ScriptedProvider([withUsage(proposal(0.8), 1000), withUsage(proposal(0.8), 1000)]) });
    await s.pod().create(TENANT, RUN, input(), starter(), far);
    await s.pod().begin(TENANT, RUN);
    s.fail.connect = 1;
    await expect(s.pod().plan(TENANT, RUN)).rejects.toThrow(/injected failure: connect/); // Temporal retries the activity
    expect(await s.pod().plan(TENANT, RUN)).toMatchObject({ next: "await_approval" });
    expect(s.provider.seen).toHaveLength(1);
    expect(s.charged).toEqual({ in: 1000, out: 1000 });
    expect((await s.state.load<RunInput, { tokensIn: number }>(TENANT, RUN))!.state.tokensIn).toBe(1000);
  });

  it("plan: a save lost after the answer is kept resumes from it; a lost save after the approval was opened still shows the approver the proposal that will be committed", async () => {
    const p1 = proposal(0.8, "1200", "passage.ap.invoice.post", { invoiceId: "INV-7", remitTo: "[IBAN-1]", amount: "1200" });
    const p2 = proposal(0.8, "9800", "passage.ap.invoice.post", { invoiceId: "INV-7", remitTo: "[IBAN-1]", amount: "9800" });
    const s = setup({ provider: new ScriptedProvider([p1, p2]) });
    await s.pod().create(TENANT, RUN, input(), starter(), far);
    await s.pod().begin(TENANT, RUN);
    s.fail.saveAwaiting = 1; // the request was opened, the state save after it was lost
    await expect(s.pod().plan(TENANT, RUN)).rejects.toThrow(/injected failure/);
    expect(await s.pod().plan(TENANT, RUN)).toMatchObject({ next: "await_approval", actionId: "action-1" });
    expect(s.provider.seen).toHaveLength(1);
    expect(JSON.parse(s.inbox.opened[0]!.proposalJson).amount).toEqual(usd("1200"));
    approve(s);
    await s.state.putCredential(TENANT, RUN, approverCred(), far);
    await s.pod().resolve(TENANT, RUN, false);
    await s.pod().commit(TENANT, RUN);
    expect(s.tools.calls[0]?.args.amount).toBe("1200");
  });

  it("plan: a lost save right after the model answered resumes from the answer", async () => {
    const s = setup({ provider: new ScriptedProvider([withUsage(proposal(0.8), 500), withUsage(proposal(0.8, "9800"), 500)]) });
    await s.pod().create(TENANT, RUN, input(), starter(), far);
    await s.pod().begin(TENANT, RUN);
    s.fail.saveCalled = 1;
    await expect(s.pod().plan(TENANT, RUN)).rejects.toThrow(/injected failure/);
    expect(await s.pod().plan(TENANT, RUN)).toMatchObject({ next: "await_approval" });
    expect(s.provider.seen).toHaveLength(1);
    expect(s.charged).toEqual({ in: 500, out: 500 });
  });

  it("begin: a retry after startRun failed reuses the one budget hold", async () => {
    const s = setup();
    await s.pod().create(TENANT, RUN, input(), starter(), far);
    s.fail.startRun = 1;
    await expect(s.pod().begin(TENANT, RUN)).rejects.toThrow(/injected failure: startRun/);
    expect(await s.pod().begin(TENANT, RUN)).toEqual({ next: "plan" });
    expect(s.reserves()).toBe(1);
    await s.pod().plan(TENANT, RUN);
    expect(s.live.size).toBe(0); // the one hold was settled
  });

  it("begin: the hold is saved before anything else can fail", async () => {
    const s = setup();
    await s.pod().create(TENANT, RUN, input(), starter(), far);
    s.fail.startRun = 1;
    await expect(s.pod().begin(TENANT, RUN)).rejects.toThrow();
    expect((await s.state.load<RunInput, { hold?: unknown }>(TENANT, RUN))!.state.hold).toBeDefined();
  });

  it("finalize: a retry after the audit row was finished but the state save failed completes, and a rejection of the same run ends once", async () => {
    const s = setup();
    await toAwaiting(s);
    s.fail.saveDone = 1;
    await expect(s.pod().resolve(TENANT, RUN, true)).rejects.toThrow(/injected failure: saveDone/);
    expect(await s.pod().resolve(TENANT, RUN, true)).toMatchObject({ status: "REJECTED", code: "AGT-0208" });
    expect(s.finished).toHaveLength(1);
    expect(s.state.creds.size).toBe(0);
  });

  it("finalize: abort after a half-finished finalize also completes", async () => {
    const s = setup();
    await toAwaiting(s);
    s.fail.finishRunAfter = 1;
    await expect(s.pod().abort(TENANT, RUN, "gave up")).rejects.toThrow(/injected failure/);
    expect(await s.pod().abort(TENANT, RUN, "gave up")).toMatchObject({ status: "FAILED" });
    expect(s.finished).toHaveLength(1);
  });

  it("commit: a retry after the tool call succeeded but the audit step failed finishes COMMITTED and does not post again", async () => {
    const s = setup();
    await toAwaiting(s);
    approve(s);
    await s.state.putCredential(TENANT, RUN, approverCred(), far);
    await s.pod().resolve(TENANT, RUN, false);
    s.fail.saveDone = 1;
    await expect(s.pod().commit(TENANT, RUN)).rejects.toThrow(/injected failure: saveDone/);
    expect(await s.pod().commit(TENANT, RUN)).toMatchObject({ status: "COMMITTED" });
    expect(s.tools.calls).toHaveLength(1);
    expect(s.rows).toHaveLength(1);
  });

  it("abort closes the open approval request so an approver cannot approve a run that ended", async () => {
    const s = setup();
    await toAwaiting(s);
    await s.pod().abort(TENANT, RUN, "agents unavailable");
    expect(s.inbox.decided).toMatchObject({ decision: "REJECTED", approverUserId: "system:abort" });
    expect(s.state.creds.size).toBe(0);
  });

  it("a finished run keeps no prompt, no pseudonym map and no model answer with real values (BR-47.7)", async () => {
    const s = setup();
    await toAwaiting(s);
    approve(s);
    await s.state.putCredential(TENANT, RUN, approverCred(), far);
    await s.pod().resolve(TENANT, RUN, false);
    await s.pod().commit(TENANT, RUN);
    const row = (await s.state.load<RunInput, { displayText: string }>(TENANT, RUN))!;
    expect(row.input.prompt).toBe("");
    expect(row.pseudonyms.entries).toEqual([]);
    expect(JSON.stringify(row)).not.toContain(REAL_IBAN);
  });

  it("assertActor refuses a token whose actor did not start the run", async () => {
    const s = setup();
    await s.pod().create(TENANT, RUN, input(), starter(), far);
    await s.pod().assertActor(TENANT, RUN, INVOKER);
    await expect(s.pod().assertActor(TENANT, RUN, APPROVER)).rejects.toMatchObject({ status: 403 });
    await expect(s.pod().assertActor(TENANT, "other", INVOKER)).rejects.toMatchObject({ status: 404 });
  });
});
