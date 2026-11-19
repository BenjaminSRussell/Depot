import type { ActionRecord } from "./audit.js";

export const NOT_REVERSIBLE_CODE = "NOT_REVERSIBLE";

type ReversalKind = "restore_before_snapshot" | "reverse_application" | "none";

interface Entry {
  kind: ReversalKind;
  reason: string;
}

const OWNING_FLOW = "it is approval-gated and reversed through its owning module's normal flow";

/**
 * Which tools an undo can compensate. Reversible kinds: the AP coding proposal (restore the before snapshot while the
 * invoice is still DRAFT) and the cash application settlement (receivables :reverse-application). Undo is a
 * compensating reversal, never a delete. Keys are the catalog's tool names. A tool not listed is not reversible.
 */
export const REVERSIBILITY: Readonly<Record<string, Entry>> = {
  "depot.ap.invoice.propose_coding": { kind: "restore_before_snapshot", reason: "cannot undo: AP invoice coding update is not available yet" },
  // TODO: DEPOT-298 follow-up slice: settlement reversal through receivables :reverse-application
  "depot.ar.settlement.propose": { kind: "reverse_application", reason: "cannot undo: settlement reversal is not available yet" },
  "depot.mrp.action_message.propose_triage": { kind: "none", reason: `cannot undo: ${OWNING_FLOW}` },
  "depot.collections.letter.draft": { kind: "none", reason: `cannot undo: ${OWNING_FLOW}` },
  "depot.ledger.journal.propose_flag": { kind: "none", reason: `cannot undo: ${OWNING_FLOW}` },
};

const UNKNOWN_TOOL_REASON = "cannot undo: this tool is not registered as reversible";

export interface Reversibility {
  reversible: boolean;
  reversibleReason: string;
}

export function reversibilityOf(record: ActionRecord): Reversibility {
  return { reversible: false, reversibleReason: REVERSIBILITY[record.toolName]?.reason ?? UNKNOWN_TOOL_REASON };
}

export type ActionView = ActionRecord & Reversibility;

export const toActionView = (record: ActionRecord): ActionView => ({ ...record, ...reversibilityOf(record) });
