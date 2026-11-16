import { useEffect, useMemo, useRef, useState } from "react";
import { useAccount } from "../../shell/auth/account";
import { EntityName } from "../../shell/lookup/EntityName";
import { Alert, Button, Card, FormField, Input, Select, Textarea } from "../../shell/ui";
import { getNodeReading, ShopfloorApiError, type Instruction, type RecipeDocument } from "./api/client";
import { CATEGORY_LABEL, checkEntry, collectsValue, flattenSteps, limitsText, MEANING_LABEL, meaningStatement, messageFor, nextIndex, runBranch, runCalculation, valuesAfter, type ProcessMessageDraft, type StepEntry, type StepValues } from "./piSheet";
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

const valuesKey = (recipeId: string) => `passage.pi.values.${recipeId}`;

/** The step values of the steps done before leaving to sign in again, once. */
function takeValues(recipeId: string): StepValues {
  try {
    const raw = sessionStorage.getItem(valuesKey(recipeId));
    sessionStorage.removeItem(valuesKey(recipeId));
    const parsed: unknown = raw === null ? {} : JSON.parse(raw);
    return parsed !== null && typeof parsed === "object" ? (parsed as StepValues) : {};
  } catch {
    return {};
  }
}

function keepResume(recipeId: string, index: number, values: StepValues) {
  try {
    sessionStorage.setItem(resumeKey(recipeId), String(index));
    sessionStorage.setItem(valuesKey(recipeId), JSON.stringify(values));
  } catch {
    // Without storage the person starts the sheet again from its first step; recorded steps are not recorded twice.
  }
}

