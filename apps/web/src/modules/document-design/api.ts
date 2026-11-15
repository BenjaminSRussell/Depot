// Client for the print service's docgen routes (services/print/api/openapi.yaml, issue #46):
// document templates, standard text blocks and output rules. The BFF rewrites /api/v1/print/...
// to /print/v1/... on the service.
import { apiFetch } from "../../shell/api/http";

const base = "/api/v1/print";

export const DOCUMENT_TYPE_LABELS: Record<string, string> = {
  PURCHASE_ORDER: "Purchase order",
  CUSTOMER_INVOICE: "Customer invoice",
  PACKING_SLIP: "Packing slip",
  REMITTANCE_ADVICE: "Remittance advice",
  PERIODIC_STATEMENT: "Statement of account",
  PAYMENT_NOTICE: "Payment notice",
  BALANCE_CONFIRMATION: "Balance confirmation",
};

export const CHANNEL_LABELS: Record<string, string> = {
  PRINTER: "Printer queue",
  EMAIL: "E-mail",
  EINVOICE: "E-invoice network",
  ARCHIVE: "Archive",
  PORTAL: "Portal",
};

export interface TemplateConfig {
  title?: string;
  footerText?: string;
  renameColumns?: Record<string, string>;
  hideColumns?: string[];
  bodyBlock?: string;
  noteBlocks?: string[];
}

export interface DocumentTemplate {
  code: string;
  name: string;
  documentType: string;
  default: boolean;
  version: number;
  config: TemplateConfig;
}

export interface TextBlock {
  code: string;
  language: string;
  body: string;
  placeholders: string[];
  version: number;
}

export interface OutputRule {
  id: string;
  documentType: string;
  legalEntityId?: string;
  accountId?: string;
  copyKind: "ORIGINAL" | "COPY";
  channel: string;
  destination?: string;
  copies: number;
  sequence: number;
}

export interface Problem {
  code: string;
  message: string;
  /** Field-level messages of a 400, keyed by the request field. */
  fields: Record<string, string>;
}

export class DocgenApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
    readonly fields: Record<string, string> = {},
  ) {
    super(message);
    this.name = "DocgenApiError";
  }
}

export function describeProblem(error: unknown): Problem {
  if (error instanceof DocgenApiError) return { code: error.code, message: error.message, fields: error.fields };
  return { code: "COM-0001", message: error instanceof Error ? error.message : "Request failed", fields: {} };
}

async function failure(res: Response): Promise<DocgenApiError> {
  const body = (await res.json().catch(() => null)) as { code?: unknown; detail?: unknown; title?: unknown; errors?: unknown } | null;
  const fields: Record<string, string> = {};
  if (body?.errors && typeof body.errors === "object") {
    for (const [k, v] of Object.entries(body.errors as Record<string, unknown>)) if (typeof v === "string") fields[k] = v;
  }
  return new DocgenApiError(
    res.status,
    typeof body?.code === "string" && body.code !== "" ? body.code : `HTTP-${res.status}`,
    typeof body?.detail === "string" ? body.detail : typeof body?.title === "string" ? body.title : `Request failed (HTTP ${res.status})`,
    fields,
  );
}

async function items<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const res = await apiFetch(`${base}${path}`, { headers: { Accept: "application/json" }, credentials: "same-origin", signal });
  if (!res.ok) throw await failure(res);
  const body = (await res.json()) as { items?: T[] };
  if (!Array.isArray(body.items)) throw new DocgenApiError(0, "COM-0001", "The response has no items");
  return body.items;
}

/** A value with the entity tag the service sent with it, for If-Match on the next save. */
export interface Tagged<T> {
  value: T;
  etag: string;
}

