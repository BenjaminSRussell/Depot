// In-browser stand-in for the portal's BFF, used only by the demo build (`vite build --mode demo`). It answers /bff and /api with
// fictional data: Harlan Agricultural Co-op, a customer of Cedar Ridge Pumps, signed in as its accounts payable contact. Card and
// bank account payments are simulated without Stripe.js: a card payment is paid at once, a bank account payment stays processing.
// Requests nothing answers are listed in window.__passageDemoMisses for the demo check.
import { fromMinor, toMinor } from "./money";

interface Item {
  id: string; documentNumber: string; documentDate: string; dueDate: string; currency: string; amount: string; openAmount: string;
  side: "DEBIT" | "CREDIT"; status: string; sourceType: string;
}
interface Intent {
  id: string; provider: string; method: "CARD" | "ACH"; status: string; amount: string; currency: string; cardBrand?: string; last4?: string;
  bankName?: string; createdAt: string; allocations: Array<{ documentNumber: string; amount: string; openItemId: string }>;
}

declare global {
  interface Window {
    __passageDemoMisses?: string[];
  }
}

const LE = "00000000-0000-0000-0000-0000000000e1";
const day = (offset: number): string => new Date(Date.now() + offset * 86_400_000).toISOString().slice(0, 10);
const id = (kind: number, n: number): string => `${kind.toString(16).padStart(8, "0")}-0000-4000-8000-${n.toString(16).padStart(12, "0")}`;
const cents = (s: string): bigint => toMinor(s, "USD") ?? 0n;
const amount = (c: bigint): string => fromMinor(c, "USD");

const invoices = [
  { n: 1, number: "INV-2027-0412", date: day(-20), due: day(25), gross: "4860.00", open: "4860.00", lines: [["Centrifugal pump CR-200, 4 in.", "2", "2100.00", "4200.00", "0.00"], ["Pump seal kit", "6", "110.00", "660.00", "0.00"]] },
  { n: 2, number: "INV-2027-0388", date: day(-41), due: day(-1), gross: "1925.50", open: "925.50", lines: [["Submersible pump SP-75", "1", "1925.50", "1925.50", "0.00"]] },
  { n: 3, number: "INV-2027-0351", date: day(-70), due: day(-25), gross: "3120.00", open: "0.00", lines: [["Irrigation booster pump IB-40", "2", "1560.00", "3120.00", "0.00"]] },
];
const items: Item[] = [
  ...invoices.filter((i) => cents(i.open) > 0n).map((i) => ({
    id: id(0x51, i.n), documentNumber: i.number, documentDate: i.date, dueDate: i.due, currency: "USD", amount: i.gross, openAmount: i.open, side: "DEBIT" as const,
    status: i.open === i.gross ? "OPEN" : "PARTIALLY_SETTLED", sourceType: "INVOICE",
  })),
  { id: id(0x51, 9), documentNumber: "CN-2027-0040", documentDate: day(-12), dueDate: day(-12), currency: "USD", amount: "-150.00", openAmount: "-150.00", side: "CREDIT", status: "OPEN", sourceType: "CREDIT_NOTE" },
];
const payments = [
  { id: id(0x52, 1), paymentMode: "CHECK", amount: "3120.00", unappliedAmount: "0.00", currency: "USD", receivedDate: day(-24), valueDate: day(-24), status: "APPLIED" },
  { id: id(0x52, 2), paymentMode: "CARD", amount: "1000.00", unappliedAmount: "0.00", currency: "USD", receivedDate: day(-9), valueDate: day(-9), status: "APPLIED" },
];
const intents: Intent[] = [
  { id: id(0x53, 1), provider: "STRIPE", method: "CARD", status: "SUCCEEDED", amount: "1000.00", currency: "USD", cardBrand: "visa", last4: "4242", createdAt: `${day(-9)}T15:04:00Z`, allocations: [{ documentNumber: "INV-2027-0388", amount: "1000.00", openItemId: id(0x51, 2) }] },
];
let seq = 1;

type Answer = { status: number; body: unknown };
const ok = (body: unknown, status = 200): Answer => ({ status, body });
const problem = (status: number, code: string, detail: string): Answer => ({ status, body: { type: `https://passage.dev/problems/${code}`, status, code, detail } });

const portalView = (p: Intent) => ({ ...p, allocations: p.allocations.map(({ documentNumber, amount: a }) => ({ documentNumber, amount: a })) });

function invoiceView(i: (typeof invoices)[number]) {
  return {
    id: id(0x50, i.n), type: "SHIPMENT", status: cents(i.open) === 0n ? "SETTLED" : "POSTED", number: i.number, currency: "USD", invoiceDate: i.date, dueDate: i.due,
    paymentTermDays: 45, totals: { net: i.gross, charges: "0.00", tax: "0.00", gross: i.gross },
    lines: i.lines.map(([description, qty, unitPrice, net, tax], n) => ({ lineNo: n + 1, description, qty, unitPrice, discountPct: "0", net, tax })),
  };
}

function createIntent(body: unknown): Answer {
  const b = body as { method?: string; allocations?: Array<{ openItemId: string; amount: string }> } | undefined;
  if (!b || (b.method !== "CARD" && b.method !== "ACH") || !Array.isArray(b.allocations) || b.allocations.length === 0) return problem(400, "COM-0001", "Choose at least one invoice and a payment method.");
  let total = 0n;
  const allocations: Intent["allocations"] = [];
  for (const a of b.allocations) {
    const it = items.find((i) => i.id === a.openItemId && i.side === "DEBIT");
    if (!it) return problem(404, "AR-OPEN-ITEM-NOT-FOUND", "open item not found");
    if (cents(a.amount) > cents(it.openAmount)) return problem(422, "AR-0019", `${it.documentNumber} has ${it.openAmount} USD left to pay; pay that much or less`);
    total += cents(a.amount);
    allocations.push({ documentNumber: it.documentNumber, amount: a.amount, openItemId: it.id });
  }
  const intent: Intent = { id: id(0x53, ++seq), provider: "STRIPE", method: b.method, status: "REQUIRES_PAYMENT", amount: amount(total), currency: "USD", createdAt: new Date().toISOString(), allocations };
  intents.unshift(intent);
  return ok({ ...portalView(intent), clientSecret: `demo_${intent.id}_secret`, publishableKey: "pk_demo" }, 201);
}

