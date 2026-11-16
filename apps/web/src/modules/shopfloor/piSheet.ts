// The PI sheet designer's and the kiosk runner's rules, apart from any screen (FR-25.16). A PI sheet is the ISA-88
// structure of a control recipe: unit procedures, operations, phases and, in each phase, numbered process instructions.

import type { Instruction, InstructionCategory, PIStepSpec, PIStepTemplate, RecipeDocument, SignatureMeaning } from "./api/client";
import { exprProblem, parseExpr } from "./piExpr";

/** Signature meanings (21 CFR 11.50): what the signer states by signing the step. */
export const MEANING_LABEL: Record<SignatureMeaning, string> = {
  performed: "Performed by",
  verified: "Verified by",
  reviewed: "Reviewed by",
  approved: "Approved by",
};

/** The sentence a signer confirms on the kiosk before the signature is recorded. */
export function meaningStatement(meaning: SignatureMeaning | undefined): string {
  switch (meaning ?? "performed") {
    case "verified":
      return "I verified this step.";
    case "reviewed":
      return "I reviewed this step.";
    case "approved":
      return "I approve this step.";
    default:
      return "I performed this step.";
  }
}

export const CATEGORY_LABEL: Record<InstructionCategory, string> = {
  "instruction-text": "Instruction",
  "process-parameter-setpoint": "Setpoint",
  "process-data-request": "Enter a value",
  "calculation": "Calculation",
  "inspection-characteristic": "Inspection",
  "material-list": "Material list",
  "signature-request": "Signature",
  "sequence-definition": "Sequence",
  "machine-readout": "Machine readout",
  "timer": "Timer",
  "branch": "Branch",
};

export const CATEGORIES = Object.keys(CATEGORY_LABEL) as InstructionCategory[];

export function emptyDocument(name = "New PI sheet"): RecipeDocument {
  return {
    procedure: {
      name,
      unitProcedures: [{ name: "Unit", operations: [{ name: "Operation", phases: [{ name: "Phase", instructions: [newInstruction("instruction-text", 1)] }] }] }],
    },
  };
}

export function newInstruction(category: InstructionCategory, seq: number): Instruction {
  switch (category) {
    case "process-parameter-setpoint":
      return { seq, category, text: "", value: 0, characteristic: { name: "", type: "numeric" } };
    case "process-data-request":
    case "inspection-characteristic":
      return { seq, category, text: "", characteristic: { name: "", type: "numeric" } };
    case "material-list":
      return { seq, category, text: "Charge the materials.", materials: [] };
    case "machine-readout":
      return { seq, category, text: "", node: "", characteristic: { name: "", type: "numeric" } };
    case "timer":
      return { seq, category, text: "", durationSeconds: 60, blocking: true };
    case "branch":
      return { seq, category, text: "", condition: "", target: seq + 1 };
    case "calculation":
      return { seq, category, text: "", expression: "" };
    default:
      return { seq, category, text: "" };
  }
}

/** Instructions are numbered 1, 2, 3 in order within their phase; call after any insert, remove or move. */
export function renumber(instructions: readonly Instruction[]): Instruction[] {
  // A branch jumps to a step number: after a move or a removal each jump follows the step it pointed at. A jump to a step
  // that is gone goes on at the next step that is left, and a jump that no longer points ahead goes to the next step.
  const newSeq = new Map(instructions.map((ins, i) => [ins.seq, i + 1]));
  return instructions.map((ins, i) => {
    const seq = i + 1;
    if (ins.category !== "branch" || ins.target === undefined) return { ...ins, seq };
    let target = newSeq.get(ins.target);
    if (target === undefined) {
      const later = instructions.findIndex((x) => x.seq > (ins.target as number));
      target = later === -1 ? instructions.length + 1 : later + 1;
    }
    return { ...ins, seq, target: target > seq ? target : seq + 1 };
  });
}

export function moveItem<T>(list: readonly T[], from: number, to: number): T[] {
  if (to < 0 || to >= list.length || from === to) return [...list];
  const copy = [...list];
  const [item] = copy.splice(from, 1);
  copy.splice(to, 0, item as T);
  return copy;
}

