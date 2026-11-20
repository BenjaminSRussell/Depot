import { useEffect, useRef, useState, type FormEvent } from "react";
import { Button, Card, EmptyState, FormField, Textarea } from "../../shell/ui";
import { AskAnswer } from "./AskAnswer";
import { COPILOT_MODES, MODE_LABELS, readRememberedMode, rememberMode, type CopilotMode, type ModeStore } from "./domain";
import styles from "./pages.module.css";
import { useCopilotConversation, type AskFn } from "./useCopilotConversation";

export interface CopilotPanelProps {
  ask?: AskFn;
  createConversation?: () => Promise<string>;
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

const PROMPT_HINTS: Readonly<Record<CopilotMode, string>> = {
  ask: "For example: what was revenue last month against the month before?",
  explain: "Name the record, or open the copilot from it, and ask why it looks the way it does.",
  draft: "Say what to draft: a dunning letter, an e-mail or PO text. Nothing is inserted until you click Insert draft.",
  summarize: "Name the workflow or case to summarize.",
};

export function CopilotPanel({ ask, createConversation, recordRef, onInsertDraft, initialMode = "ask", initialMessage, memory }: CopilotPanelProps) {
  const [mode, setModeState] = useState<CopilotMode>(
    () => (memory ? readRememberedMode(memory.store, memory.userId, memory.workspace) : undefined) ?? initialMode,
  );
  function setMode(next: CopilotMode) {
    setModeState(next);
    if (memory) rememberMode(memory.store, memory.userId, memory.workspace, next);
  }
  const [message, setMessage] = useState("");
  const conversation = useCopilotConversation({ ask, createConversation });
  const { send, pending, result, error } = conversation;

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void send(message.trim(), mode, recordRef);
  }

  const sentInitial = useRef(false);
  useEffect(() => {
    if (sentInitial.current || !initialMessage?.trim()) return;
    sentInitial.current = true;
    void send(initialMessage.trim(), mode, recordRef);
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
        <AskAnswer conversation={conversation} onInsertDraft={onInsertDraft} />
        {!result && !pending && !error ? (
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
