import { useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { usePrincipalDuties } from "../../shell/principal";
import { ProblemMessage } from "../../shell/errors";
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
  Textarea,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
  type ListColumn,
} from "../../shell/ui";
import { createPICategory, describeProblem, DUTY_PI_CATEGORY_MAINTAIN, getPICategory, listPICategories, newKey, updatePICategory, type NewPICategory, type PICategory, type Problem } from "./api/client";
import { formatDateTime } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

export const PI_CODE = /^[A-Z0-9][A-Z0-9_.-]{0,39}$/;

export interface PiCategoryValues {
  code: string;
  name: string;
  description: string;
  active: boolean;
}

type Errors = Record<string, string>;

const FIELDS: Record<string, FormFieldTarget> = {
  code: { id: "pic-code", label: "Code" },
  name: { id: "pic-name", label: "Name" },
  description: { id: "pic-description", label: "Description" },
};

/** Checks that mirror the service, and the request the form sends. */
export function validatePiCategory(values: PiCategoryValues, creating: boolean): { errors: Errors; input?: NewPICategory } {
  const errors: Errors = {};
  const code = values.code.trim();
  const name = values.name.trim();
  const description = values.description.trim();
  if (creating && !PI_CODE.test(code)) errors.code = "Use 1 to 40 characters: upper-case letters, digits, '_', '.' or '-'.";
  if (name.length < 1 || name.length > 120) errors.name = "Enter a name of up to 120 characters.";
  if (description.length > 500) errors.description = "Use at most 500 characters.";
  if (Object.keys(errors).length > 0) return { errors };
  return { errors, input: { code, name, description, active: values.active } };
}

function CategoryForm({
  initial,
  creating,
  submitLabel,
  failureTitle,
  failure,
  busy,
  canEdit,
  onSubmit,
  onSaved,
}: {
  initial: PiCategoryValues;
  creating: boolean;
  submitLabel: string;
  failureTitle: string;
  failure: Problem | null;
  busy: boolean;
  canEdit: boolean;
  onSubmit(input: NewPICategory): Promise<boolean>;
  onSaved?(): void;
}) {
  const [values, setValues] = useState(initial);
  const [errors, setErrors] = useState<Errors>({});
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePiCategory(values, creating);
    setErrors(found);
    if (!input) return;
    if (await onSubmit(input)) {
      release();
      onSaved?.();
    }
  }

  const patch = (next: Partial<PiCategoryValues>) => setValues((v) => ({ ...v, ...next }));
  return (
    <form className={styles.form} onSubmit={(e) => void submit(e)} noValidate aria-label="PI category">
      <FormErrorSummary items={summaryItems(errors, FIELDS)} attempt={attempt} />
      <FormSection title="Category" description="A category groups the step templates of the step library, such as weighing or cleaning steps.">
        <FormField id="pic-code" label="Code" hint={creating ? "Upper-case letters, digits, '_', '.' or '-'. It does not change later." : "The code does not change."} error={errors.code}>
          {(control) => <Input {...control} maxLength={40} value={values.code} readOnly={!creating} invalid={Boolean(errors.code)} onChange={(e) => patch({ code: e.target.value.toUpperCase() })} />}
        </FormField>
        <FormField id="pic-name" label="Name" error={errors.name}>
          {(control) => <Input {...control} maxLength={120} value={values.name} readOnly={!canEdit} invalid={Boolean(errors.name)} onChange={(e) => patch({ name: e.target.value })} />}
        </FormField>
        <FormField id="pic-description" label="Description" optional error={errors.description}>
          {(control) => <Textarea {...control} rows={3} maxLength={500} value={values.description} readOnly={!canEdit} invalid={Boolean(errors.description)} onChange={(e) => patch({ description: e.target.value })} />}
        </FormField>
        <FormField id="pic-active" label="Status" hint="An inactive category takes no new step templates; the ones it has stay.">
          {(control) => (
            <Select {...control} value={values.active ? "active" : "inactive"} disabled={!canEdit} onChange={(e) => patch({ active: e.target.value === "active" })}>
              <option value="active">Active</option>
              <option value="inactive">Inactive</option>
            </Select>
          )}
        </FormField>
      </FormSection>
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title={failureTitle} live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        {canEdit ? (
          <Button type="submit" variant="primary" loading={busy}>
            {submitLabel}
          </Button>
        ) : null}
        <Link className={buttonClassName("ghost")} to="/shopfloor/pi-categories">
          {canEdit ? "Cancel" : "Back to the list"}
        </Link>
      </div>
      {prompt}
    </form>
  );
}

