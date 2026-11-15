// Print service docgen routes (/api/v1/print): document templates, text blocks and output rules.
// Fictional Cedar Ridge Pumps data; saves, restores, rules and schedules change the in-memory data until the page reloads.
import type { DocumentTemplate, OutputRule, StatementSchedule, TemplateVersion, TextBlock } from "../../modules/document-design/api";
import { CUSTOMERS, LEGAL_ENTITY, USER } from "../company";
import { reply, route } from "../backend";
import { at, day, newId, uid } from "../store";

const BASE = "/api/v1/print";
const K_RULE = 390;
const K_SCHEDULE = 391;

const templates: DocumentTemplate[] = [
  { code: "INV-STANDARD", name: "Standard invoice", documentType: "CUSTOMER_INVOICE", default: true, version: 3,
    config: { title: "Tax invoice", footerText: "Cedar Ridge Pumps, Inc. - Registered in Ohio", hideColumns: ["Unit"], noteBlocks: ["PAYMENT-TERMS"] } },
  { code: "PO-STANDARD", name: "Standard purchase order", documentType: "PURCHASE_ORDER", default: true, version: 2,
    config: { renameColumns: { Qty: "Quantity" }, noteBlocks: ["PO-CONDITIONS"] } },
  { code: "STMT-MONTHLY", name: "Monthly statement of account", documentType: "PERIODIC_STATEMENT", default: true, version: 1,
    config: { bodyBlock: "STATEMENT-COVER" } },
  { code: "BALCONF-YEAREND", name: "Year-end balance confirmation", documentType: "BALANCE_CONFIRMATION", default: false, version: 1,
    config: { bodyBlock: "BALANCE-CONFIRMATION", footerText: "Please sign and return one copy." } },
];

const blocks: TextBlock[] = [
  { code: "PAYMENT-TERMS", language: "en", body: "Payment is due within 30 days of the invoice date.", placeholders: [], version: 2 },
  { code: "PAYMENT-TERMS", language: "de", body: "Zahlbar innerhalb von 30 Tagen nach Rechnungsdatum.", placeholders: [], version: 1 },
  { code: "PO-CONDITIONS", language: "en", body: "Goods are accepted subject to our general conditions of purchase.", placeholders: [], version: 1 },
  { code: "STATEMENT-COVER", language: "en", body: "Dear {{counterpartyName}}, this statement lists the open items on your account up to the statement date shown above.", placeholders: ["counterpartyName"], version: 1 },
  { code: "BALANCE-CONFIRMATION", language: "en", body: "As of {{balanceDate}} our records show a balance of {{amount}} on your account. If this differs from yours, tell us within 14 days.", placeholders: ["amount", "balanceDate"], version: 1 },
];

const customer = CUSTOMERS[0]!;
const rules: OutputRule[] = [
  { id: uid(K_RULE, 1), documentType: "CUSTOMER_INVOICE", copyKind: "ORIGINAL", channel: "PRINTER", destination: "OFFICE", copies: 1, sequence: 1 },
  { id: uid(K_RULE, 2), documentType: "CUSTOMER_INVOICE", copyKind: "COPY", channel: "ARCHIVE", copies: 1, sequence: 1 },
  { id: uid(K_RULE, 3), documentType: "CUSTOMER_INVOICE", accountId: customer.id, copyKind: "ORIGINAL", channel: "EMAIL", destination: "payables@bluewater.example", copies: 1, sequence: 1 },
  { id: uid(K_RULE, 4), documentType: "PACKING_SLIP", legalEntityId: LEGAL_ENTITY.id, copyKind: "ORIGINAL", channel: "PRINTER", destination: "DOCK", copies: 2, sequence: 1 },
];

// Every saved version of every template, oldest first; the first two templates have an earlier version to restore.
const history: TemplateVersion[] = templates.flatMap((t) => {
  const earlier: TemplateVersion[] = [];
  for (let v = 1; v < t.version; v += 1) {
    // The earlier versions had no title and no footer line yet.
    const { title: _title, footerText: _footer, ...rest } = t.config;
    earlier.push({ ...t, version: v, config: rest, savedBy: USER.id, savedAt: at(-30 + v) });
  }
  return [...earlier, { ...t, savedBy: USER.id, savedAt: at(-2) }];
});

const TEMPLATE_CODE = /^[A-Z][A-Z0-9_-]{1,39}$/;
const etagVersion = (header: string | null): number | null => {
  if (header === null || header.trim() === "") return 0;
  const m = /^"(\d+)"$/.exec(header.trim());
  return m ? Number(m[1]) : null;
};

function saveTemplate(code: string, input: Pick<DocumentTemplate, "name" | "documentType" | "default" | "config">, ifMatch: string | null) {
  const existing = templates.find((t) => t.code === code);
  const match = etagVersion(ifMatch);
  if (match === null) return reply.problem(400, "COM-0001", "request failed validation", { "If-Match": 'must be the template version as an entity tag, for example "3"' });
  if (existing && match === 0) return reply.problem(428, "PRN-0018", `the template exists (version ${existing.version}): send the version you edited as If-Match`);
  if (existing && match !== existing.version) return reply.problem(412, "PRN-0017", `the template is at version ${existing.version}, not ${match}: someone saved it after you opened it`);
  if (input.default) for (const t of templates) if (t.documentType === input.documentType && t.code !== code) t.default = false;
  const next: DocumentTemplate = { code, name: input.name, documentType: existing?.documentType ?? input.documentType, default: input.default, version: (existing?.version ?? 0) + 1, config: input.config };
  if (existing) Object.assign(existing, next);
  else templates.push(next);
  history.push({ ...next, savedBy: USER.id, savedAt: at(0) });
  return reply.versioned(next, next.version);
}

