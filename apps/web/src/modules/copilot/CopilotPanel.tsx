import { useEffect, useRef, useState, type FormEvent } from "react";
import { BUDGET_REFUSAL_CODE } from "../../shell/assistant/availability";
import { useAiAvailability } from "../../shell/assistant/AvailabilityProvider";
import { copy } from "../../shell/copy";
import { Alert, Badge, Button, Card, EmptyState, FormField, Textarea } from "../../shell/ui";
import { CopilotError, type ChatRequest, type ChatResponse } from "./api/client";
import { askCopilot } from "./api/stream";
import {
  canInsertDraft,
  COPILOT_MODES,
  GENERATED_BADGE,
  MODE_LABELS,
  readRememberedMode,
  rememberMode,
  toolCallLabel,
  type CopilotMode,
  type ModeStore,
} from "./domain";
import styles from "./pages.module.css";

export interface CopilotPanelProps {
  /**
   * Injected for tests; defaults to the BFF chat endpoint, which may stream.
   * `onToken` receives each streamed piece of the answer as it arrives.
   */
  ask?: (req: ChatRequest, onToken?: (text: string) => void, onWithdrawn?: (message: string) => void) => Promise<ChatResponse>;
  /** Record the user is looking at, sent as context for explain and summarize. */
  recordRef?: string;
  /** Called only when the user clicks "Insert draft". Never called automatically. */
  onInsertDraft?: (text: string) => void;
  initialMode?: CopilotMode;
  initialMessage?: string;
  /**
   * When set, the chosen mode is remembered per user and workspace in `store`
   * (9.47.6) and restored the next time the panel opens there.
   */
  memory?: { store: ModeStore; userId: string; workspace: string };
}

interface Result {
  mode: CopilotMode;
  response: ChatResponse;
}

const PROMPT_HINTS: Readonly<Record<CopilotMode, string>> = {
  ask: "For example: what was revenue last month against the month before?",
  explain: "Name the record, or open the copilot from it, and ask why it looks the way it does.",
  draft: "Say what to draft: a dunning letter, an e-mail or PO text. Nothing is inserted until you click Insert draft.",
  summarize: "Name the workflow or case to summarize.",
};

export function CopilotPanel({ ask = askCopilot, recordRef, onInsertDraft, initialMode = "ask", initialMessage, memory }: CopilotPanelProps) {
  const [mode, setModeState] = useState<CopilotMode>(
    () => (memory ? readRememberedMode(memory.store, memory.userId, memory.workspace) : undefined) ?? initialMode,
  );
  function setMode(next: CopilotMode) {
    setModeState(next);
    if (memory) rememberMode(memory.store, memory.userId, memory.workspace, next);
  }
  const [message, setMessage] = useState("");
  const [result, setResult] = useState<Result | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  /** The answer so far while the server streams it (FR-47.3). */
  const [partial, setPartial] = useState("");
  const { reportRefusal, reportFailure } = useAiAvailability();

  async function send(text: string) {
    if (text === "" || pending) return;
    setPending(true);
    setError(null);
    setResult(null);
    setPartial("");
    try {
      const response = await ask(
        { mode, message: text, ...(recordRef ? { recordRef } : {}) },
        (token) => setPartial((so) => so + token),
        (safeMessage) => setPartial(safeMessage),
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
        setError(err instanceof Error ? err.message : "Copilot is unavailable.");
        reportFailure();
      }
    } finally {
      setPartial("");
      setPending(false);
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void send(message.trim());
  }

  const sentInitial = useRef(false);
  useEffect(() => {
    if (sentInitial.current || !initialMessage?.trim()) return;
    sentInitial.current = true;
    void send(initialMessage.trim());
  }, []);

  return (
    <Card title="Conversation">
      <div className={styles.stack}>
        <div role="group" aria-label="Copilot mode" className={styles.modes}>
          {COPILOT_MODES.map((m) => (
            <Button key={m} size="sm" variant={m === mode ? "primary" : "secondary"} aria-pressed={m === mode} onClick={() => setMode(m)}>
              {MODE_LABELS[m]}
            </Button>
          ))}
        </div>
        <form className={styles.ask} onSubmit={submit}>
          <FormField id="copilot-input" label="Ask Copilot" hint={PROMPT_HINTS[mode]}>
            {(a11y) => <Textarea {...a11y} rows={3} value={message} onChange={(e) => setMessage(e.target.value)} />}
          </FormField>
          <div className={styles.askActions}>
            <Button type="submit" variant="primary" loading={pending}>
              Send
            </Button>
            {pending ? <p className={styles.muted}>Copilot is working on your answer…</p> : null}
          </div>
        </form>
        <Alert tone="danger" live="alert">
          {error}
        </Alert>
        {pending && partial !== "" ? (
          <article aria-label="Copilot answer in progress" aria-busy="true" className={styles.answer}>
            <div>
              <Badge tone="info">{GENERATED_BADGE}</Badge>
            </div>
            <p className={styles.answerText}>{partial}</p>
          </article>
        ) : null}
        {result ? (
          <Answer result={result} onInsertDraft={onInsertDraft} />
        ) : !pending && !error ? (
          <EmptyState
            compact
            title="No answer yet"
            description="Pick a mode, ask a question and press Send. Every answer lists the tools it used and the sources it cites."
          />
        ) : null}
      </div>
    </Card>
  );
}

function Answer({ result, onInsertDraft }: { result: Result; onInsertDraft?: ((text: string) => void) | undefined }) {
  const { mode, response } = result;
  const refused = response.refusal != null;
  return (
    <article aria-label="Copilot answer" className={styles.answer}>
      <div>
        <Badge tone="info">{GENERATED_BADGE}</Badge>
      </div>
      {response.refusal ? (
        <Alert tone="warning" title={response.refusal.code} live="alert">
          {copy.assistantStatus.refusals[response.refusal.code] ?? response.refusal.message}
        </Alert>
      ) : null}
      {response.answer ? <p className={styles.answerText}>{response.answer}</p> : null}
      <h4 className={styles.sectionTitle}>Tool calls</h4>
      {response.toolCalls.length > 0 ? (
        <ul aria-label="Tool calls" className={`${styles.list} ${styles.mono}`}>
          {response.toolCalls.map((call, i) => (
            <li key={`${call.tool}-${i}`}>{toolCallLabel(call)}</li>
          ))}
        </ul>
      ) : (
        <p className={styles.muted}>No tool calls.</p>
      )}
      {response.citations.length > 0 ? (
        <>
          <h4 className={styles.sectionTitle}>Sources</h4>
          <ol aria-label="Citations" className={styles.list}>
            {response.citations.map((c) => (
              <li key={`${c.sourceType}-${c.sourceRef}`}>
                {c.label} ({c.sourceType}: <span className={styles.mono}>{c.sourceRef}</span>)
              </li>
            ))}
          </ol>
        </>
      ) : null}
      {onInsertDraft && canInsertDraft(mode, refused) ? (
        <div>
          <Button variant="primary" onClick={() => onInsertDraft(response.answer)}>
            Insert draft
          </Button>
        </div>
      ) : null}
    </article>
  );
}
