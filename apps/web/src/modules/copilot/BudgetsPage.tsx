import { useState } from "react";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, ConfirmDialog, DataTable, FormField, Input, PageHeader, Skeleton, type BadgeTone, type DataTableColumn } from "../../shell/ui";
import { getAiDataPolicy, getBudgets, putBudgetCap, type AiDataPolicy, type BudgetCap, type BudgetEntry, type BudgetList } from "./api/agents";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const AI_POLICY_ROUTE = "/copilot/ai-policy";
export const TENANT_ROW_HINT = "Per-agent caps are reported; only the tenant budget stops runs.";
const WHOLE_NUMBER = /^\d+$/;
const NOT_A_NUMBER = "Enter a whole number of tokens, zero or more.";
const STATUS_TONE: Record<BudgetEntry["status"], BadgeTone> = { OK: "success", SOFT: "warning", HARD: "danger" };

export interface BudgetsPageProps {
  load?: (signal: AbortSignal) => Promise<BudgetList>;
  save?: (agentId: string, cap: BudgetCap) => Promise<BudgetEntry>;
  loadPolicy?: (signal: AbortSignal) => Promise<AiDataPolicy>;
  duties?: readonly string[];
}

interface Draft {
  limitIn: string;
  limitOut: string;
}

type FieldErrors = Record<string, string>;

interface PendingSave {
  agentId: string;
  cap: BudgetCap;
}

function draftOf(entry: BudgetEntry, drafts: Record<string, Draft>): Draft {
  return drafts[entry.agentId ?? ""] ?? { limitIn: String(entry.cap.tokensIn), limitOut: String(entry.cap.tokensOut) };
}

function DataPolicySummary({ loadPolicy }: { loadPolicy: (signal: AbortSignal) => Promise<AiDataPolicy> }) {
  const state = useLoad(loadPolicy, "ai-data-policy");
  return (
    <section aria-labelledby="data-policy-heading" className={styles.stack}>
      <h2 id="data-policy-heading" className={styles.sectionTitle}>
        Data the assistant may use
      </h2>
      {state.error ? (
        <Alert tone="danger" live="alert" title="We couldn't load the data policy" action={<Button onClick={state.reload}>Try again</Button>}>
          {state.error}
        </Alert>
      ) : state.data ? (
        <dl className={styles.details}>
          {Object.entries(state.data.modules).map(([module, cells]) => {
            const allowed = Object.entries(cells).filter(([, on]) => on).map(([cls]) => cls);
            const denied = Object.entries(cells).filter(([, on]) => !on).map(([cls]) => cls);
            return (
              <div key={module} className={styles.entry}>
                <dt className={styles.term}>{module.toUpperCase()}</dt>
                <dd className={styles.value}>
                  {`May reach AI: ${allowed.join(", ") || "nothing"}. Denied: ${denied.join(", ") || "nothing"}.`}
                </dd>
              </div>
            );
          })}
        </dl>
      ) : (
        <Skeleton width="1/2" />
      )}
      <p className={styles.muted}>
        <Link to={AI_POLICY_ROUTE}>Open the data policy page</Link>
      </p>
    </section>
  );
}

