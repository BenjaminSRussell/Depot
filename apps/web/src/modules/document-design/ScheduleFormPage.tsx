import { useMemo, useRef, useState, type FormEvent } from "react";
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
import { createStatementSchedule, describeProblem, listTemplates, newKey, SCHEDULE_CADENCE_LABELS, WEEKDAYS, type Problem, type StatementScheduleInput } from "./api";
import styles from "./DocumentDesignPage.module.css";
import { useLoad } from "./useLoad";

type Errors = Record<string, string>;

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const LANGUAGE = /^[a-z]{2}(-[A-Z]{2})?$/;

export interface ScheduleValues {
  name: string;
  legalEntityId: string;
  senderName: string;
  cadence: "WEEKLY" | "MONTHLY";
  runDay: string;
  language: string;
  templateCode: string;
}

export const blankSchedule: ScheduleValues = { name: "", legalEntityId: "", senderName: "", cadence: "MONTHLY", runDay: "3", language: "en", templateCode: "" };

/** Checks that mirror the service (domain.StatementSchedule.Validate). */
export function validateSchedule(v: ScheduleValues): { errors: Errors; input?: StatementScheduleInput } {
  const errors: Errors = {};
  const day = Number(v.runDay);
  if (v.name.trim() === "" || v.name.trim().length > 100) errors.name = "Enter a name of up to 100 characters.";
  if (!UUID.test(v.legalEntityId)) errors.legalEntityId = "Choose the legal entity whose customers get statements.";
  if (v.senderName.trim() === "" || v.senderName.trim().length > 200) errors.senderName = "Enter the name to print on the statements.";
  const max = v.cadence === "WEEKLY" ? 7 : 28;
  if (!Number.isInteger(day) || day < 1 || day > max) errors.runDay = v.cadence === "WEEKLY" ? "Choose a weekday." : "Enter a day of the month from 1 to 28.";
  if (!LANGUAGE.test(v.language)) errors.language = "Enter a language such as en, or en-GB with a region.";
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    input: { name: v.name.trim(), legalEntityId: v.legalEntityId, senderName: v.senderName.trim(), cadence: v.cadence, runDay: day, language: v.language, ...(v.templateCode ? { templateCode: v.templateCode } : {}) },
  };
}

const FIELDS: Record<string, FormFieldTarget> = {
  name: { id: "schedule-name", label: "Name" },
  legalEntityId: { id: "schedule-legalEntityId", label: "Legal entity" },
  senderName: { id: "schedule-senderName", label: "Name on the statements" },
  cadence: { id: "schedule-cadence", label: "Repeats" },
  runDay: { id: "schedule-runDay", label: "Day" },
  language: { id: "schedule-language", label: "Language" },
  templateCode: { id: "schedule-templateCode", label: "Template" },
};

/** A new statement schedule (46-4). */
export function NewStatementSchedulePage() {
  const navigate = useNavigate();
  const key = useRef(newKey());
  const templates = useLoad(listTemplates);
  const statementTemplates = useMemo(() => (templates.data ?? []).filter((t) => t.documentType === "PERIODIC_STATEMENT"), [templates.data]);
  const [values, setValues] = useState(blankSchedule);
  const [errors, setErrors] = useState<Errors>({});
  const [failure, setFailure] = useState<Problem | null>(null);
  const [busy, setBusy] = useState(false);
  const attempt = useSubmitAttempt(errors);
  const { prompt, release } = useUnsavedChanges(useFormBaseline([values]));
  const update = (patch: Partial<ScheduleValues>) => setValues((v) => ({ ...v, ...patch }));

  async function submit(event: FormEvent) {
    event.preventDefault();
    const { errors: found, input } = validateSchedule(values);
    setErrors(found);
    setFailure(null);
    if (!input) return;
    setBusy(true);
    try {
      await createStatementSchedule(input, key.current);
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

  return (
    <div className={styles.stack}>
      <PageHeader title="New statement schedule" description="On each run day every customer of the legal entity with open items gets a statement of account, delivered by the output rules for statements." />
      <form className={styles.stack} onSubmit={submit} noValidate aria-label="Statement schedule">
        <FormErrorSummary items={summaryItems(errors, FIELDS)} attempt={attempt} />
        <Card title="Schedule">
          <div className={styles.form}>
            <FormField id="schedule-name" label="Name" error={errors.name}>
              {(control) => <Input {...control} value={values.name} onChange={(e) => update({ name: e.target.value })} invalid={Boolean(errors.name)} />}
            </FormField>
            <FormField id="schedule-legalEntityId" label="Legal entity" error={errors.legalEntityId}>
              {(control) => <EntityLookup entity="legalEntity" {...control} value={values.legalEntityId} onChange={(e) => update({ legalEntityId: e.target.value })} invalid={Boolean(errors.legalEntityId)} />}
            </FormField>
            <FormField id="schedule-senderName" label="Name on the statements" hint="Printed in the letterhead." error={errors.senderName}>
              {(control) => <Input {...control} value={values.senderName} onChange={(e) => update({ senderName: e.target.value })} invalid={Boolean(errors.senderName)} />}
            </FormField>
            <FormField id="schedule-cadence" label="Repeats" error={errors.cadence}>
              {(control) => (
                <Select {...control} value={values.cadence} onChange={(e) => update({ cadence: e.target.value as "WEEKLY" | "MONTHLY", runDay: e.target.value === "WEEKLY" ? "1" : "3" })}>
                  {Object.entries(SCHEDULE_CADENCE_LABELS).map(([c, label]) => (
                    <option key={c} value={c}>
                      {label}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
            {values.cadence === "WEEKLY" ? (
              <FormField id="schedule-runDay" label="Day" error={errors.runDay}>
                {(control) => (
                  <Select {...control} value={values.runDay} onChange={(e) => update({ runDay: e.target.value })}>
                    {WEEKDAYS.map((d, i) => (
                      <option key={d} value={String(i + 1)}>
                        {d}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            ) : (
              <FormField id="schedule-runDay" label="Day of the month" hint="1 to 28, so every month has it." error={errors.runDay}>
                {(control) => <Input {...control} inputMode="numeric" value={values.runDay} onChange={(e) => update({ runDay: e.target.value })} invalid={Boolean(errors.runDay)} />}
              </FormField>
            )}
            <FormField id="schedule-language" label="Language" hint="The language of the template's text blocks." error={errors.language}>
              {(control) => <Input {...control} className={styles.mono} value={values.language} onChange={(e) => update({ language: e.target.value })} invalid={Boolean(errors.language)} />}
            </FormField>
            <FormField id="schedule-templateCode" label="Template" hint="The default template for statements when empty." optional error={errors.templateCode}>
              {(control) => (
                <Select {...control} value={values.templateCode} onChange={(e) => update({ templateCode: e.target.value })}>
                  <option value="">The default statement template</option>
                  {statementTemplates.map((t) => (
                    <option key={t.code} value={t.code}>
                      {t.name}
                    </option>
                  ))}
                </Select>
              )}
            </FormField>
          </div>
        </Card>
        {failure ? (
          <Alert tone="danger" title="Could not add the schedule" live="alert">
            <ProblemMessage problem={failure} />
          </Alert>
        ) : null}
        <div className={styles.actions}>
          <Button type="submit" variant="primary" loading={busy}>
            Add schedule
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
