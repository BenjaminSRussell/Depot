import { useMemo, useState, type FormEvent } from "react";
import { ProblemMessage } from "../../shell/errors";
import { resolveHelp } from "../../shell/help/routeHelp";
import { useWorkspaceNav } from "../../shell/workspaces/WorkspaceNav";
import type { NavEntry } from "../../shell/workspaces/model";
import { Alert, Button, Card, DataTable, FormField, Input, PageHeader, Select, StatGroup, StatTile, type DataTableColumn } from "../../shell/ui";
import { getUxFunnel, getUxSignals, UX_FLOWS, UX_MAX_DAYS, UX_RELEASE, type UxFlow, type UxFunnel, type UxSignals } from "./api/client";
import { count, DATE, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const FLOW_LABELS: Record<UxFlow, string> = {
  procure_to_pay: "Procure to pay",
  onboarding: "Tenant onboarding",
};

const STEP_LABELS: Record<string, string> = {
  requisition_created: "Requisition created",
  requisition_submitted: "Requisition submitted",
  purchase_order_created: "Purchase order created",
  receipt_posted: "Receipt posted",
  vendor_invoice_posted: "Vendor invoice posted",
  payment_proposal_created: "Payment proposal created",
  legal_entity_created: "Legal entity created",
  fiscal_calendar_set: "Fiscal calendar set",
  site_created: "Site created",
  party_added: "Vendor or customer added",
  product_created: "Product created",
  first_transaction: "First transaction",
};

/** "vendor_invoice_posted" -> "Vendor invoice posted" when the step is not in the list above. */
export function stepLabel(step: string): string {
  const known = STEP_LABELS[step];
  if (known) return known;
  const spaced = step.replace(/_/g, " ");
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}

/** The day `days` before a YYYY-MM-DD date. */
export function daysBefore(date: string, days: number): string {
  const d = new Date(`${date}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() - days);
  return d.toISOString().slice(0, 10);
}

function percent(ratio: number): string {
  return `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 }).format(ratio * 100)}%`;
}

/** Median seconds as "4 min 10 s", "2 h 5 min" or "3 d 4 h". */
export function duration(seconds: number): string {
  const s = Math.round(seconds);
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.floor(s / 60)} min ${s % 60} s`;
  if (s < 86400) return `${Math.floor(s / 3600)} h ${Math.floor((s % 3600) / 60)} min`;
  return `${Math.floor(s / 86400)} d ${Math.floor((s % 86400) / 3600)} h`;
}

export interface FunnelRow {
  step: string;
  sessions: number;
  conversion: number;
  dropped: number;
  /** Sessions as a share of the first step, 0 to 1: the bar length. */
  ofStart: number;
  first: boolean;
}

/** The funnel's rows with the sessions lost since the previous step and the share of the first step. */
export function funnelRows(funnel: UxFunnel): FunnelRow[] {
  const start = funnel.steps[0]?.sessions ?? 0;
  return funnel.steps.map((s, i) => {
    const prev = i === 0 ? s.sessions : (funnel.steps[i - 1]?.sessions ?? 0);
    return {
      step: s.step,
      sessions: s.sessions,
      conversion: s.conversionFromPrevious,
      dropped: i === 0 ? 0 : Math.max(prev - s.sessions, 0),
      ofStart: start > 0 ? Math.min(s.sessions / start, 1) : 0,
      first: i === 0,
    };
  });
}

/** The step that loses the most sessions, if any loses one. */
export function biggestDrop(rows: FunnelRow[]): FunnelRow | undefined {
  return rows.reduce<FunnelRow | undefined>((worst, r) => (r.dropped > 0 && (!worst || r.dropped > worst.dropped) ? r : worst), undefined);
}

export interface PageName {
  page: string;
  module: string;
}

/**
 * The page and workspace a route template such as `/purchasing/requisitions/new` belongs to, by name: the page's help
 * title or nav label, and the workspace that owns its first path segment. A route nobody names keeps its path.
 */
export function nameRoute(route: string, entries: readonly NavEntry[]): PageName {
  const segment = route.split("/")[1] ?? "";
  const own = entries.filter((e) => e.to.split("/")[1] === segment);
  const exact = own.find((e) => e.to === route);
  const help = resolveHelp(route);
  const page = exact?.label ?? (help && !help.pattern.endsWith("/*") ? help.entry.title : route);
  const owner = exact ?? own[0];
  const module = owner?.moduleTitle ?? (segment === "" ? "Home" : stepLabel(segment.replace(/-/g, "_")));
  return { page, module };
}

interface ValidationRow extends PageName {
  route: string;
  field: string;
  count: number;
}

interface AbandonedRow extends PageName {
  route: string;
  count: number;
}

