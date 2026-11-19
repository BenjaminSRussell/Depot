import { useEffect, useRef, useState, type FormEvent } from "react";
import type { PromptMode, RouterOutcome } from "../assistant/router";
import { copy } from "../copy";
import { Button, FormField, Input } from "../ui";
import styles from "./home.module.css";

export interface AssistantPromptProps {
  onSubmit: (text: string, mode?: PromptMode) => Promise<RouterOutcome | void>;
}

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

export function AssistantPrompt({ onSubmit }: AssistantPromptProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const sending = useRef(false);
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const [notice, setNotice] = useState("");
  const [blocked, setBlocked] = useState(false);

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
    setPending(true);
    try {
      const outcome = await onSubmit(trimmed, mode);
      if (outcome?.status === "refused") setError(copy.home.assistantRefused(outcome.code, outcome.message));
      setNotice(noticeFor(outcome));
      setBlocked(outcome?.status === "blocked");
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
    void send(trimmed);
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
      <p role="status" aria-live="polite" className={styles.status}>
        {pending ? copy.home.assistantLoading : notice}
      </p>
      {blocked ? (
        <Button type="button" onClick={() => void send(text.trim(), "navigate")}>
          {copy.home.assistantBlockedAction}
        </Button>
      ) : null}
    </form>
  );
}
