import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { LookupFormField } from "../../shell/lookup/LookupFormField";
import { usePrincipalDuties } from "../../shell/principal";
import {
  Alert,
  Badge,
  Button,
  buttonClassName,
  Card,
  FormErrorSummary,
  FormField,
  FormSection,
  Input,
  ListPage,
  loadedRows,
  PageHeader,
  Select,
  Skeleton,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
  type ListColumn,
  type ListFilterDef,
  type ListQuery,
} from "../../shell/ui";
import {
  createPIStep,
  describeProblem,
  DUTY_PI_STEP_MAINTAIN,
  getPIStep,
  listPISteps,
  newPIStepVersion,
  publishPIStep,
  updatePIStep,
  type InstructionCategory,
  type PIStepSpec,
  type PIStepStatus,
  type PIStepTemplate,
  type Problem,
} from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { CATEGORIES, CATEGORY_LABEL, newInstruction, stepKindProblems } from "./piSheet";
import { StepFields } from "./PiStepFields";
import { PI_CODE } from "./PiCategoryPages";
import { useLoad } from "./useLoad";

export const STEP_STATUS_LABEL: Record<PIStepStatus, string> = { draft: "Draft", published: "Published", superseded: "Superseded" };
const STATUS_TONE = { draft: "warning", published: "success", superseded: "neutral" } as const;

export interface StepValues {
  code: string;
  name: string;
  categoryId: string;
  spec: PIStepSpec;
}

type Errors = Record<string, string>;
const FIELDS: Record<string, FormFieldTarget> = {
  code: { id: "pis-code", label: "Code" },
  name: { id: "pis-name", label: "Name" },
};

/** A fresh spec of a kind: the same starting point as a step in a sheet, without a position. */
export function newSpec(kind: InstructionCategory): PIStepSpec {
  const { seq: _seq, ...spec } = newInstruction(kind, 1);
  if (kind === "branch") delete spec.target;
  return spec;
}

/** Checks that mirror the service, and the request the form sends. */
export function validateStep(values: StepValues, creating: boolean): { errors: Errors; spec?: string[]; input?: { code: string; name: string; categoryId: string | null; spec: PIStepSpec } } {
  const errors: Errors = {};
  if (creating && !PI_CODE.test(values.code.trim())) errors.code = "Use 1 to 40 characters: upper-case letters, digits, '_', '.' or '-'.";
  const name = values.name.trim();
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  const s = values.spec;
  const specProblems = stepKindProblems({ ...s, seq: 1 }, "The step");
  if (["instruction-text", "signature-request", "material-list", "sequence-definition", "calculation", "timer", "branch"].includes(s.category) && (s.text ?? "").trim() === "") specProblems.push("The step: write the text.");
  if (s.category === "calculation" && (s.expression ?? "").trim() === "") specProblems.push("The step: write the formula.");
  if (["process-parameter-setpoint", "process-data-request", "inspection-characteristic", "machine-readout"].includes(s.category) && (s.characteristic?.name ?? "").trim() === "") specProblems.push("The step: name the characteristic.");
  // A template's branch has no jump yet: it is set where a sheet uses the step.
  const filtered = specProblems.filter((p) => !p.includes("jump to a later step"));
  if (Object.keys(errors).length > 0 || filtered.length > 0) return { errors, spec: filtered };
  return { errors, input: { code: values.code.trim(), name, categoryId: values.categoryId || null, spec: { ...s, text: s.text ?? "" } } };
}

