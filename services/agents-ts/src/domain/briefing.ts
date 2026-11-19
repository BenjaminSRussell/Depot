import { AGENT_IDS, type AgentId } from "../agents/catalog.js";
import { compareMoney } from "./money.js";

export const RATE_LIMIT = 3;
export const STALE_RUNNING_MINUTES = 10;
export const DEFAULT_EXPIRY_HOURS = 72;
export const FACT_ROW_CAP = 200;
const DAY_MS = 24 * 3_600_000;
const CURSOR_TIMESTAMP = /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}(\.\d{1,6})?[+-]\d{2}(:\d{2})?$/;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

export interface PendingFact {
  agentId: AgentId;
  actionId: string;
  tool: string;
  amount: string;
  currency: string;
  selfApprovalLimit: string;
  selfApprovalCurrency: string;
  requestedAt: string;
  expiryHours: number | null;
}

export interface DecidedFact {
  agentId: AgentId;
  actionId: string;
  tool: string;
  decision: string;
  decidedAt: string;
}

export interface RunFact {
  agentId: AgentId;
  runId: string;
  status: string;
  createdAt: string;
}

export interface BriefingFacts {
  pending: PendingFact[];
  decided: DecidedFact[];
  runs: RunFact[];
}

export interface BriefingItem {
  agentId: AgentId;
  factId: string;
  text: string;
  actionId?: string;
  route?: string;
}

export interface AgentCounts {
  pending: number;
  decided: number;
  committedRuns: number;
  failedRuns: number;
}

export interface BriefingContent {
  changes: BriefingItem[];
  risks: BriefingItem[];
  recommendedActions: BriefingItem[];
  notes: string[];
  counts: Record<string, AgentCounts>;
}

export interface StoredBriefing {
  id: string;
  status: "RUNNING" | "READY" | "FAILED";
  narrative: boolean;
  createdAt: string;
  completedAt: string | null;
  readAt: string | null;
  content: BriefingContent | null;
}

export interface BriefingPage {
  items: StoredBriefing[];
  nextCursor: string | null;
}

export interface BriefingStore {
  create(tenantId: string, userId: string): Promise<string>;
  finish(tenantId: string, id: string, content: BriefingContent): Promise<void>;
  fail(tenantId: string, id: string): Promise<void>;
  get(tenantId: string, userId: string, id: string): Promise<StoredBriefing | undefined>;
  list(tenantId: string, userId: string, limit: number, cursor: Cursor | undefined): Promise<BriefingPage>;
  markRead(tenantId: string, userId: string, id: string): Promise<StoredBriefing | undefined>;
}

export interface BriefingFactsReader {
  read(tenantId: string, userId: string, duties: readonly string[], now: Date): Promise<BriefingFacts>;
}

export type Cursor = readonly [createdAt: string, id: string];

export const actionRoute = (actionId: string): string => `/assistant/proposals/${actionId}`;

export function encodeCursor(cursor: Cursor): string {
  return Buffer.from(JSON.stringify(cursor)).toString("base64url");
}

export function decodeCursor(raw: string): Cursor | undefined {
  try {
    const parsed: unknown = JSON.parse(Buffer.from(raw, "base64url").toString("utf8"));
    if (!Array.isArray(parsed) || parsed.length !== 2) return undefined;
    const [createdAt, id] = parsed as unknown[];
    if (typeof createdAt !== "string" || !CURSOR_TIMESTAMP.test(createdAt)) return undefined;
    if (typeof id !== "string" || !UUID.test(id)) return undefined;
    return [createdAt, id];
  } catch {
    return undefined;
  }
}

function amountAboveLimit(p: PendingFact): boolean {
  if (p.currency !== p.selfApprovalCurrency) return false;
  try {
    return compareMoney({ amount: p.amount, currency: p.currency }, { amount: p.selfApprovalLimit, currency: p.selfApprovalCurrency }) > 0;
  } catch {
    return false;
  }
}

export function buildBriefing(facts: BriefingFacts, now: Date, disabled: ReadonlySet<string> = new Set()): BriefingContent {
  const all = Object.fromEntries(AGENT_IDS.map((id) => [id, { pending: 0, decided: 0, committedRuns: 0, failedRuns: 0 }])) as Record<AgentId, AgentCounts>;
  const counts = Object.fromEntries(AGENT_IDS.filter((id) => !disabled.has(id)).map((id) => [id, all[id]]));
  const content: BriefingContent = { changes: [], risks: [], recommendedActions: [], notes: [], counts };
  const horizon = now.getTime() + DAY_MS;
  const enabled = <T extends { agentId: AgentId }>(rows: T[]): T[] => rows.filter((r) => !disabled.has(r.agentId));

  for (const d of enabled(facts.decided)) {
    all[d.agentId].decided += 1;
    content.changes.push({ agentId: d.agentId, factId: d.actionId, text: `${d.tool} was ${d.decision.toLowerCase()} at ${d.decidedAt}` });
  }
  for (const r of enabled(facts.runs)) {
    all[r.agentId][r.status === "FAILED" ? "failedRuns" : "committedRuns"] += 1;
    const item = { agentId: r.agentId, factId: r.runId, text: `Run ${r.runId} ${r.status.toLowerCase()} at ${r.createdAt}` };
    content.changes.push(item);
    if (r.status === "FAILED") content.risks.push(item);
  }
  for (const p of enabled(facts.pending)) {
    all[p.agentId].pending += 1;
    const expiresAt = new Date(Date.parse(p.requestedAt) + (p.expiryHours ?? DEFAULT_EXPIRY_HOURS) * 3_600_000);
    if (expiresAt.getTime() <= horizon) {
      content.risks.push({ agentId: p.agentId, factId: p.actionId, text: `${p.tool} expires at ${expiresAt.toISOString()}` });
    }
    if (amountAboveLimit(p)) {
      content.risks.push({ agentId: p.agentId, factId: p.actionId, text: `${p.tool} amount ${p.amount} ${p.currency} is above the self-approval limit ${p.selfApprovalLimit} ${p.selfApprovalCurrency}` });
    }
    content.recommendedActions.push({
      agentId: p.agentId,
      factId: p.actionId,
      text: `Review ${p.tool} for ${p.amount} ${p.currency}`,
      actionId: p.actionId,
      route: actionRoute(p.actionId),
    });
  }
  for (const id of AGENT_IDS) if (disabled.has(id)) content.notes.push(`${id} is disabled by your administrator`);
  const capped = [facts.pending, facts.decided, facts.runs].some((rows) => rows.length >= FACT_ROW_CAP);
  if (capped) content.notes.push(`Only the first ${FACT_ROW_CAP} items of a list are shown.`);
  return content;
}
