import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { rememberOption } from "../../shell/lookup/entities";
import type { Invoice } from "./api/client";
import type { Intake } from "./api/intake";
import { lineFindings, postState, receiptLineChoices, suggestMatches } from "./IntakeDetailPage";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const VENDOR = "11111111-1111-4111-8111-111111111111";
const ENTITY = "22222222-2222-4222-8222-222222222222";
const ID = "44444444-4444-4444-8444-444444444444";
const PO = "55555555-5555-4555-8555-555555555555";
const INVOICE = "33333333-3333-4333-8333-333333333333";
const RECEIPT = "0190a000-0000-7000-8000-000000000010";
const DUTIES = ["open-item.read", "payables.vendor-invoice.review", "receiving.receipt.maintain", "receiving.vendor-invoice.post"];

beforeEach(() => {
  rememberOption("vendor", { id: VENDOR, name: "Acme Supply", number: "V-100" });
});

function intake(patch: Partial<Intake> = {}): Intake {
  return {
    id: ID,
    legalEntityId: ENTITY,
    source: "X12_810",
    sourceRef: "msg-1",
    vendorId: VENDOR,
    invoiceNumber: "AC-9001",
    invoiceDate: "2026-11-09",
    purpose: "standard",
    purchaseOrderId: PO,
    purchaseOrderNumber: "PO-1001",
    currency: "USD",
    net: "150.00",
    gross: "150.00",
    status: "RECEIVED",
    receivedBy: "edi",
    receivedAt: "2026-11-10T08:00:00Z",
    lines: [{ lineNo: "1", item: "SEAL-MK-125", quantity: "10", unit: "EA", unitPrice: "15", amount: "150.00" }],
    ...patch,
  };
}

const receipt = {
  id: RECEIPT,
  receiptNumber: "RCV-0042",
  state: "POSTED",
  version: 2,
  purchaseOrderId: PO,
  vendorId: VENDOR,
  legalEntityId: ENTITY,
  packingSlip: "PS-1",
  carrier: "",
  receiptDate: "2026-11-08",
  currency: "USD",
  exchangeRate: "1",
  createdAt: "2026-11-08T08:00:00Z",
  lines: [
    {
      lineNo: 1, poLineId: "pl1", productId: "p1", orderedRemaining: "8", overDeliveryPct: "0", unitPrice: "15", unit: "EA", batchTracked: false, serialTracked: false,
      qty: "8", batch: "", serials: [], locationId: "l1", inventoryStatus: "AVAILABLE", amount: "120.00", amountAcct: "120.00",
    },
  ],
};
const posted = {
  id: INVOICE, vendorId: VENDOR, legalEntityId: ENTITY, invoiceNumber: "AC-9001", invoiceDate: "2026-11-09", postingDate: "2026-11-10", currency: "USD", ledgerId: "l", fiscalPeriodId: "p",
  total: "150.00", ledgerEntryId: "e", createdAt: "2026-11-10T08:00:00Z", status: "POSTED", lines: [],
} as unknown as Invoice;

