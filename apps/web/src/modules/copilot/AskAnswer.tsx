import { Fragment, useId } from "react";
import { Link } from "react-router-dom";
import { copy } from "../../shell/copy";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button } from "../../shell/ui";
import { AiSuggestion } from "./AiSuggestion";
import { mayBePartial, parseAnswerText } from "./answerText";
import { canInsertDraft, GENERATED_BADGE } from "./domain";
import styles from "./pages.module.css";
import { answerViewModel, citationRoute, leftOutNotice } from "./suggestionModel";
import type { CopilotConversation, Turn } from "./useCopilotConversation";

export interface AskAnswerProps {
  conversation: CopilotConversation;
  onInsertDraft?: ((text: string) => void) | undefined;
}

interface TurnViewProps {
  turn: Turn;
  turnId: string;
  latest: boolean;
  conversation: CopilotConversation;
  onInsertDraft: AskAnswerProps["onInsertDraft"];
}

function TurnView({ turn, turnId, latest, conversation, onInsertDraft }: TurnViewProps) {
  const { pending, partial, withdrawn, retry } = conversation;
  const duties = usePrincipalDuties();
  const { result, error } = turn;
  const response = result?.response;
  const refusal = response?.refusal ?? undefined;
  const tryAgain = latest ? (
    <Button size="sm" onClick={() => void retry()}>
      {copy.answer.tryAgain}
    </Button>
  ) : undefined;
  const streaming = latest && pending;
  return (
    <>
      <h3 className={styles.sectionTitle}>{copy.answer.youAsked(turn.question)}</h3>
      {error === null ? null : (
        <Alert tone="danger" live="alert" action={tryAgain}>
          {error}
        </Alert>
      )}
      {streaming && partial !== "" ? (
        <article aria-label={copy.answer.inProgress} aria-busy="true" className={styles.answer}>
          <div>
            <Badge tone="info">{GENERATED_BADGE}</Badge>
          </div>
          {withdrawn === null ? null : <p className={styles.sectionTitle}>{`${copy.answer.withdrawn} (${withdrawn.code})`}</p>}
          <p className={styles.answerText}>{partial}</p>
        </article>
      ) : null}
      {response === undefined ? null : (
        <article aria-label={copy.answer.finished} className={styles.answer}>
          <AiSuggestion host="answer" suggestion={answerViewModel(response, turnId)}>
            {refusal === undefined ? null : (
              <Alert tone="warning" title={refusal.code} live="alert" action={tryAgain}>
                {copy.assistantStatus.refusals[refusal.code] ?? refusal.message}
              </Alert>
            )}
            {response.answer ? (
              <p className={styles.answerText}>
                {parseAnswerText(response.answer, response.citations).map((segment, i) => {
                  if (segment.kind === "text") return <Fragment key={i}>{segment.text}</Fragment>;
                  if (segment.kind === "unverified") return <Badge key={i} tone="warning">{copy.answer.unverified}</Badge>;
                  return (
                    <a key={i} href={`#${turnId}-source-${segment.n}`} aria-label={`Source ${segment.n}`}>
                      [{segment.n}]
                    </a>
                  );
                })}
              </p>
            ) : null}
            {refusal === undefined && mayBePartial(response) ? (
              <p>
                <Badge tone="warning">{copy.answer.unverified}</Badge> {copy.answer.partial}
              </p>
            ) : null}
            {response.answer ? (
              <>
                <h4 className={styles.sectionTitle}>{copy.answer.sources}</h4>
                {response.citations.length === 0 ? (
                  <p className={styles.muted}>{copy.answer.noSources}</p>
                ) : (
                  <ol aria-label={copy.answer.sources} className={styles.list}>
                    {response.citations.map((c, i) => {
                      const href = citationRoute(c, duties);
                      return (
                        <li key={`${c.sourceRef}-${i}`} id={`${turnId}-source-${i + 1}`}>
                          {href === undefined ? c.label : <Link to={href}>{c.label}</Link>}
                        </li>
                      );
                    })}
                  </ol>
                )}
              </>
            ) : null}
          </AiSuggestion>
          {latest && onInsertDraft && result && canInsertDraft(result.mode, refusal !== undefined) ? (
            <div>
              <Button variant="primary" onClick={() => onInsertDraft(response.answer)}>
                Insert draft
              </Button>
            </div>
          ) : null}
        </article>
      )}
    </>
  );
}

export function AskAnswer({ conversation, onInsertDraft }: AskAnswerProps) {
  const { turns, result, pending } = conversation;
  const baseId = useId();
  const response = result?.response;
  const announcement =
    !pending && response !== undefined && response.refusal == null
      ? [copy.answer.done, leftOutNotice(response.excluded)].filter(Boolean).join(" ")
      : "";
  return (
    <div className={styles.stack}>
      <p role="status" className={styles.muted}>
        {announcement}
      </p>
      <ol aria-label={copy.answer.transcript} className={styles.list}>
        {turns.map((turn, i) => (
          <li key={i}>
            <TurnView
              turn={turn}
              turnId={`${baseId}-${i}`}
              latest={i === turns.length - 1}
              conversation={conversation}
              onInsertDraft={onInsertDraft}
            />
          </li>
        ))}
      </ol>
    </div>
  );
}
