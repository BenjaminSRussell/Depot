import { describe, expect, it } from "vitest";
import type { Message } from "../../modules/planning/api/client";
import type { InboxItem } from "../inbox/inbox";
import type { PendingApproval } from "../../modules/copilot/api/proposals";
import { approvalToFeedItem, buildFeed, dedupeFeed, inboxToFeedItem, inlineDecisions, isSafeOneClick, isSodRefusal, messageToFeedItem, rankFeed, SAFE_INLINE_TOOLS, type FeedItem } from "./feedSources";

const NOW = new Date("2026-11-20T10:00:00Z");

const inbox = (over: Partial<InboxItem> = {}): InboxItem => ({
  id: "t1",
  source: "purchasing",
  kind: "Requisition approval",
  reason: "Waiting for approval",
  number: "PR-1",
  link: "/purchasing/requisitions/1",
  actions: ["approve", "reject"],
  createdAt: "2026-11-18T10:00:00Z",
  ...over,
});

const message = (over: Partial<Message> = {}): Message => ({
  id: "m1",
  status: "open",
  version: 1,
  action: "postpone",
  itemId: "item-1",
  delayDays: 0,
  qty: "5",
  ...over,
});

const feedItem = (over: Partial<FeedItem>): FeedItem => ({
  key: "k",
  category: "inbox",
  kind: "Kind",
  number: "N",
  reason: "Why",
  link: "/x",
  overdue: false,
  expiring: false,
  amount: 0,
  ageDays: 0,
  ...over,
});

describe("inboxToFeedItem", () => {
  it.each([
    ["no due date", {}, false, undefined],
    ["an unparseable due date", { dueDate: "not-a-date" }, false, undefined],
    ["a future due date", { dueDate: "2026-12-01" }, false, "2026-12-01"],
    ["a past due date", { dueDate: "2026-11-01" }, true, "2026-11-01"],
  ])("reads %s", (_name, over, overdue, dueDate) => {
    const item = inboxToFeedItem(inbox(over), NOW);
    expect(item.overdue).toBe(overdue);
    expect(item.dueDate).toBe(dueDate);
  });

  it("keys by source and id, and ages in whole days, 0 when createdAt is unreadable", () => {
    expect(inboxToFeedItem(inbox(), NOW)).toMatchObject({ key: "inbox:purchasing:t1", category: "inbox", ageDays: 2, link: "/purchasing/requisitions/1" });
    expect(inboxToFeedItem(inbox({ createdAt: "garbage" }), NOW).ageDays).toBe(0);
  });
});

describe("messageToFeedItem", () => {
  it.each([
    ["no date, no delay", {}, false],
    ["a delay", { delayDays: 3 }, true],
    ["a negative delay", { delayDays: -2 }, false],
    ["a past suggested date", { suggestedDate: "2026-11-01" }, true],
    ["a future suggested date", { suggestedDate: "2026-12-01" }, false],
  ])("marks %s", (_name, over, overdue) => {
    expect(messageToFeedItem(message(over), NOW).overdue).toBe(overdue);
  });

  it("falls back to the item id and the action when supplyRef and text are absent, and links to the planning module", () => {
    expect(messageToFeedItem(message({ supplyRef: null, text: null, action: "no-vendor" }), NOW)).toMatchObject({
      key: "planning:m1",
      category: "exception",
      number: "item-1",
      reason: "no vendor",
      link: "/planning/messages/m1?plan=MAIN",
      amount: 0,
    });
    expect(messageToFeedItem(message({ supplyRef: "PO-9", text: "Move out two days" }), NOW)).toMatchObject({ number: "PO-9", reason: "Move out two days" });
  });
});

describe("dedupeFeed", () => {
  it.each([
    ["approval, exception, inbox", ["approval", "exception", "inbox"], "approval"],
    ["inbox, exception, approval", ["inbox", "exception", "approval"], "approval"],
    ["exception first", ["exception", "inbox"], "exception"],
    ["inbox first", ["inbox", "exception"], "exception"],
  ] as const)("keeps one row for a key and lets the highest category win when %s", (_name, order, winner) => {
    const out = dedupeFeed(order.map((category) => feedItem({ key: "same", category })));
    expect(out).toHaveLength(1);
    expect(out[0].category).toBe(winner);
  });

  it("keeps rows with different keys", () => {
    expect(dedupeFeed([feedItem({ key: "a" }), feedItem({ key: "b" })])).toHaveLength(2);
  });
});

