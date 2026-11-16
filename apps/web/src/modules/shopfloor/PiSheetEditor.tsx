import { Button, FormField, Input, Select } from "../../shell/ui";
import { listPISteps, type Instruction, type InstructionCategory, type PIStepTemplate, type RecipeDocument, type RecipeOperation, type RecipePhase, type RecipeUnitProcedure } from "./api/client";
import { CATEGORIES, CATEGORY_LABEL, instructionFromTemplate, matchesTemplate, moveItem, newInstruction, renumber } from "./piSheet";
import { StepFields } from "./PiStepFields";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

/** Edits the ISA-88 structure of a PI sheet: unit procedures, operations, phases and their numbered instructions (FR-25.16). */
export function PiSheetEditor({ doc, onChange, readOnly }: { doc: RecipeDocument; onChange(next: RecipeDocument): void; readOnly: boolean }) {
  const ups = doc.procedure.unitProcedures;
  const setUps = (next: RecipeUnitProcedure[]) => onChange({ procedure: { ...doc.procedure, unitProcedures: next } });
  const patch = <T,>(list: T[], i: number, change: Partial<T>): T[] => list.map((item, j) => (j === i ? { ...item, ...change } : item));
  return (
    <div className={styles.stack}>
      <FormField id="pi-procedure" label="Procedure name" hint="The batch or process this sheet runs.">
        {(control) => <Input {...control} value={doc.procedure.name} disabled={readOnly} maxLength={120} onChange={(e) => onChange({ procedure: { ...doc.procedure, name: e.target.value } })} />}
      </FormField>
      {ups.map((up, ui) => (
        <fieldset key={ui} className={styles.fieldset}>
          <legend>{`Unit procedure ${ui + 1}`}</legend>
          <FormField id={`pi-up-${ui}`} label="Unit procedure name">
            {(control) => <Input {...control} value={up.name} disabled={readOnly} maxLength={120} onChange={(e) => setUps(patch(ups, ui, { name: e.target.value }))} />}
          </FormField>
          {up.operations.map((op, oi) => (
            <fieldset key={oi} className={styles.fieldset}>
              <legend>{`Operation ${oi + 1}`}</legend>
              <FormField id={`pi-op-${ui}-${oi}`} label="Operation name">
                {(control) => (
                  <Input
                    {...control}
                    value={op.name}
                    disabled={readOnly}
                    maxLength={120}
                    onChange={(e) => setUps(patch(ups, ui, { operations: patch(up.operations, oi, { name: e.target.value }) }))}
                  />
                )}
              </FormField>
              {op.phases.map((ph, pi) => (
                <PhaseEditor
                  key={pi}
                  id={`${ui}-${oi}-${pi}`}
                  phase={ph}
                  readOnly={readOnly}
                  canRemove={op.phases.length > 1}
                  onChange={(next) => setUps(patch(ups, ui, { operations: patch(up.operations, oi, { phases: patch(op.phases, pi, next) }) }))}
                  onRemove={() => setUps(patch(ups, ui, { operations: patch(up.operations, oi, { phases: op.phases.filter((_, k) => k !== pi) }) }))}
                />
              ))}
              {readOnly ? null : (
                <div className={styles.actions}>
                  <Button
                    size="sm"
                    onClick={() => setUps(patch(ups, ui, { operations: patch(up.operations, oi, { phases: [...op.phases, { name: `Phase ${op.phases.length + 1}`, instructions: [newInstruction("instruction-text", 1)] }] }) }))}
                  >
                    Add phase
                  </Button>
                  {up.operations.length > 1 ? (
                    <Button size="sm" variant="ghost" onClick={() => setUps(patch(ups, ui, { operations: up.operations.filter((_, k) => k !== oi) }))}>
                      Remove this operation
                    </Button>
                  ) : null}
                </div>
              )}
            </fieldset>
          ))}
          {readOnly ? null : (
            <div className={styles.actions}>
              <Button
                size="sm"
                onClick={() => setUps(patch(ups, ui, { operations: [...up.operations, emptyOperation(up.operations.length + 1)] }))}
              >
                Add operation
              </Button>
              {ups.length > 1 ? (
                <Button size="sm" variant="ghost" onClick={() => setUps(ups.filter((_, k) => k !== ui))}>
                  Remove this unit procedure
                </Button>
              ) : null}
            </div>
          )}
        </fieldset>
      ))}
      {readOnly ? null : (
        <div className={styles.actions}>
          <Button onClick={() => setUps([...ups, { name: `Unit ${ups.length + 1}`, operations: [emptyOperation(1)] }])}>Add unit procedure</Button>
        </div>
      )}
    </div>
  );
}

function emptyOperation(n: number): RecipeOperation {
  return { name: `Operation ${n}`, phases: [{ name: "Phase 1", instructions: [newInstruction("instruction-text", 1)] }] };
}

