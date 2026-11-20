import { useEffect, useRef } from "react";
import { copy } from "../../shell/copy";
import { Alert, Button, Card, EmptyState, PageHeader, Skeleton } from "../../shell/ui";
import { ApprovalNotFound, getApproval, type Approval } from "./api/proposals";
import styles from "./pages.module.css";
import { ProposalCard } from "./ProposalCard";
import { stateAnnouncement } from "./proposalModel";
import { useLoad } from "./useLoad";

export type ProposalLoad = Approval | "not-found";

export interface ProposalPageProps {
  approvalId: string;
  load?: (signal: AbortSignal) => Promise<ProposalLoad>;
}

export function ProposalPage({ approvalId, load }: ProposalPageProps) {
  const loadProposal =
    load ?? ((signal: AbortSignal) => getApproval(approvalId, signal).catch((err: unknown) => (err instanceof ApprovalNotFound ? "not-found" : Promise.reject(err))));
  const state = useLoad<ProposalLoad>(loadProposal, approvalId);
  const approval = state.data === null || state.data === "not-found" ? undefined : state.data;
  const settled = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!state.loading) settled.current?.focus();
  }, [state.loading]);

  return (
    <div className={styles.stack} aria-busy={state.loading}>
      <PageHeader title={copy.proposal.title} description={copy.proposal.description} />
      <div role="status" className={styles.visuallyHidden}>
        {approval === undefined ? "" : stateAnnouncement(approval.state)}
      </div>
      <div ref={settled} tabIndex={-1}>
        {state.loading ? (
          <div className={styles.loading}>
            <span className={styles.visuallyHidden}>{copy.proposal.loading}</span>
            <Skeleton width="3/4" />
            <Skeleton height="tile" />
          </div>
        ) : state.error !== null ? (
          <Alert tone="danger" live="alert" title={copy.proposal.loadErrorTitle} action={<Button onClick={state.reload}>{copy.proposal.retry}</Button>}>
            {copy.proposal.loadErrorBody}
          </Alert>
        ) : approval === undefined ? (
          <Card>
            <EmptyState icon="lock" title={copy.proposal.notFoundTitle} description={copy.proposal.notFoundBody} />
          </Card>
        ) : (
          <ProposalCard approval={approval} />
        )}
      </div>
    </div>
  );
}