/** A person-readable path of one phase, as process messages name it. */
export function phasePath(doc: RecipeDocument, up: string, op: string, phase: string): string {
  return [doc.procedure.name, up, op, phase].join("/");
}

/** Problems a document has, keyed by a readable place, so the designer can show them before the service does. */
export function documentProblems(doc: RecipeDocument): string[] {
  const out: string[] = [];
  const unique = (names: string[], where: string) => {
    const seen = new Set<string>();
    for (const n of names) {
      const t = n.trim();
      if (t === "" || t.length > 120) out.push(`${where}: a name must be 1 to 120 characters.`);
      else if (seen.has(t)) out.push(`${where}: the name "${t}" is used twice.`);
      seen.add(t);
    }
  };
  if (doc.procedure.name.trim() === "") out.push("The procedure needs a name.");
  if (doc.procedure.unitProcedures.length === 0) out.push("The procedure needs at least one unit procedure.");
  unique(doc.procedure.unitProcedures.map((u) => u.name), "Unit procedures");
  for (const up of doc.procedure.unitProcedures) {
    if (up.operations.length === 0) out.push(`${up.name}: add an operation.`);
    unique(up.operations.map((o) => o.name), `${up.name}, operations`);
    for (const op of up.operations) {
      if (op.phases.length === 0) out.push(`${up.name} / ${op.name}: add a phase.`);
      unique(op.phases.map((p) => p.name), `${up.name} / ${op.name}, phases`);
      for (const ph of op.phases) {
        const where = `${up.name} / ${op.name} / ${ph.name}`;
        if (ph.instructions.length === 0) out.push(`${where}: add an instruction.`);
        for (const ins of ph.instructions) {
          const step = `${where}, step ${ins.seq}`;
          if (["instruction-text", "signature-request", "material-list", "sequence-definition", "calculation", "timer", "branch"].includes(ins.category) && (ins.text ?? "").trim() === "") {
            out.push(`${step}: write the text.`);
          }
          out.push(...stepKindProblems(ins, step, ph.instructions.length));
          if (["process-parameter-setpoint", "process-data-request", "inspection-characteristic", "machine-readout"].includes(ins.category)) {
            const c = ins.characteristic;
            if (!c || c.name.trim() === "") out.push(`${step}: name the characteristic.`);
            if (c?.min !== undefined && c.max !== undefined && c.min > c.max) out.push(`${step}: the minimum is above the maximum.`);
            if (ins.category === "process-parameter-setpoint" && ins.value !== undefined && c) {
              if ((c.min !== undefined && ins.value < c.min) || (c.max !== undefined && ins.value > c.max)) out.push(`${step}: the setpoint is outside its limits.`);
            }
          }
        }
      }
    }
  }
  return [...out, ...unknownNames(doc)];
}

const VAR_NAME = /^[a-z][a-z0-9_]{0,39}$/;

/** What the kind of a step needs, as the service checks it: a formula, a node, a duration, a condition and its jump. */
export function stepKindProblems(ins: Instruction | PIStepSpec, where: string, phaseSteps?: number): string[] {
  const out: string[] = [];
  if (ins.var !== undefined && ins.var !== "" && !VAR_NAME.test(ins.var)) out.push(`${where}: the value name must be lower-case letters, digits and '_', starting with a letter.`);
  if (ins.category === "calculation" && ins.expression !== undefined && ins.expression.trim() !== "") {
    const p = exprProblem(ins.expression, "number");
    if (p) out.push(`${where}: ${p}`);
  }
  if (ins.category === "machine-readout") {
    if ((ins.node ?? "").trim() === "") out.push(`${where}: name the node or tag the value is read from.`);
    if (ins.characteristic && ins.characteristic.type !== "numeric") out.push(`${where}: a machine readout is a number.`);
    if (ins.tolerance !== undefined && (ins.tolerance < 0 || ins.value === undefined)) out.push(`${where}: a tolerance is zero or more and needs the expected reading.`);
  }
  if (ins.category === "timer" && (!Number.isInteger(ins.durationSeconds) || (ins.durationSeconds ?? 0) < 1 || (ins.durationSeconds ?? 0) > 86400)) {
    out.push(`${where}: the timer runs 1 to 86400 seconds.`);
  }
  if (ins.category === "branch") {
    const p = exprProblem(ins.condition ?? "", "boolean");
    if (p) out.push(`${where}: ${p}`);
    if (phaseSteps !== undefined && ins.seq !== undefined && ((ins.target ?? 0) <= ins.seq || (ins.target ?? 0) > phaseSteps)) out.push(`${where}: jump to a later step of this phase, ${ins.seq + 1} to ${phaseSteps}.`);
  }
  return out;
}

