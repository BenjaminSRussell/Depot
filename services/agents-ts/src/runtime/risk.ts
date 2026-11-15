import { compareMoney, scaled6, type Money } from "../domain/money.js";
import type { RiskAssessment, RiskPort, ToolPort } from "./engine.js";

/**
 * System-side risk facts for the proposals that go to an approver (BR-47.2, 9.47.12): read from the owning domains through the same
 * tool gateway the run reads with, so the invoking user's own duties govern what is read. Nothing here comes from the model: the
 * amount replaces the model's figure for the limits, the counterparty and the creator of the document decide the high-risk and
 * segregation-of-duties flags, and the payment prediction gives the chance of a late payment.
 *
 * What the source tools answer (the contract this reads, per tool):
 * - passage.ap.invoice.get (payables `GET /v1/payables/vendor-invoice-intake/{id}`, called with `id`): `total`, `amount` or `gross` as a decimal
 *   string or `{amount, currency}` (with `currency`), `vendorId`, `createdBy` or `receivedBy`, optionally `vendorBlocked` (true) or
 *   `vendorRisk` ("HIGH").
 * - passage.ap.vendor.history.list (payables `GET /v1/payables/open-items`, called with `vendor` and `limit`): `items`, the vendor's earlier
 *   invoices; a vendor with none is unproven and counts as high risk.
 * - passage.ar.payment.get (receivables `GET /v1/receivables/payments/{id}`, called with `id`): `amount`, `currency`, `customerId`, optionally
 *   `enteredBy`.
 * - passage.ar.invoice.prediction.get: `pLate30d`, a decimal string between 0 and 1.
 * A fact that cannot be read (a failed call, a missing or malformed field) is listed in `unverified` and makes the proposal
 * high-risk, so an unreadable source can never make a proposal look safe.
 */
export interface FactRiskDeps {
  tools: Pick<ToolPort, "call">;
  invokerUserId: string;
  /** What the invoking user may approve alone; the amount's share of it is the limit utilisation. */
  selfApprovalLimit: Money;
}

const MAX_PREDICTIONS = 20;

type Doc = Record<string, unknown>;

function asDoc(output: unknown): Doc | undefined {
  return typeof output === "object" && output !== null && !Array.isArray(output) ? (output as Doc) : undefined;
}

function text(v: unknown): string | undefined {
  return typeof v === "string" && v.trim() !== "" ? v.trim() : undefined;
}

/** The amount of a document: `{amount, currency}` or a decimal string with the document's currency; undefined when it is neither. */
export function moneyOf(value: unknown, currency: unknown): Money | undefined {
  const candidate = asDoc(value);
  const amount = candidate ? text(candidate.amount) : text(value);
  const cur = candidate ? text(candidate.currency) : text(currency);
  if (amount === undefined || cur === undefined || !/^[A-Z]{3}$/.test(cur)) return undefined;
  try {
    scaled6(amount);
  } catch {
    return undefined;
  }
  return { amount, currency: cur };
}

/** The share of the limit an amount uses, 0 to 1; 1 for an amount at or above the limit or in another currency than the limit. */
export function limitUtilisation(amount: Money, limit: Money): number {
  if (amount.currency !== limit.currency) return 1;
  const a = scaled6(amount.amount);
  const l = scaled6(limit.amount);
  if (a <= 0n) return 0;
  if (l <= 0n || compareMoney(amount, limit) >= 0) return 1;
  return Number((a * 10_000n) / l) / 10_000;
}