function PhaseEditor({
  id,
  phase,
  readOnly,
  canRemove,
  onChange,
  onRemove,
}: {
  id: string;
  phase: RecipePhase;
  readOnly: boolean;
  canRemove: boolean;
  onChange(next: Partial<RecipePhase>): void;
  onRemove(): void;
}) {
  const list = phase.instructions;
  // The branch target is a position in the phase, so a move or a removal keeps each jump on the step it pointed at.
  const setList = (next: Instruction[]) => onChange({ instructions: renumber(next) });
  const library = useLoad((signal) => (readOnly ? Promise.resolve([] as PIStepTemplate[]) : listPISteps({ status: "published" }, signal)), `pi-library|${readOnly}`);
  return (
    <fieldset className={styles.fieldset}>
      <legend>{`Phase: ${phase.name || "unnamed"}`}</legend>
      <FormField id={`pi-ph-${id}`} label="Phase name">
        {(control) => <Input {...control} value={phase.name} disabled={readOnly} maxLength={120} onChange={(e) => onChange({ name: e.target.value })} />}
      </FormField>
      <ol className={styles.plainList}>
        {list.map((ins, i) => (
          <li key={i}>
            <InstructionEditor
              id={`${id}-${i}`}
              ins={ins}
              readOnly={readOnly}
              onChange={(next) => setList(list.map((x, k) => (k === i ? dropTemplateWhenEdited(x, next) : x)))}
              onMove={(by) => setList(moveItem(list, i, i + by))}
              onRemove={list.length > 1 ? () => setList(list.filter((_, k) => k !== i)) : undefined}
              first={i === 0}
              last={i === list.length - 1}
              phaseSteps={list.length}
            />
          </li>
        ))}
      </ol>
      {readOnly ? null : (
        <div className={styles.actions}>
          <FormField id={`pi-add-${id}`} label="Add a step">
            {(control) => (
              <Select
                {...control}
                value=""
                onChange={(e) => {
                  if (e.target.value) setList([...list, newInstruction(e.target.value as InstructionCategory, list.length + 1)]);
                }}
              >
                <option value="">Choose a type of step</option>
                {CATEGORIES.map((c) => (
                  <option key={c} value={c}>
                    {CATEGORY_LABEL[c]}
                  </option>
                ))}
              </Select>
            )}
          </FormField>
          {(library.data ?? []).length > 0 ? (
            <FormField id={`pi-lib-${id}`} label="Add a step from the library" hint="The template is copied into the sheet and its code and version are kept.">
              {(control) => (
                <Select
                  {...control}
                  value=""
                  onChange={(e) => {
                    const tpl = (library.data ?? []).find((t) => t.id === e.target.value);
                    if (tpl) setList([...list, instructionFromTemplate(tpl, list.length + 1)]);
                  }}
                >
                  <option value="">Choose a step template</option>
                  {(library.data ?? []).map((t) => (
                    <option key={t.id} value={t.id}>{`${t.name} (${t.code} v${t.version}, ${CATEGORY_LABEL[t.kind].toLowerCase()})`}</option>
                  ))}
                </Select>
              )}
            </FormField>
          ) : null}
          {canRemove ? (
            <Button size="sm" variant="ghost" onClick={onRemove}>
              Remove this phase
            </Button>
          ) : null}
        </div>
      )}
    </fieldset>
  );
}

function InstructionEditor({
  id,
  ins,
  readOnly,
  first,
  last,
  phaseSteps,
  onChange,
  onMove,
  onRemove,
}: {
  id: string;
  ins: Instruction;
  readOnly: boolean;
  first: boolean;
  last: boolean;
  phaseSteps: number;
  onChange(next: Instruction): void;
  onMove(by: number): void;
  onRemove?: () => void;
}) {
  return (
    <div className={styles.block}>
      <p className={styles.note}>
        {`Step ${ins.seq}: ${CATEGORY_LABEL[ins.category]}`}
        {ins.template ? ` (from the library: ${ins.template.code} version ${ins.template.version})` : ""}
      </p>
      <StepFields id={id} step={ins} readOnly={readOnly} phaseSteps={phaseSteps} onChange={onChange} />
      {readOnly ? null : (
        <div className={styles.actions}>
          <Button size="sm" variant="ghost" disabled={first} onClick={() => onMove(-1)}>
            Move up
          </Button>
          <Button size="sm" variant="ghost" disabled={last} onClick={() => onMove(1)}>
            Move down
          </Button>
          {onRemove ? (
            <Button size="sm" variant="ghost" onClick={onRemove}>
              Remove step
            </Button>
          ) : null}
        </div>
      )}
    </div>
  );
}

/**
 * A step made from a library template keeps its reference only while it is the template's step: the service refuses a
 * reference whose step differs from the template version. Editing a copied field makes it the sheet's own step.
 */
function dropTemplateWhenEdited(before: Instruction, after: Instruction): Instruction {
  if (!before.template) return after;
  const { template: _t, ...own } = after;
  return matchesTemplate(own as Instruction, withoutTemplate(before)) ? after : (own as Instruction);
}

function withoutTemplate(ins: Instruction): Instruction {
  const { template: _t, ...rest } = ins;
  return rest as Instruction;
}