/** The names a calculation or branch reads that no step before it produces, in a document: the service refuses these. */
export function unknownNames(doc: RecipeDocument): string[] {
  const out: string[] = [];
  const known = new Set<string>();
  for (const up of doc.procedure.unitProcedures) {
    for (const op of up.operations) {
      for (const ph of op.phases) {
        for (const ins of ph.instructions) {
          const src = ins.category === "calculation" ? ins.expression : ins.category === "branch" ? ins.condition : undefined;
          if (src) {
            try {
              for (const n of parseExpr(src).names) if (!known.has(n)) out.push(`${up.name} / ${op.name} / ${ph.name}, step ${ins.seq}: reads ${n}, which no earlier step produces.`);
            } catch {
              // The syntax problem is reported by documentProblems.
            }
          }
          if (ins.var) known.add(ins.var);
        }
      }
    }
  }
  return out;
}

/** A step of a sheet made from a published template: the template's spec is copied, and the reference is kept. */
export function instructionFromTemplate(t: PIStepTemplate, seq: number): Instruction {
  const spec = JSON.parse(JSON.stringify(t.spec)) as PIStepSpec;
  const ins: Instruction = { ...spec, seq, category: t.kind, template: { code: t.code, version: t.version } };
  if (ins.category === "branch") ins.target = seq + 1;
  return ins;
}

// The kiosk runner.

/** One step the operator works through, with where it sits in the recipe. */
export interface RunStep {
  /** Stable within a recipe: unit/operation/phase/seq. Used as the process message's source id. */
  key: string;
  path: string;
  unit: string;
  operation: string;
  phase: string;
  instruction: Instruction;
  /** First step of its phase: the runner shows the phase heading. */
  startsPhase: boolean;
}

export function flattenSteps(doc: RecipeDocument): RunStep[] {
  const steps: RunStep[] = [];
  for (const up of doc.procedure.unitProcedures) {
    for (const op of up.operations) {
      for (const ph of op.phases) {
        ph.instructions.forEach((instruction, i) => {
          steps.push({
            key: [up.name, op.name, ph.name, instruction.seq].join("/"),
            path: phasePath(doc, up.name, op.name, ph.name),
            unit: up.name,
            operation: op.name,
            phase: ph.name,
            instruction,
            startsPhase: i === 0,
          });
        });
      }
    }
  }
  return steps;
}

/** The value a step collects: the entry a person makes. Categories that only ask for an acknowledgement collect none. */
export function collectsValue(ins: Instruction): boolean {
  return ins.category === "process-data-request" || ins.category === "inspection-characteristic" || ins.category === "signature-request" || ins.category === "machine-readout";
}

export interface StepEntry {
  value: string;
  comment: string;
  /** A machine readout the operator typed because the connector had no recent reading. */
  manual?: boolean;
}

export function parseNumber(text: string): number | null {
  const t = text.trim().replace(",", ".");
  if (t === "" || !/^[-+]?(\d+(\.\d*)?|\.\d+)$/.test(t)) return null;
  return Number(t);
}

export type EntryCheck = { ok: true; outOfLimit: boolean } | { ok: false; message: string };

