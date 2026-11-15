import { describe, expect, it } from "vitest";
import { riskScoreOf, type ToolPort, type ToolResult } from "../src/runtime/engine.js";
import { createFactRisk, limitUtilisation, moneyOf } from "../src/runtime/risk.js";
import { INVOKER, usd } from "./fakes.js";

/** A tool port that answers each named tool from a table and records what was asked. */
function tools(answers: Record<string, ToolResult | ((args: Record<string, unknown>) => ToolResult)>): ToolPort & { asked: Array<{ name: string; args: Record<string, unknown> }> } {
  const asked: Array<{ name: string; args: Record<string, unknown> }> = [];
  return {
    asked,
    describe: () => undefined,
    async call(name, args) {
      asked.push({ name, args });
      const a = answers[name];
      if (a === undefined) return { ok: false, code: "AGT-0404", message: "no such tool" };
      return typeof a === "function" ? a(args) : a;
    },
  };
}
const ok = (output: unknown): ToolResult => ({ ok: true, output });
const LIMIT = usd("10000");
const risk = (t: ToolPort, invoker = INVOKER) => createFactRisk({ tools: t, invokerUserId: invoker, selfApprovalLimit: LIMIT });

describe("fact based risk for AP coding proposals", () => {
  const invoice = { total: "2500.00", currency: "USD", vendorId: "v-1", createdBy: "someone-else" };

  it("reads the amount, the vendor and the creator from the system and never from the proposal", async () => {
    const t = tools({ "passage.ap.invoice.get": ok(invoice), "passage.ap.vendor.history.list": ok({ items: [{ id: "i-0" }] }) });
    const got = await risk(t).assess("passage.ap.invoice.propose_coding", { invoiceId: "inv-1", amount: "1.00" });
    expect(got).toEqual({ vendorHighRisk: false, sodConflict: false, amount: usd("2500.00"), limitUtilisation: 0.25 });
    expect(t.asked.map((a) => a.name)).toEqual(["passage.ap.invoice.get", "passage.ap.vendor.history.list"]);
    expect(t.asked[0]?.args).toEqual({ id: "inv-1" });
    expect(t.asked[1]?.args).toEqual({ vendor: "v-1", limit: 1 });
  });

  it("reads a received vendor invoice the way payables answers it: gross for the amount, receivedBy for the creator", async () => {
    const received = { gross: "2500.00", currency: "USD", vendorId: "v-1", receivedBy: INVOKER };
    const t = tools({ "passage.ap.invoice.get": ok(received), "passage.ap.vendor.history.list": ok({ items: [{ id: "i-0" }] }) });
    const got = await risk(t).assess("passage.ap.invoice.propose_coding", { invoiceId: "inv-1" });
    expect(got).toEqual({ vendorHighRisk: false, sodConflict: true, amount: usd("2500.00"), limitUtilisation: 0.25 });
  });

  it("flags a vendor with no history, a blocked vendor and a high vendor risk", async () => {
    const none = await risk(tools({ "passage.ap.invoice.get": ok(invoice), "passage.ap.vendor.history.list": ok({ items: [] }) })).assess("passage.ap.invoice.propose_coding", { invoiceId: "inv-1" });
    expect(none.vendorHighRisk).toBe(true);
    for (const flagged of [{ vendorBlocked: true }, { vendorRisk: "HIGH" }]) {
      const got = await risk(tools({ "passage.ap.invoice.get": ok({ ...invoice, ...flagged }), "passage.ap.vendor.history.list": ok({ items: [{}] }) })).assess("passage.ap.invoice.propose_coding", { invoiceId: "inv-1" });
      expect(got.vendorHighRisk).toBe(true);
    }
  });

  it("is a segregation-of-duties conflict when the invoking user created the invoice", async () => {
    const t = tools({ "passage.ap.invoice.get": ok({ ...invoice, createdBy: INVOKER }), "passage.ap.vendor.history.list": ok({ items: [{}] }) });
    expect((await risk(t).assess("passage.ap.invoice.propose_coding", { invoiceId: "inv-1" })).sodConflict).toBe(true);
  });

  it("fails closed when a fact cannot be read", async () => {
    const unreadable = await risk(tools({})).assess("passage.ap.invoice.propose_coding", { invoiceId: "inv-1" });
    expect(unreadable.vendorHighRisk).toBe(true);
    expect(unreadable.unverified?.[0]).toContain("passage.ap.invoice.get");
    expect(unreadable.amount).toBeUndefined();
    const noHistory = await risk(tools({ "passage.ap.invoice.get": ok(invoice) })).assess("passage.ap.invoice.propose_coding", { invoiceId: "inv-1" });
    expect(noHistory.vendorHighRisk).toBe(true);
    expect(noHistory.unverified).toHaveLength(1);
    const noAmount = await risk(tools({ "passage.ap.invoice.get": ok({ ...invoice, total: "lots" }), "passage.ap.vendor.history.list": ok({ items: [{}] }) })).assess("passage.ap.invoice.propose_coding", { invoiceId: "inv-1" });
    expect(noAmount.unverified).toEqual(["the invoice has no readable amount"]);
    expect((await risk(tools({})).assess("passage.ap.invoice.propose_coding", {})).unverified).toEqual(["the proposal names no invoice"]);
    const thrown: ToolPort = { describe: () => undefined, call: async () => Promise.reject(new Error("gateway down")) };
    expect((await risk(thrown).assess("passage.ap.invoice.propose_coding", { invoiceId: "inv-1" })).unverified?.[0]).toContain("gateway down");
  });
});

