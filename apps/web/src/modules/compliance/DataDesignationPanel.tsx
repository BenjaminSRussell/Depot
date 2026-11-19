import { useState } from "react";
import { formatDateTime } from "../../shell/format";
import { ProblemMessage } from "../../shell/errors";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, ConfirmDialog, DataTable, FormField, Textarea, type DataTableColumn } from "../../shell/ui";
import {
  changeDesignation,
  describeProblem,
  designateRecord,
  designationHistory,
  findDesignation,
  resolveDesignation,
  type DesignationClass,
  type DesignationHistoryEntry,
  type DesignationSource,
  type Problem,
} from "./api/client";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const MAX_REASON = 500;

const INHERITED: Partial<Record<DesignationSource, string>> = {
  LEGAL_ENTITY: "its legal entity",
  TENANT: "the whole company",
  PARENT_RECORD: "the record it belongs to",
};

const ACTION_LABELS: Record<string, string> = { DESIGNATE: "Designated", UPGRADE: "Designated again", DOWNGRADE: "Downgraded" };

export function reasonError(reason: string): string | undefined {
  const length = reason.trim().length;
  if (length === 0) return "Give a reason.";
  if (length > MAX_REASON) return `Use at most ${MAX_REASON} characters.`;
  return undefined;
}

export function DataDesignationPanel({ entityType, recordId }: { entityType: string; recordId: string }) {
  const duties = usePrincipalDuties();
  if (!holdsDuty(duties, "datadesignation.read")) return null;
  return <Panel entityType={entityType} recordId={recordId} canChange={holdsDuty(duties, "datadesignation.write")} />;
}

function Panel({ entityType, recordId, canChange }: { entityType: string; recordId: string; canChange: boolean }) {
  const state = useLoad(
    async (signal) => ({ effective: await resolveDesignation(entityType, recordId, signal), row: await findDesignation(entityType, recordId, signal) }),
    `${entityType}:${recordId}`,
  );
  const [asking, setAsking] = useState<DesignationClass | null>(null);
  const [reason, setReason] = useState("");
  const [showError, setShowError] = useState(false);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [history, setHistory] = useState<DesignationHistoryEntry[] | null>(null);

  if (state.error) {
    return (
      <Alert tone="danger" title="Could not load the data designation" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
        <ProblemMessage problem={state.error} />
      </Alert>
    );
  }
  if (!state.data) {
    return <p className={styles.note}>Loading the data designation…</p>;
  }
  const { effective, row } = state.data;
  const inheritedFrom = effective.class === "CUI" ? INHERITED[effective.source] : undefined;
  const target: DesignationClass = row?.value.class === "CUI" ? "NONE" : "CUI";
  const error = showError ? reasonError(reason) : undefined;

  function close() {
    setAsking(null);
    setReason("");
    setShowError(false);
  }

  async function confirm() {
    if (reasonError(reason) || asking === null) {
      setShowError(true);
      return;
    }
    setBusy(true);
    setFailure(null);
    try {
      if (row) await changeDesignation(row.value.designationId, row.etag, asking, reason.trim());
      else await designateRecord(entityType, recordId, reason.trim());
      setDone(asking === "NONE" ? "The designation was downgraded." : "The record is designated as CUI.");
      setHistory(null);
      close();
      state.reload();
    } catch (problem) {
      setFailure(describeProblem(problem));
      close();
    } finally {
      setBusy(false);
    }
  }

  async function showHistory() {
    if (!row) return;
    try {
      setHistory(await designationHistory(row.value.designationId));
    } catch (problem) {
      setFailure(describeProblem(problem));
    }
  }

  const columns: DataTableColumn<DesignationHistoryEntry>[] = [
    { key: "occurredAt", header: "When", mono: true, render: (entry) => formatDateTime(entry.occurredAt) },
    { key: "actor", header: "Who", mono: true },
    { key: "action", header: "Change", render: (entry) => `${ACTION_LABELS[entry.action] ?? entry.action}${entry.before?.class ? `: ${entry.before.class} to ${entry.after?.class ?? ""}` : ""}` },
    { key: "scope", header: "Level", render: (entry) => entry.scope.replace("_", " ").toLowerCase() },
    { key: "reason", header: "Reason" },
  ];

  return (
    <Card
      title="Data designation"
      headingLevel={3}
      actions={
        canChange ? (
          <Button size="sm" variant="secondary" disabled={busy} onClick={() => setAsking(target)}>
            {target === "NONE" ? "Downgrade the designation" : "Designate as CUI"}
          </Button>
        ) : undefined
      }
    >
      <div className={styles.stack}>
        <p>
          {effective.class === "CUI" ? <Badge tone="warning">CUI</Badge> : effective.class === "UNKNOWN" ? <Badge tone="danger">Not known, handled as CUI</Badge> : <Badge tone="neutral">Not CUI</Badge>}{" "}
          {inheritedFrom ? `Inherited: this record is CUI because ${inheritedFrom} is designated.` : effective.class === "CUI" ? "Designated on this record." : null}
        </p>
        {row && row.value.class === "NONE" ? <p className={styles.note}>This record was designated and then downgraded; its history is kept.</p> : null}
        <Alert tone="success" live="status">
          {done}
        </Alert>
        {failure ? (
          <Alert tone="danger" title="Could not change the designation" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        {row ? (
          history ? (
            <DataTable label="Designation history" columns={columns} rows={history} getRowKey={(entry) => entry.auditId} empty={{ title: "No history", description: "Nothing has changed yet." }} />
          ) : (
            <div>
              <Button size="sm" variant="ghost" onClick={() => void showHistory()}>
                Show designation history
              </Button>
            </div>
          )
        ) : null}
      </div>
      {asking ? (
        <ConfirmDialog
          title={asking === "NONE" ? "Downgrade this designation?" : "Designate this record as CUI?"}
          description={
            asking === "NONE"
              ? "The record stops being CUI on its own account. If its legal entity or company is designated it stays CUI."
              : "AI features will treat this record as controlled unclassified information."
          }
          confirmLabel={asking === "NONE" ? "Downgrade" : "Designate as CUI"}
          cancelLabel="Keep it as it is"
          onConfirm={() => void confirm()}
          onCancel={close}
        >
          <FormField id="designation-reason" label="Reason" required error={error}>
            {(c) => <Textarea {...c} rows={3} value={reason} onChange={(e) => setReason(e.target.value)} invalid={error !== undefined} />}
          </FormField>
        </ConfirmDialog>
      ) : null}
    </Card>
  );
}
