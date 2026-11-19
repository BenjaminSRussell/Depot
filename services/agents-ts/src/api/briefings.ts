import { AGENT_INVOKE_DUTY } from "../agents/catalog.js";
import { decodeCursor, type BriefingPage, type StoredBriefing } from "../domain/briefing.js";
import type { BriefingService } from "../runtime/briefing.js";
import { ApiError, requireDuty, type Principal } from "./principal.js";

export const BRIEFING_READ_DUTY = "agents.run.read";
const DEFAULT_LIMIT = 20;
const MAX_LIMIT = 100;

export interface BriefingView {
  id: string;
  status: StoredBriefing["status"];
  narrative: boolean;
  createdAt: string;
  completedAt: string | null;
  readAt: string | null;
  changes: unknown[];
  risks: unknown[];
  recommendedActions: unknown[];
  notes: string[];
}

export interface BriefingListView {
  items: BriefingView[];
  nextCursor: BriefingPage["nextCursor"];
  nextScheduledAt: null;
}

const view = (b: StoredBriefing): BriefingView => ({
  id: b.id,
  status: b.status,
  narrative: b.narrative,
  createdAt: b.createdAt,
  completedAt: b.completedAt,
  readAt: b.readAt,
  changes: b.content?.changes ?? [],
  risks: b.content?.risks ?? [],
  recommendedActions: b.content?.recommendedActions ?? [],
  notes: b.content?.notes ?? [],
});

const notFound = (): ApiError => new ApiError(404, "AGT-0404", "no such briefing");

export function parseListQuery(params: URLSearchParams): { limit: number; cursor: ReturnType<typeof decodeCursor> } {
  const rawLimit = params.get("limit");
  const limit = rawLimit === null ? DEFAULT_LIMIT : /^[0-9]{1,3}$/.test(rawLimit) ? Number(rawLimit) : 0;
  if (limit < 1 || limit > MAX_LIMIT) throw new ApiError(400, "AGT-0400", `limit must be an integer from 1 to ${MAX_LIMIT}`);
  const rawCursor = params.get("cursor");
  const cursor = rawCursor === null ? undefined : decodeCursor(rawCursor);
  if (rawCursor !== null && cursor === undefined) throw new ApiError(400, "AGT-0400", "cursor is not valid");
  return { limit, cursor };
}

export async function handleGenerate(args: { principal: Principal; briefings: BriefingService }): Promise<{ id: string; status: "RUNNING" }> {
  requireDuty(args.principal, AGENT_INVOKE_DUTY, "briefing generation");
  return args.briefings.generate(args.principal);
}

export async function handleList(args: { principal: Principal; params: URLSearchParams; briefings: BriefingService }): Promise<BriefingListView> {
  requireDuty(args.principal, BRIEFING_READ_DUTY, "the briefings");
  const { limit, cursor } = parseListQuery(args.params);
  const page = await args.briefings.list(args.principal, limit, cursor);
  return { items: page.items.map(view), nextCursor: page.nextCursor, nextScheduledAt: null };
}

export async function handleGet(args: { principal: Principal; id: string; briefings: BriefingService }): Promise<BriefingView> {
  requireDuty(args.principal, BRIEFING_READ_DUTY, "the briefings");
  const found = await args.briefings.get(args.principal, args.id);
  if (found === undefined) throw notFound();
  return view(found);
}

export async function handleMarkRead(args: { principal: Principal; id: string; briefings: BriefingService }): Promise<BriefingView> {
  requireDuty(args.principal, BRIEFING_READ_DUTY, "the briefings");
  const found = await args.briefings.markRead(args.principal, args.id);
  if (found === undefined) throw notFound();
  return view(found);
}