const funnelColumns: DataTableColumn<FunnelRow>[] = [
  { key: "step", header: "Step", render: (r) => stepLabel(r.step) },
  { key: "sessions", header: "Sessions", align: "end", render: (r) => count(r.sessions) },
  {
    key: "bar",
    header: "Share of first step",
    render: (r) => (
      <span className={styles.funnelBar} aria-hidden="true">
        <span className={styles.funnelFill} style={{ width: `${Math.round(r.ofStart * 100)}%` }} />
      </span>
    ),
  },
  { key: "ofStart", header: "Of first step", align: "end", render: (r) => percent(r.ofStart) },
  { key: "conversion", header: "From previous step", align: "end", render: (r) => (r.first ? "—" : percent(r.conversion)) },
  { key: "dropped", header: "Dropped off", align: "end", render: (r) => (r.first ? "—" : count(r.dropped)) },
];

const validationColumns: DataTableColumn<ValidationRow>[] = [
  { key: "module", header: "Workspace" },
  { key: "page", header: "Page" },
  { key: "field", header: "Field", mono: true },
  { key: "count", header: "Errors", align: "end", render: (r) => count(r.count) },
];

const searchColumns: DataTableColumn<UxSignals["noResultSearches"][number]>[] = [
  { key: "query", header: "Search text" },
  { key: "count", header: "Searches", align: "end", render: (r) => count(r.count) },
];

const abandonedColumns: DataTableColumn<AbandonedRow>[] = [
  { key: "module", header: "Workspace" },
  { key: "page", header: "Page" },
  { key: "count", header: "Forms left unsaved", align: "end", render: (r) => count(r.count) },
];

interface Filter {
  flow: UxFlow;
  from: string;
  to: string;
  release: string;
}

type FilterErrors = Partial<Record<"from" | "to" | "release", string>>;

export function checkFilter(draft: Filter): FilterErrors {
  const found: FilterErrors = {};
  if (!DATE.test(draft.from)) found.from = "Choose the first day.";
  if (!DATE.test(draft.to)) found.to = "Choose the last day.";
  if (!found.from && !found.to) {
    const span = (Date.parse(`${draft.to}T00:00:00Z`) - Date.parse(`${draft.from}T00:00:00Z`)) / 86_400_000;
    if (span < 0) found.to = "The last day must be on or after the first day.";
    else if (span > UX_MAX_DAYS) found.to = "Choose a period of one year or less.";
  }
  const release = draft.release.trim();
  if (release !== "" && !UX_RELEASE.test(release)) found.release = "Use a release such as 2026.11.1.";
  return found;
}

/**
 * UX telemetry (#510): how far sessions get through procure to pay and tenant onboarding, where they drop off, and the
 * friction signals behind it: fields that fail validation, searches that find nothing and forms left unsaved.
 */
