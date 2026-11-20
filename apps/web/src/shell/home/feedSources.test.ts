import { describe, expect, it } from "vitest";
import type { Message } from "../../modules/planning/api/client";
import type { InboxItem } from "../inbox/inbox";
import { buildFeed, dedupeFeed, inboxToFeedItem, messageToFeedItem, rankFeed, type FeedItem } from "./feedSources";

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
    ["exception first", ["exception", "inbox"]],
    ["inbox first", ["inbox", "exception"]],
  ] as const)("keeps one row for a key and lets the exception win when %s", (_name, order) => {
    const out = dedupeFeed(order.map((category) => feedItem({ key: "same", category })));
    expect(out).toHaveLength(1);
    expect(out[0].category).toBe("exception");
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
