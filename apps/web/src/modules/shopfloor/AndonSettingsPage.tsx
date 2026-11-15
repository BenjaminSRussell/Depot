import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { ProblemMessage } from "../../shell/errors";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { UserName } from "../../shell/lookup/UserName";
import { usePrincipalDuties } from "../../shell/principal";
import {
  Alert,
  Button,
  Card,
  DataTable,
  FormErrorSummary,
  FormField,
  Input,
  PageHeader,
  Skeleton,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type DataTableColumn,
  type FormFieldTarget,
} from "../../shell/ui";
import {
  deleteAndonSiteSettings,
  describeProblem,
  getAndonSettings,
  putAndonSettings,
  putAndonSiteSettings,
  type AndonSettings,
  type AndonSiteSettings,
  type AndonThresholds,
  type Problem,
} from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const DUTY_SETUP = "shopfloor.setup.maintain";

interface Values {
  downtimeMinutes: string;
  clearMinutes: string;
  scrapPercent: string;
  scrapMinCount: string;
}

type Errors = Partial<Record<keyof Values | "siteId", string>>;

const FIELDS: Record<keyof Values, { label: string; hint: string; min: number; max: number }> = {
  downtimeMinutes: { label: "Minutes a machine may stay down", hint: "A machine down longer than this raises the downtime alert. 1 to 240.", min: 1, max: 240 },
  clearMinutes: { label: "Minutes a condition must stay clear", hint: "An alert closes once its condition has been clear this long. 0 to 120.", min: 0, max: 120 },
  scrapPercent: { label: "Scrap share that raises an alert (%)", hint: "More than this share of the pieces in an OEE bucket being scrap raises the scrap-rate alert. 1 to 100.", min: 1, max: 100 },
  scrapMinCount: { label: "Fewest pieces a bucket must have", hint: "A bucket with fewer pieces never raises a scrap-rate alert. 1 to 100000.", min: 1, max: 100000 },
};

const ORDER = Object.keys(FIELDS) as (keyof Values)[];

export function valuesOf(t: AndonThresholds): Values {
  return {
    downtimeMinutes: String(t.downtimeMinutes),
    clearMinutes: String(t.clearMinutes),
    scrapPercent: String(t.scrapPercent),
    scrapMinCount: String(t.scrapMinCount),
  };
}

/** Checks that mirror the service, and the request the form sends. */
export function validateThresholds(values: Values): { errors: Errors; input?: AndonThresholds } {
  const errors: Errors = {};
  const out: Partial<AndonThresholds> = {};
  for (const name of ORDER) {
    const spec = FIELDS[name];
    const raw = values[name].trim();
    const n = Number(raw);
    if (!/^\d{1,6}$/.test(raw) || n < spec.min || n > spec.max) errors[name] = `Enter a whole number from ${spec.min} to ${spec.max}.`;
    else out[name] = n;
  }
  return Object.keys(errors).length > 0 ? { errors } : { errors, input: out as AndonThresholds };
}

const FIELD_TARGETS = (prefix: string): Record<string, FormFieldTarget> => ({
  ...Object.fromEntries(ORDER.map((n) => [n, { id: `${prefix}-${n}`, label: FIELDS[n].label }])),
  siteId: { id: `${prefix}-site`, label: "Site" },
});

function ThresholdFields({ prefix, values, errors, onChange, disabled }: { prefix: string; values: Values; errors: Errors; onChange(patch: Partial<Values>): void; disabled?: boolean }) {
  return (
    <>
      {ORDER.map((name) => (
        <FormField key={name} id={`${prefix}-${name}`} label={FIELDS[name].label} hint={FIELDS[name].hint} error={errors[name]}>
          {(c) => <Input {...c} inputMode="numeric" disabled={disabled} value={values[name]} onChange={(e) => onChange({ [name]: e.target.value })} invalid={Boolean(errors[name])} />}
        </FormField>
      ))}
    </>
  );
}

function fieldErrorsOf(problem: Problem): Errors {
  const out: Errors = {};
  for (const name of [...ORDER, "siteId"] as const) if (problem.fields[name]) out[name] = problem.fields[name];
  return out;
}

function TenantForm({ settings, canEdit, onSaved }: { settings: AndonSettings; canEdit: boolean; onSaved(): void }) {
  const [values, setValues] = useState<Values>(valuesOf(settings));
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));

  async function submit(event: FormEvent) {
    event.preventDefault();
    setSaved(false);
    setFailure(null);
    const { errors: found, input } = validateThresholds(values);
    setErrors(found);
    if (!input) return;
    setBusy(true);
    try {
      await putAndonSettings(input);
      release();
      setSaved(true);
      onSaved();
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = fieldErrorsOf(problem);
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Thresholds for every site">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Andon thresholds">
        <FormErrorSummary items={summaryItems(errors, FIELD_TARGETS("andon"))} attempt={attempt} />
        <p className={styles.note}>
          {settings.default ? "No thresholds have been set: the standard values apply (10 minutes down, 5 minutes clear, 5 % scrap over 10 pieces)." : null}
          {!settings.default && settings.updatedBy && settings.updatedAt ? (
            <>
              Last set by <UserName id={settings.updatedBy} /> on {formatDateTime(settings.updatedAt)}.
            </>
          ) : null}
        </p>
        <ThresholdFields prefix="andon" values={values} errors={errors} onChange={(p) => setValues((v) => ({ ...v, ...p }))} disabled={!canEdit} />
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the thresholds" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        {saved ? (
          <div className={styles.formActions}>
            <Alert tone="success" title="Thresholds saved" live="status">
              The next machine event and andon sweep use them.
            </Alert>
          </div>
        ) : null}
        {canEdit ? (
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save thresholds
            </Button>
          </div>
        ) : (
          <p className={styles.note}>Changing the thresholds needs the shop floor setup duty ({DUTY_SETUP}).</p>
        )}
      </form>
      {prompt}
    </Card>
  );
}

