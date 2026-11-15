import { useEffect, useMemo, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ProblemMessage } from "../../shell/errors";
import {
  Alert,
  Button,
  buttonClassName,
  Card,
  FormErrorSummary,
  FormField,
  FormSection,
  Input,
  PageHeader,
  Select,
  Skeleton,
  Textarea,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
} from "../../shell/ui";
import {
  describeProblem,
  DOCUMENT_TYPE_LABELS,
  getTemplate,
  listTextBlocks,
  putTemplate,
  type DocumentTemplate,
  type Problem,
  type TemplateInput,
} from "./api";
import styles from "./DocumentDesignPage.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

const CODE = /^[A-Z][A-Z0-9_-]{1,39}$/;
const CORRESPONDENCE = ["PERIODIC_STATEMENT", "PAYMENT_NOTICE", "BALANCE_CONFIRMATION"];
const MAX_ITEMS = 8;

export interface TemplateValues {
  code: string;
  name: string;
  documentType: string;
  isDefault: boolean;
  title: string;
  footerText: string;
  hideColumns: string;
  renameColumns: string;
  bodyBlock: string;
  noteBlocks: string[];
}

export const blankTemplate: TemplateValues = {
  code: "",
  name: "",
  documentType: "CUSTOMER_INVOICE",
  isDefault: false,
  title: "",
  footerText: "",
  hideColumns: "",
  renameColumns: "",
  bodyBlock: "",
  noteBlocks: [],
};

export function templateValues(t: DocumentTemplate): TemplateValues {
  return {
    code: t.code,
    name: t.name,
    documentType: t.documentType,
    isDefault: t.default,
    title: t.config.title ?? "",
    footerText: t.config.footerText ?? "",
    hideColumns: (t.config.hideColumns ?? []).join("\n"),
    renameColumns: Object.entries(t.config.renameColumns ?? {})
      .map(([from, to]) => `${from} = ${to}`)
      .join("\n"),
    bodyBlock: t.config.bodyBlock ?? "",
    noteBlocks: t.config.noteBlocks ?? [],
  };
}

const lines = (text: string) =>
  text
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);

/** Checks that mirror the service (domain.DocumentTemplate.Validate); keys are the form's field names. */
export function validateTemplate(v: TemplateValues, existing: boolean): { errors: Errors; input?: TemplateInput } {
  const errors: Errors = {};
  if (!existing && !CODE.test(v.code)) errors.code = "Use 2 to 40 capital letters, digits, - or _, starting with a letter, for example INV-STANDARD.";
  if (v.name.trim() === "" || [...v.name].length > 200) errors.name = "Enter a name of up to 200 characters.";
  if (![...Object.keys(DOCUMENT_TYPE_LABELS)].includes(v.documentType)) errors.documentType = "Choose the document this template is for.";
  if ([...v.title].length > 60) errors.title = "The title can be up to 60 characters.";
  if ([...v.footerText].length > 200) errors.footerText = "The footer line can be up to 200 characters.";
  const hide = lines(v.hideColumns);
  if (hide.length > MAX_ITEMS) errors.hideColumns = `Hide up to ${MAX_ITEMS} columns.`;
  const rename: Record<string, string> = {};
  const rows = lines(v.renameColumns);
  if (rows.length > MAX_ITEMS) errors.renameColumns = `Rename up to ${MAX_ITEMS} columns.`;
  for (const row of rows) {
    const at = row.indexOf("=");
    const from = at < 0 ? "" : row.slice(0, at).trim();
    const to = at < 0 ? "" : row.slice(at + 1).trim();
    if (from === "" || to === "" || [...to].length > 200) {
      errors.renameColumns = "Write each renamed column as: Current header = New name.";
      break;
    }
    rename[from] = to;
  }
  if (v.bodyBlock !== "" && !CORRESPONDENCE.includes(v.documentType)) errors.bodyBlock = "Only statements, payment notices and balance confirmations have a letter text.";
  if (v.noteBlocks.length > MAX_ITEMS) errors.noteBlocks = `Add up to ${MAX_ITEMS} form notes.`;
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      name: v.name.trim(),
      documentType: v.documentType,
      default: v.isDefault,
      config: {
        ...(v.title.trim() ? { title: v.title.trim() } : {}),
        ...(v.footerText.trim() ? { footerText: v.footerText.trim() } : {}),
        ...(Object.keys(rename).length ? { renameColumns: rename } : {}),
        ...(hide.length ? { hideColumns: hide } : {}),
        ...(v.bodyBlock ? { bodyBlock: v.bodyBlock } : {}),
        ...(v.noteBlocks.length ? { noteBlocks: v.noteBlocks } : {}),
      },
    },
  };
}