export function createFactRisk(deps: FactRiskDeps): RiskPort {
  const read = async (name: string, args: Record<string, unknown>): Promise<{ doc?: Doc; reason?: string }> => {
    try {
      const res = await deps.tools.call(name, args);
      if (!res.ok) return { reason: `${name}: ${res.code}` };
      const doc = asDoc(res.output);
      return doc ? { doc } : { reason: `${name}: the answer is not an object` };
    } catch (err) {
      return { reason: `${name}: ${err instanceof Error ? err.message : "failed"}` };
    }
  };
  const unverified = (reasons: string[]): RiskAssessment => ({ vendorHighRisk: true, sodConflict: false, unverified: reasons });

  const apCoding = async (args: Record<string, unknown>): Promise<RiskAssessment> => {
    const invoiceId = text(args.invoiceId);
    if (invoiceId === undefined) return unverified(["the proposal names no invoice"]);
    const inv = await read("passage.ap.invoice.get", { id: invoiceId });
    if (!inv.doc) return unverified([inv.reason ?? "the invoice could not be read"]);
    const reasons: string[] = [];
    const amount = moneyOf(inv.doc.total ?? inv.doc.amount ?? inv.doc.gross, inv.doc.currency);
    if (amount === undefined) reasons.push("the invoice has no readable amount");
    const vendorId = text(inv.doc.vendorId);
    let vendorHighRisk = inv.doc.vendorBlocked === true || inv.doc.vendorRisk === "HIGH";
    if (vendorId === undefined) {
      reasons.push("the invoice has no vendor");
    } else {
      const history = await read("passage.ap.vendor.history.list", { vendor: vendorId, limit: 1 });
      if (!history.doc) reasons.push(history.reason ?? "the vendor history could not be read");
      else if (!Array.isArray(history.doc.items) || history.doc.items.length === 0) vendorHighRisk = true;
    }
    const createdBy = text(inv.doc.createdBy) ?? text(inv.doc.receivedBy);
    if (createdBy === undefined) reasons.push("the invoice does not say who created it");
    const out: RiskAssessment = { vendorHighRisk: vendorHighRisk || reasons.length > 0, sodConflict: createdBy !== undefined && createdBy === deps.invokerUserId };
    if (reasons.length > 0) out.unverified = reasons;
    if (amount !== undefined) {
      out.amount = amount;
      out.limitUtilisation = limitUtilisation(amount, deps.selfApprovalLimit);
    }
    return out;
  };

  const cashApplication = async (args: Record<string, unknown>): Promise<RiskAssessment> => {
    const paymentId = text(args.paymentId);
    if (paymentId === undefined) return unverified(["the proposal names no payment"]);
    const pay = await read("passage.ar.payment.get", { id: paymentId });
    if (!pay.doc) return unverified([pay.reason ?? "the payment could not be read"]);
    const reasons: string[] = [];
    const amount = moneyOf(pay.doc.amount, pay.doc.currency);
    if (amount === undefined) reasons.push("the payment has no readable amount");
    const out: RiskAssessment = { vendorHighRisk: reasons.length > 0, sodConflict: text(pay.doc.enteredBy) === deps.invokerUserId && deps.invokerUserId !== "" };
    if (reasons.length > 0) out.unverified = reasons;
    if (amount !== undefined) {
      out.amount = amount;
      out.limitUtilisation = limitUtilisation(amount, deps.selfApprovalLimit);
    }
    // The payment prediction of the invoices the proposal settles. An invoice without a prediction (no model yet, never scored) adds
    // nothing; it is not an unreadable fact, because the score is advice and not a source document.
    const invoices = Array.isArray(args.allocations)
      ? args.allocations
          .map((a) => text(asDoc(a)?.invoiceId))
          .filter((id): id is string => id !== undefined)
          .slice(0, MAX_PREDICTIONS)
      : [];
    let worst: number | undefined;
    for (const invoiceId of invoices) {
      const res = await read("passage.ar.invoice.prediction.get", { invoice_id: invoiceId });
      const p = res.doc ? Number(text(res.doc.pLate30d)) : Number.NaN;
      if (Number.isFinite(p) && p >= 0 && p <= 1) worst = Math.max(worst ?? 0, p);
    }
    if (worst !== undefined) out.paymentLateProbability = worst;
    return out;
  };

  return {
    async assess(tool, args) {
      switch (tool) {
        case "passage.ap.invoice.propose_coding":
          return apCoding(args);
        case "passage.ar.settlement.propose":
          return cashApplication(args);
        default:
          // No source document is known for this proposal: it stays as neutral as before, and v1 sends every proposal to a person.
          return { vendorHighRisk: false, sodConflict: false };
      }
    },
  };
}
