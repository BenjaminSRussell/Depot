import { DEFAULT_PLAN_CODE, withPlan } from "../../modules/planning/hooks";
import type { Message } from "../../modules/planning/api/client";
import { copy } from "../copy";
import { INBOX_DUTY, type InboxItem } from "../inbox/inbox";

export const FEED_PLAN_CODE = DEFAULT_PLAN_CODE;
export const FEED_VISIBLE_LIMIT = 8;
export const PLANNING_FETCH_LIMIT = 50;
export const PLANNING_DUTY = "planning.read";

export const FEED_SOURCES = [
  { category: "inbox", api: "GET /api/v1/inbox", duty: INBOX_DUTY },
  { category: "exception", api: `GET /api/v1/planning/messages?planCode=${FEED_PLAN_CODE}&status=open&sort=priority`, duty: PLANNING_DUTY },
] as const;

export const CATEGORY_PRECEDENCE = ["exception", "inbox"] as const;
export type FeedCategory = (typeof CATEGORY_PRECEDENCE)[number];

export interface FeedItem {
  key: string;
  category: FeedCategory;
  kind: string;
  number: string;
  reason: string;
  link: string;
  dueDate?: string;
  overdue: boolean;
  amount: number;
  ageDays: number;
}

const DAY_MS = 86_400_000;

const isDate = (value: string | null | undefined): value is string => value != null && !Number.isNaN(Date.parse(value));
const today = (now: Date) => now.toISOString().slice(0, 10);
const pastDue = (dueDate: string | null | undefined, now: Date) => isDate(dueDate) && dueDate.slice(0, 10) < today(now);

export function inboxToFeedItem(item: InboxItem, now: Date): FeedItem {
  const created = Date.parse(item.createdAt);
  return {
    key: `inbox:${item.source}:${item.id}`,
    category: "inbox",
    kind: item.kind,
    number: item.number,
    reason: item.reason,
    link: item.link,
    dueDate: isDate(item.dueDate) ? item.dueDate : undefined,
    overdue: pastDue(item.dueDate, now),
    amount: 0,
    ageDays: Number.isNaN(created) ? 0 : Math.max(0, Math.floor((now.getTime() - created) / DAY_MS)),
  };
}

export function messageToFeedItem(message: Message, now: Date): FeedItem {
  const delayed = message.delayDays > 0;
  return {
    key: `planning:${message.id}`,
    category: "exception",
    kind: delayed ? copy.attention.delayMessage : copy.attention.actionMessage,
    number: message.supplyRef ?? message.itemId,
    reason: message.text ?? message.action.replaceAll("-", " "),
    link: withPlan(`/planning/messages/${encodeURIComponent(message.id)}`, FEED_PLAN_CODE),
    dueDate: isDate(message.suggestedDate) ? message.suggestedDate : undefined,
    overdue: delayed || pastDue(message.suggestedDate, now),
    amount: 0,
    ageDays: Math.max(0, message.delayDays),
  };
}

export function dedupeFeed(items: FeedItem[]): FeedItem[] {
  const winners = new Map<string, FeedItem>();
  for (const item of items) {
    const held = winners.get(item.key);
    if (held === undefined || CATEGORY_PRECEDENCE.indexOf(item.category) < CATEGORY_PRECEDENCE.indexOf(held.category)) winners.set(item.key, item);
  }
  return [...winners.values()];
}

export function rankFeed(items: FeedItem[]): FeedItem[] {
  return [...items].sort(
    (a, b) => Number(b.overdue) - Number(a.overdue) || b.amount - a.amount || b.ageDays - a.ageDays || a.key.localeCompare(b.key),
  );
}

export const buildFeed = (items: FeedItem[]): FeedItem[] => rankFeed(dedupeFeed(items));