/** m:ss for a countdown. */
export function clockText(seconds: number): string {
  const s = Math.max(0, Math.ceil(seconds));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

interface RunningTimer {
  key: string;
  label: string;
  endsAt: number;
  blocking: boolean;
}

type Reading = { state: "loading" } | { state: "ok"; readAt: string } | { state: "none" };

/**
 * Renders a PI sheet on the kiosk (FR-25.16): one step at a time, in order, with the phase it belongs to. Setpoints are
 * confirmed, values are entered against their limits (outside them only with a comment), a signature step takes the
 * operator's name. The step library's kinds run here: a calculation shows its result from the values of earlier steps, a
 * machine readout fetches the value the edge connector read (the operator types it, with a reason, when there is none), a
 * timer counts down (and holds the next step while it is blocking), and a branch jumps ahead when its condition is true.
 * Each finished step is handed to onRecord as a process message draft, so what the operator entered is recorded as it
 * happens; onComplete runs after the last step. With no handlers it is a preview.
 */
export function PiSheetRunner({
  doc,
  recipeId,
  onRecord,
  onComplete,
  clock = Date.now,
}: {
  doc: RecipeDocument;
  recipeId: string;
  onRecord?(message: ProcessMessageDraft): Promise<void> | void;
  onComplete?(): Promise<void> | void;
  /** The time source of the timers; tests pass their own. */
  clock?: () => number;
}) {
  const steps = useMemo(() => flattenSteps(doc), [doc]);
  const account = useAccount();
  const [index, setIndex] = useState(() => takeResume(recipeId, flattenSteps(doc).length));
  const [values, setValues] = useState<StepValues>(() => takeValues(recipeId));
  const [needsSignIn, setNeedsSignIn] = useState(false);
  const [entry, setEntry] = useState<StepEntry>({ value: "", comment: "" });
  const [problem, setProblem] = useState<string | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [timers, setTimers] = useState<RunningTimer[]>([]);
  const [now, setNow] = useState(() => clock());
  const [reading, setReading] = useState<Reading>({ state: "loading" });
  const [readAgain, setReadAgain] = useState(0);
  const recorded = useRef(new Set<string>());
  const step = steps[index];
  const ins = step?.instruction;

  // A timer starts when its step is shown, once; a countdown ticks each second while any timer runs.
  useEffect(() => {
    if (!step || step.instruction.category !== "timer") return;
    setTimers((all) =>
      all.some((t) => t.key === step.key)
        ? all
        : [...all, { key: step.key, label: step.instruction.text ?? "Timer", endsAt: clock() + (step.instruction.durationSeconds ?? 0) * 1000, blocking: step.instruction.blocking === true }],
    );
  }, [step, clock]);
  const running = timers.some((t) => t.endsAt > now);
  useEffect(() => {
    if (!running && !(ins?.category === "timer")) return;
    const handle = setInterval(() => setNow(clock()), 1000);
    return () => clearInterval(handle);
  }, [running, ins?.category, clock]);

  // A machine readout asks the service for the connector's latest reading of the node.
  useEffect(() => {
    if (!step || step.instruction.category !== "machine-readout") return;
    const controller = new AbortController();
    setReading({ state: "loading" });
    getNodeReading(step.instruction.node ?? "", controller.signal).then(
      (r) => {
        setReading({ state: "ok", readAt: r.readAt });
        setEntry({ value: r.value, comment: "", manual: false });
      },
      () => {
        if (!controller.signal.aborted) {
          setReading({ state: "none" });
          setEntry({ value: "", comment: "", manual: true });
        }
      },
    );
    return () => controller.abort();
  }, [step, readAgain]);

  const calc = ins && ins.category === "calculation" && ins.expression ? runCalculation(ins, values) : null;
  const branch = ins && ins.category === "branch" ? runBranch(ins, values) : null;
  const here = step ? timers.find((t) => t.key === step.key) : undefined;
  const remaining = here ? (here.endsAt - now) / 1000 : 0;
  const holding = Boolean(here?.blocking && remaining > 0);

  if (steps.length === 0) return <Alert tone="info" title="This sheet has no steps">There is nothing to run.</Alert>;
  if (done || !step || !ins) {
    return (
      <Alert tone="success" title="All steps are done" live="status">
        {onComplete ? "The order's recipe is completed." : "That was the whole sheet."}
      </Alert>
    );
  }
  const jump = branch?.ok === true && branch.jump;
  const nextAt = nextIndex(steps, index, jump);
  const last = nextAt >= steps.length;
  const blocked = holding || (calc !== null && !calc.ok) || (branch !== null && !branch.ok) || (ins.category === "machine-readout" && reading.state === "loading");

  async function next() {
    if (!step) return;
    // What the step collected: the operator's entry, or what the sheet itself worked out.
    const made: StepEntry | undefined =
      ins!.category === "calculation" && calc?.ok ? { value: calc.value, comment: "" } : ins!.category === "timer" ? { value: "", comment: "" } : ins!.category === "branch" ? { value: String(jump), comment: "" } : entry;
    const check = checkEntry(ins!, entry);
    if (!check.ok) {
      setProblem(check.message);
      return;
    }
    setProblem(null);
    setFailure(null);
    setNeedsSignIn(false);
    setBusy(true);
    try {
      const message = messageFor(recipeId, step, made);
      if (message && onRecord && !recorded.current.has(message.sourceMessageId)) {
        await onRecord(message);
        recorded.current.add(message.sourceMessageId);
      }
      setValues((v) => valuesAfter(ins!, made, v));
      if (last) {
        await onComplete?.();
        setDone(true);
      } else {
        setIndex(nextAt);
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
  const others = timers.filter((t) => t.key !== step.key && t.endsAt > now);

  return (
    <Card title={`Step ${index + 1} of ${steps.length}`}>
      <div className={styles.stack}>
        {step.startsPhase ? <h3>{`${step.unit} / ${step.operation} / ${step.phase}`}</h3> : null}
        {others.length > 0 ? (
          <ul className={styles.plainList} aria-label="Running timers">
            {others.map((t) => (
              <li key={t.key}>{`${t.label}: ${clockText((t.endsAt - now) / 1000)} left`}</li>
            ))}
          </ul>
        ) : null}
        <p className={styles.note}>{CATEGORY_LABEL[ins.category]}</p>
        <StepBody ins={ins} />
        {ins.category === "timer" && here ? (
          <p className={styles.value} role="timer" aria-label="Countdown">
            {remaining > 0 ? `${clockText(remaining)} left${here.blocking ? ": the next step waits for the timer" : ""}` : "The timer has run out."}
          </p>
        ) : null}
        {calc ? (
          calc.ok ? (
            <p className={styles.value}>{`Result${ins.var ? ` (${ins.var})` : ""}: ${calc.value}${ins.characteristic?.unit ? ` ${ins.characteristic.unit}` : ""}`}</p>
          ) : (
            <Alert tone="danger" title="This step cannot be calculated" live="alert">{calc.message}</Alert>
          )
        ) : null}
        {branch ? (
          branch.ok ? (
            <p className={styles.value}>{branch.jump ? `The condition is met: the sheet goes on at step ${ins.target}.` : "The condition is not met: the sheet goes on with the next step."}</p>
          ) : (
            <Alert tone="danger" title="This step cannot be decided" live="alert">{branch.message}</Alert>
          )
        ) : null}
        {ins.category === "machine-readout" ? (
          <ReadoutStatus reading={reading} node={ins.node ?? ""} onAgain={() => setReadAgain((n) => n + 1)} />
        ) : null}
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
                    keepResume(recipeId, index, values);
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
        <Button className={styles.kioskButton} variant="primary" loading={busy} disabled={blocked} onClick={() => void next()}>
          {last ? "Finish" : ins.category === "process-parameter-setpoint" ? "Done, next step" : "Next step"}
        </Button>
      </div>
    </Card>
  );
}

function ReadoutStatus({ reading, node, onAgain }: { reading: Reading; node: string; onAgain(): void }) {
  if (reading.state === "loading") return <p className={styles.note} aria-live="polite">{`Reading ${node}...`}</p>;
  if (reading.state === "ok") return <p className={styles.note}>{`Read from ${node} at ${new Date(reading.readAt).toLocaleTimeString()}.`}</p>;
  return (
    <Alert tone="warning" title="No recent reading from the machine" live="status" action={<Button size="sm" onClick={onAgain}>Read again</Button>}>
      {`The connector has not reported ${node} lately. Read again, or enter the reading by hand with a reason.`}
    </Alert>
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
        <FormField id="pi-sign" label={`${MEANING_LABEL[ins.meaning ?? "performed"]}: your name`} hint="Type your name to confirm that you are the one signing. The signature is recorded for the user you are signed in as." error={problem ?? undefined}>
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
  const fromMachine = ins.category === "machine-readout" && entry.manual !== true;
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
            <Input {...control} inputMode={c?.type === "text" ? "text" : "decimal"} autoComplete="off" readOnly={fromMachine} value={entry.value} invalid={Boolean(problem)} onChange={(e) => onChange({ ...entry, value: e.target.value })} />
          )
        }
      </FormField>
      <FormField id="pi-comment" label="Comment" optional={!outOfLimit && entry.manual !== true} hint={entry.manual ? "Say why the reading was entered by hand." : outOfLimit ? "The value is outside its limits: say why." : undefined}>
        {(control) => <Textarea {...control} rows={2} maxLength={500} value={entry.comment} onChange={(e) => onChange({ ...entry, comment: e.target.value })} />}
      </FormField>
    </div>
  );
}
