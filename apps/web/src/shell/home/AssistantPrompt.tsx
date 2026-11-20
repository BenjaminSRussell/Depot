import { useEffect, useRef, useState, type FormEvent } from "react";
import type { ChooserOptions, PromptKind, PromptMode, RouterOutcome } from "../assistant/router";
import { copy } from "../copy";
import { Button, FormField, Input } from "../ui";
import styles from "./home.module.css";

export interface AssistantPromptProps {
  onSubmit: (text: string, mode?: PromptMode) => Promise<RouterOutcome | void>;
  canAct?: boolean;
  suggestions?: readonly string[];
}

const MODE_ORDER = ["auto", "ask", "navigate", "act"] as const;
type SelectedMode = (typeof MODE_ORDER)[number];

function noticeFor(outcome: RouterOutcome | void): string {
  switch (outcome?.status) {
    case "blocked":
      return copy.home.assistantBlocked;
    case "clarify":
      return copy.home.assistantClarify;
    case "search":
      return copy.home.assistantSearchNote;
    default:
      return "";
  }
}

export function AssistantPrompt({ onSubmit, canAct = false, suggestions = [] }: AssistantPromptProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const sending = useRef(false);
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState("");
  const [blocked, setBlocked] = useState(false);
  const [mode, setMode] = useState<SelectedMode>("auto");
  const [choices, setChoices] = useState<ChooserOptions | null>(null);

  useEffect(() => {
    const held = document.activeElement;
    if (held === null || held === document.body) inputRef.current?.focus();
  }, []);

  async function send(trimmed: string, mode?: PromptMode) {
    if (sending.current) return;
    sending.current = true;
    setError(null);
    setNotice("");
    setBlocked(false);
    setChoices(null);
    setPending(true);
    try {
      const outcome = await onSubmit(trimmed, mode);
      if (outcome?.status === "refused") setError(copy.home.assistantRefused(outcome.code, outcome.message));
      setNotice(noticeFor(outcome));
      setBlocked(outcome?.status === "blocked");
      setChoices(outcome?.status === "clarify" ? outcome.options : null);
    } catch {
      setError(copy.home.assistantFailed);
    } finally {
      sending.current = false;
      setPending(false);
    }
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const trimmed = text.trim();
    if (trimmed === "") {
      setError(copy.home.assistantEmpty);
      inputRef.current?.focus();
      return;
    }
    void send(trimmed, mode === "auto" ? undefined : mode);
  }

  return (
    <form className={styles.prompt} onSubmit={submit} noValidate>
      <div className={styles.promptRow}>
        <div className={styles.promptField}>
          <FormField id="home-assistant" label={copy.home.assistantLabel} hint={copy.home.assistantHint} error={error ?? undefined}>
            {(a11y) => (
              <Input
                {...a11y}
                ref={inputRef}
                type="text"
                autoComplete="off"
                className={styles.promptControl}
                value={text}
                onChange={(e) => {
                  setText(e.target.value);
                  if (error !== null) setError(null);
                }}
              />
            )}
          </FormField>
        </div>
        <Button type="submit" variant="primary" loading={pending} className={styles.promptSend}>
          {copy.home.assistantSend}
        </Button>
      </div>
      {suggestions.length > 0 ? (
        <div role="group" aria-label={copy.home.suggestionsTitle} className={styles.suggestions}>
          <span aria-hidden="true" className={styles.suggestionsTitle}>
            {copy.home.suggestionsTitle}
          </span>
          {suggestions.map((suggestion) => (
            <Button
              key={suggestion}
              type="button"
              size="sm"
              onClick={() => {
                setText(suggestion);
                setError(null);
                inputRef.current?.focus();
              }}
            >
              {suggestion}
            </Button>
          ))}
        </div>
      ) : null}
      <fieldset className={styles.modes}>
        <legend>{copy.home.assistantModeLegend}</legend>
        {MODE_ORDER.map((m) => (
          <span key={m} className={styles.mode}>
            <input id={`assistant-mode-${m}`} type="radio" name="assistant-mode" value={m} checked={mode === m} disabled={m === "act" && !canAct} onChange={() => setMode(m)} />
            <label htmlFor={`assistant-mode-${m}`}>{copy.home.assistantModes[m]}</label>
          </span>
        ))}
      </fieldset>
      <p role="status" aria-live="polite" className={styles.status}>
        {pending ? copy.home.assistantLoading : notice}
      </p>
      {choices !== null ? (
        <div className={styles.choices}>
          {choices.map((kind: PromptKind) => (
            <Button key={kind} type="button" onClick={() => void send(text.trim(), kind)}>
              {copy.home.assistantChoices[kind]}
            </Button>
          ))}
        </div>
      ) : null}
      {blocked ? (
        <Button type="button" onClick={() => void send(text.trim(), "navigate")}>
          {copy.home.assistantBlockedAction}
        </Button>
      ) : null}
    </form>
  );
}
