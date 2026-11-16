import { describe, expect, it } from "vitest";
import type { ApprovalRequest, RecordedDecision } from "../src/api/decision.js";
import type { ActionRecord } from "../src/domain/audit.js";
import type { McpConnector } from "../src/runtime/mcp.js";
import type { RunRequest } from "../src/runtime/engine.js";
import { createRunSteps, initialStepState, type RunInput, type StepAudit, type StepsDeps } from "../src/runtime/steps.js";
import type { RunCredential } from "../src/store/runstate.js";
import { MemState } from "./memstate.js";
import { apAgent, APPROVER, codingDescriptor, FakeTools, INVOKER, memoryBudgets, postDescriptor, proposal, REAL_IBAN, ScriptedProvider, TENANT, usd } from "./fakes.js";

const RUN = "run-1";
const CODING = "passage.ap.invoice.propose_coding";

class MemInbox {
  decided: RecordedDecision | undefined;
  opened: Array<{ seq: number; approvalDuty: string; proposalJson: string }> = [];
  requestedAtMs = Date.UTC(2026, 9, 21);
  async open(a: { seq: number; approvalDuty: string; proposalJson: string }) {
    this.opened.push(a);
    return "action-1";
  }
  async find(): Promise<ApprovalRequest | undefined> {
    return { invokerUserId: INVOKER, requestedAtMs: this.requestedAtMs, approvalDuty: "APInvoicePost", amount: usd("1200"), selfApprovalLimit: usd("1000"), ...(this.decided ? { decided: this.decided } : {}) };
  }
  async record(_t: string, _r: string, _a: string, d: RecordedDecision) {
    this.decided ??= d;
    return this.decided;
  }
}

function setup(over: Partial<{ provider: ScriptedProvider; tools: FakeTools; halted: { on: boolean }; nowMs: { ms: number } }> = {}) {
  const state = new MemState();
  const inbox = new MemInbox();
  const rows: ActionRecord[] = [];
  const finished: Array<{ status: string; code?: string | undefined }> = [];
  const budgets = memoryBudgets({ limitIn: 1e9, limitOut: 1e9, usedIn: 0, usedOut: 0, softPct: 80 });
  const audit: StepAudit = {
    ...budgets,
    startRun: async () => void started.add(RUN),
    runStatus: async () => (started.has(RUN) ? "RUNNING" : undefined),
    finishRun: async (_req: RunRequest, r) => void finished.push({ status: r.status, code: r.code }),
    recordAction: async (_t, a) => {
      if (!rows.some((x) => x.runId === a.runId && x.seq === a.seq)) rows.push(a);
    },
  };
  const started = new Set<string>();
  const provider = over.provider ?? new ScriptedProvider([proposal(0.8)]);
  const tools = over.tools ?? new FakeTools({ "passage.ap.invoice.post": postDescriptor, [CODING]: codingDescriptor });
  const halted = over.halted ?? { on: false };
  const clock = over.nowMs ?? state.clock;
  const mcp: McpConnector = { connect: async () => ({ tools: [], describe: (n) => tools.describe(n), call: (n, a) => tools.call(n, a) }) };
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
  return { state, inbox, rows, finished, provider, tools, halted, clock, pod };
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

  it("the commit runs as the approver and falls back to the starter; with neither unexpired the run fails and posts nothing", async () => {
    const s = setup();
    await toAwaiting(s);
    s.inbox.decided = { decision: "APPROVED", approverUserId: APPROVER, decidedAt: "2026-10-21T01:00:00.000Z" };
    await s.pod().resolve(TENANT, RUN, false);
    s.clock.ms = far + 1; // every token has expired
    expect(await s.pod().commit(TENANT, RUN)).toMatchObject({ status: "FAILED", code: "AGT-0214" });
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
