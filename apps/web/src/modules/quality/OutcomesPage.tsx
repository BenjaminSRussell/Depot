import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
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
  Skeleton,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
  type ListColumn,
} from "../../shell/ui";
import { createOutcome, describeProblem, getOutcome, listOutcomes, newKey, updateOutcome, type Outcome, type Problem } from "./api/client";
import { CODE } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const columns: ListColumn<Outcome>[] = [
  { key: "code", header: "Code", mono: true, sortValue: (row) => row.code },
  { key: "name", header: "Name", sortValue: (row) => row.name },
  { key: "active", header: "Status", sortValue: (row) => (row.active ? 0 : 1), render: (row) => <Badge tone={row.active ? "success" : "neutral"}>{row.active ? "Active" : "Retired"}</Badge> },
  {
    key: "edit",
    header: "Details",
    render: (row) => (
      <Link to={`/quality/outcomes/${row.id}`} aria-label={`Edit ${row.name}`}>
        Edit
      </Link>
    ),
  },
];

/** Attribute outcomes (FR-33.4): the names test groups list as passing and results record. */
export function OutcomesPage() {
  const state = useLoad((signal) => listOutcomes({}, signal), "outcomes");
  return (
    <div className={styles.stack}>
      <ListPage
        id="quality-outcomes"
        title="Outcomes"
        description="The attribute outcomes an inspection can record, such as No visible defect. A test group lists the outcomes that pass; anything else fails the test."
        noun="outcomes"
        singular="outcome"
        columns={columns}
        useRows={() => loadedRows(state)}
        getRowKey={(row) => row.id}
        search={(row) => ({ name: row.name, number: row.code })}
        actions={
          <Link className={buttonClassName("primary")} to="/quality/outcomes/new">
            New outcome
          </Link>
        }
        empty={{ title: "No outcomes yet", description: "Add the outcomes your inspectors record, then list the passing ones on a test group." }}
      />
    </div>
  );
}

const NEW_OUTCOME_FIELDS: Record<string, FormFieldTarget> = {
  code: { id: "outcome-code", label: "Code" },
  name: { id: "outcome-name", label: "Name" },
};

/** Checks that mirror NewOutcome in the service. */
export function validateOutcome(code: string, name: string): Record<string, string> {
  const errors: Record<string, string> = {};
  if (!CODE.test(code.trim().toUpperCase())) errors.code = "Use 2 to 32 capital letters, digits, _ or -, such as NO-DEFECT.";
  if (name.trim() === "" || name.trim().length > 120) errors.name = "Name the outcome in up to 120 characters.";
  return errors;
}

/** Register an attribute outcome (quality.setup.maintain). A code already in use is refused with QM-0191. */
export function NewOutcomePage() {
  const navigate = useNavigate();
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const key = useRef(newKey());
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([code, name]) && !saved);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateOutcome(code, name);
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await createOutcome({ code: code.trim().toUpperCase(), name: name.trim() }, key.current);
      setSaved(true);
      release();
      navigate("/quality/outcomes");
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "QM-0191") setErrors({ code: "This code is already used by another outcome." });
      else if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.code !== "COM-0001" && !problem.code.startsWith("HTTP-5")) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title="New outcome" description="Test groups list the outcomes that pass; results record one of them. The code is how people and imports name it." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="New outcome">
          <FormErrorSummary items={summaryItems(errors, NEW_OUTCOME_FIELDS)} attempt={attempt} />
          <FormSection>
            <FormField id="outcome-code" label="Code" hint="Capital letters, digits, _ or -." error={errors.code}>
              {(control) => <Input {...control} maxLength={32} className={styles.mono} value={code} invalid={Boolean(errors.code)} onChange={(event) => { setCode(event.target.value.toUpperCase()); key.current = newKey(); }} />}
            </FormField>
            <FormField id="outcome-name" label="Name" error={errors.name}>
              {(control) => <Input {...control} maxLength={120} value={name} invalid={Boolean(errors.name)} onChange={(event) => { setName(event.target.value); key.current = newKey(); }} />}
            </FormField>
          </FormSection>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the outcome" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save outcome
            </Button>
            <Link className={buttonClassName("secondary")} to="/quality/outcomes">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {prompt}
    </div>
  );
}

/** Rename or retire an outcome. A retired outcome stays on the results and test groups that name it. */
export function OutcomeEditPage() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const state = useLoad((signal) => getOutcome(id, signal), `outcome:${id}`);
  const [name, setName] = useState<string | null>(null);
  const [active, setActive] = useState<boolean | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([name, active]) && !saved);

  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Outcome" />
        <Alert tone="danger" title="Could not load this outcome" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  const outcome = state.data;
  if (!outcome) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Outcome" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const shownName = name ?? outcome.name;
  const shownActive = active ?? outcome.active;

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found: Record<string, string> = {};
    if (shownName.trim() === "" || shownName.trim().length > 120) found.name = "Name the outcome in up to 120 characters.";
    setErrors(found);
    setFailure(null);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      await updateOutcome(id, outcome!.version, { name: shownName.trim(), active: shownActive });
      setSaved(true);
      release();
      navigate("/quality/outcomes");
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "conflict_version") setFailure({ ...problem, message: "Someone else changed this outcome. Reload it and try again." });
      else setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles.stack}>
      <PageHeader title={`Outcome ${outcome.code}`} description="Rename the outcome or retire it. Retired outcomes stay on the results and test groups that name them and are left out of pick lists." />
      <Card>
        <form className={styles.form} onSubmit={submit} noValidate aria-label="Edit outcome">
          <FormErrorSummary items={summaryItems(errors, { name: { id: "outcome-edit-name", label: "Name" } })} attempt={attempt} />
          <FormSection>
            <FormField id="outcome-edit-name" label="Name" error={errors.name}>
              {(control) => <Input {...control} maxLength={120} value={shownName} invalid={Boolean(errors.name)} onChange={(event) => setName(event.target.value)} />}
            </FormField>
            <FormField id="outcome-edit-active" label="Active">
              {(control) => <Input {...control} type="checkbox" checked={shownActive} onChange={(event) => setActive(event.target.checked)} />}
            </FormField>
          </FormSection>
          {failure ? (
            <div className={styles.formActions}>
              <Alert tone="danger" title="Could not save the outcome" live="alert">
                <ProblemMessage problem={failure} />
              </Alert>
            </div>
          ) : null}
          <div className={styles.formActions}>
            <Button type="submit" variant="primary" loading={busy}>
              Save changes
            </Button>
            <Link className={buttonClassName("secondary")} to="/quality/outcomes">
              Cancel
            </Link>
          </div>
        </form>
      </Card>
      {prompt}
    </div>
  );
}