function SiteForm({ taken, onSaved }: { taken: Set<string>; onSaved(): void }) {
  const [siteId, setSiteId] = useState("");
  const [values, setValues] = useState<Values>({ downtimeMinutes: "10", clearMinutes: "5", scrapPercent: "5", scrapMinCount: "10" });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const attempt = useSubmitAttempt(errors);

  async function submit(event: FormEvent) {
    event.preventDefault();
    setFailure(null);
    const { errors: found, input } = validateThresholds(values);
    if (siteId.trim() === "") found.siteId = "Choose the site.";
    setErrors(found);
    if (!input || found.siteId) return;
    setBusy(true);
    try {
      await putAndonSiteSettings(siteId.trim().toLowerCase(), input);
      setSiteId("");
      onSaved();
    } catch (error) {
      const problem = describeProblem(error);
      const fieldErrors = fieldErrorsOf(problem);
      if (Object.keys(fieldErrors).length > 0) setErrors(fieldErrors);
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Set values for one site">
      <form className={styles.form} onSubmit={submit} noValidate aria-label="Andon thresholds of a site">
        <FormErrorSummary items={summaryItems(errors, FIELD_TARGETS("site-andon"))} attempt={attempt} />
        <p className={styles.note}>The machines of the site use these values instead of the ones for every site. Choosing a site that has values already replaces them.</p>
        <FormField id="site-andon-site" label="Site" error={errors.siteId} hint={taken.size > 0 ? "Sites that already have values are listed above." : undefined}>
          {(c) => <EntityLookup {...c} entity="site" value={siteId} onChange={(e) => setSiteId(e.target.value)} invalid={Boolean(errors.siteId)} />}
        </FormField>
        <ThresholdFields prefix="site-andon" values={values} errors={errors} onChange={(p) => setValues((v) => ({ ...v, ...p }))} />
        {failure ? (
          <div className={styles.formActions}>
            <Alert tone="danger" title="Could not save the values of the site" live="alert">
              <ProblemMessage problem={failure} />
            </Alert>
          </div>
        ) : null}
        <div className={styles.formActions}>
          <Button type="submit" variant="primary" loading={busy}>
            Save values for the site
          </Button>
        </div>
      </form>
    </Card>
  );
}

/** The andon thresholds: one set for the tenant, and values of their own for the sites that need them (FR-25.11). */
export function AndonSettingsPage() {
  const canEdit = holdsDuty(usePrincipalDuties(), DUTY_SETUP);
  const state = useLoad((signal) => getAndonSettings(signal), "andon-settings");
  const [removing, setRemoving] = useState("");
  const [removeFailure, setRemoveFailure] = useState<Problem | null>(null);
  const settings = state.data;

  async function remove(site: AndonSiteSettings) {
    setRemoving(site.siteId);
    setRemoveFailure(null);
    try {
      await deleteAndonSiteSettings(site.siteId);
      state.reload();
    } catch (error) {
      setRemoveFailure(describeProblem(error));
    } finally {
      setRemoving("");
    }
  }

  const columns: DataTableColumn<AndonSiteSettings>[] = [
    { key: "siteId", header: "Site", render: (s) => <EntityName entity="site" id={s.siteId} /> },
    { key: "downtimeMinutes", header: "Minutes down", align: "end" },
    { key: "clearMinutes", header: "Minutes clear", align: "end" },
    { key: "scrapPercent", header: "Scrap share (%)", align: "end" },
    { key: "scrapMinCount", header: "Fewest pieces", align: "end" },
    { key: "updatedBy", header: "Set by", render: (s) => <><UserName id={s.updatedBy} />, {formatDateTime(s.updatedAt)}</> },
    {
      key: "actions",
      header: "Actions",
      render: (s) =>
        canEdit ? (
          <Button size="sm" loading={removing === s.siteId} onClick={() => void remove(s)}>
            Use the values for every site
          </Button>
        ) : (
          "—"
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Andon settings"
        description={
          <>
            How long a machine may stay down before the andon alert, and when an alert closes. <Link to="/shopfloor/andon">Open the andon board</Link>
          </>
        }
      />
      {state.error && !settings ? (
        <Alert tone="danger" title="Could not load the andon settings" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      ) : !settings ? (
        <Card>
          <Skeleton />
        </Card>
      ) : (
        <>
          <TenantForm settings={settings} canEdit={canEdit} onSaved={state.reload} />
          <Card title="Sites with values of their own">
            {removeFailure ? (
              <Alert tone="danger" title="Could not remove the values of the site" live="alert">
                <ProblemMessage problem={removeFailure} />
              </Alert>
            ) : null}
            <DataTable
              label="Sites with their own andon values"
              columns={columns}
              rows={settings.sites}
              getRowKey={(s) => s.siteId}
              empty={{ title: "No site has values of its own", description: "Every machine uses the thresholds above." }}
            />
          </Card>
          {canEdit ? <SiteForm taken={new Set(settings.sites.map((s) => s.siteId))} onSaved={state.reload} /> : null}
        </>
      )}
    </div>
  );
}
