import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Badge, Button, buttonClassName, Card, ConfirmDialog, DataTable, PageHeader, type DataTableColumn } from "../../shell/ui";
import { describeProblem, DOCUMENT_TYPE_LABELS, etagOf, getTemplate, listTemplateVersions, restoreTemplateVersion, type Problem, type TemplateVersion } from "./api";
import styles from "./DocumentDesignPage.module.css";
import { changesSummary } from "./summary";
import { useLoad } from "./useLoad";

const when = (iso: string) => new Date(iso).toLocaleString("en-US", { dateStyle: "medium", timeStyle: "short" });

/** Every saved version of a template, with who saved it and a way back to an earlier one (46-1). */
export function TemplateHistoryPage() {
  const { code = "" } = useParams();
  const current = useLoad(() => getTemplate(code), code);
  const versions = useLoad((signal) => listTemplateVersions(code, signal), code);
  const [pending, setPending] = useState<TemplateVersion | null>(null);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [done, setDone] = useState("");

  const now = current.data?.value.version ?? 0;
  const reloadAll = () => {
    current.reload();
    versions.reload();
  };

  async function restore() {
    if (!pending) return;
    const from = pending.version;
    setBusy(true);
    setFailure(null);
    setDone("");
    try {
      const res = await restoreTemplateVersion(code, from, etagOf(now));
      setDone(`Version ${from} was restored as version ${res.value.version}.`);
      reloadAll();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
      setPending(null);
    }
  }

  const columns: DataTableColumn<TemplateVersion>[] = [
    { key: "version", header: "Version", align: "end", render: (v) => String(v.version) },
    { key: "savedAt", header: "Saved", render: (v) => when(v.savedAt) },
    { key: "savedBy", header: "Saved by", render: (v) => (v.savedBy ? <EntityName entity="user" id={v.savedBy} fallback="A user" /> : "Before history was kept") },
    { key: "name", header: "Name", render: (v) => v.name },
    { key: "changes", header: "Changes", render: (v) => changesSummary(v.config) },
    {
      key: "actions",
      header: "Restore",
      render: (v) =>
        v.version === now ? (
          <Badge tone="success">Current</Badge>
        ) : (
          <Button size="sm" disabled={busy || !current.data} onClick={() => setPending(v)}>
            {`Restore version ${v.version}`}
          </Button>
        ),
    },
  ];

  const stale = failure?.code === "PRN-0017" || failure?.code === "PRN-0018";
  return (
    <div className={styles.stack}>
      <PageHeader
        title="Version history"
        description={`${current.data ? `${current.data.value.name}, ${DOCUMENT_TYPE_LABELS[current.data.value.documentType] ?? current.data.value.documentType}. ` : ""}Restoring an earlier version saves its name and layout as a new version, so nothing in the history is lost.`}
      />
      <div className={styles.actions}>
        <Link className={buttonClassName("ghost")} to={`/document-design/templates/${encodeURIComponent(code)}`}>
          Back to the template
        </Link>
      </div>
      <Alert tone="success" live="status">
        {done}
      </Alert>
      {failure ? (
        <Alert
          tone={stale ? "warning" : "danger"}
          title={stale ? "This template changed after you opened the page" : "Could not restore the version"}
          live="alert"
          action={
            stale ? (
              <Button size="sm" onClick={reloadAll}>
                Reload
              </Button>
            ) : undefined
          }
        >
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <Card title="Saved versions">
        {versions.error ? (
          <Alert tone="danger" title="Could not load the history" live="alert" action={<Button size="sm" onClick={versions.reload}>Try again</Button>}>
            <ProblemMessage problem={versions.error} />
          </Alert>
        ) : (
          <DataTable
            label="Template versions"
            columns={columns}
            rows={versions.data ?? []}
            getRowKey={(v) => String(v.version)}
            loading={versions.loading}
            empty={{ title: "No versions", description: "This template has no saved versions." }}
          />
        )}
      </Card>
      {pending ? (
        <ConfirmDialog
          title={`Restore version ${pending.version}?`}
          description={`The name and layout of version ${pending.version} become the current version of this template, saved as version ${now + 1}. Documents that use the template change from now on.`}
          confirmLabel="Restore"
          cancelLabel="Keep the current version"
          onConfirm={() => void restore()}
          onCancel={() => setPending(null)}
        />
      ) : null}
    </div>
  );
}
