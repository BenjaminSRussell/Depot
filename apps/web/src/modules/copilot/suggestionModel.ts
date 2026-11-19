import { copy } from "../../shell/copy";
import { holdsDuty } from "../../shell/dutyGate";
import type { ChatResponse } from "./api/client";
import type { DataUsedRecord, RunTrace } from "./api/runTrace";
import { toolActionLabel, toolCallLabel } from "./domain";

export type SuggestionHost =
  "answer" | "proposal" | "feed-item" | "briefing-item";

export interface SuggestionViewModel {
  actionId: string;
  agentLabel: string;
  agentVersion?: number | undefined;
  rationale: string | undefined;
  confidencePercent: number | undefined;
  tools: string[];
  records: DataUsedRecord[];
  dataClass?: string | undefined;
  excluded?: LeftOutInput | undefined;
}

export interface LeftOut {
  reason: string;
  count: number;
}

export type LeftOutInput = LeftOut | LeftOut[];

const NEED_TO_KNOW_REASON = "NEED_TO_KNOW";

export function carriesCui(dataClass: string | undefined): boolean {
  return dataClass === "CUI" || dataClass === "UNKNOWN";
}

export function leftOutNotice(excluded: LeftOutInput | undefined): string | undefined {
  const shown = (excluded === undefined ? [] : Array.isArray(excluded) ? excluded : [excluded]).filter(
    (e) => e.reason !== NEED_TO_KNOW_REASON && e.count > 0,
  );
  const count = shown.reduce((sum, e) => sum + e.count, 0);
  if (count === 0) return undefined;
  return copy.marking.leftOut(count, [...new Set(shown.map((e) => copy.marking.reason(e.reason)))]);
}

export interface ResolvedRecord {
  entityLabel: string;
  label: string | undefined;
  href: string | undefined;
}

interface RecordPage {
  entityLabel: string;
  duty: string;
  route: (recordId: string) => string;
}

const RECORD_PAGES: Readonly<Record<string, RecordPage>> = {
  "ap-invoice": {
    entityLabel: "Vendor invoice",
    duty: "receiving.ap.inquire",
    route: (id) => `/ap-invoices/${id}`,
  },
  "ar-payment": {
    entityLabel: "Customer payment",
    duty: "receivables.view",
    route: (id) => `/receivables/payments/${id}`,
  },
  "dunning-run": {
    entityLabel: "Dunning run",
    duty: "receivables.view",
    route: (id) => `/receivables/dunning-runs/${id}`,
  },
  "purchase-order": {
    entityLabel: "Purchase order",
    duty: "procurement.purchase-order.inquire",
    route: (id) => `/purchasing/purchase-orders/${id}`,
  },
  "planned-order": {
    entityLabel: "Planned order",
    duty: "production.order.read",
    route: (id) => `/production/planned-intake/${id}`,
  },
  "journal-entry": {
    entityLabel: "Journal entry",
    duty: "ledger.journal.read",
    route: (id) => `/ledger/entries/${id}`,
  },
};

const AGENT_LABELS: Readonly<Record<string, string>> = {
  ap_coding: "Invoice coding agent",
  cash_application: "Cash application agent",
  mrp_triage: "Planning triage agent",
  collections_outreach: "Collections outreach agent",
  journal_anomaly: "Journal review agent",
};

const UNKNOWN_RECORD_LABEL = "A record";

export function agentLabel(agentId: string): string {
  return AGENT_LABELS[agentId] ?? "An AI agent";
}

export function resolveRecord(
  record: DataUsedRecord,
  duties: readonly string[],
): ResolvedRecord {
  const page = Object.hasOwn(RECORD_PAGES, record.entityType)
    ? RECORD_PAGES[record.entityType]
    : undefined;
  if (!page)
    return {
      entityLabel: UNKNOWN_RECORD_LABEL,
      label: undefined,
      href: undefined,
    };
  if (!holdsDuty(duties, page.duty))
    return { entityLabel: page.entityLabel, label: undefined, href: undefined };
  return {
    entityLabel: page.entityLabel,
    label: record.label,
    href: page.route(encodeURIComponent(record.recordId)),
  };
}

const RECORD_CITATION = /^record:[^/]+\/([^/]+)\/([A-Za-z0-9_-]+)$/;

export function citationRoute(citation: { sourceRef: string }, duties: readonly string[]): string | undefined {
  const match = RECORD_CITATION.exec(citation.sourceRef);
  const [, entity, id] = match ?? [];
  if (entity === undefined || id === undefined || !Object.hasOwn(RECORD_PAGES, entity)) return undefined;
  const page = RECORD_PAGES[entity];
  return page !== undefined && holdsDuty(duties, page.duty) ? page.route(encodeURIComponent(id)) : undefined;
}

export function answerViewModel(response: ChatResponse, turnId: string): SuggestionViewModel {
  return {
    actionId: turnId,
    agentLabel: copy.answer.agentLabel,
    rationale: copy.answer.why,
    confidencePercent: undefined,
    tools: response.toolCalls.map(toolCallLabel),
    records: [],
    dataClass: response.dataClass,
    excluded: response.excluded,
  };
}

export function toSuggestionViewModels(trace: RunTrace): SuggestionViewModel[] {
  const tools = [
    ...new Set(
      [...trace.actions]
        .sort((a, b) => a.seq - b.seq)
        .map((a) => toolActionLabel(a.toolName)),
    ),
  ];
  return trace.actions.map((action) => ({
    actionId: action.actionId,
    agentLabel: agentLabel(trace.run.agentId),
    agentVersion: trace.run.agentVersion,
    rationale: action.rationale?.trim() ? action.rationale : undefined,
    confidencePercent:
      action.confidence === undefined
        ? undefined
        : Math.round(action.confidence * 100),
    tools,
    records: action.dataUsed ?? [],
  }));
}
