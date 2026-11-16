import { useEffect, useMemo, useState, type FormEvent } from "react";
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
  /** Sessions as a share of the first step, 0 to 1 (capped); null when the first step has none. */
  ofStart: number | null;
  /** Sessions as a share of the busiest step, 0 to 1: the bar length. */
  ofLargest: number;
  first: boolean;
}

/**
 * The funnel's rows with the sessions lost since the previous step. Steps are counted per session, and a session may
 * have started before the period or under another release, so a later step can count more than the first one, and the
 * first one can be empty: shares of the first step are capped at 100% and left out when it has no sessions.
 */
export function funnelRows(funnel: UxFunnel): FunnelRow[] {
  const start = funnel.steps[0]?.sessions ?? 0;
  const largest = Math.max(0, ...funnel.steps.map((s) => s.sessions));
  return funnel.steps.map((s, i) => {
    const prev = i === 0 ? s.sessions : (funnel.steps[i - 1]?.sessions ?? 0);
    return {
      step: s.step,
      sessions: s.sessions,
      conversion: s.conversionFromPrevious,
      dropped: i === 0 ? 0 : Math.max(prev - s.sessions, 0),
      ofStart: start > 0 ? Math.min(s.sessions / start, 1) : null,
      ofLargest: largest > 0 ? s.sessions / largest : 0,
      first: i === 0,
    };
  });
}

export interface FunnelSummary {
  started: number;
  finished: number;
  /** Finished over started, capped at 1; null when no session reached the first step. */
  finishedShare: number | null;
  /** True when a later step counts more sessions than the first. */
  laterExceedsFirst: boolean;
  /** True when any step has a session. */
  any: boolean;
}

export function funnelSummary(rows: FunnelRow[]): FunnelSummary {
  const started = rows[0]?.sessions ?? 0;
  const finished = rows[rows.length - 1]?.sessions ?? 0;
  return {
    started,
    finished,
    finishedShare: started > 0 ? Math.min(finished / started, 1) : null,
    laterExceedsFirst: rows.some((r) => r.sessions > started),
    any: rows.some((r) => r.sessions > 0),
  };
}

/** The step that loses the most sessions, if any loses one. */
export function biggestDrop(rows: FunnelRow[]): FunnelRow | undefined {
  return rows.reduce<FunnelRow | undefined>((worst, r) => (r.dropped > 0 && (!worst || r.dropped > worst.dropped) ? r : worst), undefined);
}

export interface PageName {
  page: string;
  module: string;
}

const ACRONYMS: Record<string, string> = { ap: "AP", ar: "AR", eam: "EAM", fx: "FX", gl: "GL", hr: "HR", ic: "IC", pi: "PI", rfq: "RFQ", rfid: "RFID", uom: "UoM", vat: "VAT", id: "ID" };

/** "ap-invoices" -> "AP invoices": a route segment or name in words, sentence case, acronyms kept. */
export function wordsOf(name: string): string {
  const words = name
    .replace(/([a-z0-9])([A-Z])/g, "$1 $2")
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((w) => w.toLowerCase());
  return words
    .map((w, i) => ACRONYMS[w] ?? (i === 0 ? w.charAt(0).toUpperCase() + w.slice(1) : w))
    .join(" ");
}

/**
 * A readable label for a form-field path as the service names it: `lines[0].unitPrice` -> "Line 1 – Unit price",
 * `vendorInvoiceNumber` -> "Vendor invoice number". The raw path stays available as a tooltip.
 */
export function fieldLabel(path: string): string {
  return path
    .split(".")
    .filter(Boolean)
    .map((part) => {
      const m = /^([A-Za-z][A-Za-z0-9_-]*)\[(\d+)\]$/.exec(part);
      if (!m) return wordsOf(part);
      const base = wordsOf(m[1]).replace(/sses$/, "ss").replace(/ies$/, "y").replace(/([^s])s$/, "$1");
      return `${base} ${Number(m[2]) + 1}`;
    })
    .join(" – ");
}

/** The workspaces the viewer's navigation offers: route prefix (such as /purchasing) and name, by name. */
export function workspaceOptions(entries: readonly NavEntry[]): { prefix: string; name: string }[] {
  const byPrefix = new Map<string, string>();
  for (const e of entries) {
    const segment = e.to.split("/")[1] ?? "";
    if (segment !== "" && !byPrefix.has(`/${segment}`)) byPrefix.set(`/${segment}`, e.moduleTitle);
  }
  return [...byPrefix].map(([prefix, name]) => ({ prefix, name })).sort((a, b) => a.name.localeCompare(b.name));
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
  const module = owner?.moduleTitle ?? (segment === "" ? "Home" : wordsOf(segment));
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
    header: "Compared with the busiest step",
    render: (r) => (
      <span className={styles.funnelBar} aria-hidden="true">
        <span className={styles.funnelFill} style={{ width: `${Math.round(r.ofLargest * 100)}%` }} />
      </span>
    ),
  },
  { key: "ofStart", header: "Of first step", align: "end", render: (r) => (r.ofStart === null ? "—" : percent(r.ofStart)) },
  { key: "conversion", header: "From previous step", align: "end", render: (r) => (r.first ? "—" : percent(r.conversion)) },
  { key: "dropped", header: "Dropped off", align: "end", render: (r) => (r.first ? "—" : count(r.dropped)) },
];