function StepForm({
  initial,
  creating,
  canEdit,
  busy,
  failure,
  submitLabel,
  onSubmit,
  onSaved,
  extra,
}: {
  initial: StepValues;
  creating: boolean;
  canEdit: boolean;
  busy: boolean;
  failure: Problem | null;
  submitLabel: string;
  onSubmit(input: { code: string; name: string; categoryId: string | null; spec: PIStepSpec }): Promise<boolean>;
  onSaved?(): void;
  extra?: React.ReactNode;
}) {
  const [values, setValues] = useState(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [specProblems, setSpecProblems] = useState<string[]>([]);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, spec, input } = validateStep(values, creating);
    setErrors(found);
    setSpecProblems(spec ?? []);
    if (!input) return;
    if (await onSubmit(input)) {
      release();
      onSaved?.();
    }
  }

  return (
    <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="PI step template">
      <FormErrorSummary items={summaryItems(errors, FIELDS)} attempt={attempt} />
      <FormSection title="Template" description="A reusable step. Sheets copy it and keep its code and version; a published version never changes.">
        <FormField id="pis-code" label="Code" hint={creating ? "Upper-case letters, digits, '_', '.' or '-'. It does not change later." : "The code does not change."} error={errors.code}>
          {(control) => <Input {...control} maxLength={40} value={values.code} readOnly={!creating} invalid={Boolean(errors.code)} onChange={(e) => setValues({ ...values, code: e.target.value.toUpperCase() })} />}
        </FormField>
        <FormField id="pis-name" label="Name" error={errors.name}>
          {(control) => <Input {...control} maxLength={120} value={values.name} readOnly={!canEdit} invalid={Boolean(errors.name)} onChange={(e) => setValues({ ...values, name: e.target.value })} />}
        </FormField>
        <LookupFormField id="pis-category" label="PI category" entity="piCategory" optional value={values.categoryId} disabled={!canEdit} onChange={(categoryId) => setValues({ ...values, categoryId })} />
        <FormField id="pis-kind" label="Type of step" hint={creating ? "Chosen once; it decides the fields below." : undefined}>
          {(control) => (
            <Select
              {...control}
              value={values.spec.category}
              disabled={!creating}
              onChange={(e) => setValues({ ...values, spec: newSpec(e.target.value as InstructionCategory) })}
            >
              {CATEGORIES.map((k) => (
                <option key={k} value={k}>
                  {CATEGORY_LABEL[k]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      </FormSection>
      <FormSection title="Step">
        <StepFields id="spec" step={values.spec} readOnly={!canEdit} onChange={(spec) => setValues({ ...values, spec })} />
      </FormSection>
      {specProblems.length > 0 ? (
        <Alert tone="danger" title="The step is not complete" live="alert">
          <ul className={styles.plainList}>
            {specProblems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not save the template" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <div className={styles.formActions}>
        {canEdit ? (
          <Button type="submit" variant="primary" loading={busy}>
            {submitLabel}
          </Button>
        ) : null}
        {extra}
        <Link className={buttonClassName("ghost")} to="/shopfloor/pi-step-templates">
          Back to the library
        </Link>
      </div>
      {prompt}
    </form>
  );
}

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", options: [{ value: "draft", label: "Draft" }, { value: "published", label: "Published" }, { value: "superseded", label: "Superseded" }] },
  { kind: "select", key: "kind", label: "Type of step", options: Object.entries(CATEGORY_LABEL).map(([value, label]) => ({ value, label })) },
  { kind: "entity", key: "categoryId", label: "PI category", entity: "piCategory" },
];

const columns: ListColumn<PIStepTemplate>[] = [
  { key: "code", header: "Code", mono: true, sortValue: (t) => t.code },
  { key: "version", header: "Version", align: "end", sortValue: (t) => t.version },
  { key: "name", header: "Name", sortValue: (t) => t.name },
  { key: "kind", header: "Type of step", render: (t) => CATEGORY_LABEL[t.kind], sortValue: (t) => t.kind },
  { key: "categoryId", header: "PI category", render: (t) => (t.categoryId ? <EntityName entity="piCategory" id={t.categoryId} /> : "None") },
  { key: "status", header: "Status", render: (t) => <Badge tone={STATUS_TONE[t.status]}>{STEP_STATUS_LABEL[t.status]}</Badge>, sortValue: (t) => t.status },
  { key: "updatedAt", header: "Changed", render: (t) => formatDateTime(t.updatedAt), sortValue: (t) => t.updatedAt },
];

function useStepRows(query: ListQuery) {
  const { status = "", kind = "", categoryId = "" } = query.filters;
  const state = useLoad(
    (signal) => listPISteps({ status: (status || undefined) as PIStepStatus | undefined, kind: (kind || undefined) as InstructionCategory | undefined, categoryId: categoryId || undefined }, signal),
    `pi-steps|${status}|${kind}|${categoryId}`,
  );
  return loadedRows(state);
}

/** The PI step library: reusable, versioned process instructions that sheets copy and refer to by code and version. */
export function PiStepLibraryPage() {
  const navigate = useNavigate();
  const duties = usePrincipalDuties();
  return (
    <ListPage
      id="shopfloor-pi-step-templates"
      title="PI step library"
      description="Reusable steps for PI sheets: timers, calculations, machine readouts, branches and the rest. A published version never changes; a newer version supersedes it for new sheets."
      actions={
        holdsDuty(duties, DUTY_PI_STEP_MAINTAIN) ? (
          <Link className={buttonClassName("primary")} to="/shopfloor/pi-step-templates/new">
            New step template
          </Link>
        ) : undefined
      }
      noun="step templates"
      singular="step template"
      columns={columns}
      filters={filters}
      useRows={useStepRows}
      getRowKey={(t) => t.id}
      onRowOpen={(t) => navigate(`/shopfloor/pi-step-templates/${t.id}`)}
      search={(t) => ({ number: t.code, name: t.name })}
      empty={{ title: "No step templates yet", description: "Create one, such as a cure timer or an oven temperature readout, to reuse it in sheets." }}
    />
  );
}

/** Draft version 1 of a new template. */
export function NewPiStepPage() {
  const navigate = useNavigate();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const openId = useRef("");

  async function create(input: { code: string; name: string; categoryId: string | null; spec: PIStepSpec }): Promise<boolean> {
    setBusy(true);
    setFailure(null);
    try {
      const made = await createPIStep(input);
      openId.current = made.id;
      return true;
    } catch (error) {
      setFailure(describeProblem(error));
      return false;
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New step template" />
      <Card>
        <StepForm
          initial={{ code: "", name: "", categoryId: "", spec: newSpec("instruction-text") }}
          creating
          canEdit
          busy={busy}
          failure={failure}
          submitLabel="Create draft"
          onSubmit={create}
          onSaved={() => navigate(openId.current ? `/shopfloor/pi-step-templates/${openId.current}` : "/shopfloor/pi-step-templates")}
        />
      </Card>
    </div>
  );
}

/** One version of a template: a draft is edited and published; a published or superseded version is read-only. */
export function PiStepDetailPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getPIStep(id, signal), `pi-step|${id}`);
  const duties = usePrincipalDuties();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState("");
  const [busy, setBusy] = useState(false);
  const mayMaintain = holdsDuty(duties, DUTY_PI_STEP_MAINTAIN);

  async function act(run: () => Promise<PIStepTemplate>, done: string, after?: (t: PIStepTemplate) => void): Promise<boolean> {
    setBusy(true);
    setFailure(null);
    setSaved("");
    try {
      const out = await run();
      setSaved(done);
      if (after) after(out);
      else state.reload();
      return true;
    } catch (error) {
      setFailure(describeProblem(error));
      state.reload();
      return false;
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Step template" />
        <Alert tone="danger" title="Could not load the template" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const t = state.data;
  if (!t) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Step template" />
        <Skeleton aria-label="Loading the template" />
      </div>
    );
  }
  const draft = t.status === "draft";
  return (
    <div className={styles.stack}>
      <PageHeader
        title={`${t.code} version ${t.version}`}
        description={draft ? "A draft: edit it, then publish it so sheets can use it." : t.status === "published" ? "Published and unchangeable. Make a new version to change it." : "Superseded by a newer version. Sheets that name this version keep it."}
        actions={<Badge tone={STATUS_TONE[t.status]}>{STEP_STATUS_LABEL[t.status]}</Badge>}
      />
      {saved ? (
        <Alert tone="success" title={saved} live="status">
          {`Version ${t.version} is at rev ${t.rev}.`}
        </Alert>
      ) : null}
      <Card>
        <StepForm
          key={t.rev}
          initial={{ code: t.code, name: t.name, categoryId: t.categoryId ?? "", spec: t.spec }}
          creating={false}
          canEdit={draft && mayMaintain}
          busy={busy}
          failure={failure}
          submitLabel="Save draft"
          onSubmit={(input) => act(() => updatePIStep(t, { name: input.name, categoryId: input.categoryId, spec: input.spec }), "Draft saved")}
          extra={
            mayMaintain ? (
              <>
                {draft ? (
                  <Button variant="secondary" loading={busy} onClick={() => void act(() => publishPIStep(t), "Version published")}>
                    Publish this version
                  </Button>
                ) : null}
                {t.status === "published" ? (
                  <Button
                    variant="secondary"
                    loading={busy}
                    onClick={() => void act(() => newPIStepVersion(t), "New draft made", (made) => navigate(`/shopfloor/pi-step-templates/${made.id}`))}
                  >
                    Make a new version
                  </Button>
                ) : null}
              </>
            ) : undefined
          }
        />
      </Card>
    </div>
  );
}
