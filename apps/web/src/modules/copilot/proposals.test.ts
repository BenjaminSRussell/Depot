import { describe, expect, it, vi } from "vitest";
import { ApprovalNotFound, approvalPath, approvalSchema, getApproval } from "./api/proposals";
import { canDecide, showsAskAgain, stateAnnouncement, stateLabel, type ProposalState } from "./proposalModel";

const ACTION = "00000000-0000-7000-8000-00000000a001";
const approval = {
  runId: "00000000-0000-7000-8000-0000000000d1",
  actionId: ACTION,
  tool: "depot.ar.settlement.propose",
  state: "PENDING",
  amount: { amount: "1200.50", currency: "USD" },
  requestedAt: "2026-11-19T10:00:00.000Z",
  expiresAt: "2026-11-22T10:00:00.000Z",
};

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

describe("proposal state machine", () => {
  const states: ProposalState[] = ["PENDING", "APPROVED", "REJECTED", "EXPIRED", "FAILED"];

  it("lets only a pending proposal be decided and only an expired one be asked again", () => {
    expect(states.filter(canDecide)).toEqual(["PENDING"]);
    expect(states.filter(showsAskAgain)).toEqual(["EXPIRED"]);
  });

  it("labels and announces every state in plain words", () => {
    expect(states.map(stateLabel)).toEqual(["Awaiting your approval", "Approved", "Declined", "Expired", "Failed"]);
    expect(stateAnnouncement("EXPIRED")).toBe("Proposal status: Expired.");
  });
});

describe("approvalSchema", () => {
  it("accepts a pending and a decided proposal and ignores extra fields", () => {
    expect(approvalSchema.safeParse({ ...approval, extra: 1 }).success).toBe(true);
    expect(approvalSchema.safeParse({ ...approval, state: "EXPIRED", decided: { decision: "REJECTED", reason: "EXPIRED", decidedAt: approval.expiresAt } }).success).toBe(true);
  });

  it.each([
    ["a numeric amount", { ...approval, amount: { amount: 1200.5, currency: "USD" } }],
    ["a string in place of the amount object", { ...approval, amount: "1200.50" }],
    ["a bad currency", { ...approval, amount: { amount: "1", currency: "usd" } }],
    ["a date that is not a date", { ...approval, expiresAt: "soon" }],
    ["an unknown state", { ...approval, state: "FAILED" }],
    ["a missing tool", { ...approval, tool: undefined }],
    ["a null body", null],
  ])("rejects %s", (_name, body) => {
    expect(approvalSchema.safeParse(body).success).toBe(false);
  });
});

describe("getApproval", () => {
  it("reads the approval path with no custom auth header and returns the parsed body", async () => {
    const fetchFn = vi.fn(async () => json(approval));
    expect(await getApproval(ACTION, undefined, fetchFn)).toEqual(approval);
    const [url, init] = fetchFn.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe(approvalPath(ACTION));
    expect(Object.keys(init.headers ?? {}).map((h) => h.toLowerCase())).toEqual(["accept"]);
  });

  it("maps a 404 to ApprovalNotFound whatever the problem body says", async () => {
    await expect(getApproval(ACTION, undefined, async () => json({ code: "AGT-0404", detail: "no approval request for this action" }, 404))).rejects.toBeInstanceOf(ApprovalNotFound);
    await expect(getApproval(ACTION, undefined, async () => new Response("", { status: 404 }))).rejects.toBeInstanceOf(ApprovalNotFound);
  });

  it("raises a plain error, not not-found, for a 500, a body that fails the schema and a network failure", async () => {
    const failures = [
      getApproval(ACTION, undefined, async () => json({ code: "AGT-0500" }, 500)),
      getApproval(ACTION, undefined, async () => json({ ...approval, amount: { amount: 5, currency: "USD" } })),
      getApproval(ACTION, undefined, async () => Promise.reject(new TypeError("network"))),
    ];
    for (const failure of failures) {
      const err = await failure.catch((e: unknown) => e);
      expect(err).toBeInstanceOf(Error);
      expect(err).not.toBeInstanceOf(ApprovalNotFound);
    }
  });
});
