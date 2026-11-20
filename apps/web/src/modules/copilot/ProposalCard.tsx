import { Link } from "react-router-dom";
import { copy } from "../../shell/copy";
import { formatCurrency, formatDateTime } from "../../shell/format";
import { Card, StatusPill } from "../../shell/ui";
import { AiSuggestion } from "./AiSuggestion";
import type { Approval } from "./api/proposals";
import { toolActionLabel } from "./domain";
import styles from "./pages.module.css";
import { showsAskAgain, stateLabel } from "./proposalModel";
import { agentLabel, type SuggestionViewModel } from "./suggestionModel";

const viewModelOf = (approval: Approval): SuggestionViewModel => ({
  actionId: approval.actionId,
  agentLabel: agentLabel(""),
  rationale: undefined,
  confidencePercent: undefined,
  tools: [toolActionLabel(approval.tool)],
  records: [],
});

export function ProposalCard({ approval }: { approval: Approval }) {
  const { state, decided } = approval;
  return (
    <Card title={copy.proposal.cardTitle}>
      <AiSuggestion host="proposal" suggestion={viewModelOf(approval)}>
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>{copy.proposal.actionTerm}</dt>
            <dd className={styles.value}>{toolActionLabel(approval.tool)}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>{copy.proposal.amountTerm}</dt>
            <dd className={styles.value}>{formatCurrency(approval.amount.amount, approval.amount.currency)}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>{copy.proposal.statusTerm}</dt>
            <dd className={styles.value}>
              <StatusPill status={state} /> {stateLabel(state)}
            </dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>{decided === undefined ? copy.proposal.expiryTerm : copy.proposal.decidedTerm}</dt>
            <dd className={styles.value}>{formatDateTime(decided === undefined ? approval.expiresAt : decided.decidedAt)}</dd>
          </div>
        </dl>
        {showsAskAgain(state) ? (
          <p>
            {copy.proposal.expiredExplanation} <Link to="/">{copy.proposal.askAgain}</Link>
          </p>
        ) : null}
        <p>{copy.proposal.notAvailable}</p>
      </AiSuggestion>
    </Card>
  );
}