/** Whether a step can be completed with this entry. A value outside its limits needs a comment (BR-25.12). */
export function checkEntry(ins: Instruction, entry: StepEntry): EntryCheck {
  if (ins.category === "signature-request") {
    if (entry.value.trim() === "") return { ok: false, message: "Type your name to sign." };
    return entry.comment === "confirmed" ? { ok: true, outOfLimit: false } : { ok: false, message: `Confirm the statement "${meaningStatement(ins.meaning)}" to sign.` };
  }
  if (ins.category === "machine-readout") {
    const n = parseNumber(entry.value);
    if (n === null) return { ok: false, message: entry.manual ? "Enter the reading." : "Wait for the reading, or enter it by hand." };
    const c = ins.characteristic;
    const out = (c?.min !== undefined && n < c.min) || (c?.max !== undefined && n > c.max) || (ins.value !== undefined && ins.tolerance !== undefined && Math.abs(n - ins.value) > ins.tolerance);
    // A value typed in because the machine gave none is the operator's word, so it always needs a comment.
    if ((out || entry.manual) && entry.comment.trim() === "") return { ok: false, message: entry.manual ? "Say why the reading was entered by hand." : "The reading is outside its limits or tolerance. Say why in the comment to go on." };
    return { ok: true, outOfLimit: out };
  }
  if (ins.category !== "process-data-request" && ins.category !== "inspection-characteristic") return { ok: true, outOfLimit: false };
  const c = ins.characteristic;
  if (c?.type === "text") return entry.value.trim() === "" ? { ok: false, message: "Enter a value." } : { ok: true, outOfLimit: false };
  if (c?.type === "boolean") return entry.value === "true" || entry.value === "false" ? { ok: true, outOfLimit: false } : { ok: false, message: "Choose yes or no." };
  const n = parseNumber(entry.value);
  if (n === null) return { ok: false, message: "Enter a number." };
  const out = (c?.min !== undefined && n < c.min) || (c?.max !== undefined && n > c.max);
  if (out && entry.comment.trim() === "") return { ok: false, message: "The value is outside its limits. Say why in the comment to go on." };
  return { ok: true, outOfLimit: out };
}

/** The limits of a characteristic as text, for the label. */
export function limitsText(c: { min?: number; max?: number; unit?: string } | undefined): string {
  if (!c || (c.min === undefined && c.max === undefined)) return "";
  const unit = c.unit ? ` ${c.unit}` : "";
  if (c.min !== undefined && c.max !== undefined) return `${c.min} to ${c.max}${unit}`;
  return c.min !== undefined ? `at least ${c.min}${unit}` : `at most ${c.max}${unit}`;
}

export interface ProcessMessageDraft {
  sourceMessageId: string;
  categoryCode: "process-data" | "quality-result";
  phasePath: string;
  values: { name: string; type: "numeric" | "text" | "boolean"; value: string; min?: string; max?: string; unit?: string }[];
}

/**
 * The process message a finished step records, or null for a step that records nothing. A setpoint the operator
 * confirmed is recorded as the value set; entries are recorded with their limits, so the service flags what is
 * outside them, and a comment travels as its own text value. Idempotent by recipe and step.
 */
export function messageFor(recipeId: string, step: RunStep, entry: StepEntry | undefined): ProcessMessageDraft | null {
  const ins = step.instruction;
  const c = ins.characteristic;
  const base = { sourceMessageId: `${recipeId}:${step.key}`, phasePath: step.path };
  const num = (n: number | undefined) => (n === undefined ? undefined : String(n));
  if (ins.category === "process-parameter-setpoint" && c) {
    return { ...base, categoryCode: "process-data", values: [{ name: c.name, type: "numeric", value: String(ins.value ?? 0), min: num(c.min), max: num(c.max), unit: c.unit }] };
  }
  if ((ins.category === "process-data-request" || ins.category === "inspection-characteristic") && c && entry) {
    const values: ProcessMessageDraft["values"] = [
      c.type === "numeric"
        ? { name: c.name, type: "numeric", value: String(parseNumber(entry.value) ?? entry.value), min: num(c.min), max: num(c.max), unit: c.unit }
        : { name: c.name, type: c.type, value: entry.value },
    ];
    if (entry.comment.trim() !== "") values.push({ name: "comment", type: "text", value: entry.comment.trim() });
    return { ...base, categoryCode: ins.category === "inspection-characteristic" ? "quality-result" : "process-data", values };
  }
  if (ins.category === "machine-readout" && c && entry) {
    const values: ProcessMessageDraft["values"] = [
      { name: c.name, type: "numeric", value: String(parseNumber(entry.value) ?? entry.value), min: num(c.min), max: num(c.max), unit: c.unit },
      { name: "readout-source", type: "text", value: entry.manual ? "entered-by-hand" : "machine" },
    ];
    if (ins.node) values.push({ name: "node", type: "text", value: ins.node });
    if (ins.value !== undefined && ins.tolerance !== undefined) values.push({ name: "expected", type: "numeric", value: String(ins.value) }, { name: "tolerance", type: "numeric", value: String(ins.tolerance) });
    if (entry.comment.trim() !== "") values.push({ name: "comment", type: "text", value: entry.comment.trim() });
    return { ...base, categoryCode: "process-data", values };
  }
  if (ins.category === "calculation" && ins.expression && entry && entry.value !== "") {
    return { ...base, categoryCode: "process-data", values: [{ name: ins.var || "calculation", type: "numeric", value: entry.value }, { name: "expression", type: "text", value: ins.expression }] };
  }
  if (ins.category === "timer" && entry) {
    return { ...base, categoryCode: "process-data", values: [{ name: "timer-seconds", type: "numeric", value: String(ins.durationSeconds ?? 0) }, { name: "timer-blocking", type: "boolean", value: String(ins.blocking === true) }] };
  }
  if (ins.category === "branch" && entry) {
    return { ...base, categoryCode: "process-data", values: [{ name: "branch-taken", type: "boolean", value: entry.value }, { name: "branch-target", type: "numeric", value: String(ins.target ?? 0) }] };
  }
  if (ins.category === "signature-request" && entry && entry.value.trim() !== "") {
    return {
      ...base,
      categoryCode: "process-data",
      // The service records the signed-in user as the signer (signed-by); the typed name only confirms it is the person.
      values: [{ name: "signature-meaning", type: "text", value: ins.meaning ?? "performed" }],
    };
  }
  return null;
}

