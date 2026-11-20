import type { PendingApproval } from "../../modules/copilot/api/proposals";
import { toolActionLabel } from "../../modules/copilot/domain";
import { DEFAULT_PLAN_CODE, withPlan } from "../../modules/planning/hooks";
import type { Message } from "../../modules/planning/api/client";
import { copy } from "../copy";
import { holdsDuty } from "../dutyGate";
import { decisionDuty, INBOX_DUTY, type InboxDecision, type InboxItem } from "../inbox/inbox";

export const FEED_PLAN_CODE = DEFAULT_PLAN_CODE;
export const FEED_VISIBLE_LIMIT = 8;
export const PLANNING_FETCH_LIMIT = 50;
export const PLANNING_DUTY = "planning.read";
export const APPROVALS_DUTY = "agents.run.read";
const EXPIRING_SOON_MS = 24 * 3_600_000;
const SOD_REFUSAL_CODE = "AGT-0209";

export const SAFE_INLINE_TOOLS: Set<string> = new Set();

export const FEED_SOURCES = [
  { category: "approval", api: "GET /api/v1/agents/approvals", duty: APPROVALS_DUTY },
  { category: "inbox", api: "GET /api/v1/inbox", duty: INBOX_DUTY },
  { category: "exception", api: `GET /api/v1/planning/messages?planCode=${FEED_PLAN_CODE}&status=open&sort=priority`, duty: PLANNING_DUTY },
] as const;

export const CATEGORY_PRECEDENCE = ["approval", "exception", "inbox"] as const;
export type FeedCategory = (typeof CATEGORY_PRECEDENCE)[number];

export type FeedOrigin =
  | { type: "inbox"; item: InboxItem }
  | { type: "approval"; agentId: string; runId: string; actionId: string; tool: string };

export interface FeedItem {
  key: string;
  category: FeedCategory;
  kind: string;
  number: string;
  reason: string;
  link: string;
  dueDate?: string;
  overdue: boolean;
  expiring: boolean;
  amount: number;
  ageDays: number;
  origin?: FeedOrigin;
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
    expiring: false,
    amount: 0,
    ageDays: Number.isNaN(created) ? 0 : Math.max(0, Math.floor((now.getTime() - created) / DAY_MS)),
    origin: { type: "inbox", item },
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
    expiring: false,
    amount: 0,
    ageDays: Math.max(0, message.delayDays),
  };
}

export function approvalToFeedItem(approval: PendingApproval, now: Date): FeedItem {
  const requested = Date.parse(approval.requestedAt);
  const msLeft = Date.parse(approval.expiresAt) - now.getTime();
  return {
    key: `approval:${approval.actionId}`,
    category: "approval",
    kind: copy.attention.approvalKind,
    number: approval.actionId.slice(0, 8),
    reason: copy.attention.approvalReason(toolActionLabel(approval.tool), `${approval.amount.amount} ${approval.amount.currency}`),
    link: `/assistant/proposals/${encodeURIComponent(approval.actionId)}`,
    dueDate: approval.expiresAt,
    overdue: false,
    expiring: msLeft <= EXPIRING_SOON_MS,
    amount: Number(approval.amount.amount) || 0,
    ageDays: Number.isNaN(requested) ? 0 : Math.max(0, Math.floor((now.getTime() - requested) / DAY_MS)),
    origin: { type: "approval", agentId: approval.agentId, runId: approval.runId, actionId: approval.actionId, tool: approval.tool },
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
    (a, b) => Number(b.overdue || b.expiring) - Number(a.overdue || a.expiring) || b.amount - a.amount || b.ageDays - a.ageDays || a.key.localeCompare(b.key),
  );
}

export const buildFeed = (items: FeedItem[]): FeedItem[] => rankFeed(dedupeFeed(items));

const DECISIONS: readonly InboxDecision[] = ["approve", "reject"];

export function inlineDecisions(item: FeedItem, duties: readonly string[]): InboxDecision[] {
  const { origin } = item;
  if (origin === undefined) return [];
  if (origin.type === "approval") return SAFE_INLINE_TOOLS.has(origin.tool) ? [...DECISIONS] : [];
  const duty = decisionDuty(origin.item);
  return duty !== undefined && holdsDuty(duties, duty) ? DECISIONS.filter((d) => origin.item.actions.includes(d)) : [];
}

export const isSafeOneClick = (item: FeedItem, duties: readonly string[]): boolean => inlineDecisions(item, duties).length > 0;

export const isSodRefusal = (problem: unknown): boolean => (problem as { code?: unknown } | null)?.code === SOD_REFUSAL_CODE;
