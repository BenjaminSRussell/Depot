import { useMemo, useRef, useState } from "react";
import { useAccount } from "../../shell/auth/account";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Button, Card, FormField, Input, Select, Textarea } from "../../shell/ui";
import { ShopfloorApiError, type Instruction, type RecipeDocument } from "./api/client";
import { CATEGORY_LABEL, checkEntry, collectsValue, flattenSteps, limitsText, MEANING_LABEL, meaningStatement, messageFor, type ProcessMessageDraft, type StepEntry } from "./piSheet";
import styles from "./pages.module.css";

/** SF-0188: the service wants a sign-in from the last few minutes before it accepts a signature (21 CFR 11.200). */
export const CODE_SIGNATURE_REAUTH = "SF-0188";

const resumeKey = (recipeId: string) => `passage.pi.resume.${recipeId}`;

/** The step a person was on when they left to sign in again, once; reading it forgets it. */
function takeResume(recipeId: string, count: number): number {
  try {
    const raw = sessionStorage.getItem(resumeKey(recipeId));
    sessionStorage.removeItem(resumeKey(recipeId));
    const n = raw === null ? 0 : Number(raw);
    return Number.isInteger(n) && n >= 0 && n < count ? n : 0;
  } catch {
    return 0;
  }
}

function keepResume(recipeId: string, index: number) {
  try {
    sessionStorage.setItem(resumeKey(recipeId), String(index));
  } catch {
    // Without storage the person starts the sheet again from its first step; recorded steps are not recorded twice.
  }
}

/**
 * Renders a PI sheet on the kiosk (FR-25.16): one step at a time, in order, with the phase it belongs to. Setpoints are
 * confirmed, values are entered against their limits (outside them only with a comment), a signature step takes the
 * operator's name. Each finished step is handed to onRecord as a process message draft, so what the operator
 * entered is recorded as it happens; onComplete runs after the last step. With no handlers it is a preview.
 */
export function PiSheetRunner({
  doc,
  recipeId,
  onRecord,
  onComplete,
}: {
  doc: RecipeDocument;
  recipeId: string;
  onRecord?(message: ProcessMessageDraft): Promise<void> | void;
  onComplete?(): Promise<void> | void;
}) {
  const steps = useMemo(() => flattenSteps(doc), [doc]);
  const account = useAccount();
  const [index, setIndex] = useState(() => takeResume(recipeId, flattenSteps(doc).length));
  const [needsSignIn, setNeedsSignIn] = useState(false);
  const [entry, setEntry] = useState<StepEntry>({ value: "", comment: "" });
  const [problem, setProblem] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const recorded = useRef(new Set<string>());
  const step = steps[index];

  if (steps.length === 0) return <Alert tone="info" title="This sheet has no steps">There is nothing to run.</Alert>;
  if (done || !step) {
    return (
      <Alert tone="success" title="All steps are done" live="status">
        {onComplete ? "The order's recipe is completed." : "That was the whole sheet."}
      </Alert>
    );
  }
  const ins = step.instruction;
  const last = index === steps.length - 1;

  async function next() {
    if (!step) return;
    const check = checkEntry(ins, entry);
    if (!check.ok) {
      setProblem(check.message);
      return;
    }
    setProblem(null);
    setFailure(null);
    setNeedsSignIn(false);
    setBusy(true);
    try {
      const message = messageFor(recipeId, step, entry);
      if (message && onRecord && !recorded.current.has(message.sourceMessageId)) {
        await onRecord(message);
        recorded.current.add(message.sourceMessageId);
      }
      if (last) {
        await onComplete?.();
        setDone(true);
      } else {
        setIndex(index + 1);
        setEntry({ value: "", comment: "" });
      }
    } catch (error) {
      if (error instanceof ShopfloorApiError && error.code === CODE_SIGNATURE_REAUTH) setNeedsSignIn(true);
      else setFailure(error instanceof Error ? error.message : "The step could not be saved.");
    } finally {
      setBusy(false);
    }
  }

  const outOfLimit = (() => {
    const c = checkEntry(ins, { ...entry, comment: "x" });
    return c.ok && c.outOfLimit;
  })();

  return (
    <Card title={`Step ${index + 1} of ${steps.length}`}>
      <div className={styles.stack}>
        {step.startsPhase ? <h3>{`${step.unit} / ${step.operation} / ${step.phase}`}</h3> : null}
        <p className={styles.note}>{CATEGORY_LABEL[ins.category]}</p>
        <StepBody ins={ins} />
        {collectsValue(ins) ? <StepInput ins={ins} entry={entry} onChange={(e) => { setEntry(e); setProblem(null); }} outOfLimit={outOfLimit} problem={problem} /> : null}
        {needsSignIn ? (
          <Alert
            tone="warning"
            title="Sign in again to sign this step"
            live="alert"
            action={
              account ? (
                <Button
                  size="sm"
                  onClick={() => {
                    keepResume(recipeId, index);
                    account.reauthenticate();
                  }}
                >
                  Sign in again
                </Button>
              ) : undefined
            }
          >
            A signature needs you to have signed in within the last few minutes. {account ? "You come back to this step after signing in." : "Sign in again, then press the button once more."}
          </Alert>
        ) : null}
        {failure ? <Alert tone="danger" title="The step was not saved" live="alert">{failure} Press the button again to retry.</Alert> : null}
        <Button className={styles.kioskButton} variant="primary" loading={busy} onClick={() => void next()}>
          {last ? "Finish" : ins.category === "process-parameter-setpoint" ? "Done, next step" : "Next step"}
        </Button>
      </div>
    </Card>
  );
}