route("GET", `${BASE}/document-templates`, () => ({ items: templates }));
route("GET", `${BASE}/document-templates/{code}`, ({ params }) => {
  const t = templates.find((x) => x.code === params.code);
  return t ? reply.versioned(t, t.version) : reply.problem(404, "PRN-0012", "document template not found");
});
route("PUT", `${BASE}/document-templates/{code}`, ({ params, body, headers }) => {
  const input = body as Pick<DocumentTemplate, "name" | "documentType" | "default" | "config">;
  const errors: Record<string, string> = {};
  if (!TEMPLATE_CODE.test(params.code ?? "")) errors.code = "must be 2 to 40 characters: capital letters, digits, - and _, starting with a letter";
  if (!input?.name?.trim()) errors.name = "is required and at most 200 characters";
  if (Object.keys(errors).length) return reply.problem(400, "COM-0001", "request failed validation", errors);
  return saveTemplate(params.code ?? "", input, headers.get("If-Match"));
});
route("GET", `${BASE}/document-templates/{code}/versions`, ({ params }) => {
  const rows = history.filter((h) => h.code === params.code).sort((a, b) => b.version - a.version);
  return rows.length ? { items: rows } : reply.problem(404, "PRN-0012", "document template not found");
});
route("POST", `${BASE}/document-templates/{code}/versions/{version}:restore`, ({ params, headers }) => {
  const current = templates.find((t) => t.code === params.code);
  const from = history.find((h) => h.code === params.code && h.version === Number(params.version));
  if (!current || !from) return reply.problem(404, "PRN-0012", "that version of the document template does not exist");
  return saveTemplate(current.code, { name: from.name, documentType: from.documentType, default: current.default, config: from.config }, headers.get("If-Match"));
});

route("GET", `${BASE}/text-blocks`, () => ({ items: blocks }));
route("PUT", `${BASE}/text-blocks/{code}/{language}`, ({ params, body }) => {
  const text = (body as { body?: string } | undefined)?.body ?? "";
  if (text.trim() === "") return reply.problem(400, "COM-0001", "request failed validation", { body: "is required and at most 4000 characters" });
  const placeholders = [...new Set([...text.matchAll(/\{\{\s*([A-Za-z][A-Za-z0-9_.]*)\s*\}\}/g)].map((m) => m[1]!))].sort();
  const existing = blocks.find((b) => b.code === params.code && b.language === params.language);
  if (existing) Object.assign(existing, { body: text, placeholders, version: existing.version + 1 });
  else blocks.push({ code: params.code ?? "", language: params.language ?? "", body: text, placeholders, version: 1 });
  return blocks.find((b) => b.code === params.code && b.language === params.language)!;
});

route("GET", `${BASE}/output-rules`, () => ({ items: rules }));
route("POST", `${BASE}/output-rules`, ({ body }) => {
  const input = body as Omit<OutputRule, "id">;
  const rule: OutputRule = { ...input, id: newId() };
  rules.push(rule);
  return reply.created(rule);
});
route("DELETE", `${BASE}/output-rules/{id}`, ({ params }) => {
  const i = rules.findIndex((r) => r.id === params.id);
  if (i < 0) return reply.problem(404, "PRN-0015", "output rule not found");
  rules.splice(i, 1);
  return reply.noContent();
});

const schedules: StatementSchedule[] = [
  { id: uid(K_SCHEDULE, 1), name: "Monthly customer statements", legalEntityId: LEGAL_ENTITY.id, senderName: LEGAL_ENTITY.name, cadence: "MONTHLY", runDay: 3, language: "en", templateCode: "STMT-MONTHLY", enabled: true, nextRunOn: day(18), lastRunOn: day(-12) },
  { id: uid(K_SCHEDULE, 2), name: "Weekly statements for past-due accounts", legalEntityId: LEGAL_ENTITY.id, senderName: LEGAL_ENTITY.name, cadence: "WEEKLY", runDay: 1, language: "en", enabled: false, nextRunOn: day(4) },
];
route("GET", `${BASE}/statement-schedules`, () => ({ items: schedules }));
route("POST", `${BASE}/statement-schedules`, ({ body }) => {
  const input = body as Omit<StatementSchedule, "id" | "enabled" | "nextRunOn">;
  const sc: StatementSchedule = { ...input, id: newId(), enabled: true, nextRunOn: day(7) };
  schedules.push(sc);
  return reply.created(sc);
});
route("PATCH", `${BASE}/statement-schedules/{id}`, ({ params, body }) => {
  const sc = schedules.find((s) => s.id === params.id);
  if (!sc) return reply.problem(404, "PRN-0019", "statement schedule not found");
  sc.enabled = Boolean((body as { enabled?: boolean }).enabled);
  return sc;
});
route("DELETE", `${BASE}/statement-schedules/{id}`, ({ params }) => {
  const i = schedules.findIndex((s) => s.id === params.id);
  if (i < 0) return reply.problem(404, "PRN-0019", "statement schedule not found");
  schedules.splice(i, 1);
  return reply.noContent();
});
route("POST", `${BASE}/statement-schedules/{id}:run`, ({ params }) => {
  const sc = schedules.find((s) => s.id === params.id);
  if (!sc) return reply.problem(404, "PRN-0019", "statement schedule not found");
  sc.lastRunOn = day(0);
  return { scheduleId: sc.id, asOf: day(0), delivered: 14, skipped: 3, failed: 0 };
});