const columns: ListColumn<PICategory>[] = [
  { key: "code", header: "Code", mono: true, sortValue: (row) => row.code },
  { key: "name", header: "Name", sortValue: (row) => row.name },
  { key: "description", header: "Description", render: (row) => row.description || "None" },
  { key: "active", header: "Status", render: (row) => (row.active ? <Badge tone="success">Active</Badge> : <Badge tone="neutral">Inactive</Badge>), sortValue: (row) => (row.active ? 0 : 1) },
  { key: "updatedAt", header: "Changed", render: (row) => formatDateTime(row.updatedAt), sortValue: (row) => row.updatedAt },
];

function useCategoryRows() {
  const state = useLoad((signal) => listPICategories({}, signal), "pi-categories");
  return loadedRows(state);
}

/** The PI category master: the groups the step library's templates are filed under (FR-25.15, D7). */
export function PiCategoriesPage() {
  const navigate = useNavigate();
  const duties = usePrincipalDuties();
  return (
    <ListPage
      id="shopfloor-pi-categories"
      title="PI categories"
      description="Categories group the reusable step templates of the PI step library, so a designer finds the weighing steps or the cleaning steps together."
      actions={
        holdsDuty(duties, DUTY_PI_CATEGORY_MAINTAIN) ? (
          <Link className={buttonClassName("primary")} to="/shopfloor/pi-categories/new">
            New PI category
          </Link>
        ) : undefined
      }
      noun="PI categories"
      singular="PI category"
      columns={columns}
      useRows={useCategoryRows}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => navigate(`/shopfloor/pi-categories/${row.id}`)}
      search={(row) => ({ number: row.code, name: row.name })}
      empty={{ title: "No PI categories yet", description: "Create one, such as Weighing, to file step templates under it." }}
    />
  );
}

/** Create a PI category. */
export function NewPiCategoryPage() {
  const navigate = useNavigate();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [key, setKey] = useState(() => newKey());

  async function create(input: NewPICategory): Promise<boolean> {
    setBusy(true);
    setFailure(null);
    try {
      await createPICategory(input, key);
      return true;
    } catch (error) {
      const problem = describeProblem(error);
      setFailure(problem.code === "SF-0142" ? { ...problem, message: "A category with this code exists. Use another code." } : problem);
      if (problem.code === "idempotency_key_reuse") setKey(newKey());
      return false;
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New PI category" />
      <Card>
        <CategoryForm
          initial={{ code: "", name: "", description: "", active: true }}
          creating
          submitLabel="Create PI category"
          failureTitle="Could not create the category"
          failure={failure}
          busy={busy}
          canEdit
          onSubmit={create}
          onSaved={() => navigate("/shopfloor/pi-categories")}
        />
      </Card>
    </div>
  );
}

/** One PI category with the form that changes it; the code stays. The version read travels as If-Match. */
export function PiCategoryDetailPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getPICategory(id, signal), `pi-category|${id}`);
  const duties = usePrincipalDuties();
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState(false);
  const [busy, setBusy] = useState(false);

  async function save(input: NewPICategory): Promise<boolean> {
    if (!state.data) return false;
    setBusy(true);
    setFailure(null);
    setSaved(false);
    try {
      await updatePICategory(state.data, { name: input.name, description: input.description, active: input.active });
      setSaved(true);
      state.reload();
      return true;
    } catch (error) {
      setFailure(describeProblem(error));
      // A stale version (SF-0143) is fixed by reading the category again.
      state.reload();
      return false;
    } finally {
      setBusy(false);
    }
  }

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="PI category" />
        <Alert tone="danger" title="Could not load the category" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack}>
        <PageHeader title="PI category" />
        <Skeleton aria-label="Loading the category" />
      </div>
    );
  }
  const category = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader title={`PI category ${category.code}`} description={`Changed ${formatDateTime(category.updatedAt)}.`} actions={category.active ? <Badge tone="success">Active</Badge> : <Badge tone="neutral">Inactive</Badge>} />
      {saved ? (
        <Alert tone="success" title="Category saved" live="status">
          The category is now at version {category.version}.
        </Alert>
      ) : null}
      <Card>
        <CategoryForm
          // A new version remounts the form with what the service stored.
          key={category.version}
          initial={{ code: category.code, name: category.name, description: category.description, active: category.active }}
          creating={false}
          submitLabel="Save category"
          failureTitle="Could not save the category"
          failure={failure}
          busy={busy}
          canEdit={holdsDuty(duties, DUTY_PI_CATEGORY_MAINTAIN)}
          onSubmit={save}
        />
      </Card>
    </div>
  );
}
