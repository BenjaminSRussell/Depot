import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { ProblemMessage } from "../../shell/errors";
import { UserName } from "../../shell/lookup/UserName";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, Card, DataTable, ListPage, loadedRows, PageHeader, Skeleton, type DataTableColumn, type ListColumn } from "../../shell/ui";
import { OrderNumber } from "../mfg-shared/orders";
import { ResourceName } from "../mfg-shared/resources";
import {
  describeProblem,
  DUTY_PI_EXECUTION_VERIFY,
  getPIExecution,
  listPIExecutions,
  verifyPIExecution,
  type PIExecution,
  type PIExecutionStep,
  type PIExecutionStepStatus,
  type PIExecutionVerification,
  type Problem,
} from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { CATEGORY_LABEL, MEANING_LABEL } from "./piSheet";
import { useLoad } from "./useLoad";

const STATUS_LABEL: Record<PIExecutionStepStatus, string> = { recorded: "Recorded", acknowledged: "Passed", skipped: "Skipped by a branch", missing: "Not recorded" };
const STATUS_TONE = { recorded: "success", acknowledged: "neutral", skipped: "info", missing: "danger" } as const;

const columns: ListColumn<PIExecution>[] = [
  { key: "orderId", header: "Order", render: (x) => <OrderNumber id={x.orderId} /> },
  { key: "sheet", header: "PI sheet", render: (x) => (x.sheetCode ? `${x.sheetCode} version ${x.sheetVersion ?? ""}` : "Recipe sent whole"), sortValue: (x) => x.sheetCode ?? "" },
  { key: "resourceId", header: "Resource", render: (x) => <ResourceName id={x.resourceId} /> },
  { key: "executedBy", header: "Completed by", render: (x) => <UserName id={x.executedBy} /> },
  { key: "completedAt", header: "Completed", render: (x) => formatDateTime(x.completedAt), sortValue: (x) => x.completedAt },
  { key: "stepCount", header: "Steps", align: "end", sortValue: (x) => x.stepCount },
  { key: "deviationCount", header: "Deviations", align: "end", render: (x) => (x.deviationCount > 0 ? <Badge tone="warning">{x.deviationCount}</Badge> : "None"), sortValue: (x) => x.deviationCount },
];

function useRows() {
  return loadedRows(useLoad((signal) => listPIExecutions({}, signal), "pi-executions"));
}

/** The sealed records of PI sheets run at the kiosk (FR-25.18). */
export function PiExecutionsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="shopfloor-pi-executions"
      title="PI execution records"
      description="What was entered, read and signed on each PI sheet a kiosk finished, with the deviations. A record cannot be changed; its hash chain shows whether it was."
      noun="records"
      singular="record"
      columns={columns}
      useRows={useRows}
      getRowKey={(x) => x.id}
      onRowOpen={(x) => navigate(`/shopfloor/pi-executions/${x.id}`)}
      empty={{ title: "No records yet", description: "A record is sealed when a kiosk completes a PI sheet." }}
    />
  );
}

function valuesText(s: PIExecutionStep): string {
  const shown = s.values.filter((v) => !["comment", "signature-meaning", "readout-source", "node", "expected", "tolerance", "expression"].includes(v.name));
  return shown.map((v) => `${v.name}: ${v.value}${v.unit ? ` ${v.unit}` : ""}`).join("; ") || "None";
}

