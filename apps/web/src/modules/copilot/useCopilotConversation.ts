import { useRef, useState } from "react";
import { BUDGET_REFUSAL_CODE } from "../../shell/assistant/availability";
import { useAiAvailability } from "../../shell/assistant/AvailabilityProvider";
import { copy } from "../../shell/copy";
import { CopilotError, createConversation as postConversation, type ChatRequest, type ChatResponse } from "./api/client";
import { askCopilot } from "./api/stream";
import type { CopilotMode } from "./domain";

export type AskFn = (
  req: ChatRequest,
  onToken?: (text: string) => void,
  onWithdrawn?: (message: string) => void,
  fetchFn?: typeof fetch,
  conversationId?: string,
) => Promise<ChatResponse>;

export interface CopilotResult {
  mode: CopilotMode;
  response: ChatResponse;
}

export interface CopilotConversation {
  result: CopilotResult | null;
  error: string | null;
  pending: boolean;
  partial: string;
  send: (text: string, mode?: CopilotMode, recordRef?: string) => Promise<void>;
  retry: () => Promise<void>;
}

interface Sent {
  text: string;
  mode: CopilotMode;
  recordRef: string | undefined;
}

export function useCopilotConversation({
  ask = askCopilot,
  createConversation = postConversation,
}: { ask?: AskFn | undefined; createConversation?: (() => Promise<string>) | undefined } = {}): CopilotConversation {
  const [result, setResult] = useState<CopilotResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [partial, setPartial] = useState("");
  const conversationId = useRef<string | undefined>(undefined);
  const busy = useRef(false);
  const last = useRef<Sent | undefined>(undefined);
  const { reportRefusal, reportFailure } = useAiAvailability();

  async function send(text: string, mode: CopilotMode = "ask", recordRef?: string) {
    if (text === "" || busy.current) return;
    busy.current = true;
    last.current = { text, mode, recordRef };
    setPending(true);
    setError(null);
    setResult(null);
    setPartial("");
    try {
      conversationId.current ??= await createConversation();
      const response = await ask(
        { mode, message: text, ...(recordRef ? { recordRef } : {}) },
        (token) => setPartial((so) => so + token),
        (safeMessage) => setPartial(safeMessage),
        undefined,
        conversationId.current,
      );
      setResult({ mode, response });
      const code = response.refusal?.code;
      if (code === BUDGET_REFUSAL_CODE) reportRefusal(code);
      else if (code !== undefined) reportFailure();
    } catch (err) {
      if (err instanceof CopilotError && err.code === BUDGET_REFUSAL_CODE) {
        setResult({ mode, response: { answer: "", citations: [], toolCalls: [], refusal: { code: err.code, message: err.message }, generated: true } });
        reportRefusal(err.code);
      } else {
        setResult(null);
        setError(err instanceof Error ? err.message : copy.answer.failed);
        reportFailure();
      }
    } finally {
      setPartial("");
      setPending(false);
      busy.current = false;
    }
  }

  async function retry() {
    if (last.current) await send(last.current.text, last.current.mode, last.current.recordRef);
  }

  return { result, error, pending, partial, send, retry };
}
