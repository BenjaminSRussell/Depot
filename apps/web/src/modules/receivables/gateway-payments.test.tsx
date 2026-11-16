import { findTable } from "../../test-utils";
import { screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { GatewayPayment, OpenItem, Payment } from "./api/client";
import { methodText } from "./GatewayPayments";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-0000000000c1";
const PAY = "0190f0a0-0000-7000-8000-000000000301";
const ITEM = "0190f0a0-0000-7000-8000-000000000401";

const card: GatewayPayment = {
  id: "0190f0a0-0000-7000-8000-000000000a01",
  legalEntityId: LE,
  customerId: CUST,
  provider: "STRIPE",
  providerIntentId: "pi_123",
  method: "CARD",
  amount: "1250.00",
  currency: "USD",
  status: "SUCCEEDED",
  cardBrand: "visa",
  last4: "4242",
  cardExpMonth: 3,
  cardExpYear: 2030,
  paymentId: PAY,
  refundedAmount: "0",
  needsReview: false,
  version: 3,
  createdAt: "2027-05-01T10:00:00Z",
  updatedAt: "2027-05-01T10:01:00Z",
  allocations: [{ openItemId: ITEM, documentNumber: "INV-1001", amount: "1250.00" }],
};
const ach: GatewayPayment = { ...card, id: "0190f0a0-0000-7000-8000-000000000a02", method: "ACH", status: "PROCESSING", cardBrand: undefined, cardExpMonth: undefined, cardExpYear: undefined, bankName: "STRIPE TEST BANK", last4: "6789", paymentId: undefined };
const failed: GatewayPayment = { ...card, id: "0190f0a0-0000-7000-8000-000000000a03", status: "FAILED", failureCode: "card_declined", failureMessage: "Your card was declined. Try another card or contact your bank.", paymentId: undefined };

const payment: Payment = {
  id: PAY,
  legalEntityId: LE,
  customerId: CUST,
  paymentMode: "CARD",
  source: "PORTAL",
  sourceRef: "pi_123",
  amount: "1250.00",
  unappliedAmount: "0",
  currency: "USD",
  receivedDate: "2027-05-01",
  valueDate: "2027-05-01",
  remittance: [{ reference: "INV-1001", amount: "1250.00" }],
  status: "APPLIED",
  version: 2,
};

describe("portal card and bank payments", () => {
  it("describes the method with brand, last four and expiry, or the bank, and nothing more", () => {
    expect(methodText(card)).toBe("Visa ending 4242, expires 03/2030");
    expect(methodText(ach)).toBe("Bank account at STRIPE TEST BANK ending 6789");
    expect(methodText({ ...card, last4: undefined, cardBrand: undefined })).toBe("Card");
  });

  it("lists them with status, method and the reason a payment failed", async () => {
    server.handler = (url) => (url.pathname.endsWith("/gateway-payments") ? json(200, { items: [card, ach, failed] }) : undefined);
    const { container } = renderAt("/receivables/gateway-payments");
    const table = await findTable({ name: "Portal card and bank payments" });
    expect(within(table).getAllByText("Visa ending 4242, expires 03/2030")).toHaveLength(2);
    expect(within(table).getByText("Bank account at STRIPE TEST BANK ending 6789")).toBeInTheDocument();
    expect(within(table).getByText("Processing")).toBeInTheDocument();
    expect(within(table).getByText("Your card was declined. Try another card or contact your bank.")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("shows on the payment page the portal payment a PORTAL payment came from", async () => {
    server.handler = (url) => {
      if (url.pathname.endsWith("/gateway-payments") && url.searchParams.get("payment") === PAY) return json(200, { items: [card] });
      if (url.pathname.endsWith(`/payments/${PAY}/settlements`)) return json(200, { items: [] });
      if (url.pathname.endsWith(`/payments/${PAY}`)) return json(200, payment);
      if (url.pathname.endsWith("/open-items")) return json(200, { items: [] as OpenItem[] });
      return undefined;
    };
    const { container } = renderAt(`/receivables/payments/${PAY}`);
    expect(await screen.findByText("Paid in the customer portal")).toBeInTheDocument();
    expect(screen.getByText("Visa ending 4242, expires 03/2030")).toBeInTheDocument();
    await waitFor(() => expect(pageViolations(container)).toEqual([]));
  });
});