/** One record, read only: the header, the steps in order and the hash check. */
export function PiExecutionDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getPIExecution(id, signal), `pi-execution|${id}`);
  const duties = usePrincipalDuties();
  const [check, setCheck] = useState<PIExecutionVerification | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);

  async function verify() {
    setBusy(true);
    setFailure(null);
    try {
      setCheck(await verifyPIExecution(id));
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="PI execution record" />
        <Alert tone="danger" title="Could not load the record" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const x = state.data;
  if (!x) {
    return (
      <div className={styles.stack}>
        <PageHeader title="PI execution record" />
        <Skeleton aria-label="Loading the record" />
      </div>
    );
  }
  const stepColumns: DataTableColumn<PIExecutionStep>[] = [
    { key: "seq", header: "Step", align: "end" },
    { key: "phasePath", header: "Phase" },
    { key: "kind", header: "Type", render: (s) => CATEGORY_LABEL[s.kind] },
    { key: "text", header: "Instruction", render: (s) => s.text || "None" },
    { key: "status", header: "Status", render: (s) => <Badge tone={STATUS_TONE[s.status]}>{STATUS_LABEL[s.status]}</Badge> },
    { key: "values", header: "Entered or read", render: valuesText },
    { key: "when", header: "When", render: (s) => (s.occurredAt ? formatDateTime(s.occurredAt) : "None") },
    {
      key: "signature",
      header: "Signed",
      render: (s) =>
        s.signerUser ? (
          <span>
            <UserName id={s.signerUser} />
            {` (${MEANING_LABEL[(s.signatureMeaning || "performed") as keyof typeof MEANING_LABEL].toLowerCase()}, signed in ${s.signerAuthTime ? formatDateTime(s.signerAuthTime) : "at an unknown time"})`}
          </span>
        ) : (
          "None"
        ),
    },
    { key: "deviation", header: "Deviation", render: (s) => (s.deviation ? <span>{[...s.deviationReasons, ...(s.comment ? [`comment: ${s.comment}`] : [])].join("; ")}</span> : s.comment ? `Comment: ${s.comment}` : "None") },
  ];
  return (
    <div className={styles.stack}>
      <PageHeader
        title="PI execution record"
        description={<Link to="/shopfloor/pi-executions">Back to the records</Link>}
        actions={x.deviationCount > 0 ? <Badge tone="warning">{`${x.deviationCount} deviation${x.deviationCount === 1 ? "" : "s"}`}</Badge> : <Badge tone="success">No deviations</Badge>}
      />
      <Card title="Run">
        <dl className={styles.details}>
          <div className={styles.entry}>
            <dt className={styles.term}>Order</dt>
            <dd className={styles.value}><OrderNumber id={x.orderId} /></dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Resource</dt>
            <dd className={styles.value}><ResourceName id={x.resourceId} /></dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>PI sheet</dt>
            <dd className={styles.value}>{x.sheetCode ? `${x.sheetCode} version ${x.sheetVersion ?? ""}` : "Recipe sent whole"}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Completed by</dt>
            <dd className={styles.value}><UserName id={x.executedBy} /></dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Started</dt>
            <dd className={styles.value}>{formatDateTime(x.startedAt)}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Completed</dt>
            <dd className={styles.value}>{formatDateTime(x.completedAt)}</dd>
          </div>
          <div className={styles.entry}>
            <dt className={styles.term}>Record hash (SHA-256)</dt>
            <dd className={`${styles.value} ${styles.mono ?? ""}`}>{x.recordHash}</dd>
          </div>
        </dl>
      </Card>
      <Card title="Integrity">
        <div className={styles.stack}>
          <p className={styles.note}>Each step is hashed together with the step before it, and the record hash covers the end of the chain. Checking recomputes them from what is stored.</p>
          {holdsDuty(duties, DUTY_PI_EXECUTION_VERIFY) ? (
            <div className={styles.actions}>
              <Button loading={busy} onClick={() => void verify()}>
                Check the record
              </Button>
            </div>
          ) : null}
          {failure ? (
            <Alert tone="danger" title="Could not check the record" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          ) : null}
          {check ? (
            check.valid ? (
              <Alert tone="success" title="The record is intact" live="status">{`All ${check.checkedSteps} steps and the record hash match what was sealed.`}</Alert>
            ) : (
              <Alert tone="danger" title="The record does not match what was sealed" live="alert">
                {`${check.reason}${check.brokenAtSeq ? ` (step ${check.brokenAtSeq})` : check.brokenAtSeq === 0 ? " (record header)" : ""}.`}
              </Alert>
            )
          ) : null}
        </div>
      </Card>
      <DataTable label="Steps of the record" columns={stepColumns} rows={x.steps} getRowKey={(s) => String(s.seq)} empty={{ title: "No steps", description: "The recipe had no steps." }} />
    </div>
  );
}