export function BudgetsPage({ load = getBudgets, save = (agentId, cap) => putBudgetCap(agentId, cap), loadPolicy = getAiDataPolicy, duties }: BudgetsPageProps) {
  const principalDuties = usePrincipalDuties();
  const held = duties ?? principalDuties;
  const writable = holdsDuty(held, "agentbudget.write");
  const state = useLoad(load, "budgets");
  const [drafts, setDrafts] = useState<Record<string, Draft>>({});
  const [errors, setErrors] = useState<FieldErrors>({});
  const [pending, setPending] = useState<PendingSave | null>(null);
  const [saving, setSaving] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  const header = <PageHeader title="AI controls" description="Token budgets for the assistant and each agent this month. The tenant budget is the one that stops runs at 100%." />;

  function edit(agentId: string, entry: BudgetEntry, patch: Partial<Draft>) {
    setDrafts((prev) => ({ ...prev, [agentId]: { ...draftOf(entry, prev), ...patch } }));
    setErrors((prev) => ({ ...prev, [`${agentId}:limitIn`]: "", [`${agentId}:limitOut`]: "" }));
  }

  async function commit(agentId: string, cap: BudgetCap) {
    setPending(null);
    setSaving(agentId);
    setFailure(null);
    setNotice(null);
    try {
      await save(agentId, cap);
      setDrafts(({ [agentId]: _saved, ...rest }) => rest);
      setNotice(`Token caps for ${agentId} were saved.`);
      state.reload();
    } catch (err) {
      const code = (err as { code?: unknown }).code;
      const message = err instanceof Error ? err.message : "The cap could not be saved.";
      if (code === "AGT-0422") setErrors((prev) => ({ ...prev, [`${agentId}:${message.includes("limitOut") ? "limitOut" : "limitIn"}`]: message }));
      else setFailure(message);
    } finally {
      setSaving(null);
    }
  }

  function submit(entry: BudgetEntry) {
    const agentId = entry.agentId ?? "";
    const draft = draftOf(entry, drafts);
    const invalid: FieldErrors = {
      [`${agentId}:limitIn`]: WHOLE_NUMBER.test(draft.limitIn.trim()) ? "" : NOT_A_NUMBER,
      [`${agentId}:limitOut`]: WHOLE_NUMBER.test(draft.limitOut.trim()) ? "" : NOT_A_NUMBER,
    };
    setErrors((prev) => ({ ...prev, ...invalid }));
    if (Object.values(invalid).some(Boolean)) return;
    const cap = { limitIn: Number(draft.limitIn), limitOut: Number(draft.limitOut) };
    if (cap.limitIn < entry.tokensUsed.tokensIn || cap.limitOut < entry.tokensUsed.tokensOut) setPending({ agentId, cap });
    else void commit(agentId, cap);
  }

  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        {header}
        <div className={styles.loading}>
          <span className={styles.visuallyHidden}>Loading budgets…</span>
          <Skeleton width="1/2" />
          <Skeleton height="tile" />
        </div>
      </div>
    );
  }

  if (state.error || !state.data) {
    return (
      <div className={styles.stack}>
        {header}
        <Alert tone="danger" live="alert" title="We couldn't load the budgets" action={<Button onClick={state.reload}>Try again</Button>}>
          {state.error ?? "No budgets were returned."}
        </Alert>
      </div>
    );
  }

  const capField = (entry: BudgetEntry, key: keyof Draft, label: string) => {
    const agentId = entry.agentId ?? "";
    const field = `${agentId}:${key}`;
    return (
      <FormField id={`cap-${agentId}-${key}`} label={`${label} for ${agentId}`} {...(errors[field] ? { error: errors[field] } : {})}>
        {(control) => (
          <Input
            {...control}
            inputMode="numeric"
            value={draftOf(entry, drafts)[key]}
            readOnly={!writable}
            onChange={(e) => edit(agentId, entry, { [key]: e.target.value })}
          />
        )}
      </FormField>
    );
  };

  const isTenant = (entry: BudgetEntry) => entry.scope === "TENANT";
  const columns: DataTableColumn<BudgetEntry>[] = [
    { key: "agentId", header: "Agent", render: (e) => (isTenant(e) ? "Tenant" : e.agentId) },
    {
      key: "limitIn",
      header: "Input token cap",
      align: "end",
      render: (e) => (isTenant(e) ? e.cap.tokensIn.toLocaleString() : capField(e, "limitIn", "Input token cap")),
    },
    {
      key: "limitOut",
      header: "Output token cap",
      align: "end",
      render: (e) => (isTenant(e) ? e.cap.tokensOut.toLocaleString() : capField(e, "limitOut", "Output token cap")),
    },
    { key: "used", header: "Tokens used", align: "end", render: (e) => `${e.tokensUsed.tokensIn.toLocaleString()} in, ${e.tokensUsed.tokensOut.toLocaleString()} out` },
    { key: "costUsed", header: "Cost (USD)", align: "end", render: (e) => e.costUsed },
    { key: "percentConsumed", header: "Consumed", align: "end", render: (e) => `${e.percentConsumed}%` },
    { key: "status", header: "Status", render: (e) => <Badge tone={STATUS_TONE[e.status]}>{e.status}</Badge> },
  ];
  if (writable) {
    columns.push({
      key: "actions",
      header: "Actions",
      render: (e) =>
        isTenant(e) ? null : (
          <Button loading={saving === e.agentId} onClick={() => submit(e)}>
            {`Save caps for ${e.agentId}`}
          </Button>
        ),
    });
  }

  const rows = [state.data.tenant, ...state.data.agents];
  return (
    <div className={styles.stack}>
      {header}
      {notice ? (
        <Alert tone="success" live="status">
          {notice}
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" live="alert" title="The cap was not saved">
          {failure}
        </Alert>
      ) : null}
      <DataTable label={`Token budgets for ${state.data.month}`} columns={columns} rows={rows} getRowKey={(e) => `${e.scope}:${e.agentId ?? ""}`} />
      <p className={styles.muted}>{TENANT_ROW_HINT}</p>
      {writable ? null : <p className={styles.muted}>You can view budgets but not change them.</p>}
      {holdsDuty(held, "aipolicy.read") ? <DataPolicySummary loadPolicy={loadPolicy} /> : null}
      {pending ? (
        <ConfirmDialog
          title="Set a cap below current use?"
          description={`The new cap for ${pending.agentId} is below the tokens it has already used this month. Agent caps are reported only, so no run stops because of it. Save anyway?`}
          confirmLabel="Save cap"
          cancelLabel="Cancel"
          onConfirm={() => void commit(pending.agentId, pending.cap)}
          onCancel={() => setPending(null)}
        />
      ) : null}
    </div>
  );
}