/** The service names a config field `config.title`; the form's field is `title`. */
function formErrors(fields: Record<string, string>): Errors {
  const out: Errors = {};
  for (const [k, v] of Object.entries(fields)) out[k.replace(/^config\./, "")] = v;
  return out;
}

const FIELDS: Record<string, FormFieldTarget> = {
  code: { id: "template-code", label: "Code" },
  name: { id: "template-name", label: "Name" },
  documentType: { id: "template-documentType", label: "Document" },
  title: { id: "template-title", label: "Title" },
  footerText: { id: "template-footerText", label: "Footer line" },
  hideColumns: { id: "template-hideColumns", label: "Hidden columns" },
  renameColumns: { id: "template-renameColumns", label: "Renamed columns" },
  bodyBlock: { id: "template-bodyBlock", label: "Letter text" },
  noteBlocks: { id: "template-noteBlocks", label: "Form notes" },
  config: { id: "template-title", label: "Layout" },
};

function TemplateForm({ initial, etag, existing, blockCodes }: { initial: TemplateValues; etag?: string; existing: boolean; blockCodes: string[] }) {
  const navigate = useNavigate();
  const [values, setValues] = useState(initial);
  const [current, setCurrent] = useState({ etag, version: etag ? Number(etag.replaceAll('"', "")) : 0 });
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [saved, setSaved] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setValues(initial);
    setCurrent({ etag, version: etag ? Number(etag.replaceAll('"', "")) : 0 });
  }, [initial, etag]);
  const attempt = useSubmitAttempt(errors);
  // An edit saves in place and stays on the page, so only the create form guards against leaving.
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]) && !existing);
  const update = (patch: Partial<TemplateValues>) => setValues((v) => ({ ...v, ...patch }));
  const letter = CORRESPONDENCE.includes(values.documentType);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateTemplate(values, existing);
    setErrors(found);
    setFailure(null);
    setSaved("");
    if (!input) return;
    setBusy(true);
    try {
      const res = await putTemplate(values.code, input, existing ? current.etag : undefined);
      if (!existing) {
        release();
        navigate(`/document-design/templates/${encodeURIComponent(res.value.code)}`);
        return;
      }
      setCurrent({ etag: res.etag, version: res.value.version });
      setSaved(`Saved as version ${res.value.version}.`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(formErrors(problem.fields));
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const stale = failure?.code === "PRN-0017" || failure?.code === "PRN-0018";
  return (
    <form className={styles.stack} onSubmit={submit} noValidate aria-label="Document template">
      <FormErrorSummary items={summaryItems(errors, FIELDS)} attempt={attempt} />
      <Card title="Template">
        <div className={styles.form}>
          <FormField id="template-code" label="Code" hint="A short code the documents and schedules name this template by. It cannot change later." error={errors.code}>
            {(control) => (
              <Input {...control} className={styles.mono} value={values.code} readOnly={existing} onChange={(e) => update({ code: e.target.value.toUpperCase() })} invalid={Boolean(errors.code)} />
            )}
          </FormField>
          <FormField id="template-name" label="Name" error={errors.name}>
            {(control) => <Input {...control} value={values.name} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
          </FormField>
          <FormField id="template-documentType" label="Document" hint={existing ? "The document of a saved template cannot change." : undefined} error={errors.documentType}>
            {(control) => (
              <Select {...control} value={values.documentType} disabled={existing} onChange={(e) => update({ documentType: e.target.value, ...(CORRESPONDENCE.includes(e.target.value) ? {} : { bodyBlock: "" }) })}>
                {Object.entries(DOCUMENT_TYPE_LABELS).map(([type, label]) => (
                  <option key={type} value={type}>
                    {label}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          <div className={styles.check}>
            <Input id="template-default" type="checkbox" checked={values.isDefault} onChange={(e) => update({ isDefault: e.target.checked })} />
            <label htmlFor="template-default">Use for every document of this kind that names no template</label>
          </div>
        </div>
      </Card>
      <Card title="What the template changes">
        <div className={styles.form}>
          <FormField id="template-title" label="Title" hint="Replaces the standard title, up to 60 characters." optional error={errors.title}>
            {(control) => <Input {...control} value={values.title} onChange={(e) => update({ title: e.target.value })} invalid={Boolean(errors.title)} />}
          </FormField>
          <FormField id="template-footerText" label="Footer line" hint="Printed at the foot of every page, up to 200 characters." optional error={errors.footerText}>
            {(control) => <Input {...control} value={values.footerText} onChange={(e) => update({ footerText: e.target.value })} invalid={Boolean(errors.footerText)} />}
          </FormField>
          {letter ? (
            <FormField id="template-bodyBlock" label="Letter text" hint="The standard text that is the body of the letter." optional error={errors.bodyBlock}>
              {(control) => (
                <Select {...control} value={values.bodyBlock} onChange={(e) => update({ bodyBlock: e.target.value })}>
                  <option value="">The standard sentence</option>
                  {blockCodes.map((c) => (
                    <option key={c} value={c}>
                      {c}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
        </div>
        <FormSection title="Columns and form notes" collapse="more" defaultOpen={Boolean(values.hideColumns || values.renameColumns || values.noteBlocks.length)}>
          <div className={styles.form}>
            <FormField id="template-hideColumns" label="Hidden columns" hint="One column header per line, as the document prints it." optional error={errors.hideColumns}>
              {(control) => <Textarea {...control} rows={4} value={values.hideColumns} onChange={(e) => update({ hideColumns: e.target.value })} invalid={Boolean(errors.hideColumns)} />}
            </FormField>
            <FormField id="template-renameColumns" label="Renamed columns" hint="One per line: Current header = New name." optional error={errors.renameColumns}>
              {(control) => <Textarea {...control} rows={4} value={values.renameColumns} onChange={(e) => update({ renameColumns: e.target.value })} invalid={Boolean(errors.renameColumns)} />}
            </FormField>
            <FormField id="template-noteBlocks" label="Form notes" hint="Standard texts added to the notes of the document, in this order." optional error={errors.noteBlocks}>
              {(control) =>
                blockCodes.length === 0 ? (
                  <p className={styles.note}>There are no standard texts yet. Add one under text blocks first.</p>
                ) : (
                  <div className={styles.checks} role="group" aria-labelledby={`${control.id}-label`}>
                    <span id={`${control.id}-label`} className="visually-hidden">
                      Form notes
                    </span>
                    {blockCodes.map((c) => (
                      <span key={c} className={styles.check}>
                        <Input
                          id={`${control.id}-${c}`}
                          type="checkbox"
                          checked={values.noteBlocks.includes(c)}
                          onChange={(e) => update({ noteBlocks: e.target.checked ? [...values.noteBlocks, c] : values.noteBlocks.filter((x) => x !== c) })}
                        />
                        <label htmlFor={`${control.id}-${c}`}>{c}</label>
                      </span>
                    ))}
                  </div>
                )
              }
            </FormField>
          </div>
        </FormSection>
      </Card>
      <Alert tone="success" live="status">
        {saved}
      </Alert>
      {failure ? (
        <Alert tone={stale ? "warning" : "danger"} title={stale ? "This template changed while you were editing" : "Could not save the template"} live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <div className={styles.actions}>
        <Button type="submit" variant="primary" loading={busy}>
          {existing ? "Save template" : "Create template"}
        </Button>
        {existing ? (
          <Link className={buttonClassName("ghost")} to={`/document-design/templates/${encodeURIComponent(values.code)}/versions`}>
            Version history
          </Link>
        ) : null}
        <Link className={buttonClassName("ghost")} to="/document-design">
          Back to document design
        </Link>
        {existing && current.version ? <span className={styles.muted}>Editing version {current.version}</span> : null}
      </div>
      {prompt}
    </form>
  );
}

function useBlockCodes() {
  const blocks = useLoad(listTextBlocks);
  return useMemo(() => [...new Set((blocks.data ?? []).map((b) => b.code))].sort(), [blocks.data]);
}

/** A new document template (46-1). */
export function NewTemplatePage() {
  const blockCodes = useBlockCodes();
  return (
    <div className={styles.stack}>
      <PageHeader title="New document template" description="Change how one kind of document looks: its title, footer line, column names, letter text and form notes." />
      <TemplateForm initial={blankTemplate} existing={false} blockCodes={blockCodes} />
    </div>
  );
}

/** One template, saved at the version that was opened (If-Match). */
export function TemplatePage() {
  const { code = "" } = useParams();
  const state = useLoad((signal) => (signal.aborted ? Promise.reject(new Error("aborted")) : getTemplate(code)), code);
  const blockCodes = useBlockCodes();
  const initial = useMemo(() => (state.data ? templateValues(state.data.value) : null), [state.data]);
  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Document template" />
        <Alert tone="danger" title="Could not load the template" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (!state.data || !initial) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Document template" />
        <span className="visually-hidden">Loading the template…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader title={state.data.value.name} description="Every save raises the version, and every version is kept in the history." />
      <TemplateForm initial={initial} etag={state.data.etag || `"${state.data.value.version}"`} existing blockCodes={blockCodes} />
    </div>
  );
}
