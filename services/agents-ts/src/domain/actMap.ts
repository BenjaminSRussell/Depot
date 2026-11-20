import { classifyByRules, decideRoute, type RouteResult } from "./promptClass.js";

export interface ActEntry {
  tool: string;
  agent: string;
  label: string;
  cue: RegExp;
}

export const ACT_TOOL_MAP: readonly ActEntry[] = [
  {
    tool: "depot.ap.invoice.propose_coding",
    agent: "ap_coding",
    label: "propose coding for a vendor invoice",
    cue: /\b(?:code|coding|recode|gl)\b[^?]*\b(?:vendor )?(?:invoice|bill)\b|\b(?:vendor )?(?:invoice|bill)\b[^?]*\b(?:coding|account|gl)\b/,
  },
  {
    tool: "depot.ar.settlement.propose",
    agent: "cash_app",
    label: "apply a customer payment to invoices",
    cue: /\b(?:apply|settle|allocate|match)\b[^?]*\bpayment\b|\bpayment\b[^?]*\b(?:apply|settle|allocate|match)\b/,
  },
  {
    tool: "depot.mrp.action_message.propose_triage",
    agent: "mrp_triage",
    label: "triage a planned order or action message",
    cue: /\b(?:cancel|expedite|postpone|advance|reschedule|release)\b[^?]*\b(?:planned order|action message|supply)\b/,
  },
  {
    tool: "depot.collections.letter.draft",
    agent: "collections",
    label: "draft collection outreach for overdue invoices",
    cue: /\b(?:draft|write|prepare)\b[^?]*\b(?:dunning|collections?|reminder|overdue|outreach|letter)\b/,
  },
];

export type ActResolution = { status: "mapped"; entry: ActEntry } | { status: "none" } | { status: "ambiguous"; entries: ActEntry[] };

export function resolveActTool(message: string): ActResolution {
  const text = message.trim().toLowerCase();
  const entries = ACT_TOOL_MAP.filter((e) => e.cue.test(text));
  const [only] = entries;
  if (only === undefined) return { status: "none" };
  return entries.length === 1 ? { status: "mapped", entry: only } : { status: "ambiguous", entries };
}

export function navigateAlternative(message: string): { kind: "navigate"; route: RouteResult } {
  return { kind: "navigate", route: decideRoute(classifyByRules(message), "") };
}
