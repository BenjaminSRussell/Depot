import { copy } from "../copy";
import { holdsDuty } from "../dutyGate";
import { FEED_SOURCES, type FeedCategory, type FeedItem } from "../home/feedSources";
import type { Pin } from "../pins/pinStore";
import { pinHref, RECORD_ROUTES } from "../pins/pinTargets";
import type { ResolvedWorkspace } from "../workspaces/model";
import type { Activity } from "./activityStore";

export const MIN_ACTIVITY = 3;
export const HALF_LIFE_DAYS = 7;
export const SUGGESTION_LIMIT = 3;
export const RECENT_RECORD_LIMIT = 5;

const DAY_MS = 86_400_000;

type PromptKey = keyof typeof copy.home.suggestionPrompts;

interface PromptEntry {
  key: PromptKey;
  duty?: string;
  route?: string;
}

interface RolePriority {
  id: string;
  markers: readonly string[];
  feedOrder: readonly FeedCategory[];
  workspaces: readonly string[];
  prompts: readonly PromptEntry[];
}

const PURCHASE_ORDERS: PromptEntry = { key: "purchaseOrders", duty: "procurement.purchase-order.inquire", route: "/purchasing/purchase-orders" };

export const ROLE_PRIORITY: readonly RolePriority[] = [
  {
    id: "ap-clerk",
    markers: ["receiving.vendor-invoice.post", "receiving.ap.inquire", "payment-proposal.read"],
    feedOrder: ["inbox", "approval", "exception"],
    workspaces: ["ap-clerk", "buyer", "controller"],
    prompts: [
      { key: "invoicesToMatch", duty: "receiving.ap.inquire", route: "/ap-invoices" },
      { key: "invoicesDue", duty: "payment-proposal.read", route: "/payables" },
    ],
  },
  {
    id: "planner",
    markers: ["planning.run"],
    feedOrder: ["exception", "approval", "inbox"],
    workspaces: ["production-planner", "shopfloor-supervisor", "buyer"],
    prompts: [
      { key: "planningMessages", duty: "planning.read", route: "/planning/messages" },
      { key: "lateOrders", duty: "scheduling.read", route: "/scheduling/late-orders" },
    ],
  },
];

const GENERAL_PROMPTS: readonly PromptEntry[] = [{ key: "attention" }, PURCHASE_ORDERS];

const SOURCE_DUTY: Record<FeedCategory, string> = Object.fromEntries(FEED_SOURCES.map((s) => [s.category, s.duty])) as Record<FeedCategory, string>;

export const rolesFor = (duties: readonly string[]): RolePriority[] => ROLE_PRIORITY.filter((role) => role.markers.some((duty) => holdsDuty(duties, duty)));

export function prioritizeFeed(items: FeedItem[], duties: readonly string[]): FeedItem[] {
  const allowed = items.filter((item) => holdsDuty(duties, SOURCE_DUTY[item.category]));
  const role = rolesFor(duties)[0];
  if (role === undefined) return allowed;
  return [...allowed].sort((a, b) => role.feedOrder.indexOf(a.category) - role.feedOrder.indexOf(b.category));
}

const listRoute = (row: Pin): string => (row.kind === "page" ? row.route : RECORD_ROUTES[row.recordType].replace(/\/:id$/, ""));

function scoreRoutes(activity: readonly Activity[], now: Date): ReadonlyMap<string, number> {
  const scores = new Map<string, number>();
  if (activity.length < MIN_ACTIVITY) return scores;
  for (const row of activity) {
    const at = Date.parse(row.at);
    const ageDays = Math.max(0, (now.getTime() - at) / DAY_MS);
    const score = Number.isNaN(ageDays) ? 0 : 0.5 ** (ageDays / HALF_LIFE_DAYS);
    scores.set(listRoute(row), (scores.get(listRoute(row)) ?? 0) + score);
  }
  return scores;
}

export function orderWorkspaces(rest: readonly ResolvedWorkspace[], duties: readonly string[], activity: readonly Activity[], now: Date): ResolvedWorkspace[] {
  const roleOrder = rolesFor(duties).flatMap((role) => role.workspaces);
  const rank = (w: ResolvedWorkspace) => (roleOrder.includes(w.id) ? roleOrder.indexOf(w.id) : roleOrder.length);
  const scores = scoreRoutes(activity, now);
  const score = (w: ResolvedWorkspace) => w.entries.reduce((sum, entry) => sum + (scores.get(entry.to) ?? 0), 0);
  return [...rest].sort((a, b) => score(b) - score(a) || rank(a) - rank(b));
}

export function suggestedPrompts(duties: readonly string[], activity: readonly Activity[], now: Date): string[] {
  const pool = [...rolesFor(duties).flatMap((role) => role.prompts), ...GENERAL_PROMPTS].filter((p) => p.duty === undefined || holdsDuty(duties, p.duty));
  const scores = scoreRoutes(activity, now);
  const score = (p: PromptEntry) => (p.route === undefined ? 0 : (scores.get(p.route) ?? 0));
  return [...new Set(pool)]
    .sort((a, b) => score(b) - score(a))
    .slice(0, SUGGESTION_LIMIT)
    .map((p) => copy.home.suggestionPrompts[p.key]);
}

export function recentRecords(activity: readonly Activity[], canOpen: (to: string) => boolean): Extract<Pin, { kind: "record" }>[] {
  return activity
    .flatMap((row) => (row.kind === "record" && canOpen(pinHref(row)) ? [row] : []))
    .sort((a, b) => Date.parse(b.at) - Date.parse(a.at))
    .slice(0, RECENT_RECORD_LIMIT);
}
