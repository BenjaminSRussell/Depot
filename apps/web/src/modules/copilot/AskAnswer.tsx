import { useId } from "react";
import { Link } from "react-router-dom";
import { copy } from "../../shell/copy";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button } from "../../shell/ui";
import { AiSuggestion } from "./AiSuggestion";
import { canInsertDraft, GENERATED_BADGE } from "./domain";
import styles from "./pages.module.css";
import { answerViewModel, citationRoute } from "./suggestionModel";
import type { CopilotConversation } from "./useCopilotConversation";

export interface AskAnswerProps {
  conversation: CopilotConversation;
  onInsertDraft?: ((text: string) => void) | undefined;
}

export function AskAnswer({ conversation, onInsertDraft }: AskAnswerProps) {
  const { result, error, pending, partial, retry } = conversation;
  const duties = usePrincipalDuties();
  const turnId = useId();
  const response = result?.response;
  const refusal = response?.refusal ?? undefined;
  const tryAgain = (
    <Button size="sm" onClick={() => void retry()}>
      {copy.answer.tryAgain}
    </Button>
  );
  return (
    <div className={styles.stack}>
      <p role="status" className={styles.muted}>
        {!pending && response !== undefined && refusal === undefined ? copy.answer.done : ""}
      </p>
      {error === null ? null : (
        <Alert tone="danger" live="alert" action={tryAgain}>
          {error}
        </Alert>
      )}
      {pending && partial !== "" ? (
        <article aria-label={copy.answer.inProgress} aria-busy="true" className={styles.answer}>
          <div>
            <Badge tone="info">{GENERATED_BADGE}</Badge>
          </div>
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
            {response.answer ? <p className={styles.answerText}>{response.answer}</p> : null}
            {response.answer ? (
              <>
                <h4 className={styles.sectionTitle}>{copy.answer.sources}</h4>
                {response.citations.length === 0 ? (
                  <p className={styles.muted}>{copy.answer.noSources}</p>
                ) : (
                  <ol aria-label={copy.answer.sources} className={styles.list}>
                    {response.citations.map((c, i) => {
                      const href = citationRoute(c, duties);
                      return <li key={`${c.sourceRef}-${i}`}>{href === undefined ? c.label : <Link to={href}>{c.label}</Link>}</li>;
                    })}
                  </ol>
                )}
              </>
            ) : null}
          </AiSuggestion>
          {onInsertDraft && result && canInsertDraft(result.mode, refusal !== undefined) ? (
            <div>
              <Button variant="primary" onClick={() => onInsertDraft(response.answer)}>
                Insert draft
              </Button>
            </div>
          ) : null}
        </article>
      )}
    </div>
  );
}