describe("fact based risk for cash application proposals", () => {
  const payment = { amount: "800.00", currency: "USD", customerId: "c-1", enteredBy: "clerk" };
  const prediction = (p: Record<string, string>) => (args: Record<string, unknown>): ToolResult => (p[String(args.invoice_id)] === undefined ? { ok: false, code: "AGT-0404", message: "none" } : ok({ pLate30d: p[String(args.invoice_id)] }));

  it("takes the highest late-payment chance of the invoices it settles and ignores an invoice with no prediction", async () => {
    const t = tools({ "passage.ar.payment.get": ok(payment), "passage.ar.invoice.prediction.get": prediction({ a: "0.1200", b: "0.6100" }) });
    const got = await risk(t).assess("passage.ar.settlement.propose", { paymentId: "p-1", allocations: [{ invoiceId: "a" }, { invoiceId: "b" }, { invoiceId: "c" }] });
    expect(got).toEqual({ vendorHighRisk: false, sodConflict: false, amount: usd("800.00"), limitUtilisation: 0.08, paymentLateProbability: 0.61 });
    expect(t.asked.filter((a) => a.name === "passage.ar.invoice.prediction.get")).toHaveLength(3);
  });

  it("asks for at most twenty predictions and flags the clerk who entered the payment as the conflict", async () => {
    const t = tools({ "passage.ar.payment.get": ok({ ...payment, enteredBy: INVOKER }), "passage.ar.invoice.prediction.get": ok({ pLate30d: "0.2000" }) });
    const allocations = Array.from({ length: 30 }, (_, i) => ({ invoiceId: `i-${i}` }));
    const got = await risk(t).assess("passage.ar.settlement.propose", { paymentId: "p-1", allocations });
    expect(got.sodConflict).toBe(true);
    expect(t.asked.filter((a) => a.name === "passage.ar.invoice.prediction.get")).toHaveLength(20);
  });

  it("fails closed without the payment and stays neutral for a proposal it has no source for", async () => {
    expect((await risk(tools({})).assess("passage.ar.settlement.propose", { paymentId: "p-1" })).vendorHighRisk).toBe(true);
    expect(await risk(tools({})).assess("passage.mrp.action_message.propose_triage", {})).toEqual({ vendorHighRisk: false, sodConflict: false });
  });
});

describe("amounts, limits and the score the approver sees", () => {
  it("reads money from a string with a currency or from an object, and refuses the rest", () => {
    expect(moneyOf("12.5", "EUR")).toEqual({ amount: "12.5", currency: "EUR" });
    expect(moneyOf({ amount: "3", currency: "USD" }, undefined)).toEqual({ amount: "3", currency: "USD" });
    for (const bad of [[undefined, "USD"], ["x", "USD"], ["1", "usd"], ["1", undefined], [{ amount: "1" }, "USD"]] as const) expect(moneyOf(bad[0], bad[1])).toBeUndefined();
  });

  it("uses a share of the limit, all of it at or above the limit or in another currency", () => {
    expect(limitUtilisation(usd("2500"), LIMIT)).toBe(0.25);
    expect(limitUtilisation(usd("10000"), LIMIT)).toBe(1);
    expect(limitUtilisation(usd("0"), LIMIT)).toBe(0);
    expect(limitUtilisation({ amount: "1", currency: "EUR" }, LIMIT)).toBe(1);
  });

  it("scores 1 for a high-risk, conflicting or unverified proposal and otherwise the largest of the doubts", () => {
    const base = { vendorHighRisk: false, sodConflict: false };
    expect(riskScoreOf({ ...base, vendorHighRisk: true }, 0.99)).toBe(1);
    expect(riskScoreOf({ ...base, sodConflict: true }, 0.99)).toBe(1);
    expect(riskScoreOf({ ...base, unverified: ["x"] }, 0.99)).toBe(1);
    expect(riskScoreOf(base, 0.9)).toBeCloseTo(0.1);
    expect(riskScoreOf({ ...base, limitUtilisation: 0.5 }, 0.9)).toBe(0.5);
    expect(riskScoreOf({ ...base, paymentLateProbability: 0.7, limitUtilisation: 0.5 }, 0.9)).toBe(0.7);
  });
});
