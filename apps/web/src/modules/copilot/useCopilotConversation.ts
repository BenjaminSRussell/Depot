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
  onWithdrawn?: (message: string, code: string) => void,
  fetchFn?: typeof fetch,
  conversationId?: string,
) => Promise<ChatResponse>;

export interface CopilotResult {
  mode: CopilotMode;
  response: ChatResponse;
}

export interface Turn {
  question: string;
  result: CopilotResult | null;
  error: string | null;
}

export interface Withdrawn {
  code: string;
  message: string;
}

export interface CopilotConversation {
  turns: Turn[];
  withdrawn: Withdrawn | null;
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
  const [turns, setTurns] = useState<Turn[]>([]);
  const [withdrawn, setWithdrawn] = useState<Withdrawn | null>(null);
  const [pending, setPending] = useState(false);
  const [partial, setPartial] = useState("");
  const conversationId = useRef<string | undefined>(undefined);
  const latest = turns.at(-1);
  const busy = useRef(false);
  const last = useRef<Sent | undefined>(undefined);
  const { reportRefusal, reportFailure } = useAiAvailability();

  function settleLatest(outcome: Pick<Turn, "result" | "error">) {
    setTurns((all) => all.map((turn, i) => (i === all.length - 1 ? { ...turn, ...outcome } : turn)));
  }

  async function send(text: string, mode: CopilotMode = "ask", recordRef?: string, replaceLatest = false) {
    if (text === "" || busy.current) return;
    busy.current = true;
    last.current = { text, mode, recordRef };
    setPending(true);
    setWithdrawn(null);
    setTurns((all) => [...(replaceLatest ? all.slice(0, -1) : all), { question: text, result: null, error: null }]);
    setPartial("");
    try {
      conversationId.current ??= await createConversation();
      const response = await ask(
        { mode, message: text, ...(recordRef ? { recordRef } : {}) },
        (token) => setPartial((so) => so + token),
        (message, code) => {
          setPartial(message);
          setWithdrawn({ code, message });
        },
        undefined,
        conversationId.current,
      );
      settleLatest({ result: { mode, response }, error: null });
      const code = response.refusal?.code;
      if (code === BUDGET_REFUSAL_CODE) reportRefusal(code);
      else if (code !== undefined) reportFailure();
    } catch (err) {
      if (err instanceof CopilotError && err.code === BUDGET_REFUSAL_CODE) {
        settleLatest({
          result: { mode, response: { answer: "", citations: [], toolCalls: [], refusal: { code: err.code, message: err.message }, generated: true } },
          error: null,
        });
        reportRefusal(err.code);
      } else {
        settleLatest({ result: null, error: err instanceof Error ? err.message : copy.answer.failed });
        reportFailure();
      }
    } finally {
      setPartial("");
      setWithdrawn(null);
      setPending(false);
      busy.current = false;
    }
  }

  async function retry() {
    const { current } = last;
    if (current) await send(current.text, current.mode, current.recordRef, latest?.error != null || latest?.result?.response.refusal != null);
  }

  return { turns, withdrawn, result: latest?.result ?? null, error: latest?.error ?? null, pending, partial, send, retry };
}
