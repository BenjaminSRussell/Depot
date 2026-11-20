import { describe, expect, it } from "vitest";
import { loadModules } from "../../modules/registry";
import { CATEGORY_PRECEDENCE, type FeedItem } from "../home/feedSources";
import { copy } from "../copy";
import readme from "../README.md?raw";
import type { ResolvedWorkspace } from "../workspaces/model";
import type { Activity } from "./activityStore";
import { HALF_LIFE_DAYS, MIN_ACTIVITY, orderWorkspaces, prioritizeFeed, recentRecords, ROLE_PRIORITY, rolesFor, SUGGESTION_LIMIT, suggestedPrompts } from "./homeOrder";

const NOW = new Date("2026-11-19T10:00:00.000Z");
const daysAgo = (days: number) => new Date(NOW.getTime() - days * 86_400_000).toISOString();
const visit = (route: string, days = 0): Activity => ({ kind: "page", route, at: daysAgo(days) });
const record = (recordType: "purchase_order" | "party", id: string, days = 0): Activity => ({ kind: "record", recordType, id, at: daysAgo(days) });

const AP = ["receiving.ap.inquire", "inbox.read", "agents.run.read", "planning.read"];
const PLANNER = ["planning.run", "planning.read", "inbox.read", "agents.run.read"];
const ALL_FEED = ["inbox.read", "agents.run.read", "planning.read"];

const feedItem = (category: FeedItem["category"], key: string = category): FeedItem => ({ key, category, kind: "", number: "", reason: "", link: "/", overdue: false, expiring: false, amount: 0, ageDays: 0 });
const workspace = (id: string, ...entries: string[]): ResolvedWorkspace => ({
  id,
  label: id,
  persona: id,
  description: "",
  entries: entries.map((to) => ({ to, label: to, originalLabel: to, moduleId: "m", moduleTitle: "m", area: "a", areaLabel: "a", decision: "keep", gated: true })),
});
const ids = (list: ResolvedWorkspace[]) => list.map((w) => w.id);

describe("role priority table", () => {
  it("lists each role once, orders all three feed sources, and names only duties that a module page requires", () => {
    const dutiesInNav = new Set(loadModules().flatMap((m) => m.nav.flatMap((n) => (n.duty ? [n.duty] : []))));
    expect(new Set(ROLE_PRIORITY.map((r) => r.id)).size).toBe(ROLE_PRIORITY.length);
    for (const role of ROLE_PRIORITY) {
      expect([...role.feedOrder].sort()).toEqual([...CATEGORY_PRECEDENCE].sort());
      for (const duty of [...role.markers, ...role.prompts.flatMap((p) => (p.duty ? [p.duty] : []))]) expect(dutiesInNav.has(duty), duty).toBe(true);
    }
  });

  it("is documented in the shell README, role by role", () => {
    for (const role of ROLE_PRIORITY) {
      expect(readme, role.id).toContain(`| ${role.id} |`);
      for (const marker of role.markers) expect(readme, marker).toContain(marker);
    }
  });

  it("matches no role for a user holding none of the marker duties", () => {
    expect(rolesFor(["planning.read", "inbox.read"])).toEqual([]);
  });
});

describe("prioritizeFeed", () => {
  const items = [feedItem("approval"), feedItem("exception"), feedItem("inbox")];

  it.each([
    ["an AP clerk", AP, ["inbox", "approval", "exception"]],
    ["a planner", PLANNER, ["exception", "approval", "inbox"]],
    ["a user with no role", ALL_FEED, ["approval", "exception", "inbox"]],
  ])("orders sources for %s", (_who, duties, expected) => {
    expect(prioritizeFeed(items, duties).map((i) => i.category)).toEqual(expected);
  });

  it("keeps the urgency order inside one source", () => {
    const two = [feedItem("inbox", "inbox:b"), feedItem("inbox", "inbox:a"), feedItem("exception")];
    expect(prioritizeFeed(two, AP).map((i) => i.key)).toEqual(["inbox:b", "inbox:a", "exception"]);
  });

  it("never shows an item whose source duty is not held, though it is in the list", () => {
    expect(prioritizeFeed(items, ["receiving.ap.inquire", "inbox.read"]).map((i) => i.category)).toEqual(["inbox"]);
    expect(prioritizeFeed(items, [])).toEqual([]);
  });

  it("returns an empty feed unchanged", () => {
    expect(prioritizeFeed([], AP)).toEqual([]);
  });
});

