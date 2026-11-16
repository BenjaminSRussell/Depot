import { FormField, Input, Select, Textarea } from "../../shell/ui";
import type { Instruction, PIStepSpec, SignatureMeaning } from "./api/client";
import { MEANING_LABEL } from "./piSheet";
import styles from "./pages.module.css";

type StepLike = Instruction | PIStepSpec;

const optionalNumber = (text: string): number | undefined => {
  const n = Number(text);
  return text.trim() === "" || !Number.isFinite(n) ? undefined : n;
};

const PRODUCES_VALUE = ["process-parameter-setpoint", "process-data-request", "inspection-characteristic", "machine-readout", "calculation"];

/**
 * The fields of one process instruction that depend on its kind: text, signature meaning, characteristic and setpoint, the
 * value name later steps read, a calculation's formula, a machine readout's node and tolerance, a timer's length and a
 * branch's condition and jump. Used by the PI sheet editor and by the step library form (a template has no jump target:
 * pass `phaseSteps` only where the step has a place in a phase).
 */
export function StepFields<T extends StepLike>({
  id,
  step,
  readOnly,
  phaseSteps,
  onChange,
}: {
  id: string;
  step: T;
  readOnly: boolean;
  /** How many steps the step's phase has; set where the step sits in a sheet, so a branch can pick its jump target. */
  phaseSteps?: number;
  onChange(next: T): void;
}) {
  const c = step.characteristic;
  const hasCharacteristic = ["process-parameter-setpoint", "process-data-request", "inspection-characteristic", "machine-readout"].includes(step.category);
  const setChar = (change: Partial<NonNullable<StepLike["characteristic"]>>) => onChange({ ...step, characteristic: { name: "", type: "numeric", ...c, ...change } });
  const seq = (step as Instruction).seq as number | undefined;
  const textLabel = step.category === "material-list" ? "Summary" : step.category === "timer" ? "What to do while it runs" : step.category === "branch" ? "Question shown to the operator" : "Text";
  return (
    <>
      <FormField id={`pi-text-${id}`} label={textLabel} optional={step.category === "machine-readout"}>
        {(control) => <Textarea {...control} rows={2} maxLength={4000} value={step.text ?? ""} disabled={readOnly} onChange={(e) => onChange({ ...step, text: e.target.value })} />}
      </FormField>
      {step.category === "signature-request" ? (
        <FormField id={`pi-meaning-${id}`} label="Signature meaning" hint="What the signer states by signing (21 CFR 11.50).">
          {(control) => (
            <Select {...control} value={step.meaning ?? "performed"} disabled={readOnly} onChange={(e) => onChange({ ...step, meaning: e.target.value as SignatureMeaning })}>
              {(Object.keys(MEANING_LABEL) as SignatureMeaning[]).map((m) => (
                <option key={m} value={m}>
                  {MEANING_LABEL[m]}
                </option>
              ))}
            </Select>
          )}
        </FormField>
      ) : null}
      {hasCharacteristic ? (
        <div className={styles.premiumRow}>
          <FormField id={`pi-cname-${id}`} label="Characteristic">
            {(control) => <Input {...control} value={c?.name ?? ""} disabled={readOnly} maxLength={120} onChange={(e) => setChar({ name: e.target.value })} />}
          </FormField>
          <FormField id={`pi-ctype-${id}`} label="Kind of value">
            {(control) => (
              <Select
                {...control}
                value={c?.type ?? "numeric"}
                disabled={readOnly || step.category === "process-parameter-setpoint" || step.category === "machine-readout"}
                onChange={(e) => setChar({ type: e.target.value as "numeric" | "text" | "boolean" })}
              >
                <option value="numeric">Number</option>
                <option value="text">Text</option>
                <option value="boolean">Yes or no</option>
              </Select>
            )}
          </FormField>
          {(c?.type ?? "numeric") === "numeric" ? (
            <>
              <FormField id={`pi-cunit-${id}`} label="Unit" optional>
                {(control) => <Input {...control} value={c?.unit ?? ""} disabled={readOnly} maxLength={20} onChange={(e) => setChar({ unit: e.target.value || undefined })} />}
              </FormField>
              <FormField id={`pi-cmin-${id}`} label="Lowest allowed" optional>
                {(control) => <Input {...control} inputMode="decimal" value={c?.min ?? ""} disabled={readOnly} onChange={(e) => setChar({ min: optionalNumber(e.target.value) })} />}
              </FormField>
              <FormField id={`pi-cmax-${id}`} label="Highest allowed" optional>
                {(control) => <Input {...control} inputMode="decimal" value={c?.max ?? ""} disabled={readOnly} onChange={(e) => setChar({ max: optionalNumber(e.target.value) })} />}
              </FormField>
            </>
          ) : null}
          {step.category === "process-parameter-setpoint" ? (
            <FormField id={`pi-value-${id}`} label="Setpoint">
              {(control) => <Input {...control} inputMode="decimal" value={step.value ?? ""} disabled={readOnly} onChange={(e) => onChange({ ...step, value: optionalNumber(e.target.value) })} />}
            </FormField>
          ) : null}
        </div>
      ) : null}
      {step.category === "machine-readout" ? (
        <div className={styles.premiumRow}>
          <FormField id={`pi-node-${id}`} label="Node or tag" hint="The OPC-UA node the value is read from, for example ns=2;s=Oven1.Temp.">
            {(control) => <Input {...control} value={step.node ?? ""} disabled={readOnly} maxLength={300} onChange={(e) => onChange({ ...step, node: e.target.value })} />}
          </FormField>
          <FormField id={`pi-expected-${id}`} label="Expected reading" optional hint="With a tolerance, a reading farther away than this needs a comment.">
            {(control) => <Input {...control} inputMode="decimal" value={step.value ?? ""} disabled={readOnly} onChange={(e) => onChange({ ...step, value: optionalNumber(e.target.value) })} />}
          </FormField>
          <FormField id={`pi-tolerance-${id}`} label="Tolerance" optional>
            {(control) => <Input {...control} inputMode="decimal" value={step.tolerance ?? ""} disabled={readOnly} onChange={(e) => onChange({ ...step, tolerance: optionalNumber(e.target.value) })} />}
          </FormField>
        </div>
      ) : null}
      {step.category === "calculation" ? (
        <FormField id={`pi-expression-${id}`} label="Formula" hint="Over the value names of earlier steps, for example gross - tare. You can use + - * /, comparisons, abs, min, max, round, floor and ceil.">
          {(control) => <Input {...control} value={step.expression ?? ""} disabled={readOnly} maxLength={500} onChange={(e) => onChange({ ...step, expression: e.target.value })} />}
        </FormField>
      ) : null}
      {step.category === "timer" ? (
        <div className={styles.premiumRow}>
          <FormField id={`pi-duration-${id}`} label="Duration (seconds)">
            {(control) => <Input {...control} inputMode="numeric" value={step.durationSeconds ?? ""} disabled={readOnly} onChange={(e) => onChange({ ...step, durationSeconds: e.target.value.trim() === "" ? undefined : Math.trunc(Number(e.target.value)) })} />}
          </FormField>
          <label className={styles.checkRow}>
            <input type="checkbox" checked={step.blocking === true} disabled={readOnly} onChange={(e) => onChange({ ...step, blocking: e.target.checked })} /> The next step waits until the timer has run out
          </label>
        </div>
      ) : null}
      {step.category === "branch" ? (
        <div className={styles.premiumRow}>
          <FormField id={`pi-condition-${id}`} label="Condition" hint="True or false over earlier value names, for example temp > 80 and level < 5.">
            {(control) => <Input {...control} value={step.condition ?? ""} disabled={readOnly} maxLength={500} onChange={(e) => onChange({ ...step, condition: e.target.value })} />}
          </FormField>
          {phaseSteps !== undefined && seq !== undefined ? (
            <FormField id={`pi-target-${id}`} label="When true, go on at step" hint="The steps between this one and the target are skipped.">
              {(control) => (
                <Select {...control} value={String(step.target ?? seq + 1)} disabled={readOnly} onChange={(e) => onChange({ ...step, target: Number(e.target.value) })}>
                  {Array.from({ length: Math.max(0, phaseSteps - seq) }, (_, i) => seq + 1 + i).map((n) => (
                    <option key={n} value={n}>{`Step ${n}`}</option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
        </div>
      ) : null}
      {PRODUCES_VALUE.includes(step.category) ? (
        <FormField id={`pi-var-${id}`} label="Value name" optional hint="A short lower-case name, such as gross, so a later calculation or branch can use this step's value.">
          {(control) => <Input {...control} value={step.var ?? ""} disabled={readOnly} maxLength={40} onChange={(e) => onChange({ ...step, var: e.target.value === "" ? undefined : e.target.value })} />}
        </FormField>
      ) : null}
    </>
  );
}
