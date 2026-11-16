import { useRef, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router-dom";
import {
  Alert,
  Button,
  Card,
  DataTable,
  EmptyState,
  FormField,
  Input,
  PageHeader,
  Skeleton,
  type DataTableColumn,
} from "../../shell/ui";
import {
  createSavedQuery,
  deleteSavedQuery,
  describeProblem,
  listMetrics,
  listSavedQueries,
  MAX_DIMENSIONS,
  MAX_LOOKBACK_DAYS,
  MAX_MEASURES,
  MAX_SPAN_YEARS,
  planQuery,
  type Filter,
  type Metric,
  type Plan,
  type Problem,
  type QueryInput,
} from "./api/client";
import { ExportPanel } from "./ExportsPage";
import { DATE, dimensionLabel, grainLabel, monthStart, today } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
import { clearResolvedOptions } from "../../shell/lookup/entities";
import { ProblemMessage } from "../../shell/errors";

type Errors = Partial<Record<"measures" | "dimensions" | "from" | "to", string>>;

/** Dimensions every selected measure allows (the service rejects any other). */
export function allowedDimensions(catalog: Metric[], measures: string[]): string[] {
  const selected = catalog.filter((m) => measures.includes(m.metricId));
  if (selected.length === 0) return [];
  return selected[0].allowedDimensions.filter((d) => selected.every((m) => m.allowedDimensions.includes(d)));
}

function addYears(date: string, years: number): string {
  return `${Number(date.slice(0, 4)) + years}${date.slice(4)}`;
}

/** Checks that mirror domain.ValidateShape and BuildPlan in services/analytics. */
export function validateQuery(input: QueryInput, catalog: Metric[]): Errors {
  const errors: Errors = {};
  if (input.measures.length === 0) errors.measures = "Choose at least one metric.";
  else if (input.measures.length > MAX_MEASURES) errors.measures = `Choose at most ${MAX_MEASURES} metrics.`;
  else {
    const grains = new Set(catalog.filter((m) => input.measures.includes(m.metricId)).map((m) => m.grain));
    if (grains.size > 1) errors.measures = "Choose metrics of one grain; journal-line, sales-line and resource-shift metrics cannot be combined.";
  }
  if (input.dimensions.length > MAX_DIMENSIONS) errors.dimensions = `Choose at most ${MAX_DIMENSIONS} dimensions.`;
  if (!DATE.test(input.from)) errors.from = "Choose the first day.";
  if (!DATE.test(input.to)) errors.to = "Choose the last day.";
  else if (DATE.test(input.from) && input.to < input.from) errors.to = "The last day must not be before the first day.";
  else if (DATE.test(input.from) && input.to > addYears(input.from, MAX_SPAN_YEARS))
    errors.to = `The range must not exceed ${MAX_SPAN_YEARS} years.`;
  return errors;
}

const filterColumns: DataTableColumn<Filter>[] = [
  { key: "member", header: "Member", mono: true },
  { key: "operator", header: "Operator", mono: true },
  { key: "values", header: "Values", mono: true, render: (f) => f.values.join(", ") },
];

function PlanResult({ plan }: { plan: Plan }) {
  if (plan.reason === "NO_SCOPE") {
    return (
      <Card>
        <EmptyState
          icon="lock"
          title="You have no data scope for this model"
          description="Your access grants no legal entity, so this query returns no rows. Ask your workspace administrator for legal-entity access."
        />
      </Card>
    );
  }
  return (
    <Card title="Query plan">
      <dl className={styles.details}>
        <div className={styles.entry}>
          <dt className={styles.term}>Model</dt>
          <dd className={`${styles.value} ${styles.mono}`}>{plan.model}</dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Measures</dt>
          <dd className={`${styles.value} ${styles.mono}`}>{plan.measures.join(", ")}</dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Dimensions</dt>
          <dd className={styles.value}>{plan.dimensions.length > 0 ? plan.dimensions.map(dimensionLabel).join(", ") : "None (one total)"}</dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Range</dt>
          <dd className={`${styles.value} ${styles.mono}`}>
            {plan.from} to {plan.to}
          </dd>
        </div>
      </dl>
      <DataTable
        label="Scope filters"
        columns={filterColumns}
        rows={plan.filters}
        getRowKey={(f) => f.member}
        empty={{ title: "No scope filter", description: "You hold every legal entity in this tenant, so no legal-entity filter is added." }}
      />
    </Card>
  );
}

function daysAgo(days: number): string {
  const d = new Date(`${today()}T00:00:00Z`);
  d.setUTCDate(d.getUTCDate() - days);
  return d.toISOString().slice(0, 10);
}

/** Whole days a query's range covers, to keep as the look-back of a saved query. */
export function lookbackOf(input: QueryInput): number {
  const span = Math.round((Date.parse(`${input.to}T00:00:00Z`) - Date.parse(`${input.from}T00:00:00Z`)) / 86_400_000);
  return Math.min(MAX_LOOKBACK_DAYS, Math.max(1, Number.isFinite(span) ? span : 30));
}

/**
 * Saved queries: pick one of your own by name to fill the form, or save the current choice under a name. A saved query
 * keeps its metrics, dimensions and a look-back (the last N days up to today), so it stays current and can be scheduled.
 */
function SavedQueriesPanel({ input, apply }: { input: QueryInput; apply(next: QueryInput): void }) {
  const [savedId, setSavedId] = useState("");
  const [name, setName] = useState("");
  const [days, setDays] = useState(() => String(lookbackOf(input)));
  const [message, setMessage] = useState<string | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const [nameError, setNameError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const key = useRef(crypto.randomUUID());

  async function pick(id: string) {
    setSavedId(id);
    setMessage(null);
    setFailure(null);
    if (id === "") return;
    try {
      const found = (await listSavedQueries()).find((q) => q.id === id);
      if (!found) return;
      apply({ measures: found.measures, dimensions: found.dimensions, from: daysAgo(found.lookbackDays), to: today() });
      setDays(String(found.lookbackDays));
      setMessage(`Loaded ${found.name}.`);
    } catch (error) {
      setFailure(describeProblem(error));
    }
  }

  async function save(event: FormEvent) {
    event.preventDefault();
    setMessage(null);
    setFailure(null);
    const lookback = Number(days);
    if (name.trim() === "") return setNameError("Name the query.");
    if (!Number.isInteger(lookback) || lookback < 1 || lookback > MAX_LOOKBACK_DAYS) return setNameError(`Keep a look-back of 1 to ${MAX_LOOKBACK_DAYS} days.`);
    if (input.measures.length === 0) return setNameError("Choose at least one metric before saving.");
    setNameError(undefined);
    setBusy(true);
    try {
      const saved = await createSavedQuery({ name: name.trim(), measures: input.measures, dimensions: input.dimensions, lookbackDays: lookback }, key.current);
      key.current = crypto.randomUUID();
      clearResolvedOptions();
      setSavedId(saved.id);
      setName("");
      setMessage(`Saved ${saved.name}.`);
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "ANA-0115") setNameError("You already have a saved query with this name.");
      else setFailure(problem);
      if (problem.status >= 400 && problem.status < 500) key.current = crypto.randomUUID();
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    setBusy(true);
    setMessage(null);
    try {
      await deleteSavedQuery(savedId);
      clearResolvedOptions();
      setSavedId("");
      setMessage("Deleted the saved query.");
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Saved queries">
      <form className={styles.form} onSubmit={save} noValidate aria-label="Saved queries">
        <LookupFormField id="saved-query" label="Saved query" optional entity="savedQuery" hint="Fills the form from one of your saved queries." value={savedId} onChange={(id) => void pick(id)} />
        <FormField id="saved-query-name" label="Save the current choice as" error={nameError}>
          {(control) => <Input {...control} maxLength={120} value={name} invalid={Boolean(nameError)} onChange={(e) => setName(e.target.value)} />}
        </FormField>
        <FormField id="saved-query-days" label="Look-back (days)" hint="The saved range is the last N days up to today.">
          {(control) => <Input {...control} inputMode="numeric" value={days} onChange={(e) => setDays(e.target.value)} />}
        </FormField>
        <div className={styles.formActions}>
          <Button type="submit" loading={busy}>
            Save query
          </Button>
          {savedId ? (
            <Button variant="ghost" onClick={() => void remove()} disabled={busy}>
              Delete the selected query
            </Button>
          ) : null}
        </div>
      </form>
      {failure ? (
        <Alert tone="danger" title="The saved query could not be used" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {message ? (
        <p role="status" className={styles.muted}>
          {message}
        </p>
      ) : null}
    </Card>
  );
}

/**
 * Self-service explorer (FR-46.9, FR-46.7): compose metrics by dimensions over
 * a date range and see the Cube plan the service builds, including the
 * legal-entity filter mirrored from your access, then export that query
 * (slice 4). Running the plan for rows is a later slice.
 */
export function ExplorerPage() {
  const [params] = useSearchParams();
  const preset = params.get("measure");
  const catalog = useLoad((signal) => listMetrics(undefined, signal), "metrics");
  const [input, setInput] = useState<QueryInput>(() => ({
    measures: preset ? [preset] : [],
    dimensions: [],
    from: monthStart(today()),
    to: today(),
  }));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [plan, setPlan] = useState<{ plan: Plan; query: QueryInput } | null>(null);
  const [busy, setBusy] = useState(false);

  const metrics = catalog.data ?? [];
  const dimensions = allowedDimensions(metrics, input.measures);

  function toggle(list: "measures" | "dimensions", value: string, on: boolean) {
    setInput((current) => {
      const next = on ? [...current[list], value] : current[list].filter((v) => v !== value);
      const updated = { ...current, [list]: next };
      // Drop dimensions the new measure set no longer allows.
      if (list === "measures") updated.dimensions = current.dimensions.filter((d) => allowedDimensions(metrics, next).includes(d));
      return updated;
    });
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateQuery(input, metrics);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      // The export panel exports the query this plan was built from, not later edits.
      setPlan({ plan: await planQuery(input), query: input });
    } catch (error) {
      const problem = describeProblem(error);
      setErrors(problem.fields as Errors);
      setFailure(problem);
      setPlan(null);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Explorer"
        description="Choose governed metrics, the dimensions to group by and a date range. The plan shows exactly what runs, including the legal-entity filter from your access."
      />
      {catalog.error ? (
        <Alert tone="danger" title="Could not load the metric catalog" live="alert" action={<Button size="sm" onClick={catalog.reload}>Try again</Button>}>
          <ProblemMessage problem={catalog.error} />
        </Alert>
      ) : catalog.loading ? (
        <Card>
          <div className={styles.loading} aria-busy="true">
            <Skeleton width="1/2" height="control" />
            <Skeleton width="3/4" height="control" />
            <Skeleton width="1/4" height="control" />
          </div>
          <p role="status" className={styles.muted}>
            Loading governed metrics…
          </p>
        </Card>
      ) : metrics.length === 0 ? (
        <Card>
          <EmptyState icon="database" title="No published metrics yet" description="The explorer lists metrics once a version is published." />
        </Card>
      ) : (
        <Card>
          <form className={styles.form} onSubmit={submit} noValidate aria-label="Explorer query">
            <fieldset className={styles.choices} aria-describedby={errors.measures ? "explorer-measures-error" : undefined}>
              <legend className={styles.choicesLegend}>Metrics</legend>
              {metrics.map((m) => (
                <span key={m.metricId} className={styles.check}>
                  <Input
                    id={`measure-${m.metricId}`}
                    type="checkbox"
                    checked={input.measures.includes(m.metricId)}
                    onChange={(e) => toggle("measures", m.metricId, e.target.checked)}
                  />
                  <label htmlFor={`measure-${m.metricId}`} title={m.formula}>
                    {m.name}
                  </label>
                  <span className={styles.muted}>{grainLabel(m.grain)}</span>
                </span>
              ))}
              {errors.measures ? (
                <p id="explorer-measures-error" role="alert" className={styles.fieldError}>
                  {errors.measures}
                </p>
              ) : null}
            </fieldset>
            <fieldset className={styles.choices} aria-describedby={errors.dimensions ? "explorer-dimensions-error" : undefined}>
              <legend className={styles.choicesLegend}>Group by</legend>
              {dimensions.length === 0 ? (
                <p className={styles.muted}>Choose a metric to see the dimensions it can be grouped by.</p>
              ) : (
                dimensions.map((d) => (
                  <span key={d} className={styles.check}>
                    <Input id={`dimension-${d}`} type="checkbox" checked={input.dimensions.includes(d)} onChange={(e) => toggle("dimensions", d, e.target.checked)} />
                    <label htmlFor={`dimension-${d}`}>{dimensionLabel(d)}</label>
                  </span>
                ))
              )}
              {errors.dimensions ? (
                <p id="explorer-dimensions-error" role="alert" className={styles.fieldError}>
                  {errors.dimensions}
                </p>
              ) : null}
            </fieldset>
            <FormField id="explorer-from" label="From" error={errors.from}>
              {(control) => (
                <Input {...control} type="date" value={input.from} onChange={(e) => setInput((v) => ({ ...v, from: e.target.value }))} invalid={Boolean(errors.from)} />
              )}
            </FormField>
            <FormField id="explorer-to" label="To" error={errors.to}>
              {(control) => (
                <Input {...control} type="date" value={input.to} onChange={(e) => setInput((v) => ({ ...v, to: e.target.value }))} invalid={Boolean(errors.to)} />
              )}
            </FormField>
            {failure ? (
              <div className={styles.formActions}>
                <Alert tone="danger" title="The query was not accepted" live="alert">
                  <ProblemMessage problem={failure} />
                </Alert>
              </div>
            ) : null}
            <div className={styles.formActions}>
              <Button type="submit" variant="primary" loading={busy}>
                Build plan
              </Button>
            </div>
          </form>
        </Card>
      )}
      {metrics.length > 0 ? <SavedQueriesPanel input={input} apply={(next) => { setInput(next); setPlan(null); setErrors({}); }} /> : null}
      {plan ? <PlanResult plan={plan.plan} /> : null}
      {plan && plan.plan.reason !== "NO_SCOPE" ? <ExportPanel key={JSON.stringify(plan.query)} query={plan.query} /> : null}
    </div>
  );
}