export function UxTelemetryPage() {
  const end = today();
  const [draft, setDraft] = useState<Filter>({ flow: "procure_to_pay", from: daysBefore(end, 29), to: end, release: "" });
  const [filter, setFilter] = useState<Filter>(draft);
  const [errors, setErrors] = useState<FilterErrors>({});
  const [workspace, setWorkspace] = useState("");
  const { entries } = useWorkspaceNav();

  const period = { from: filter.from, to: filter.to, release: filter.release };
  const periodKey = `${filter.from}/${filter.to}/${filter.release}`;
  const funnel = useLoad((signal) => getUxFunnel(filter.flow, period, undefined, signal), `${filter.flow}/${periodKey}`);
  const signals = useLoad((signal) => getUxSignals(period, undefined, signal), periodKey);

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = checkFilter(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) setFilter({ ...draft, release: draft.release.trim() });
  }

  const rows = funnel.data ? funnelRows(funnel.data) : [];
  const started = rows[0]?.sessions ?? 0;
  const finished = rows[rows.length - 1]?.sessions ?? 0;
  const worst = biggestDrop(rows);

  const named = useMemo(() => {
    const s = signals.data;
    if (!s) return { validation: [] as ValidationRow[], abandoned: [] as AbandonedRow[] };
    return {
      validation: s.validationErrors.map((v) => ({ ...v, ...nameRoute(v.route, entries) })),
      abandoned: s.abandonedForms.map((a) => ({ ...a, ...nameRoute(a.route, entries) })),
    };
  }, [signals.data, entries]);
  const workspaces = [...new Set([...named.validation, ...named.abandoned].map((r) => r.module))].sort((a, b) => a.localeCompare(b));
  const inWorkspace = <T extends PageName>(list: T[]) => (workspace === "" ? list : list.filter((r) => r.module === workspace));
  const validation = inWorkspace(named.validation);
  const abandoned = inWorkspace(named.abandoned);

  return (
    <div className={styles.stack}>
      <PageHeader
        title="UX telemetry"
        description="How far people get through the measured flows, where they drop off, and what slows them down. Counts are by browser session; no names or search text that looks personal are kept."
      />
      <form className={styles.toolbar} onSubmit={submit} noValidate aria-label="Telemetry filter">
        <FormField id="ux-flow" label="Flow">
          {(control) => (
            <Select {...control} value={draft.flow} onChange={(e) => setDraft((d) => ({ ...d, flow: e.target.value as UxFlow }))}>
              {UX_FLOWS.map((f) => (
                <option key={f} value={f}>
                  {FLOW_LABELS[f]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <FormField id="ux-from" label="From" error={errors.from}>
          {(control) => (
            <Input {...control} type="date" value={draft.from} onChange={(e) => setDraft((d) => ({ ...d, from: e.target.value }))} invalid={Boolean(errors.from)} />
          )}
        </FormField>
        <FormField id="ux-to" label="To" error={errors.to}>
          {(control) => <Input {...control} type="date" value={draft.to} onChange={(e) => setDraft((d) => ({ ...d, to: e.target.value }))} invalid={Boolean(errors.to)} />}
        </FormField>
        <FormField id="ux-release" label="Release" hint="Leave empty for every release." error={errors.release} optional>
          {(control) => (
            <Input {...control} value={draft.release} onChange={(e) => setDraft((d) => ({ ...d, release: e.target.value }))} invalid={Boolean(errors.release)} />
          )}
        </FormField>
        <Button type="submit">Show telemetry</Button>
      </form>

      <Card title={`${FLOW_LABELS[filter.flow]} funnel`}>
        {funnel.error ? (
          <Alert tone="danger" title="Could not load the funnel" live="alert" action={<Button size="sm" onClick={funnel.reload}>Try again</Button>}>
            <ProblemMessage problem={funnel.error} />
          </Alert>
        ) : (
          <div className={styles.stack}>
            {funnel.data ? (
              <StatGroup label="Funnel summary">
                <StatTile label="Sessions that started" value={count(started)} />
                <StatTile label="Sessions that finished" value={count(finished)} />
                <StatTile label="Finished of started" value={started > 0 ? percent(finished / started) : "—"} />
                <StatTile
                  label="Median time to finish"
                  value={funnel.data.secondsToLastStep != null ? duration(funnel.data.secondsToLastStep) : "—"}
                  hint="From a session's first step to its last"
                />
              </StatGroup>
            ) : null}
            {worst ? (
              <p className={styles.muted}>
                Most sessions are lost before {stepLabel(worst.step).toLowerCase()}: {count(worst.dropped)} did not get there from the step before.
              </p>
            ) : null}
            <DataTable
              label={`${FLOW_LABELS[filter.flow]} funnel steps`}
              columns={funnelColumns}
              rows={started > 0 ? rows : []}
              getRowKey={(r) => r.step}
              loading={funnel.loading}
              empty={{ title: "No sessions in this period", description: "Nobody reached the first step of this flow in the chosen period and release." }}
            />
          </div>
        )}
      </Card>

      {signals.error ? (
        <Alert tone="danger" title="Could not load the friction signals" live="alert" action={<Button size="sm" onClick={signals.reload}>Try again</Button>}>
          <ProblemMessage problem={signals.error} />
        </Alert>
      ) : (
        <>
          <div className={styles.toolbar}>
            <FormField id="ux-workspace" label="Workspace" hint="Narrows the validation errors and unsaved forms.">
              {(control) => (
                <Select {...control} value={workspace} onChange={(e) => setWorkspace(e.target.value)}>
                  <option value="">Every workspace</option>
                  {workspaces.map((w) => (
                    <option key={w} value={w}>
                      {w}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
          <Card title="Fields that fail validation">
            <DataTable
              label="Validation errors by field"
              columns={validationColumns}
              rows={validation}
              getRowKey={(r) => `${r.route}/${r.field}`}
              loading={signals.loading}
              empty={{ title: "No validation errors", description: "No form refused a value in this period." }}
            />
          </Card>
          <Card title="Searches that found nothing">
            <DataTable
              label="Searches without results"
              columns={searchColumns}
              rows={signals.data?.noResultSearches ?? []}
              getRowKey={(r) => r.query}
              loading={signals.loading}
              empty={{ title: "Every search found something", description: "No search came back empty in this period." }}
            />
          </Card>
          <Card title="Forms left unsaved">
            <DataTable
              label="Forms left unsaved by page"
              columns={abandonedColumns}
              rows={abandoned}
              getRowKey={(r) => r.route}
              loading={signals.loading}
              empty={{ title: "No forms left unsaved", description: "Nobody left a form with unsaved changes in this period." }}
            />
          </Card>
        </>
      )}
    </div>
  );
}
