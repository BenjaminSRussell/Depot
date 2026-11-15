// Client for the vendor invoices received electronically (payables, /v1/payables/vendor-invoice-intake): a cXML or X12 810 invoice
// edi took in, waiting for an AP clerk to match it to the purchase order and its receipt lines, post the vendor invoice and record it
// here, or reject it with a reason (#35, 35-4 and 35-5).
import { z } from "zod";
import { ApiError, defaultFetch, requestUrl, type FetchLike } from "../../../shell/api/http";

export { ApiError };

const BASE = "/api/v1/payables/vendor-invoice-intake";

export const INTAKE_STATUSES = ["RECEIVED", "POSTED", "REJECTED"] as const;
export type IntakeStatus = (typeof INTAKE_STATUSES)[number];
export const INTAKE_SOURCES = ["CXML", "X12_810", "INTERCOMPANY"] as const;
export type IntakeSource = (typeof INTAKE_SOURCES)[number];

export const intakeSchema = z.object({
  id: z.string(),
  legalEntityId: z.string(),
  source: z.enum(INTAKE_SOURCES),
  sourceRef: z.string(),
  vendorId: z.string(),
  invoiceNumber: z.string(),
  invoiceDate: z.string(),
  purpose: z.string(),
  purchaseOrderId: z.string().optional(),
  purchaseOrderNumber: z.string().optional(),
  currency: z.string(),
  net: z.string(),
  gross: z.string(),
  status: z.enum(INTAKE_STATUSES),
  receivedBy: z.string(),
  receivedAt: z.string(),
  decidedAt: z.string().optional(),
  decidedBy: z.string().optional(),
  decisionNote: z.string().optional(),
  vendorInvoiceId: z.string().optional(),
  lines: z.array(z.object({ lineNo: z.string(), item: z.string().optional(), quantity: z.string(), unit: z.string().optional(), unitPrice: z.string(), amount: z.string() })),
});
export type Intake = z.infer<typeof intakeSchema>;
export type IntakeLine = Intake["lines"][number];

const pageSchema = z.object({ items: z.array(intakeSchema), nextCursor: z.string().optional() });

export interface IntakeQuery {
  vendorId?: string;
  status?: IntakeStatus;
  cursor?: string;
  limit?: number;
}

export function fetchIntakes(q: IntakeQuery = {}, fetchImpl: FetchLike = defaultFetch) {
  const params = new URLSearchParams();
  if (q.vendorId) params.set("vendorId", q.vendorId);
  if (q.status) params.set("status", q.status);
  if (q.cursor) params.set("cursor", q.cursor);
  if (q.limit) params.set("limit", String(q.limit));
  return requestUrl("GET", `${BASE}${params.size > 0 ? `?${params}` : ""}`, pageSchema, fetchImpl);
}

export function fetchIntake(id: string, fetchImpl: FetchLike = defaultFetch) {
  return requestUrl("GET", `${BASE}/${encodeURIComponent(id)}`, intakeSchema, fetchImpl);
}

/** POST {id}:resolve records the vendor invoice the clerk posted from the received invoice. */
export function resolveIntake(id: string, vendorInvoiceId: string, fetchImpl: FetchLike = defaultFetch) {
  return requestUrl("POST", `${BASE}/${encodeURIComponent(id)}:resolve`, intakeSchema, fetchImpl, {}, { vendorInvoiceId });
}

/** POST {id}:reject closes the received invoice with a reason. */
export function rejectIntake(id: string, reason: string, fetchImpl: FetchLike = defaultFetch) {
  return requestUrl("POST", `${BASE}/${encodeURIComponent(id)}:reject`, intakeSchema, fetchImpl, {}, { reason });
}

export const intakeStatusLabel: Record<IntakeStatus, string> = { RECEIVED: "Waiting for review", POSTED: "Posted as an invoice", REJECTED: "Rejected" };
export const intakeSourceLabel: Record<IntakeSource, string> = { CXML: "cXML network", X12_810: "X12 810 from the vendor", INTERCOMPANY: "Intercompany charge" };
export const intakePurposeLabel: Record<string, string> = { standard: "Invoice", creditMemo: "Credit memo", debitMemo: "Debit memo", lineLevelCreditMemo: "Line-level credit memo" };
