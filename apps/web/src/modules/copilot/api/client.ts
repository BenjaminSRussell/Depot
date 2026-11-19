import { z } from "zod";
import type { CopilotMode } from "../domain";
import { apiFetch } from "../../../shell/api/http";
import { AI_REQUEST_TIMEOUT_MS, withDeadline } from "../../../shell/assistant/availability";

export const COPILOT_TIMEOUT_MESSAGE = "Copilot did not answer in time.";

export class CopilotError extends Error {
  readonly code: string | undefined;
  constructor(message: string, code?: string) {
    super(message);
    this.name = "CopilotError";
    this.code = code;
  }
}

const problemSchema = z.object({ code: z.string() });

export async function failedCopilotCall(res: Response): Promise<CopilotError> {
  let code: string | undefined;
  try {
    code = problemSchema.parse(await res.json()).code;
  } catch {
    code = undefined;
  }
  return new CopilotError(`Copilot is unavailable (HTTP ${res.status}).`, code);
}

/** Response of POST /api/v1/copilot/chat. Validated before any field is rendered. */
export const citationSchema = z.object({
  sourceType: z.enum(["RECORD", "METRIC", "ARTICLE", "DOC", "POLICY"]),
  sourceRef: z.string(),
  label: z.string(),
});

export const toolCallSchema = z.object({
  tool: z.string(),
  status: z.enum(["OK", "DENIED", "ERROR"]),
  code: z.string().optional(),
});

const excludedSchema = z.object({ reason: z.string(), count: z.number() });

export const chatResponseSchema = z.object({
  answer: z.string(),
  citations: z.array(citationSchema),
  toolCalls: z.array(toolCallSchema),
  refusal: z.object({ code: z.string(), message: z.string() }).nullable().optional(),
  dataClass: z.string().optional(),
  excluded: z.union([z.array(excludedSchema), excludedSchema]).optional(),
  generated: z.literal(true),
});

export type ChatResponse = z.infer<typeof chatResponseSchema>;

export interface ChatRequest {
  mode: CopilotMode;
  message: string;
  recordRef?: string;
}

export async function postChat(req: ChatRequest, fetchFn: typeof fetch = apiFetch): Promise<ChatResponse> {
  const deadline = withDeadline(AI_REQUEST_TIMEOUT_MS);
  try {
    const res = await fetchFn("/api/v1/copilot/chat", {
      method: "POST",
      cache: "no-store",
      headers: { "content-type": "application/json", accept: "application/json" },
      body: JSON.stringify(req),
      signal: deadline.signal,
    });
    if (!res.ok) throw await failedCopilotCall(res);
    return chatResponseSchema.parse(await res.json());
  } catch (err) {
    throw deadline.expired() ? new CopilotError(COPILOT_TIMEOUT_MESSAGE) : err;
  } finally {
    deadline.clear();
  }
}