function StepBody({ ins }: { ins: Instruction }) {
  const c = ins.characteristic;
  return (
    <>
      {ins.category === "process-parameter-setpoint" && c ? (
        <p className={styles.value}>{`Set ${c.name || "the value"} to ${ins.value ?? ""}${c.unit ? ` ${c.unit}` : ""}.`}</p>
      ) : null}
      {ins.text ? <p className={styles.value}>{ins.text}</p> : null}
      {ins.category === "material-list" && (ins.materials ?? []).length > 0 ? (
        <ul className={styles.plainList} aria-label="Materials">
          {(ins.materials ?? []).map((m, i) => (
            <li key={i}>
              {`${m.qty}${m.unit ? ` ${m.unit}` : ""} `}
              <EntityName entity="product" id={m.itemId} />
              {m.role && m.role !== "ingredient" ? ` (${m.role})` : ""}
            </li>
          ))}
        </ul>
      ) : null}
    </>
  );
}

function StepInput({ ins, entry, onChange, outOfLimit, problem }: { ins: Instruction; entry: StepEntry; onChange(next: StepEntry): void; outOfLimit: boolean; problem: string | null }) {
  const c = ins.characteristic;
  if (ins.category === "signature-request") {
    // The signer states what the signature means (21 CFR 11.50): the confirmation travels as the entry's comment.
    return (
      <div className={styles.stack}>
        <FormField id="pi-sign" label={`${MEANING_LABEL[ins.meaning ?? "performed"]}: your name`} hint="Type your name to sign this step." error={problem ?? undefined}>
          {(control) => <Input {...control} autoComplete="off" value={entry.value} invalid={Boolean(problem)} onChange={(e) => onChange({ ...entry, value: e.target.value })} />}
        </FormField>
        <label className={styles.checkRow}>
          <input type="checkbox" checked={entry.comment === "confirmed"} onChange={(e) => onChange({ ...entry, comment: e.target.checked ? "confirmed" : "" })} />{" "}
          {meaningStatement(ins.meaning)}
        </label>
      </div>
    );
  }
  const limits = limitsText(c);
  const label = c?.name || "Value";
  return (
    <div className={styles.stack}>
      <FormField id="pi-value" label={`${label}${c?.unit ? ` (${c.unit})` : ""}`} hint={limits ? `Allowed: ${limits}.` : undefined} error={problem ?? undefined}>
        {(control) =>
          c?.type === "boolean" ? (
            <Select {...control} value={entry.value} onChange={(e) => onChange({ ...entry, value: e.target.value })}>
              <option value="">Choose</option>
              <option value="true">Yes</option>
              <option value="false">No</option>
            </Select>
          ) : (
            <Input {...control} inputMode={c?.type === "text" ? "text" : "decimal"} autoComplete="off" value={entry.value} invalid={Boolean(problem)} onChange={(e) => onChange({ ...entry, value: e.target.value })} />
          )
        }
      </FormField>
      <FormField id="pi-comment" label="Comment" optional={!outOfLimit} hint={outOfLimit ? "The value is outside its limits: say why." : undefined}>
        {(control) => <Textarea {...control} rows={2} maxLength={500} value={entry.comment} onChange={(e) => onChange({ ...entry, comment: e.target.value })} />}
      </FormField>
    </div>
  );
}
