import { useRef, useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router-dom";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
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
  Select,
  summaryItems,
  useFormBaseline,
  useSubmitAttempt,
  useUnsavedChanges,
  type FormFieldTarget,
} from "../../shell/ui";
import { CHANNEL_LABELS, createOutputRule, describeProblem, DOCUMENT_TYPE_LABELS, newKey, type OutputRuleInput, type Problem } from "./api";
import styles from "./DocumentDesignPage.module.css";

type Errors = Record<string, string>;

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const QUEUE = /^[A-Z][A-Z0-9_-]{1,39}$/;

export interface RuleValues {
  documentType: string;
  legalEntityId: string;
  accountId: string;
  copyKind: "ORIGINAL" | "COPY";
  channel: string;
  destination: string;
  copies: string;
  sequence: string;
}

export const blankRule: RuleValues = {
  documentType: "CUSTOMER_INVOICE",
  legalEntityId: "",
  accountId: "",
  copyKind: "ORIGINAL",
  channel: "EMAIL",
  destination: "",
  copies: "1",
  sequence: "1",
};

/** Checks that mirror the service (domain.OutputRule.Validate). */
export function validateRule(v: RuleValues): { errors: Errors; input?: OutputRuleInput } {
  const errors: Errors = {};
  const copies = Number(v.copies);
  const sequence = Number(v.sequence);
  if (v.channel === "PRINTER" && !QUEUE.test(v.destination)) errors.destination = "Enter the print queue code, for example OFFICE.";
  if (v.channel === "EMAIL") {
    const d = v.destination.trim();
    if (d.indexOf("@") < 1 || d.endsWith("@") || /[\s,;]/.test(d) || d.split("@").length !== 2 || d.length > 254) errors.destination = "Enter one e-mail address.";
  }
  if (v.channel === "PRINTER" ? !Number.isInteger(copies) || copies < 1 || copies > 9 : copies !== 1) errors.copies = v.channel === "PRINTER" ? "Enter 1 to 9 copies." : "Only a printer prints more than one copy.";
  if (!Number.isInteger(sequence) || sequence < 1) errors.sequence = "Enter a sequence of 1 or more.";
  if (v.legalEntityId !== "" && !UUID.test(v.legalEntityId)) errors.legalEntityId = "Choose the legal entity.";
  if (v.accountId !== "" && !UUID.test(v.accountId)) errors.accountId = "Choose the customer.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: {
      documentType: v.documentType,
      ...(v.legalEntityId ? { legalEntityId: v.legalEntityId } : {}),
      ...(v.accountId ? { accountId: v.accountId } : {}),
      copyKind: v.copyKind,
      channel: v.channel,
      ...(v.channel === "PRINTER" || v.channel === "EMAIL" ? { destination: v.destination.trim() } : {}),
      copies,
      sequence,
    },
  };
}

const FIELDS: Record<string, FormFieldTarget> = {
  documentType: { id: "rule-documentType", label: "Document" },
  legalEntityId: { id: "rule-legalEntityId", label: "Legal entity" },
  accountId: { id: "rule-accountId", label: "Customer" },
  copyKind: { id: "rule-copyKind", label: "Copy" },
  channel: { id: "rule-channel", label: "Goes to" },
  destination: { id: "rule-destination", label: "Destination" },
  copies: { id: "rule-copies", label: "Copies" },
  sequence: { id: "rule-sequence", label: "Sequence" },
};

const CHANNEL_HELP: Record<string, string> = {
  PRINTER: "A print queue of the site print agent. The document goes to the queue's printers as a PDF.",
  EMAIL: "The PDF is sent to the address as an attachment.",
  EINVOICE: "The e-invoice network is reached through the e-invoice service; the print service only records it.",
  ARCHIVE: "The archive is not available yet. The rule is kept and shows as skipped until it is.",
  PORTAL: "The document is shown to the customer in the customer portal.",
};

