import { useState } from "react";
import { Link } from "react-router-dom";
import { Alert, Badge, Button, buttonClassName, Card, ConfirmDialog, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { EntityName } from "../../shell/lookup/EntityName";
import { ProblemMessage } from "../../shell/errors";
import {
  CHANNEL_LABELS,
  deleteOutputRule,
  deleteStatementSchedule,
  describeProblem,
  DOCUMENT_TYPE_LABELS,
  listOutputRules,
  listStatementSchedules,
  listTemplates,
  listTextBlocks,
  runStatementSchedule,
  SCHEDULE_CADENCE_LABELS,
  setStatementScheduleEnabled,
  WEEKDAYS,
  type DocumentTemplate,
  type OutputRule,
  type Problem,
  type StatementSchedule,
  type TextBlock,
} from "./api";
import { changesSummary } from "./summary";
import { useLoad } from "./useLoad";
import styles from "./DocumentDesignPage.module.css";

const typeLabel = (t: string) => DOCUMENT_TYPE_LABELS[t] ?? t;

const templateColumns: DataTableColumn<DocumentTemplate>[] = [
  { key: "name", header: "Template", render: (t) => <Link to={`/document-design/templates/${encodeURIComponent(t.code)}`}>{t.name}</Link> },
  { key: "documentType", header: "Document", render: (t) => typeLabel(t.documentType) },
  { key: "default", header: "Used", render: (t) => (t.default ? <Badge tone="success">Default for this document</Badge> : "When named") },
  { key: "title", header: "Title", render: (t) => t.config.title ?? "Standard" },
  { key: "changes", header: "Changes", render: (t) => changesSummary(t.config) },
  { key: "version", header: "Version", align: "end", render: (t) => String(t.version) },
  {
    key: "history",
    header: "History",
    render: (t) => (
      <Link to={`/document-design/templates/${encodeURIComponent(t.code)}/versions`} aria-label={`Version history of ${t.name}`}>
        Version history
      </Link>
    ),
  },
];

const blockColumns: DataTableColumn<TextBlock>[] = [
  { key: "code", header: "Text block", mono: true, render: (b) => <Link to={`/document-design/text-blocks/${encodeURIComponent(b.code)}/${encodeURIComponent(b.language)}`}>{b.code}</Link> },
  { key: "language", header: "Language", mono: true },
  { key: "body", header: "Text", render: (b) => (b.body.length > 90 ? `${b.body.slice(0, 90)}…` : b.body) },
  { key: "placeholders", header: "Fills in", render: (b) => (b.placeholders.length ? b.placeholders.join(", ") : "Nothing") },
  { key: "version", header: "Version", align: "end", render: (b) => String(b.version) },
];

const baseRuleColumns: DataTableColumn<OutputRule>[] = [
  { key: "documentType", header: "Document", render: (r) => typeLabel(r.documentType) },
  {
    key: "scope",
    header: "Applies to",
    render: (r) =>
      r.legalEntityId || r.accountId ? (
        <span>
          {r.legalEntityId ? <EntityName entity="legalEntity" id={r.legalEntityId} fallback="A legal entity" /> : null}
          {r.legalEntityId && r.accountId ? " and " : null}
          {r.accountId ? <EntityName entity="customer" id={r.accountId} fallback="A customer" /> : null}
        </span>
      ) : (
        "Every legal entity and account"
      ),
  },
  { key: "copyKind", header: "Copy", render: (r) => (r.copyKind === "ORIGINAL" ? "Original" : "Copy") },
  { key: "channel", header: "Goes to", render: (r) => CHANNEL_LABELS[r.channel] ?? r.channel },
  { key: "destination", header: "Destination", render: (r) => (r.destination ? `${r.destination}${r.copies > 1 ? ` (${r.copies} copies)` : ""}` : "None needed") },
  { key: "sequence", header: "Sequence", align: "end", render: (r) => String(r.sequence) },
];

function Section<T>({
  title,
  description,
  label,
  columns,
  state,
  getRowKey,
  empty,
}: {
  title: string;
  description: string;
  label: string;
  columns: DataTableColumn<T>[];
  state: ReturnType<typeof useLoad<T[]>>;
  getRowKey: (row: T) => string;
  empty: { title: string; description: string };
}) {
  return (
    <Card title={title}>
      <p className={styles.muted}>{description}</p>
      {state.error ? (
        <Alert tone="danger" title={`Could not load ${label.toLowerCase()}`} live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : (
        <DataTable label={label} columns={columns} rows={state.data ?? []} getRowKey={getRowKey} loading={state.loading} empty={empty} />
      )}
    </Card>
  );
}

function ruleColumns(onDelete: (r: OutputRule) => void): DataTableColumn<OutputRule>[] {
  return [
    ...baseRuleColumns,
    {
      key: "actions",
      header: "Remove",
      render: (r) => (
        <Button size="sm" onClick={() => onDelete(r)}>
          {`Remove ${typeLabel(r.documentType)} rule`}
        </Button>
      ),
    },
  ];
}

const when = (iso?: string) => (iso ? new Date(`${iso}T00:00:00Z`).toLocaleDateString("en-US", { dateStyle: "medium", timeZone: "UTC" }) : "Never");

function runDayLabel(s: StatementSchedule) {
  return s.cadence === "WEEKLY" ? (WEEKDAYS[s.runDay - 1] ?? String(s.runDay)) : `day ${s.runDay}`;
}

/** Document templates, standard text blocks, output rules and statement schedules of the print service (issue #46). */
export function DocumentDesignPage() {
  const templates = useLoad(listTemplates);
  const blocks = useLoad(listTextBlocks);
  const rules = useLoad(listOutputRules);
  const schedules = useLoad(listStatementSchedules);
  const [removing, setRemoving] = useState<OutputRule | null>(null);
  const [removingSchedule, setRemovingSchedule] = useState<StatementSchedule | null>(null);
  const [notice, setNotice] = useState("");
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState("");

  async function act(id: string, work: () => Promise<string>, reload: () => void) {
    setBusy(id);
    setFailure(null);
    setNotice("");
    try {
      setNotice(await work());
      reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy("");
    }
  }

  const scheduleColumns: DataTableColumn<StatementSchedule>[] = [
    { key: "name", header: "Schedule" },
    { key: "legalEntityId", header: "Legal entity", render: (s) => <EntityName entity="legalEntity" id={s.legalEntityId} fallback="A legal entity" /> },
    { key: "cadence", header: "Repeats", render: (s) => `${SCHEDULE_CADENCE_LABELS[s.cadence] ?? s.cadence} on ${runDayLabel(s)}` },
    { key: "next", header: "Next run", render: (s) => (s.enabled ? when(s.nextRunOn) : <Badge tone="neutral">Paused</Badge>) },
    { key: "last", header: "Last run", render: (s) => when(s.lastRunOn) },
    {
      key: "actions",
      header: "Actions",
      render: (s) => (
        <span className={styles.actions}>
          <Button
            size="sm"
            disabled={busy !== ""}
            onClick={() =>
              void act(
                s.id,
                async () => {
                  await setStatementScheduleEnabled(s.id, !s.enabled);
                  return `${s.name} ${s.enabled ? "paused" : "resumed"}.`;
                },
                schedules.reload,
              )
            }
          >
            {s.enabled ? `Pause ${s.name}` : `Resume ${s.name}`}
          </Button>
          <Button
            size="sm"
            disabled={busy !== ""}
            onClick={() =>
              void act(
                s.id,
                async () => {
                  const r = (await runStatementSchedule(s.id)).value;
                  return `${s.name} ran for ${when(r.asOf)}: ${r.delivered} delivered, ${r.skipped} skipped, ${r.failed} failed.`;
                },
                schedules.reload,
              )
            }
          >
            {`Run ${s.name} now`}
          </Button>
          <Button size="sm" disabled={busy !== ""} onClick={() => setRemovingSchedule(s)}>
            {`Delete ${s.name}`}
          </Button>
        </span>
      ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Document design"
        description="How purchase orders, invoices, packing slips, remittance advices and letters look, which standard texts they use, where the original and the copies of each document go, and which statements are sent on a schedule."
        actions={
          <span className={styles.actions}>
            <Link className={buttonClassName("primary")} to="/document-design/templates/new">
              New template
            </Link>
            <Link className={buttonClassName("secondary")} to="/document-design/text-blocks/new">
              New text block
            </Link>
            <Link className={buttonClassName("secondary")} to="/document-design/output-rules/new">
              New output rule
            </Link>
          </span>
        }
      />
      <Alert tone="success" live="status">
        {notice}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="That did not work" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Section
        title="Document templates"
        description="A template changes the title, footer line, column names and visible columns of a document type, and adds form notes or the letter text from the text blocks. The default template of a document type is used when a document names none."
        label="Document templates"
        columns={templateColumns}
        state={templates}
        getRowKey={(t) => t.code}
        empty={{ title: "No templates yet", description: "Documents print with the standard layout until a template is saved." }}
      />
      <Section
        title="Standard text blocks"
        description="Texts with placeholders such as {{dueDate}}, kept per language. A document uses the language it asks for, then the language without its region, then English."
        label="Text blocks"
        columns={blockColumns}
        state={blocks}
        getRowKey={(b) => `${b.code}/${b.language}`}
        empty={{ title: "No text blocks yet", description: "Add a text block to give letters and form notes their wording." }}
      />
      <Section
        title="Output rules"
        description="Where each document goes: a printer queue, an e-mail address, the e-invoice network, the archive or the portal, as the original or as a copy. The most specific rule wins: legal entity and account, then account, then legal entity, then the default."
        label="Output rules"
        columns={ruleColumns(setRemoving)}
        state={rules}
        getRowKey={(r) => r.id}
        empty={{ title: "No output rules yet", description: "Without a rule a document goes nowhere on its own; the screen that raised it still offers to download the PDF." }}
      />
      <Card title="Statement schedules">
        <div className={styles.sectionHead}>
          <p className={styles.muted}>
            A schedule renders the statement of account of every customer with open items and delivers it by the output rules for statements. Archive delivery is not available yet.
          </p>
          <Link className={buttonClassName("secondary")} to="/document-design/statement-schedules/new">
            New statement schedule
          </Link>
        </div>
        {schedules.error ? (
          <Alert tone="danger" title="Could not load statement schedules" live="alert" action={<Button size="sm" onClick={schedules.reload}>Try again</Button>}>
            <ProblemMessage problem={schedules.error} />
          </Alert>
        ) : (
          <DataTable
            label="Statement schedules"
            columns={scheduleColumns}
            rows={schedules.data ?? []}
            getRowKey={(s) => s.id}
            loading={schedules.loading}
            empty={{ title: "No statement schedules yet", description: "Add a schedule to send customers their statements without anyone starting it." }}
          />
        )}
      </Card>
      {removing ? (
        <ConfirmDialog
          title="Remove this output rule?"
          description={`Documents of this kind stop going to ${CHANNEL_LABELS[removing.channel] ?? removing.channel}${removing.destination ? ` (${removing.destination})` : ""} as the ${removing.copyKind === "ORIGINAL" ? "original" : "copy"}.`}
          confirmLabel="Remove"
          cancelLabel="Keep it"
          onConfirm={() => {
            const rule = removing;
            setRemoving(null);
            void act(rule.id, async () => (await deleteOutputRule(rule.id), "The output rule was removed."), rules.reload);
          }}
          onCancel={() => setRemoving(null)}
        />
      ) : null}
      {removingSchedule ? (
        <ConfirmDialog
          title="Delete this statement schedule?"
          description={`${removingSchedule.name} stops, and its run records are deleted. Statements already sent stay sent.`}
          confirmLabel="Delete"
          cancelLabel="Keep it"
          onConfirm={() => {
            const sc = removingSchedule;
            setRemovingSchedule(null);
            void act(sc.id, async () => (await deleteStatementSchedule(sc.id), `${sc.name} was deleted.`), schedules.reload);
          }}
          onCancel={() => setRemovingSchedule(null)}
        />
      ) : null}
    </div>
  );
}
