import type { ChatResponse } from "../../modules/copilot/api/client";

export const AUDIT_TRAIL_TOOL = "depot.platform.audit_trail.get";
export const DOCUMENT_FLOW_TOOL = "depot.platform.document_flow.get";
export const WORKFLOW_HISTORY_TOOL = "depot.workflow.history.get";

export type AssistAction = "explain" | "changed" | "next";

export const ASSIST_ACTIONS: Readonly<Record<AssistAction, readonly string[]>> = {
  explain: [AUDIT_TRAIL_TOOL, DOCUMENT_FLOW_TOOL, WORKFLOW_HISTORY_TOOL],
  changed: [AUDIT_TRAIL_TOOL],
  next: [AUDIT_TRAIL_TOOL, DOCUMENT_FLOW_TOOL, WORKFLOW_HISTORY_TOOL],
};

export interface NextLink {
  label: string;
  to: (id: string) => string;
  duty: string;
}

export interface RecordAssistEntry {
  entityLabel: string;
  nextLinks: readonly NextLink[];
}

export const RECORD_ASSIST: Readonly<Record<string, RecordAssistEntry>> = {
  "purchase-order": {
    entityLabel: "purchase order",
    nextLinks: [
      { label: "Record arrival of the goods", to: (id) => `/receiving/receipts/new?purchaseOrderId=${encodeURIComponent(id)}`, duty: "receiving.receipt.maintain" },
      { label: "Post the vendor invoice", to: () => "/ap-invoices/post", duty: "receiving.vendor-invoice.post" },
    ],
  },
  "ap-invoice": {
    entityLabel: "vendor invoice",
    nextLinks: [{ label: "Check goods received against invoices", to: () => "/ap-invoices/grir", duty: "receiving.ap.inquire" }],
  },
  "ar-payment": {
    entityLabel: "customer payment",
    nextLinks: [{ label: "Review open customer items", to: () => "/receivables/open-items", duty: "receivables.view" }],
  },
  "journal-entry": {
    entityLabel: "journal entry",
    nextLinks: [{ label: "Open journal batches", to: () => "/ledger/journal-batches", duty: "ledger.journal.read" }],
  },
};

const RECORD_ID = /^[A-Za-z0-9_-]{1,120}$/;

export function recordAssistEntry(entity: string): RecordAssistEntry | undefined {
  return Object.hasOwn(RECORD_ASSIST, entity) ? RECORD_ASSIST[entity] : undefined;
}

export function buildRecordRef(entity: string, id: unknown): string | undefined {
  return recordAssistEntry(entity) !== undefined && typeof id === "string" && RECORD_ID.test(id) ? `record:${entity}/${id}` : undefined;
}

export function missingParts(action: AssistAction, toolCalls: ChatResponse["toolCalls"]): string[] {
  return ASSIST_ACTIONS[action].filter((tool) => !toolCalls.some((call) => call.tool === tool && call.status === "OK"));
}
