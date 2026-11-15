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
  Input,
  PageHeader,
  Skeleton,
  Textarea,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
} from "../../shell/ui";
import { describeProblem, listTextBlocks, putTextBlock, type Problem, type TextBlock } from "./api";
import styles from "./DocumentDesignPage.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

const CODE = /^[A-Z][A-Z0-9_-]{1,39}$/;
const LANGUAGE = /^[a-z]{2}(-[A-Z]{2})?$/;
const PLACEHOLDER = /\{\{\s*([A-Za-z][A-Za-z0-9_.]*)\s*\}\}/g;
const MAX_BODY = 4000;

export interface BlockValues {
  code: string;
  language: string;
  body: string;
}

export const placeholdersOf = (body: string) => [...new Set([...body.matchAll(PLACEHOLDER)].map((m) => m[1]!))].sort();

/** Checks that mirror the service (domain.TextBlock.Validate). */
export function validateBlock(v: BlockValues, existing: boolean): Errors {
  const errors: Errors = {};
  if (!existing && !CODE.test(v.code)) errors.code = "Use 2 to 40 capital letters, digits, - or _, starting with a letter, for example PAYMENT-TERMS.";
  if (!existing && !LANGUAGE.test(v.language)) errors.language = "Enter a language such as en, or en-GB with a region.";
  if (v.body.trim() === "") errors.body = "Enter the text.";
  else if ([...v.body].length > MAX_BODY) errors.body = `The text can be up to ${MAX_BODY} characters.`;
  return errors;
}

const FIELDS: Record<string, FormFieldTarget> = {
  code: { id: "block-code", label: "Code" },
  language: { id: "block-language", label: "Language" },
  body: { id: "block-body", label: "Text" },
};

function BlockForm({ initial, existing, version }: { initial: BlockValues; existing: boolean; version?: number }) {
  const navigate = useNavigate();
  const [values, setValues] = useState(initial);
  const [saved, setSaved] = useState("");
  const [shown, setShown] = useState(version ?? 0);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    setValues(initial);
    setShown(version ?? 0);
  }, [initial, version]);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]) && !existing);
  const placeholders = useMemo(() => placeholdersOf(values.body), [values.body]);

  async function submit(event: FormEvent) {
    event.preventDefault();
    const found = validateBlock(values, existing);
    setErrors(found);
    setFailure(null);
    setSaved("");
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const res = await putTextBlock(values.code, values.language, values.body);
      if (!existing) {
        release();
        navigate(`/document-design/text-blocks/${encodeURIComponent(res.value.code)}/${encodeURIComponent(res.value.language)}`);
        return;
      }
      setShown(res.value.version);
      setSaved(`Saved as version ${res.value.version}.`);
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form className={styles.stack} onSubmit={submit} noValidate aria-label="Text block">
      <FormErrorSummary items={summaryItems(errors, FIELDS)} attempt={attempt} />
      <Card title="Standard text">
        <div className={styles.form}>
          <FormField id="block-code" label="Code" hint="Templates and documents name the text by this code. One code can have a version per language." error={errors.code}>
            {(control) => <Input {...control} className={styles.mono} value={values.code} readOnly={existing} onChange={(e) => setValues({ ...values, code: e.target.value.toUpperCase() })} invalid={Boolean(errors.code)} />}
          </FormField>
          <FormField id="block-language" label="Language" hint="A document uses its own language, then the language without its region, then English." error={errors.language}>
            {(control) => <Input {...control} className={styles.mono} value={values.language} readOnly={existing} onChange={(e) => setValues({ ...values, language: e.target.value })} invalid={Boolean(errors.language)} />}
          </FormField>
          <div className={styles.wide}>
            <FormField id="block-body" label="Text" hint="Write {{name}} where a value is filled in, for example {{dueDate}}." error={errors.body}>
              {(control) => <Textarea {...control} rows={8} value={values.body} onChange={(e) => setValues({ ...values, body: e.target.value })} invalid={Boolean(errors.body)} />}
            </FormField>
          </div>
          <p className={styles.note}>{placeholders.length ? `Fills in: ${placeholders.join(", ")}.` : "This text fills in nothing."}</p>
        </div>
      </Card>
      <Alert tone="success" live="status">
        {saved}
      </Alert>
      {failure ? (
        <Alert tone="danger" title="Could not save the text block" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      <div className={styles.actions}>
        <Button type="submit" variant="primary" loading={busy}>
          {existing ? "Save text block" : "Create text block"}
        </Button>
        <Link className={buttonClassName("ghost")} to="/document-design">
          Back to document design
        </Link>
        {existing && shown ? <span className={styles.muted}>Version {shown}</span> : null}
      </div>
      {prompt}
    </form>
  );
}

const blank: BlockValues = { code: "", language: "en", body: "" };

/** A new standard text, or a new language version of an existing one (46-4). */
export function NewTextBlockPage() {
  return (
    <div className={styles.stack}>
      <PageHeader title="New text block" description="A standard text with placeholders, kept per language, for letters and form notes." />
      <BlockForm initial={blank} existing={false} />
    </div>
  );
}

/** One language version of a text block. */
export function TextBlockPage() {
  const { code = "", language = "" } = useParams();
  const state = useLoad(listTextBlocks, `${code}/${language}`);
  const block: TextBlock | undefined = state.data?.find((b) => b.code === code && b.language === language);
  const initial = useMemo(() => (block ? { code: block.code, language: block.language, body: block.body } : null), [block]);
  if (state.error) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Text block" />
        <Alert tone="danger" title="Could not load the text block" live="alert" action={<Button size="sm" onClick={state.reload}>Try again</Button>}>
          <ProblemMessage problem={state.error} />
        </Alert>
      </div>
    );
  }
  if (state.data && !block) {
    return (
      <div className={styles.stack}>
        <PageHeader title="Text block" />
        <Alert tone="warning" title="That text block wasn't found">
          It may have another language. <Link to="/document-design">Back to document design</Link>.
        </Alert>
      </div>
    );
  }
  if (!initial || !block) {
    return (
      <div className={styles.stack} aria-busy="true">
        <PageHeader title="Text block" />
        <span className="visually-hidden">Loading the text block…</span>
        <Skeleton height="tile" />
      </div>
    );
  }
  return (
    <div className={styles.stack}>
      <PageHeader title={`${block.code} in ${block.language}`} description="Saving replaces this language version and raises its version." />
      <BlockForm initial={initial} existing version={block.version} />
    </div>
  );
}
