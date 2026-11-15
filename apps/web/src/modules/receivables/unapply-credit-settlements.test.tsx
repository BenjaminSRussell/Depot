import { findAlert, findTable } from "../../test-utils";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { CreditSettlementBatch, OpenItem, Payment, PaymentSettlement } from "./api/client";
import { reversalSummary } from "./CreditSettlements";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-0000000000c1";
const PAY = "0190f0a0-0000-7000-8000-000000000301";
const ITEM1 = "0190f0a0-0000-7000-8000-000000000401";
const ITEM2 = "0190f0a0-0000-7000-8000-000000000402";
const ST1 = "0190f0a0-0000-7000-8000-000000000a01";
const ST2 = "0190f0a0-0000-7000-8000-000000000a02";
const BATCH = "0190f0a0-0000-7000-8000-000000000b01";

const payment: Payment = {
  id: PAY,
  legalEntityId: LE,
  customerId: CUST,
  paymentMode: "WIRE",
  source: "MANUAL",
  sourceRef: "WIRE-9",
  amount: "500.00",
  unappliedAmount: "0",
  currency: "USD",
  receivedDate: "2027-05-01",
  valueDate: "2027-05-01",
  remittance: [],
  status: "APPLIED",
  version: 4,
};

const settlements: PaymentSettlement[] = [
  { id: ST1, openItemId: ITEM1, documentNumber: "INV-1/1", amount: "333.34", cashDiscount: "0", writeOff: "0", settledAt: "2027-05-01T10:00:00Z", reversed: false },
  { id: ST2, openItemId: ITEM2, documentNumber: "INV-1/2", amount: "166.66", cashDiscount: "0", writeOff: "0", settledAt: "2027-05-01T10:00:00Z", reversed: true },
];

describe("payment applications", () => {
  it("unapplies one settlement under If-Match after a confirmation and names it in the request", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/payments/${PAY}:unapply`) && init.method === "POST") {
        return json(200, {
          payment: { ...payment, status: "ON_ACCOUNT", unappliedAmount: "333.34", version: 5 },
          reversed: [{ settlementId: ST1, openItemId: ITEM1, amount: "333.34", itemAmount: "333.34", openAfter: "333.34", itemStatus: "OPEN" }],
        });
      }
      if (url.pathname.endsWith(`/payments/${PAY}/settlements`)) return json(200, { items: settlements });
      if (url.pathname.endsWith(`/payments/${PAY}`)) return json(200, payment);
      if (url.pathname.endsWith("/open-items")) return json(200, { items: [] as OpenItem[] });
      return undefined;
    };
    const { container } = renderAt(`/receivables/payments/${PAY}`);
    const table = await findTable({ name: "Applications of this payment" });
    expect(within(table).getByText("Unapplied")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    // Only the live settlement can be unapplied.
    expect(screen.queryByRole("button", { name: "Unapply the settlement of INV-1/2" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Unapply the settlement of INV-1/1" }));
    expect(server.calls.some((c) => c.url.pathname.endsWith(":unapply"))).toBe(false);
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Unapply settlement" }));
    expect(await screen.findByText(/1 settlement\(s\) reversed; 333.34 USD is unapplied again/)).toBeInTheDocument();
    const call = server.calls.find((c) => c.url.pathname.endsWith(":unapply"))!;
    expect(new Headers(call.init.headers).get("If-Match")).toBe('"4"');
    expect(call.body).toEqual({ settlementId: ST1 });
  });

  it("shows the service's refusal when the payment changed meanwhile", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith(":unapply") && init.method === "POST") return json(412, { code: "AR-VERSION-CONFLICT", detail: "payment changed since it was read" });
      if (url.pathname.endsWith(`/payments/${PAY}/settlements`)) return json(200, { items: [settlements[0]] });
      if (url.pathname.endsWith(`/payments/${PAY}`)) return json(200, payment);
      if (url.pathname.endsWith("/open-items")) return json(200, { items: [] });
      return undefined;
    };
    renderAt(`/receivables/payments/${PAY}`);
    fireEvent.click(await screen.findByRole("button", { name: "Unapply the settlement of INV-1/1" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Unapply settlement" }));
    expect(await findAlert()).toHaveTextContent("changed since you opened it");
  });
});

const batch: CreditSettlementBatch = {
  batchId: BATCH,
  legalEntityId: LE,
  customerId: CUST,
  currency: "EUR",
  creditItemId: "0190f0a0-0000-7000-8000-000000000c01",
  creditDocument: "CN-30001",
  total: "100.00",
  settledAt: "2027-05-02T09:00:00Z",
  reversed: false,
  debits: [{ openItemId: ITEM1, documentNumber: "INV-30001", amount: "100.00", itemCurrency: "USD", itemAmount: "108.00", realizedFx: "2.00" }],
};

describe("credit settlements page", () => {
  it("lists the settlements with their realized FX and reverses one after a confirmation", async () => {
    let reversed = false;
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`/credit-settlements/${BATCH}:reverse`) && init.method === "POST") {
        reversed = true;
        return json(200, {
          batchId: BATCH,
          reversalBatchId: "0190f0a0-0000-7000-8000-000000000b02",
          creditItemId: batch.creditItemId,
          creditOpenAfter: "-100.00",
          creditStatus: "OPEN",
          total: "100.00",
          realizedFx: "2.00",
          reopened: [{ openItemId: ITEM1, documentNumber: "INV-30001", amount: "100.00", openAfter: "1000.00", itemStatus: "OPEN" }],
        });
      }
      if (url.pathname.endsWith("/credit-settlements")) return json(200, { items: [{ ...batch, reversed }] });
      return undefined;
    };
    const { container } = renderAt("/receivables/credit-settlements");
    const table = await findTable({ name: "Credit settlements" });
    expect(within(table).getByText("CN-30001")).toBeInTheDocument();
    expect(within(table).getByText("2.00")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Reason for a reversal"), { target: { value: "Wrong invoice" } });
    fireEvent.click(screen.getByRole("button", { name: "Reverse the settlement of CN-30001" }));
    expect(server.calls.some((c) => c.url.pathname.endsWith(":reverse"))).toBe(false);
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Reverse settlement" }));
    expect(await screen.findByText(/Credit settlement reversed/)).toBeInTheDocument();
    const call = server.calls.find((c) => c.url.pathname.endsWith(":reverse"))!;
    expect(call.body).toEqual({ reason: "Wrong invoice" });
    await waitFor(() => expect(screen.getByText("Reversed")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Reverse the settlement of CN-30001" })).not.toBeInTheDocument();
  });

  it("explains what a reversal gives back", () => {
    expect(reversalSummary(batch)).toBe("100.00 EUR goes back onto CN-30001 and INV-30001 is open again by what the settlement took off it.");
  });

  it("shows a refusal for a settlement already reversed", async () => {
    server.handler = (url, init) => {
      if (url.pathname.endsWith(":reverse") && init.method === "POST") return json(409, { code: "AR-CREDIT-SETTLEMENT-REVERSED", detail: "the credit settlement is already reversed" });
      if (url.pathname.endsWith("/credit-settlements")) return json(200, { items: [batch] });
      return undefined;
    };
    renderAt("/receivables/credit-settlements");
    fireEvent.click(await screen.findByRole("button", { name: "Reverse the settlement of CN-30001" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Reverse settlement" }));
    expect(await findAlert()).toHaveTextContent("AR-CREDIT-SETTLEMENT-REVERSED");
  });
});
