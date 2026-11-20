import { z } from "zod";
import { defaultFetch, readProblem, type FetchLike } from "../../../shell/api/http";

export const approvalPath = (actionId: string) => `/api/v1/agents/approvals/${encodeURIComponent(actionId)}`;

export class ApprovalNotFound extends Error {
  constructor() {
    super("approval not found");
    this.name = "ApprovalNotFound";
  }
}

export const approvalSchema = z.object({
  runId: z.string(),
  actionId: z.string(),
  tool: z.string(),
  state: z.enum(["PENDING", "APPROVED", "REJECTED", "EXPIRED"]),
  amount: z.object({ amount: z.string().regex(/^-?\d+(\.\d+)?$/), currency: z.string().regex(/^[A-Z]{3}$/) }),
  requestedAt: z.iso.datetime(),
  expiresAt: z.iso.datetime(),
  decided: z.object({ decision: z.enum(["APPROVED", "REJECTED"]), reason: z.literal("EXPIRED").optional(), decidedAt: z.iso.datetime() }).optional(),
});

export type Approval = z.infer<typeof approvalSchema>;

export async function getApproval(actionId: string, signal?: AbortSignal, fetchFn: FetchLike = defaultFetch): Promise<Approval> {
  const res = await fetchFn(approvalPath(actionId), { cache: "no-store", headers: { accept: "application/json" }, ...(signal ? { signal } : {}) });
  if (res.status === 404) throw new ApprovalNotFound();
  if (!res.ok) throw await readProblem(res);
  const parsed = approvalSchema.safeParse(await res.json());
  if (!parsed.success) throw new Error("The agent runtime sent a response we couldn't read.");
  return parsed.data;
}
