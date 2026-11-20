import { z } from "zod";
import { apiFetch } from "../../../shell/api/http";
import { AI_REQUEST_TIMEOUT_MS, withDeadline } from "../../../shell/assistant/availability";
import { COPILOT_TIMEOUT_MESSAGE, CopilotError, failedCopilotCall } from "./client";

const BRIEFINGS_PATH = "/api/v1/agents/briefings";
const PROPOSALS_PREFIX = "/assistant/proposals/";
const UUID = /^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i;

export const BRIEFING_AGENT_IDS = ["ap_coding", "cash_app", "mrp_triage", "collections", "anomaly"] as const;

const briefingItemSchema = z.strictObject({
  agentId: z.enum(BRIEFING_AGENT_IDS),
  factId: z.string(),
  text: z.string(),
  actionId: z.string().optional(),
  route: z.string().optional(),
});

export const briefingSchema = z.strictObject({
  id: z.string(),
  status: z.enum(["RUNNING", "READY", "FAILED"]),
  narrative: z.boolean(),
  createdAt: z.iso.datetime(),
  completedAt: z.iso.datetime().nullable(),
  readAt: z.iso.datetime().nullable(),
  changes: z.array(briefingItemSchema),
  risks: z.array(briefingItemSchema),
  recommendedActions: z.array(briefingItemSchema),
  notes: z.array(z.string()),
});

export const briefingListSchema = z.strictObject({
  items: z.array(briefingSchema),
  nextCursor: z.string().nullable(),
  nextScheduledAt: z.null(),
});

export type Briefing = z.infer<typeof briefingSchema>;
export type BriefingItem = z.infer<typeof briefingItemSchema>;
export type BriefingList = z.infer<typeof briefingListSchema>;

export function briefingItemRoute(item: BriefingItem): string | undefined {
  const fromRoute = item.route?.startsWith(PROPOSALS_PREFIX) ? item.route.slice(PROPOSALS_PREFIX.length) : undefined;
  const approvalId = [fromRoute, item.actionId].find((id) => id !== undefined && UUID.test(id));
  return approvalId === undefined ? undefined : `${PROPOSALS_PREFIX}${approvalId}`;
}

export async function listBriefings(signal?: AbortSignal, fetchFn: typeof fetch = apiFetch): Promise<BriefingList> {
  const deadline = withDeadline(AI_REQUEST_TIMEOUT_MS, signal);
  try {
    const res = await fetchFn(`${BRIEFINGS_PATH}?limit=20`, {
      cache: "no-store",
      headers: { accept: "application/json" },
      signal: deadline.signal,
    });
    if (!res.ok) throw await failedCopilotCall(res);
    return briefingListSchema.parse(await res.json());
  } catch (err) {
    throw deadline.expired() ? new CopilotError(COPILOT_TIMEOUT_MESSAGE) : err;
  } finally {
    deadline.clear();
  }
}
