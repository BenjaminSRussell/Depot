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
import {
  createAuditPolicy,
  describeProblem,
  getAuditPolicyVersioned,
  listAuditPolicies,
  newKey,
  updateAuditPolicy,
  type AuditPolicy,
  type Problem,
} from "./api/client";
import { emptySpec, ruleLabel, SpecFields, specToValues, toAuditSpec, type SpecErrors, type SpecValues } from "./auditSpec";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const CODE = /^[A-Z0-9][A-Z0-9_-]{1,31}$/;

const columns: ListColumn<AuditPolicy>[] = [
  { key: "code", header: "Code", mono: true, sortValue: (row) => row.code },
  { key: "name", header: "Name", sortValue: (row) => row.name },
  { key: "ruleType", header: "Rule", sortValue: (row) => ruleLabel[row.ruleType], render: (row) => ruleLabel[row.ruleType] },
  { key: "version", header: "Version", align: "end", sortValue: (row) => row.version, render: (row) => `v${row.version}` },
  { key: "active", header: "Status", sortValue: (row) => (row.active ? 0 : 1), render: (row) => <Badge tone={row.active ? "success" : "neutral"}>{row.active ? "Active" : "Retired"}</Badge> },
  {
    key: "edit",
    header: "",
    render: (row) => (
      <Link to={`/compliance/audit/policies/${row.policyId}`} aria-label={`Edit ${row.name}`}>
        Edit
      </Link>
    ),
  },
];

/** Stored audit rules (PRD §9.48.6, FR-48.14): the policies an audit run names. */
export function AuditPoliciesPage() {
  const state = useLoad((signal) => listAuditPolicies(signal), "audit-policies");
  return (
    <div className={styles.stack}>
      <ListPage
        id="compliance-audit-policies"
        title="Audit policies"
        description="A policy is a named audit rule, such as flagging gift cards in expense text or vendor invoices that repeat. Changing a rule makes a new version, because a violation is tied to the version that found it."
        noun="audit policies"
        singular="audit policy"
        columns={columns}
        useRows={() => loadedRows(state)}
        getRowKey={(row) => row.policyId}
        search={(row) => ({ name: row.name, number: row.code })}
        actions={
          <>
            <Link className={buttonClassName("secondary")} to="/compliance/audit/run">
              Run a policy
            </Link>
            <Link className={buttonClassName("primary")} to="/compliance/audit/policies/new">
              New audit policy
            </Link>
          </>
        }
        empty={{ title: "No audit policies yet", description: "Add a policy, then run it over a window of documents." }}
      />
    </div>
  );
}

const FIELDS: Record<string, FormFieldTarget> = {
  code: { id: "policy-code", label: "Code" },
  name: { id: "policy-name", label: "Name" },
  words: { id: "policy-words", label: "Keywords" },
  currency: { id: "policy-currency", label: "Limit currency" },
  limit: { id: "policy-limit", label: "Monthly limit" },
  limits: { id: "policy-limits", label: "Limits by category" },
  holidays: { id: "policy-holidays", label: "Holidays" },
  seed: { id: "policy-seed", label: "Seed" },
  sampleSize: { id: "policy-sample-size", label: "Sample size" },
};

/** Checks the form and builds the request the service validates again. */
export function validatePolicy(code: string, name: string, spec: SpecValues, requireCode = true): { errors: Record<string, string>; input?: { code: string; name: string; spec: NonNullable<ReturnType<typeof toAuditSpec>["spec"]> } } {
  const errors: Record<string, string> = {};
  if (requireCode && !CODE.test(code.trim().toUpperCase())) errors.code = "Use 2 to 32 capital letters, digits, _ or -, such as GIFT-CARDS.";
  if (name.trim() === "" || name.trim().length > 120) errors.name = "Name the policy in up to 120 characters.";
  const built = toAuditSpec(spec);
  Object.assign(errors, built.errors);
  if (Object.keys(errors).length > 0 || !built.spec) return { errors };
  return { errors, input: { code: code.trim().toUpperCase(), name: name.trim(), spec: built.spec } };
}