const validationColumns: DataTableColumn<ValidationRow>[] = [
  { key: "module", header: "Workspace" },
  { key: "page", header: "Page" },
  { key: "field", header: "Field", render: (r) => <span title={r.field}>{fieldLabel(r.field)}</span> },
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
  const [chosenWorkspace, setWorkspace] = useState("");
  const { entries } = useWorkspaceNav();
  const workspaces = useMemo(() => workspaceOptions(entries), [entries]);
  // A workspace that is no longer offered (the navigation changed) filters nothing, and the select shows it so.
  const workspace = workspaces.some((w) => w.prefix === chosenWorkspace) ? chosenWorkspace : "";
  useEffect(() => {
    if (workspace !== chosenWorkspace) setWorkspace("");
  }, [workspace, chosenWorkspace]);

  const period = { from: filter.from, to: filter.to, release: filter.release };
  const periodKey = `${filter.from}/${filter.to}/${filter.release}`;
  const funnel = useLoad((signal) => getUxFunnel(filter.flow, period, undefined, signal), `${filter.flow}/${periodKey}`);
  const signals = useLoad((signal) => getUxSignals({ ...period, routePrefix: workspace }, undefined, signal), `${periodKey}/${workspace}`);
  const workspaceName = workspaces.find((w) => w.prefix === workspace)?.name;

  function submit(event: FormEvent) {
    event.preventDefault();
    const found = checkFilter(draft);
    setErrors(found);
    if (Object.keys(found).length === 0) setFilter({ ...draft, release: draft.release.trim() });
  }

  const rows = funnel.data ? funnelRows(funnel.data) : [];
  const summary = funnelSummary(rows);
  const worst = biggestDrop(rows);

  const named = useMemo(() => {
    const s = signals.data;
    if (!s) return { validation: [] as ValidationRow[], abandoned: [] as AbandonedRow[] };
    return {
      validation: s.validationErrors.map((v) => ({ ...v, ...nameRoute(v.route, entries) })),
      abandoned: s.abandonedForms.map((a) => ({ ...a, ...nameRoute(a.route, entries) })),
    };
  }, [signals.data, entries]);
  const scope = workspaceName ? `the 20 most frequent in ${workspaceName}` : "the 20 most frequent across every workspace";

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
                <StatTile label="Sessions that started" value={count(summary.started)} />
                <StatTile label="Sessions that finished" value={count(summary.finished)} />
                <StatTile
                  label="Finished of started"
                  value={summary.finishedShare === null ? "—" : percent(summary.finishedShare)}
                  hint={
                    summary.finishedShare === null
                      ? "No session reached the first step in this period"
                      : summary.laterExceedsFirst
                        ? "Capped at 100%: some sessions began before this period or release"
                        : undefined
                  }
                />
                <StatTile
                  label="Median time to finish"
                  value={funnel.data.secondsToLastStep != null ? duration(funnel.data.secondsToLastStep) : "—"}
                  hint="From a session's first step to its last"
                />
              </StatGroup>
            ) : null}
            {summary.laterExceedsFirst ? (
              <p className={styles.muted}>
                A later step counts more sessions than the first: steps are counted per session, and some sessions began the flow before this period or
                under another release. Shares of the first step are capped at 100%.
              </p>
            ) : null}
            {worst ? (
              <p className={styles.muted}>
                Most sessions are lost before {stepLabel(worst.step).toLowerCase()}: {count(worst.dropped)} did not get there from the step before.
              </p>
            ) : null}
            <DataTable
              label={`${FLOW_LABELS[filter.flow]} funnel steps`}
              columns={funnelColumns}
              rows={summary.any ? rows : []}
              getRowKey={(r) => r.step}
              loading={funnel.loading}
              empty={{ title: "No sessions in this period", description: "Nobody reached any step of this flow in the chosen period and release." }}
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
            <FormField id="ux-workspace" label="Workspace" hint="Shows the fields and unsaved forms of one workspace. Searches are always counted across every workspace.">
              {(control) => (
                <Select {...control} value={workspace} onChange={(e) => setWorkspace(e.target.value)}>
                  <option value="">Every workspace</option>
                  {workspaces.map((w) => (
                    <option key={w.prefix} value={w.prefix}>
                      {w.name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
          <Card title="Fields that fail validation">
            <p className={styles.muted}>Showing {scope}.</p>
            <DataTable
              label="Validation errors by field"
              columns={validationColumns}
              rows={named.validation}
              getRowKey={(r) => `${r.route}/${r.field}`}
              loading={signals.loading}
              empty={{ title: "No validation errors", description: "No form refused a value in this period." }}
            />
          </Card>
          <Card title="Searches that found nothing">
            <p className={styles.muted}>Showing the 20 most frequent across every workspace: a search is not tied to a page.</p>
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
            <p className={styles.muted}>Showing {scope}.</p>
            <DataTable
              label="Forms left unsaved by page"
              columns={abandonedColumns}
              rows={named.abandoned}
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
