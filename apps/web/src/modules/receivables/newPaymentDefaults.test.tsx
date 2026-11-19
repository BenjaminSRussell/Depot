import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { currencyLookups } from "../../shell/defaults/testLookup";
import { json, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const LE = "0190f0a0-0000-7000-8000-000000000001";
const CUST = "0190f0a0-0000-7000-8000-0000000000c1";

const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

function open(currencies: Record<string, string>) {
  server.handler = (_url, init) => (init.method === "POST" ? json(201, { id: "p1", sourceRef: "ACH-7" }) : undefined);
  renderAt("/receivables/payments/new");
  currencyLookups(currencies);
  fill("Legal entity", LE);
  fill("Customer", CUST);
}

describe("Record payment currency default", () => {
  it.each([
    ["the customer", { [CUST]: "EUR", [LE]: "USD" }, "EUR"],
    ["the legal entity", { [LE]: "CHF" }, "CHF"],
  ])("follows %s", async (_source, currencies, expected) => {
    open(currencies);
    await waitFor(() => expect(screen.getByLabelText("Currency")).toHaveValue(expected));
  });

  it("sends the derived currency and a typed one instead when the clerk changes it", async () => {
    open({ [CUST]: "EUR" });
    await waitFor(() => expect(screen.getByLabelText("Currency")).toHaveValue("EUR"));
    fill("Amount", "980.00");
    fill("Check or bank reference", "ACH-7");
    fireEvent.click(screen.getByRole("button", { name: "Record payment" }));
    await waitFor(() => expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(1));
    expect(server.calls.find((c) => c.method === "POST")?.body).toMatchObject({ currency: "EUR" });
  });

  it("keeps a typed currency over the derived one", async () => {
    open({ [CUST]: "EUR" });
    await waitFor(() => expect(screen.getByLabelText("Currency")).toHaveValue("EUR"));
    fill("Currency", "GBP");
    expect(screen.getByLabelText("Currency")).toHaveValue("GBP");
  });

  it("blocks the post with a field error when no source resolves", async () => {
    open({});
    fill("Amount", "980.00");
    fill("Check or bank reference", "ACH-7");
    fireEvent.click(screen.getByRole("button", { name: "Record payment" }));
    expect((await screen.findAllByText("Enter a three-letter currency code such as USD.")).length).toBeGreaterThan(0);
    expect(server.calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });
});