describe("rankFeed", () => {
  it("orders overdue first, then amount, then age, then key", () => {
    const ranked = rankFeed([
      feedItem({ key: "e", ageDays: 1 }),
      feedItem({ key: "d", ageDays: 9 }),
      feedItem({ key: "c", amount: 50 }),
      feedItem({ key: "b", overdue: true, ageDays: 1 }),
      feedItem({ key: "a", overdue: true, ageDays: 5 }),
    ]);
    expect(ranked.map((i) => i.key)).toEqual(["a", "b", "c", "d", "e"]);
  });

  it("breaks a tie on every field by key, the same way every time", () => {
    const items = [feedItem({ key: "z" }), feedItem({ key: "m" }), feedItem({ key: "a" })];
    expect(rankFeed(items).map((i) => i.key)).toEqual(["a", "m", "z"]);
    expect(rankFeed([...items].reverse()).map((i) => i.key)).toEqual(["a", "m", "z"]);
  });

  it("does not change its input, and builds an empty feed from nothing", () => {
    const items = [feedItem({ key: "b" }), feedItem({ key: "a" })];
    rankFeed(items);
    expect(items.map((i) => i.key)).toEqual(["b", "a"]);
    expect(buildFeed([])).toEqual([]);
  });
});

const approval = (over: Partial<PendingApproval> = {}): PendingApproval => ({
  runId: "r1",
  actionId: "11111111-2222-4333-8444-555555555555",
  agentId: "cash_application",
  tool: "depot.ar.settlement.propose",
  state: "PENDING",
  amount: { amount: "1200.50", currency: "USD" },
  requestedAt: "2026-11-19T10:00:00.000Z",
  expiresAt: "2026-11-22T10:00:00.000Z",
  ...over,
});

describe("approvalToFeedItem", () => {
  it("keys by action id, links to the proposal, ranks by amount and ages in days", () => {
    expect(approvalToFeedItem(approval(), NOW)).toMatchObject({
      key: "approval:11111111-2222-4333-8444-555555555555",
      category: "approval",
      link: "/assistant/proposals/11111111-2222-4333-8444-555555555555",
      amount: 1200.5,
      ageDays: 1,
      expiring: false,
      overdue: false,
    });
  });

  it.each([
    ["inside 24 hours", "2026-11-21T09:00:00.000Z", true],
    ["beyond 24 hours", "2026-11-22T10:00:00.000Z", false],
  ])("marks expiry %s", (_name, expiresAt, expiring) => {
    expect(approvalToFeedItem(approval({ expiresAt }), NOW).expiring).toBe(expiring);
  });

  it("ranks an expiring approval ahead of a larger one that is not", () => {
    const ranked = rankFeed([feedItem({ key: "big", amount: 900 }), feedItem({ key: "soon", expiring: true })]);
    expect(ranked.map((i) => i.key)).toEqual(["soon", "big"]);
  });
});

describe("inlineDecisions (default deny)", () => {
  const party = inboxToFeedItem(inbox({ source: "party", actions: ["approve", "reject"] }), NOW);
  const approvalItem = approvalToFeedItem(approval(), NOW);

  it.each([
    ["a party task, duty held, both actions", party, ["party.approve"], ["approve", "reject"]],
    ["a party task, duty missing", party, [], []],
    ["a party task offering only approve", inboxToFeedItem(inbox({ source: "party", actions: ["approve"] }), NOW), ["party.approve"], ["approve"]],
    ["a party task offering only open", inboxToFeedItem(inbox({ source: "party", actions: ["open"] }), NOW), ["party.approve"], []],
    ["a purchasing task the web cannot decide", inboxToFeedItem(inbox(), NOW), ["procurement.requisition.approve"], []],
    ["a settlement approval, not allowlisted", approvalItem, ["agents.run.read"], []],
    ["an exception", messageToFeedItem(message(), NOW), ["planning.read", "party.approve"], []],
    ["an item with no origin", feedItem({}), ["party.approve"], []],
  ])("%s", (_name, item, duties, expected) => {
    expect(inlineDecisions(item, duties)).toEqual(expected);
    expect(isSafeOneClick(item, duties)).toBe(expected.length > 0);
  });

  it("allows an approval only for a tool on the explicit allowlist", () => {
    SAFE_INLINE_TOOLS.add(approvalItem.origin?.type === "approval" ? approvalItem.origin.tool : "");
    expect(inlineDecisions(approvalItem, [])).toEqual(["approve", "reject"]);
    SAFE_INLINE_TOOLS.clear();
  });

  it("recognises the self-approval refusal code only", () => {
    expect(isSodRefusal({ code: "AGT-0209" })).toBe(true);
    expect(isSodRefusal({ code: "AGT-0203" })).toBe(false);
    expect(isSodRefusal(null)).toBe(false);
  });
});