describe("invoices received electronically", () => {
  it("lists them with the vendor name, how they arrived and the status in words", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/payables/vendor-invoice-intake" ? json(200, { items: [intake(), intake({ id: "i2", invoiceNumber: "AC-8000", source: "CXML", status: "REJECTED" })], nextCursor: "" }) : undefined);
    const { container } = renderAt("/ap-invoices/intake", DUTIES);
    const table = await screen.findByRole("table", { name: "Invoices received electronically" });
    expect(await within(table).findByText("AC-9001")).toBeInTheDocument();
    expect(await within(table).findAllByText("Acme Supply (V-100)")).toHaveLength(2);
    expect(within(table).getByText("X12 810 from the vendor")).toBeInTheDocument();
    expect(within(table).getByText("cXML network")).toBeInTheDocument();
    expect(within(table).getByText("Waiting for review")).toBeInTheDocument();
    expect(within(table).getByText("Rejected")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers the posted receipts of the order, suggests a match and says what differs", () => {
    const choices = receiptLineChoices([receipt, { ...receipt, id: "r2", state: "DRAFT" }, { ...receipt, id: "r3", purchaseOrderId: "other" }] as never, PO);
    expect(choices).toHaveLength(1);
    expect(choices[0]).toMatchObject({ key: `${RECEIPT}:1`, receiptNumber: "RCV-0042", qty: "8" });
    const lines = intake().lines;
    const matches = suggestMatches(lines, choices);
    expect(matches).toEqual({ "1": `${RECEIPT}:1` });
    expect(lineFindings(lines[0]!, choices[0])).toEqual(["Invoiced 10, only 8 received"]);
    expect(lineFindings({ ...lines[0]!, quantity: "8", unitPrice: "16" }, choices[0])).toEqual(["Price 16, the order says 15"]);
    expect(postState(intake(), matches, choices)).toEqual({
      header: { legalEntityId: ENTITY, invoiceNumber: "AC-9001", invoiceDate: "2026-11-09" },
      items: [{ vendorId: VENDOR, currency: "USD", receiptId: RECEIPT, receiptLineNo: 1, qty: "10", unitPrice: "15" }],
    });
    expect(postState(intake(), {}, choices).items).toEqual([]);
  });

  const load = (status = 200, answer?: (url: URL, init: RequestInit) => ReturnType<typeof json> | undefined) => {
    server.handler = (url, init) => {
      const custom = answer?.(url, init);
      if (custom) return custom;
      if (url.pathname === `/api/v1/payables/vendor-invoice-intake/${ID}`) return json(status, intake());
      if (url.pathname === "/api/v1/payables/vendor-invoice-intake") return json(200, { items: [], nextCursor: "" });
      if (url.pathname === "/api/v1/receiving/receipts") return json(200, { items: [{ ...receipt, lines: [] }] });
      if (url.pathname === `/api/v1/receiving/receipts/${RECEIPT}`) return json(200, receipt);
      if (url.pathname === "/api/v1/receiving/invoices") return json(200, { items: url.searchParams.get("status") === "POSTED" ? [posted] : [] });
      return undefined;
    };
  };

  it("shows the invoice with its lines, the match to the receipt and the way on to posting", async () => {
    load();
    const { container } = renderAt(`/ap-invoices/intake/${ID}`, DUTIES);
    expect(await screen.findByText("Received invoice AC-9001")).toBeInTheDocument();
    expect(screen.getByText("PO-1001")).toBeInTheDocument();
    expect(await screen.findByText("Invoiced 10, only 8 received")).toBeInTheDocument();
    const link = screen.getByRole("link", { name: "Post as vendor invoice (1 line)" });
    expect(link).toHaveAttribute("href", "/ap-invoices/post");
    expect(pageViolations(container)).toEqual([]);
    // The clerk can take a line out of the match.
    fireEvent.change(screen.getByLabelText(/Line 1:/), { target: { value: "" } });
    expect(screen.queryByRole("link", { name: /Post as vendor invoice/ })).toBeNull();
    expect(screen.getByText("Match at least one line to post it.")).toBeInTheDocument();
  });

  it("records the posted vendor invoice", async () => {
    load(200, (url, init) => (init.method === "POST" && url.pathname.endsWith(":resolve") ? json(200, intake({ status: "POSTED", vendorInvoiceId: INVOICE, decidedAt: "2026-11-11T09:00:00Z" })) : undefined));
    renderAt(`/ap-invoices/intake/${ID}`, DUTIES);
    const select = await screen.findByLabelText("Posted vendor invoice");
    fireEvent.click(screen.getByRole("button", { name: "Mark as posted" }));
    expect(await screen.findByText("Choose the vendor invoice you posted from this received invoice.")).toBeInTheDocument();
    await waitFor(() => expect(within(select).getByRole("option", { name: /AC-9001, 150.00 USD/ })).toBeInTheDocument());
    fireEvent.change(select, { target: { value: INVOICE } });
    fireEvent.click(screen.getByRole("button", { name: "Mark as posted" }));
    expect(await screen.findByText(/Recorded\. The received invoice is posted/)).toBeInTheDocument();
    const call = server.calls.find((c) => c.method === "POST")!;
    expect(call.url.pathname).toBe(`/api/v1/payables/vendor-invoice-intake/${ID}:resolve`);
    expect(call.body).toEqual({ vendorInvoiceId: INVOICE });
  });

  it("rejects with a reason after asking, and says plainly when the invoice is not posted yet", async () => {
    let refuse = true;
    load(200, (url, init) => {
      if (init.method !== "POST") return undefined;
      if (url.pathname.endsWith(":reject")) return json(200, intake({ status: "REJECTED", decisionNote: "Quantities do not match", decidedAt: "2026-11-11T09:00:00Z" }));
      return refuse ? json(422, { code: "AP-VII-004", detail: "not posted" }) : undefined;
    });
    renderAt(`/ap-invoices/intake/${ID}`, DUTIES);
    const select = await screen.findByLabelText("Posted vendor invoice");
    await waitFor(() => expect(within(select).getByRole("option", { name: /AC-9001/ })).toBeInTheDocument());
    fireEvent.change(select, { target: { value: INVOICE } });
    fireEvent.click(screen.getByRole("button", { name: "Mark as posted" }));
    expect(await screen.findByText(/That vendor invoice isn't posted for this supplier yet\./)).toBeInTheDocument();
    refuse = false;
    fireEvent.click(screen.getByRole("button", { name: "Reject received invoice" }));
    expect(await screen.findByText("Enter a reason of at most 1000 characters.")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: " Quantities do not match " } });
    fireEvent.click(screen.getByRole("button", { name: "Reject received invoice" }));
    fireEvent.click(await screen.findByRole("button", { name: "Confirm rejection" }));
    expect(await screen.findByText(/Rejected\. The reason is kept/)).toBeInTheDocument();
    expect(server.calls.filter((c) => c.url.pathname.endsWith(":reject"))[0]!.body).toEqual({ reason: "Quantities do not match" });
    expect(screen.getByText("Quantities do not match")).toBeInTheDocument();
  });

  it("says what a user without the review duty cannot do", async () => {
    load();
    renderAt(`/ap-invoices/intake/${ID}`, ["open-item.read", "receiving.receipt.maintain"]);
    expect(await screen.findByText("Recording or rejecting a received invoice needs the payables.vendor-invoice.review duty.")).toBeInTheDocument();
  });
});