/** A new output rule (46-2). */
export function NewOutputRulePage() {
  const navigate = useNavigate();
  const key = useRef(newKey());
  const [values, setValues] = useState(blankRule);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));
  const update = (patch: Partial<RuleValues>) => setValues((v) => ({ ...v, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateRule(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createOutputRule(input, key.current);
      release();
      navigate("/document-design");
    } catch (error) {
      const problem = describeProblem(error);
      if (Object.keys(problem.fields).length > 0) setErrors(problem.fields);
      setFailure(problem);
    } finally {
      setBusy(false);
    }
  }

  const printer = values.channel === "PRINTER";
  const needsDestination = printer || values.channel === "EMAIL";
  return (
    <div className={styles.stack}>
      <PageHeader title="New output rule" description="Say where the original or a copy of one kind of document goes. The most specific rule wins: legal entity and customer, then customer, then legal entity, then the default." />
      <form className={styles.stack} onSubmit={submit} noValidate aria-label="Output rule">
        <FormErrorSummary items={summaryItems(errors, FIELDS)} attempt={attempt} />
        <Card title="Which documents">
          <div className={styles.form}>
            <FormField id="rule-documentType" label="Document" error={errors.documentType}>
              {(control) => (
                <Select {...control} value={values.documentType} onChange={(e) => update({ documentType: e.target.value })}>
                  {Object.entries(DOCUMENT_TYPE_LABELS).map(([t, label]) => (
                    <option key={t} value={t}>
                      {label}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            <FormField id="rule-legalEntityId" label="Legal entity" hint="Leave empty for every legal entity. That needs tenant-wide authority." optional error={errors.legalEntityId}>
              {(control) => <EntityLookup entity="legalEntity" {...control} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
            </FormField>
            <FormField id="rule-accountId" label="Customer" hint="Leave empty for every customer." optional error={errors.accountId}>
              {(control) => <EntityLookup entity="customer" {...control} scope={{ legalEntityId: values.legalEntityId || undefined }} value={values.accountId} onChange={(e) => update({ accountId: e.target.value })} invalid={Boolean(errors.accountId)} />}
            </FormField>
            <FormField id="rule-copyKind" label="Copy" error={errors.copyKind}>
              {(control) => (
                <Select {...control} value={values.copyKind} onChange={(e) => update({ copyKind: e.target.value as "ORIGINAL" | "COPY" })}>
                  <option value="ORIGINAL">Original</option>
                  <option value="COPY">Copy</option>
                </Select>
              )}
            </FormField>
          </div>
        </Card>
        <Card title="Where it goes">
          <div className={styles.form}>
            <FormField id="rule-channel" label="Goes to" hint={CHANNEL_HELP[values.channel]} error={errors.channel}>
              {(control) => (
                <Select {...control} value={values.channel} onChange={(e) => update({ channel: e.target.value, destination: "", copies: "1" })}>
                  {Object.entries(CHANNEL_LABELS).map(([c, label]) => (
                    <option key={c} value={c}>
                      {label}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {needsDestination ? (
              <FormField id="rule-destination" label={printer ? "Print queue" : "E-mail address"} error={errors.destination}>
                {(control) => <Input {...control} type={printer ? "text" : "email"} value={values.destination} onChange={(e) => update({ destination: printer ? e.target.value.toUpperCase() : e.target.value })} invalid={Boolean(errors.destination)} />}
              </FormField>
            ) : null}
            {printer ? (
              <FormField id="rule-copies" label="Copies" error={errors.copies}>
                {(control) => <Input {...control} inputMode="numeric" value={values.copies} onChange={(e) => update({ copies: e.target.value })} invalid={Boolean(errors.copies)} />}
              </FormField>
            ) : null}
            <FormField id="rule-sequence" label="Sequence" hint="Rules of the same scope are listed by sequence." error={errors.sequence}>
              {(control) => <Input {...control} inputMode="numeric" value={values.sequence} onChange={(e) => update({ sequence: e.target.value })} invalid={Boolean(errors.sequence)} />}
            </FormField>
          </div>
        </Card>
        {failure ? (
          <Alert tone="danger" title="Could not add the output rule" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Add output rule
          </Button>
          <Link className={buttonClassName("ghost")} to="/document-design">
            Back to document design
          </Link>
        </div>
        {prompt}
      </form>
    </div>
  );
}
