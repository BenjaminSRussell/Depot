// Card and bank account (ACH) payments customers made in the customer portal (#30), as the AR team sees them. No Stripe: the
// records are what receivables keeps after Stripe's verified notifications (brand, last four, expiry, bank name and status only).
// One card payment (Harlan Agricultural Co-op when it has an open invoice) is paid and became a PORTAL payment applied to its
// invoice; an ACH payment (Bluewater) is still processing; a declined card (Summit) failed.
import type { GatewayPayment, Payment } from "../../../modules/receivables/api/client";
import { LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, table, uid } from "../../store";
import { BLUEWATER, COASTAL, HARLAN, KR, METRO, PRAIRIE, SUMMIT } from "./arLedger";
import { payments, settleItem } from "./arModel";
import { invoices } from "./model";

const BASE = "/api/v1/receivables";
/** uid kind of the gateway payments (KR in arLedger.ts ends at 144). */
const KG = 145;

export const gatewayPayments = table<GatewayPayment>([]);

const openOf = (i: { totals: { gross: string }; settled: number }) => Number(i.totals.gross) * (1 - i.settled);
const used: string[] = [];
/** An open USD invoice of the first of the customers that has one (each invoice is used once), so the story holds whatever the seed. */
const openInvoiceOf = (...customerIds: string[]) => {
  for (const customerId of customerIds) {
    const found = invoices
      .all()
      .filter((i) => i.customerId === customerId && (i.status === "POSTED" || i.status === "PARTIALLY_SETTLED") && i.currency === "USD" && openOf(i) > 1500 && i.number && !used.includes(i.id))
      .sort((a, b) => (a.number ?? "").localeCompare(b.number ?? ""))[0];
    if (found) {
      used.push(found.id);
      return found;
    }
  }
  return undefined;
};
const ALL = [HARLAN.id, SUMMIT.id, COASTAL.id, PRAIRIE.id, METRO.id, BLUEWATER.id];

function gateway(n: number, g: Partial<GatewayPayment> & Pick<GatewayPayment, "customerId" | "method" | "amount" | "status" | "allocations">): GatewayPayment {
  return gatewayPayments.insert({
    id: uid(KG, n),
    legalEntityId: LEGAL_ENTITY.id,
    provider: "STRIPE",
    providerIntentId: `pi_demo${String(n).padStart(4, "0")}`,
    currency: "USD",
    refundedAmount: "0",
    needsReview: false,
    version: 3,
    createdAt: at(-n),
    updatedAt: at(-n),
    events: [],
    ...g,
  });
}

// Harlan paid 1,250.00 of an invoice by Visa two days ago: the webhook recorded the PORTAL payment and applied it.
const harlanInvoice = openInvoiceOf(...ALL);
if (harlanInvoice) {
  const paid = "1250.00";
  const pay: Payment = payments.insert({
    id: uid(KR.payment, 801),
    legalEntityId: LEGAL_ENTITY.id,
    customerId: harlanInvoice.customerId,
    paymentMode: "CARD",
    source: "PORTAL",
    sourceRef: "pi_demo0002",
    amount: paid,
    unappliedAmount: "0.00",
    currency: "USD",
    receivedDate: day(-2),
    valueDate: day(-2),
    payerName: null,
    payerAccountHash: null,
    shortPayReason: null,
    remittance: [{ reference: harlanInvoice.number ?? "", amount: paid }],
    status: "APPLIED",
    version: 2,
  } as Payment);
  settleItem(harlanInvoice.id, 1250, day(-2));
  gateway(2, {
    customerId: harlanInvoice.customerId,
    method: "CARD",
    amount: paid,
    status: "SUCCEEDED",
    cardBrand: "visa",
    last4: "4242",
    cardExpMonth: 12,
    cardExpYear: 2030,
    paymentId: pay.id,
    allocations: [{ openItemId: harlanInvoice.id, documentNumber: harlanInvoice.number ?? "", amount: paid }],
    events: [
      { eventId: "evt_demo0002a", eventType: "payment_intent.succeeded", outcome: "succeeded: payment recorded and applied", occurredAt: at(-2), processedAt: at(-2) },
    ],
  });
}

// Bluewater pays by bank account: ACH takes a few business days, so the payment is processing and nothing is applied yet.
const bluewaterInvoice = openInvoiceOf(BLUEWATER.id, ...ALL);
if (bluewaterInvoice) {
  const open = dec(Math.round(openOf(bluewaterInvoice) * 100) / 100);
  gateway(1, {
    customerId: bluewaterInvoice.customerId,
    method: "ACH",
    amount: open,
    status: "PROCESSING",
    bankName: "STRIPE TEST BANK",
    last4: "6789",
    allocations: [{ openItemId: bluewaterInvoice.id, documentNumber: bluewaterInvoice.number ?? "", amount: open }],
    events: [{ eventId: "evt_demo0001a", eventType: "payment_intent.processing", outcome: "processing", occurredAt: at(-1), processedAt: at(-1) }],
  });
}

// Summit's card was declined for insufficient funds.
const summitInvoice = openInvoiceOf(SUMMIT.id, ...ALL);
if (summitInvoice) {
  gateway(3, {
    customerId: summitInvoice.customerId,
    method: "CARD",
    amount: "800.00",
    status: "FAILED",
    cardBrand: "mastercard",
    last4: "4444",
    cardExpMonth: 3,
    cardExpYear: 2028,
    failureCode: "insufficient_funds",
    failureMessage: "There was not enough money in the account. Try another card or bank account.",
    allocations: [{ openItemId: summitInvoice.id, documentNumber: summitInvoice.number ?? "", amount: "800.00" }],
    events: [{ eventId: "evt_demo0003a", eventType: "payment_intent.payment_failed", outcome: "failed: insufficient_funds", occurredAt: at(-3), processedAt: at(-3) }],
  });
}

route("GET", `${BASE}/gateway-payments`, ({ query }) => {
  const status = query.get("status") ?? "";
  const customer = query.get("customer") ?? "";
  const payment = query.get("payment") ?? "";
  const items = gatewayPayments
    .all()
    .filter((g) => (!status || g.status === status) && (!customer || g.customerId === customer) && (!payment || g.paymentId === payment))
    .sort((a, b) => b.createdAt.localeCompare(a.createdAt))
    .map(({ events: _events, ...rest }) => rest);
  return { items };
});

route("GET", `${BASE}/gateway-payments/{id}`, ({ params }) => gatewayPayments.get(params.id) ?? reply.problem(404, "AR-GATEWAY-NOT-FOUND", "Payment not found"));
