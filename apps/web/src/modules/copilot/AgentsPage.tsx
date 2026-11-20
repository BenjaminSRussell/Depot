import { useState } from "react";
import { Alert, Badge, Button, ConfirmDialog, DataTable, PageHeader, Skeleton, type DataTableColumn } from "../../shell/ui";
import { getAgentDefinitions, setAgentEnabled, type AgentDefinition } from "./api/agents";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export interface AgentsPageProps {
  load?: (signal: AbortSignal) => Promise<AgentDefinition[]>;
  toggle?: (agentId: string, enabled: boolean) => Promise<AgentDefinition>;
}

function formatTime(iso: string | null): string {
  if (iso === null) return "";
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

export function AgentsPage({ load = getAgentDefinitions, toggle = (agentId, enabled) => setAgentEnabled(agentId, enabled) }: AgentsPageProps) {
  const state = useLoad(load, "agent-definitions");
  const [pending, setPending] = useState<AgentDefinition | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);

  async function change(agent: AgentDefinition, enabled: boolean) {
    setPending(null);
    setBusy(agent.agentId);
    setFailure(null);
    setNotice(null);
    try {
      await toggle(agent.agentId, enabled);
      setNotice(enabled ? `${agent.agentId} is enabled.` : `${agent.agentId} is disabled. Runs already started continue; use the kill switch to stop them.`);
      state.reload();
    } catch (err) {
      setFailure(err instanceof Error ? err.message : "The agent could not be changed.");
    } finally {
      setBusy(null);
    }
  }

  const header = <PageHeader title="Agents" description="Disable an agent to stop new runs and leave it out of briefings. Enable it again at any time." />;

  if (state.loading && !state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        {header}
        <div className={styles.loading}>
          <span className={styles.visuallyHidden}>Loading agents…</span>
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
        <Alert tone="danger" live="alert" title="We couldn't load the agents" action={<Button onClick={state.reload}>Try again</Button>}>
          {state.error ?? "No agents were returned."}
        </Alert>
      </div>
    );
  }

  const columns: DataTableColumn<AgentDefinition>[] = [
    { key: "agent", header: "Agent", mono: true, render: (a) => a.agentId },
    { key: "enabled", header: "State", render: (a) => <Badge tone={a.enabled ? "success" : "danger"}>{a.enabled ? "Enabled" : "Disabled"}</Badge> },
    { key: "disabledBy", header: "Disabled by", mono: true, render: (a) => a.disabledBy ?? "" },
    { key: "disabledAt", header: "Disabled at", render: (a) => formatTime(a.disabledAt) },
    {
      key: "actions",
      header: "Actions",
      render: (a) =>
        a.enabled ? (
          <Button variant="danger" loading={busy === a.agentId} onClick={() => setPending(a)}>
            {`Disable ${a.agentId}`}
          </Button>
        ) : (
          <Button loading={busy === a.agentId} onClick={() => void change(a, true)}>
            {`Enable ${a.agentId}`}
          </Button>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      {header}
      {notice ? (
        <Alert tone="success" live="status">
          {notice}
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" live="alert" title="The agent was not changed">
          {failure}
        </Alert>
      ) : null}
      <DataTable label="Agents" columns={columns} rows={state.data} getRowKey={(a) => a.agentId} empty={{ title: "No agents are registered" }} />
      {pending ? (
        <ConfirmDialog
          title={`Disable ${pending.agentId}?`}
          description="New runs of this agent are refused and its items are left out of briefings. Runs already started continue until they finish or the kill switch stops them."
          confirmLabel="Disable agent"
          cancelLabel="Cancel"
          onConfirm={() => void change(pending, false)}
          onCancel={() => setPending(null)}
        />
      ) : null}
    </div>
  );
}