// Running the step library's kinds.

/** The values the steps so far produced, by their var, as a calculation or branch reads them. */
export type StepValues = Record<string, number>;

/** What a finished step adds to the values: a setpoint its value, a number entered or read its reading, a calculation its result. */
export function valuesAfter(ins: Instruction, entry: StepEntry | undefined, values: StepValues): StepValues {
  if (!ins.var) return values;
  if (ins.category === "process-parameter-setpoint" && ins.value !== undefined) return { ...values, [ins.var]: ins.value };
  if (!entry) return values;
  if (ins.characteristic?.type === "boolean" && (ins.category === "process-data-request" || ins.category === "inspection-characteristic")) return { ...values, [ins.var]: entry.value === "true" ? 1 : 0 };
  const n = parseNumber(entry.value);
  return n === null ? values : { ...values, [ins.var]: n };
}

export type CalcResult = { ok: true; value: string } | { ok: false; message: string };

/** A calculation step's result from the values so far; a formula that cannot be worked out says why. */
export function runCalculation(ins: Instruction, values: StepValues): CalcResult {
  if (!ins.expression) return { ok: false, message: "This step has no formula." };
  try {
    const r = parseExpr(ins.expression).eval(values);
    return r.type === "number" ? { ok: true, value: String(r.value) } : { ok: false, message: "The formula is not a number." };
  } catch (error) {
    const message = error instanceof Error ? error.message : "The formula could not be worked out.";
    return { ok: false, message: message.startsWith("the step value") ? `${message}: the step that gives it was skipped or has no value.` : message[0]!.toUpperCase() + message.slice(1) + "." };
  }
}

/** Whether a branch step jumps, from the values so far. */
export function runBranch(ins: Instruction, values: StepValues): { ok: true; jump: boolean } | { ok: false; message: string } {
  try {
    const r = parseExpr(ins.condition ?? "").eval(values);
    return r.type === "boolean" ? { ok: true, jump: r.value } : { ok: false, message: "The condition is not true or false." };
  } catch (error) {
    return { ok: false, message: (error instanceof Error ? error.message : "The condition could not be worked out") + "." };
  }
}

/** The index of the step that follows steps[index]: the next one, or for a branch whose condition is true the target step of its phase. */
export function nextIndex(steps: readonly RunStep[], index: number, jump: boolean): number {
  const here = steps[index];
  if (!here || here.instruction.category !== "branch" || !jump) return index + 1;
  const at = steps.findIndex((s) => s.path === here.path && s.instruction.seq === here.instruction.target);
  return at > index ? at : index + 1;
}
