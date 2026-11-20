import { useId, useRef, useState, type KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router-dom";
import { AskAnswer } from "../../modules/copilot/AskAnswer";
import { useCopilotConversation, type AskFn } from "../../modules/copilot/useCopilotConversation";
import { useAiAvailability } from "../assistant/AvailabilityProvider";
import { copy } from "../copy";
import { holdsDuty } from "../dutyGate";
import { useFocusTrap } from "../hooks/useFocusTrap";
import { ASSISTANT_DUTY } from "../home/assistantRouter";
import { usePrincipalDuties } from "../principal";
import { Alert, Button, buttonClassName } from "../ui";
import styles from "./AskAboutRecord.module.css";
import { ASSIST_ACTIONS, buildRecordRef, missingParts, recordAssistEntry, type AssistAction, type RecordAssistEntry } from "./recordAssist";

export interface AskAboutRecordProps {
  entity: string;
  id: string;
  ask?: AskFn | undefined;
  createConversation?: (() => Promise<string>) | undefined;
}

const ACTION_ORDER = Object.keys(ASSIST_ACTIONS) as AssistAction[];

interface PanelProps {
  entry: RecordAssistEntry;
  id: string;
  recordRef: string;
  onClose: () => void;
  ask: AskFn | undefined;
  createConversation: (() => Promise<string>) | undefined;
}

function AssistPanel({ entry, id, recordRef, onClose, ask, createConversation }: PanelProps) {
  const titleId = useId();
  const panel = useRef<HTMLDivElement>(null);
  const duties = usePrincipalDuties();
  const conversation = useCopilotConversation({ ask, createConversation });
  const [action, setAction] = useState<AssistAction | undefined>(undefined);
  useFocusTrap(panel, true);

  const response = conversation.pending ? undefined : conversation.result?.response;
  const missing = action !== undefined && response !== undefined ? missingParts(action, response.toolCalls) : [];
  const links = entry.nextLinks.filter((link) => holdsDuty(duties, link.duty));

  function run(chosen: AssistAction) {
    setAction(chosen);
    void conversation.send(copy.recordAssist.actions[chosen].message, "explain", recordRef);
  }

  function onKeyDown(event: KeyboardEvent) {
    if (event.key === "Escape") onClose();
  }

  return createPortal(
    <>
      <div className={styles.scrim} onClick={onClose} aria-hidden="true" />
      <div ref={panel} role="dialog" aria-modal="true" aria-labelledby={titleId} className={styles.panel} onKeyDown={onKeyDown}>
        <div className={styles.header}>
          <h2 id={titleId} className={styles.title}>
            {copy.recordAssist.panelTitle(entry.entityLabel)}
          </h2>
          <Button variant="ghost" size="sm" onClick={onClose}>
            {copy.recordAssist.close}
          </Button>
        </div>
        <div role="group" aria-label={copy.recordAssist.actionsLabel} className={styles.actions}>
          {ACTION_ORDER.map((name) => (
            <Button key={name} size="sm" disabled={conversation.pending} onClick={() => run(name)}>
              {copy.recordAssist.actions[name].label}
            </Button>
          ))}
        </div>
        {conversation.turns.length === 0 ? null : <AskAnswer conversation={conversation} />}
        {missing.length === 0 ? null : (
          <Alert tone="warning" title={copy.recordAssist.missingTitle} live="status">
            <ul className={styles.list}>
              {missing.map((tool) => (
                <li key={tool}>{copy.recordAssist.missing[tool]}</li>
              ))}
            </ul>
          </Alert>
        )}
        {action === "next" && response !== undefined && response.refusal == null && links.length > 0 ? (
          <section aria-label={copy.recordAssist.nextHeading} className={styles.next}>
            <h3 className={styles.nextTitle}>{copy.recordAssist.nextHeading}</h3>
            <p className={styles.note}>{copy.recordAssist.nextNote}</p>
            {/* TODO DEPOT-281: proposal-start button once the act handler is registered */}
            <ul className={styles.list}>
              {links.map((link) => (
                <li key={link.label}>
                  <Link className={buttonClassName("secondary", "sm")} to={link.to(id)}>
                    {link.label}
                  </Link>
                </li>
              ))}
            </ul>
          </section>
        ) : null}
      </div>
    </>,
    document.body,
  );
}

export function AskAboutRecord({ entity, id, ask, createConversation }: AskAboutRecordProps) {
  const { state } = useAiAvailability();
  const duties = usePrincipalDuties();
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const entry = recordAssistEntry(entity);
  const recordRef = buildRecordRef(entity, id);

  if (entry === undefined || recordRef === undefined || state.status !== "enabled" || !holdsDuty(duties, ASSISTANT_DUTY)) return null;

  function close() {
    setOpen(false);
    trigger.current?.focus();
  }

  return (
    <>
      <Button ref={trigger} aria-haspopup="dialog" onClick={() => setOpen(true)}>
        {copy.recordAssist.button}
      </Button>
      {open ? <AssistPanel entry={entry} id={id} recordRef={recordRef} onClose={close} ask={ask} createConversation={createConversation} /> : null}
    </>
  );
}