async function send<T>(method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<Tagged<T>> {
  const res = await apiFetch(`${base}${path}`, {
    method,
    headers: { Accept: "application/json", ...(body === undefined ? {} : { "Content-Type": "application/json" }), ...headers },
    credentials: "same-origin",
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  if (!res.ok) throw await failure(res);
  const etag = res.headers.get("ETag") ?? "";
  if (res.status === 204) return { value: undefined as T, etag };
  return { value: (await res.json()) as T, etag };
}

export const etagOf = (version: number) => `"${version}"`;
export const newKey = () => crypto.randomUUID();

export const listTemplates = (signal?: AbortSignal) => items<DocumentTemplate>("/document-templates", signal);
export const listTextBlocks = (signal?: AbortSignal) => items<TextBlock>("/text-blocks", signal);
export const listOutputRules = (signal?: AbortSignal) => items<OutputRule>("/output-rules", signal);

export interface TemplateVersion extends DocumentTemplate {
  savedBy?: string;
  savedAt: string;
}

export interface TemplateInput {
  name: string;
  documentType: string;
  default: boolean;
  config: TemplateConfig;
}

export const getTemplate = (code: string) => send<DocumentTemplate>("GET", `/document-templates/${encodeURIComponent(code)}`);
/** Saves a template. `etag` is the version being edited ("3"); omit it to create a new template. */
export const putTemplate = (code: string, input: TemplateInput, etag?: string) =>
  send<DocumentTemplate>("PUT", `/document-templates/${encodeURIComponent(code)}`, input, etag ? { "If-Match": etag } : {});
export const listTemplateVersions = (code: string, signal?: AbortSignal) => items<TemplateVersion>(`/document-templates/${encodeURIComponent(code)}/versions`, signal);
/** Makes an earlier version the current one as a new version. `etag` is the current version the page showed. */
export const restoreTemplateVersion = (code: string, version: number, etag: string) =>
  send<DocumentTemplate>("POST", `/document-templates/${encodeURIComponent(code)}/versions/${version}:restore`, undefined, { "If-Match": etag });

export const putTextBlock = (code: string, language: string, body: string) =>
  send<TextBlock>("PUT", `/text-blocks/${encodeURIComponent(code)}/${encodeURIComponent(language)}`, { body });

export interface OutputRuleInput {
  documentType: string;
  legalEntityId?: string;
  accountId?: string;
  copyKind: "ORIGINAL" | "COPY";
  channel: string;
  destination?: string;
  copies: number;
  sequence: number;
}
export const createOutputRule = (input: OutputRuleInput, key: string) => send<OutputRule>("POST", "/output-rules", input, { "Idempotency-Key": key });
export const deleteOutputRule = (id: string) => send<undefined>("DELETE", `/output-rules/${encodeURIComponent(id)}`);

export const SCHEDULE_CADENCE_LABELS: Record<string, string> = { WEEKLY: "Every week", MONTHLY: "Every month" };
export const WEEKDAYS = ["Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"];

export interface StatementSchedule {
  id: string;
  name: string;
  legalEntityId: string;
  senderName: string;
  cadence: "WEEKLY" | "MONTHLY";
  runDay: number;
  language: string;
  templateCode?: string;
  enabled: boolean;
  nextRunOn: string;
  lastRunOn?: string;
  /** False when the runner is not enabled for the tenant: the schedule never runs by itself. */
  runsEnabled?: boolean;
  /** The date of a run that did not finish because a customer's statement failed; it is retried. */
  runAsOf?: string;
}
export type StatementScheduleInput = Omit<StatementSchedule, "id" | "enabled" | "nextRunOn" | "lastRunOn">;
export interface StatementRunSummary {
  scheduleId: string;
  asOf: string;
  delivered: number;
  skipped: number;
  failed: number;
}

export const listStatementSchedules = (signal?: AbortSignal) => items<StatementSchedule>("/statement-schedules", signal);
export const createStatementSchedule = (input: StatementScheduleInput, key: string) => send<StatementSchedule>("POST", "/statement-schedules", input, { "Idempotency-Key": key });
export const setStatementScheduleEnabled = (id: string, enabled: boolean) => send<StatementSchedule>("PATCH", `/statement-schedules/${encodeURIComponent(id)}`, { enabled });
export const deleteStatementSchedule = (id: string) => send<undefined>("DELETE", `/statement-schedules/${encodeURIComponent(id)}`);
export const runStatementSchedule = (id: string) => send<StatementRunSummary>("POST", `/statement-schedules/${encodeURIComponent(id)}:run`);