describe("orderWorkspaces", () => {
  const buyer = workspace("buyer", "/purchasing/purchase-orders", "/party");
  const sales = workspace("sales-rep", "/sales/orders");
  const apClerk = workspace("ap-clerk", "/ap-invoices");
  const planner = workspace("production-planner", "/planning");
  const held = [sales, planner, buyer, apClerk];
  const lately = [visit("/purchasing/purchase-orders", 1), record("party", "p-1", 1), visit("/purchasing/purchase-orders", 2)];

  it("follows the role table when there is no activity", () => {
    expect(ids(orderWorkspaces(held, AP, [], NOW))).toEqual(["ap-clerk", "buyer", "sales-rep", "production-planner"]);
    expect(ids(orderWorkspaces(held, PLANNER, [], NOW))).toEqual(["production-planner", "buyer", "sales-rep", "ap-clerk"]);
  });

  it("keeps the held order for a user with no role and no activity", () => {
    expect(ids(orderWorkspaces(held, ALL_FEED, [], NOW))).toEqual(ids(held));
  });

  it("ranks the workspaces holding recent pages and record lists first", () => {
    expect(ids(orderWorkspaces(held, ALL_FEED, lately, NOW))[0]).toBe("buyer");
    expect(ids(orderWorkspaces(held, AP, lately, NOW))[0]).toBe("buyer");
  });

  it(`falls back to the role order below ${MIN_ACTIVITY} rows`, () => {
    expect(ids(orderWorkspaces(held, AP, lately.slice(0, MIN_ACTIVITY - 1), NOW))).toEqual(ids(orderWorkspaces(held, AP, [], NOW)));
  });

  it("weighs recent visits above older ones, by the half-life", () => {
    const old = [visit("/sales/orders", 3 * HALF_LIFE_DAYS), visit("/sales/orders", 3 * HALF_LIFE_DAYS), visit("/sales/orders", 3 * HALF_LIFE_DAYS)];
    const fresh = [visit("/planning", 0), visit("/planning", 0), visit("/planning", 0)];
    expect(ids(orderWorkspaces(held, ALL_FEED, [...old, ...fresh], NOW))[0]).toBe("production-planner");
  });

  it("breaks equal scores by role rank, then by held order", () => {
    const tie = [visit("/sales/orders", 1), visit("/ap-invoices", 1), visit("/planning", 1)];
    expect(ids(orderWorkspaces(held, AP, tie, NOW))).toEqual(["ap-clerk", "sales-rep", "production-planner", "buyer"]);
    expect(ids(orderWorkspaces(held, ALL_FEED, tie, NOW))).toEqual(["sales-rep", "production-planner", "ap-clerk", "buyer"]);
  });

  it("gives an unparsable timestamp no weight and clamps a future timestamp to now", () => {
    const odd = [{ ...visit("/sales/orders"), at: "not a date" }, visit("/planning", -30), visit("/planning", -30)];
    expect(ids(orderWorkspaces(held, ALL_FEED, odd, NOW))[0]).toBe("production-planner");
    expect(orderWorkspaces(held, ALL_FEED, odd, NOW)).toHaveLength(held.length);
  });

  it("never adds a workspace that activity names but the user does not hold", () => {
    const ordered = orderWorkspaces([buyer], AP, [visit("/sales/orders"), visit("/sales/orders"), visit("/sales/orders")], NOW);
    expect(ids(ordered)).toEqual(["buyer"]);
  });
});

describe("suggestedPrompts", () => {
  const text = copy.home.suggestionPrompts;

  it("shows different prompts to an AP clerk and a planner", () => {
    const duties = (extra: string[]) => [...extra, "procurement.purchase-order.inquire", "scheduling.read"];
    expect(suggestedPrompts(duties(["receiving.ap.inquire", "planning.read"]), [], NOW)[0]).toBe(text.invoicesToMatch);
    expect(suggestedPrompts(duties(["planning.run", "planning.read"]), [], NOW)[0]).toBe(text.planningMessages);
  });

  it("shows only the general prompt to a user holding no duties", () => {
    expect(suggestedPrompts([], [], NOW)).toEqual([text.attention]);
  });

  it("never shows a prompt tied to a duty the user lacks, even with matching activity", () => {
    const activity = [visit("/payables"), visit("/payables"), visit("/payables"), record("purchase_order", "po-1")];
    expect(suggestedPrompts(["receiving.ap.inquire"], activity, NOW)).not.toContain(text.invoicesDue);
    expect(suggestedPrompts(["receiving.ap.inquire"], activity, NOW)).not.toContain(text.purchaseOrders);
  });

  it("lifts the prompt for the area the user worked in recently", () => {
    const activity = [record("purchase_order", "po-1", 1), record("purchase_order", "po-2", 1), record("purchase_order", "po-3", 1)];
    expect(suggestedPrompts(["receiving.ap.inquire", "payment-proposal.read", "procurement.purchase-order.inquire"], activity, NOW)[0]).toBe(text.purchaseOrders);
  });

  it("shows at most the limit", () => {
    const duties = ["receiving.ap.inquire", "payment-proposal.read", "planning.run", "planning.read", "scheduling.read", "procurement.purchase-order.inquire"];
    expect(suggestedPrompts(duties, [], NOW)).toHaveLength(SUGGESTION_LIMIT);
  });
});

describe("recentRecords", () => {
  const rows = [record("purchase_order", "po-old", 5), visit("/party", 0), record("party", "p-1", 1), record("purchase_order", "po-new", 0)];

  it("lists record rows newest first and leaves out pages", () => {
    expect(recentRecords(rows, () => true).map((r) => r.id)).toEqual(["po-new", "p-1", "po-old"]);
  });

  it("never lists a record the user cannot open, and is empty when nothing can be opened", () => {
    expect(recentRecords(rows, (to) => !to.startsWith("/purchasing")).map((r) => r.id)).toEqual(["p-1"]);
    expect(recentRecords(rows, () => false)).toEqual([]);
  });
});