/** The demo's stand-in for Stripe and its webhook: a card is paid and applied, a bank account payment starts processing. */
function confirmIntent(intentId: string): Answer {
  const intent = intents.find((p) => p.id === intentId);
  if (!intent) return problem(404, "AR-GATEWAY-NOT-FOUND", "Payment not found");
  if (intent.status !== "REQUIRES_PAYMENT") return ok(portalView(intent));
  if (intent.method === "ACH") {
    Object.assign(intent, { status: "PROCESSING", bankName: "STRIPE TEST BANK", last4: "6789" });
    return ok(portalView(intent));
  }
  Object.assign(intent, { status: "SUCCEEDED", cardBrand: "visa", last4: "4242" });
  for (const a of intent.allocations) {
    const it = items.find((i) => i.id === a.openItemId);
    if (!it) continue;
    const open = cents(it.openAmount) - cents(a.amount);
    it.openAmount = amount(open);
    it.status = open === 0n ? "SETTLED" : "PARTIALLY_SETTLED";
    const inv = invoices.find((i) => i.number === it.documentNumber);
    if (inv) inv.open = it.openAmount;
  }
  payments.unshift({ id: id(0x52, 100 + seq), paymentMode: "CARD", amount: intent.amount, unappliedAmount: "0.00", currency: "USD", receivedDate: day(0), valueDate: day(0), status: "APPLIED" });
  return ok(portalView(intent));
}

function handle(method: string, url: URL, body: unknown): Answer | undefined {
  const p = url.pathname;
  const open = () => items.filter((i) => cents(i.openAmount) !== 0n);
  if (method === "GET") {
    switch (p) {
      case "/bff/me": return ok({ userId: "demo-contact", name: "Dana Whitfield, Harlan Agricultural Co-op", legalEntityIds: [LE], csrfToken: "demo-csrf" });
      case "/api/v1/companies": return ok({ items: [{ legalEntityId: LE, name: "Cedar Ridge Pumps, Inc." }] });
      case "/api/v1/accounts": return ok({ items: [] });
      case "/api/v1/open-items": return ok({ items: url.searchParams.get("status") === "all" ? items : open() });
      case "/api/v1/invoices": return ok({ items: invoices.map(invoiceView) });
      case "/api/v1/payments": return ok({ items: payments });
      case "/api/v1/payment-intents": return ok({ items: intents.map(portalView) });
      case "/api/v1/orders": case "/api/v1/returns": case "/api/v1/products": case "/api/v1/delivered-documents": return ok({ items: [] });
      case "/api/v1/statement": return ok({ asOf: day(0), sections: [] });
    }
    const inv = /^\/api\/v1\/invoices\/([^/]+)$/.exec(p);
    if (inv) {
      const found = invoices.find((i) => id(0x50, i.n) === inv[1]);
      return found ? ok(invoiceView(found)) : problem(404, "BIL-0404", "invoice not found");
    }
    const pi = /^\/api\/v1\/payment-intents\/([^/]+)$/.exec(p);
    if (pi) {
      const found = intents.find((x) => x.id === pi[1]);
      return found ? ok(portalView(found)) : problem(404, "AR-GATEWAY-NOT-FOUND", "Payment not found");
    }
  }
  if (method === "POST") {
    if (p === "/api/v1/payment-intents") return createIntent(body);
    const cancel = /^\/api\/v1\/payment-intents\/([^/]+)\/cancel$/.exec(p);
    if (cancel) {
      const found = intents.find((x) => x.id === cancel[1]);
      if (!found) return problem(404, "AR-GATEWAY-NOT-FOUND", "Payment not found");
      if (found.status !== "REQUIRES_PAYMENT") return problem(409, "AR-GATEWAY-STATE", "the payment is already under way and cannot be cancelled");
      found.status = "FAILED";
      return ok(portalView(found));
    }
    const confirm = /^\/demo\/payment-intents\/([^/]+)\/confirm$/.exec(p);
    if (confirm) return confirmIntent(confirm[1] as string);
    if (p === "/bff/logout") return ok({ logoutUrl: "/" });
  }
  return undefined;
}

export function installDemo(): void {
  const misses: string[] = [];
  window.__passageDemoMisses = misses;
  const real = window.fetch.bind(window);
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, window.location.origin);
    if (url.origin !== window.location.origin || !/^\/(api|bff|demo)\//.test(url.pathname)) return real(input, init);
    const method = (init?.method ?? "GET").toUpperCase();
    let body: unknown;
    try {
      body = typeof init?.body === "string" ? JSON.parse(init.body) : undefined;
    } catch {
      body = undefined;
    }
    const answer = handle(method, url, body);
    if (!answer) {
      misses.push(`${method} ${url.pathname}`);
      return new Response(JSON.stringify({ code: "DEMO-0404", detail: "This part of the portal has no demo data." }), { status: 404, headers: { "content-type": "application/problem+json" } });
    }
    return new Response(JSON.stringify(answer.body), { status: answer.status, headers: { "content-type": answer.status >= 400 ? "application/problem+json" : "application/json" } });
  };
}
