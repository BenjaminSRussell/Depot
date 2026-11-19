import { useEffect, useRef, useState, type FormEvent } from "react";
import { copy } from "../copy";
import { Button, FormField, Input } from "../ui";
import styles from "./home.module.css";

export interface AssistantPromptProps {
  onSubmit: (text: string) => Promise<void>;
}

export function AssistantPrompt({ onSubmit }: AssistantPromptProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const sending = useRef(false);
  const [text, setText] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  useEffect(() => {
    const held = document.activeElement;
    if (held === null || held === document.body) inputRef.current?.focus();
  }, []);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (sending.current) return;
    const trimmed = text.trim();
    if (trimmed === "") {
      setError(copy.home.assistantEmpty);
      inputRef.current?.focus();
      return;
    }
    sending.current = true;
    setError(null);
    setPending(true);
    try {
      await onSubmit(trimmed);
    } catch {
      setError(copy.home.assistantFailed);
    } finally {
      sending.current = false;
      setPending(false);
    }
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
        {pending ? copy.home.assistantLoading : ""}
      </p>
    </form>
  );
}