function PolicyForm({ policy, etag }: { policy?: AuditPolicy; etag?: string }) {
  const navigate = useNavigate();
  const [code, setCode] = useState(policy?.code ?? "");
  const [name, setName] = useState(policy?.name ?? "");
  const [spec, setSpec] = useState<SpecValues>(policy ? specToValues(policy.spec) : emptySpec);
  const [active, setActive] = useState(policy?.active ?? true);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  const key = useRef(newKey());
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([code, name, spec, active]) && !saved);

  function set<K extends keyof SpecValues>(field: K, value: SpecValues[K]) {
    setSpec((s) => ({ ...s, [field]: value }));
    key.current = newKey();
  }

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validatePolicy(code, name, spec, !policy);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      if (policy && etag) {
        const changedSpec = JSON.stringify(input.spec) !== JSON.stringify(policy.spec);
        await updateAuditPolicy(policy.policyId, etag, { name: input.name, active, ...(changedSpec ? { spec: input.spec } : {}) });
      } else {
        await createAuditPolicy(input, key.current);
      }
      setSaved(true);
      release();
      navigate("/compliance/audit/policies");
    } catch (error) {
      const problem = describeProblem(error);
      if (problem.code === "AUD-0721") setErrors({ code: "This code is already used by another audit policy." });
      else if (problem.code === "SOD-1109") setFailure({ ...problem, message: "Someone else changed this policy. Reload it and try again." });
      else if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      else setFailure(problem);
      if (problem.status >= 400 && problem.status < 500) key.current = newKey();
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.form} onSubmit={submit} noValidate aria-label={policy ? "Edit audit policy" : "New audit policy"}>
      <FormErrorSummary items={summaryItems(errors, FIELDS)} attempt={attempt} />
      <FormSection title="Policy">
        {policy ? null : (
          <FormField id="policy-code" label="Code" hint="Capital letters, digits, _ or -." error={errors.code}>
            {(control) => <Input {...control} maxLength={32} value={code} invalid={Boolean(errors.code)} onChange={(e) => { setCode(e.target.value.toUpperCase()); key.current = newKey(); }} />}
          </FormField>
        )}
        <FormField id="policy-name" label="Name" error={errors.name}>
          {(control) => <Input {...control} maxLength={120} value={name} invalid={Boolean(errors.name)} onChange={(e) => { setName(e.target.value); key.current = newKey(); }} />}
        </FormField>
        {policy ? (
          <FormField id="policy-active" label="Active" hint="A retired policy cannot be run, and its violations keep their keys.">
            {(control) => <Input {...control} type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} />}
          </FormField>
        ) : null}
      </FormSection>
      <FormSection title="Rule" description={policy ? "Changing the rule saves a new version of the policy." : undefined}>
        <SpecFields values={spec} errors={errors as SpecErrors} set={set} idPrefix="policy" />
      </FormSection>
      {failure ? (
        <div className={styles.formActions}>
          <Alert tone="danger" title="Could not save the policy" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        </div>
      ) : null}
      <div className={styles.formActions}>
        <Button type="submit" variant="primary" loading={busy}>
          {policy ? "Save changes" : "Save policy"}
        </Button>
        <Link className={buttonClassName("secondary")} to="/compliance/audit/policies">
          Cancel
        </Link>
      </div>
      {prompt}
    </form>
  );
}

/** Create an audit policy (compliance.audit-policy.maintain). */
export function NewAuditPolicyPage() {
  return (
    <div className={styles.stack}>
      <PageHeader title="New audit policy" description="Name the rule once; audit runs then pick it by name." />
      <Card>
        <PolicyForm />
      </Card>
    </div>
  );
}

/** Rename, retire or change the rule of an audit policy. */
export function AuditPolicyEditPage() {
  const { id = "" } = useParams();
  const state = useLoad((signal) => getAuditPolicyVersioned(id, signal), `audit-policy:${id}`);
  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Audit policy" />
        <Alert tone="danger" title="Could not load this policy" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Audit policy" />
        <Skeleton height="tile" />
        <span className="visually-hidden">Loading…</span>
      </div>
    );
  }
  const { value, etag } = state.data;
  return (
    <div className={styles.stack}>
      <PageHeader title={`Audit policy ${value.code}`} description={`Version ${value.version}. Changing the rule saves a new version; renaming or retiring does not.`} />
      <Card>
        <PolicyForm policy={value} etag={etag} />
      </Card>
    </div>
  );
}
