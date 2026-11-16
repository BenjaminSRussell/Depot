import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import axe from "axe-core";
import type { ReactElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { configureApi } from "../src/api";
import { display, fromMinor, toMinor } from "../src/money";
import { Invoices } from "../src/pages/Invoices";
import { checkAmounts, Pay, PaymentStatus } from "../src/pages/Pay";
import { loadStripeJs, STRIPE_JS, stripeErrorText, type StripeFactory } from "../src/stripe";

const A = "aaaaaaaa-0000-4000-8000-000000000001";
const B = "aaaaaaaa-0000-4000-8000-000000000002";
const E = "aaaaaaaa-0000-4000-8000-000000000003";
const G = "bbbbbbbb-0000-4000-8000-000000000001";
const item = (over: Record<string, unknown>) => ({ id: A, documentNumber: "INV-1", documentDate: "2027-01-01", dueDate: "2027-02-01", currency: "USD", amount: "100.00", openAmount: "100.00", side: "DEBIT", status: "OPEN", sourceType: "INVOICE", ...over });
const items = [item({}), item({ id: B, documentNumber: "INV-2/1", amount: "250.00", openAmount: "250.00" }), item({ id: E, documentNumber: "INV-3", currency: "EUR" })];
const intent = { id: G, provider: "STRIPE", method: "CARD", status: "REQUIRES_PAYMENT", amount: "300", currency: "USD", createdAt: "2027-03-01T10:00:00Z", allocations: [{ documentNumber: "INV-1", amount: "50" }, { documentNumber: "INV-2/1", amount: "250" }], clientSecret: "pi_1_secret_x", publishableKey: "pk_test_x" };

interface Call { method: string; url: string; headers: Record<string, string>; body?: unknown }
let calls: Call[];
let routes: Record<string, unknown>;

beforeEach(() => {
  calls = [];
  routes = { "GET /api/v1/open-items": { items } };
  configureApi({ csrfToken: "csrf-1", legalEntityId: "le-1", onSignedOut: () => undefined });
  vi.stubGlobal("fetch", vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? "GET";
    calls.push({ method, url, headers: init?.headers as Record<string, string>, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    const key = `${method} ${url.split("?")[0]}`;
    if (!(key in routes)) return new Response(JSON.stringify({ code: "NF", detail: `no stub for ${key}` }), { status: 404 });
    const r = routes[key];
    return r instanceof Response ? r : new Response(JSON.stringify(r), { status: 200, headers: { "content-type": "application/json" } });
  }));
});

function at(path: string, el: ReactElement, pattern = "/pay") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path={pattern} element={el} />
        <Route path="/pay/:id" element={<PaymentStatus />} />
      </Routes>
    </MemoryRouter>,
  );
}

async function accessible(container: HTMLElement) {
  const r = await axe.run(container, { rules: { "color-contrast": { enabled: false } } });
  expect(r.violations.map((v) => `${v.id}: ${v.nodes[0]?.html}`)).toEqual([]);
}

describe("amounts", () => {
  it("are whole minor units, never floating point, with the currency's decimals", () => {
    expect(toMinor("0.10", "USD")! + toMinor("0.20", "USD")!).toBe(30n);
    expect(fromMinor(30n, "USD")).toBe("0.30");
    expect(toMinor("5000", "JPY")).toBe(5000n);
    expect(toMinor("5000.5", "JPY")).toBeNull();
    expect(toMinor("1.234", "KWD")).toBe(1234n);
    expect(toMinor("1.005", "USD")).toBeNull();
    expect(toMinor("12abc", "USD")).toBeNull();
    expect(display("1250.5", "USD")).toBe("1,250.50 USD");
    expect(display("5000", "JPY")).toBe("5,000 JPY");
  });

  it("refuse overpayment, zero, too many decimals and mixed currencies; partial payment is fine", () => {
    const rows = (over: Array<{ amount: string; i?: number }>) => over.map((o, n) => ({ item: items[o.i ?? n] as never, chosen: true, amount: o.amount }));
    expect(checkAmounts(rows([{ amount: "50" }])).errors).toEqual({});
    expect(checkAmounts(rows([{ amount: "50" }])).total).toBe(5000n);
    expect(checkAmounts(rows([{ amount: "100.01" }])).errors[A]).toMatch(/at most 100.00 USD on INV-1/);
    expect(checkAmounts(rows([{ amount: "0" }])).errors[A]).toMatch(/above zero/);
    expect(checkAmounts(rows([{ amount: "1.001" }])).errors[A]).toMatch(/at most 2 decimals/);
    expect(checkAmounts(rows([{ amount: "1", i: 0 }, { amount: "1", i: 2 }])).errors[E]).toMatch(/one currency/);
  });
});

describe("Stripe.js", () => {
  it("is loaded from js.stripe.com only when asked, once", async () => {
    expect(document.querySelector(`script[src="${STRIPE_JS}"]`)).toBeNull();
    const pending = loadStripeJs();
    const script = document.querySelector<HTMLScriptElement>(`script[src="${STRIPE_JS}"]`);
    expect(script).not.toBeNull();
    void loadStripeJs();
    expect(document.querySelectorAll(`script[src="${STRIPE_JS}"]`)).toHaveLength(1);
    const factory = (() => ({})) as unknown as StripeFactory;
    window.Stripe = factory;
    script!.onload?.(new Event("load"));
    await expect(pending).resolves.toBe(factory);
  });

  it("errors read as plain language", () => {
    expect(stripeErrorText({ type: "card_error", code: "card_declined", decline_code: "insufficient_funds" })).toMatch(/not enough money/);
    expect(stripeErrorText({ type: "api_error", message: "internal detail" })).toBe("The payment did not go through. Try again or use another payment method.");
  });
});

describe("paying invoices", () => {
  it("sends only the chosen items and amounts with one key, then mounts Stripe's element and confirms there", async () => {
    const mount = vi.fn();
    const confirmPayment = vi.fn(async () => ({ paymentIntent: { id: "pi_1", status: "succeeded" } }));
    const stripeFactory = vi.fn(() => ({ elements: vi.fn(() => ({ create: vi.fn(() => ({ mount, destroy: vi.fn() })) })), confirmPayment }));
    window.Stripe = stripeFactory as unknown as StripeFactory;
    routes["POST /api/v1/payment-intents"] = new Response(JSON.stringify(intent), { status: 201, headers: { "content-type": "application/json" } });
    routes[`GET /api/v1/payment-intents/${G}`] = { ...intent, clientSecret: undefined, status: "SUCCEEDED", cardBrand: "visa", last4: "4242" };
    const user = userEvent.setup();
    const { container } = at(`/pay?items=${A},${B}`, <Pay />);
    const amountA = await screen.findByLabelText("Amount to pay on INV-1 in USD");
    await accessible(container);
    expect(screen.getByLabelText("Pay INV-2/1")).toBeChecked();
    expect(screen.getByLabelText("Pay INV-3")).not.toBeChecked();

    // Too much: refused on the page, nothing sent.
    await user.clear(amountA);
    await user.type(amountA, "100.01");
    await user.click(screen.getByRole("button", { name: "Continue to payment details" }));
    expect(screen.getByRole("alert")).toHaveTextContent("You can pay at most 100.00 USD on INV-1.");
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);

    await user.clear(amountA);
    await user.type(amountA, "50");
    await user.click(screen.getByRole("button", { name: "Continue to payment details" }));
    await screen.findByRole("heading", { name: "Payment details" });
    const sent = calls.find((c) => c.method === "POST")!;
    expect(sent.url).toBe("/api/v1/payment-intents");
    expect(sent.body).toEqual({ method: "CARD", allocations: [{ openItemId: A, amount: "50.00" }, { openItemId: B, amount: "250.00" }] });
    expect(sent.headers["idempotency-key"]).toMatch(/^p-/);
    expect(JSON.stringify(sent.body)).not.toMatch(/customer/i);
    await waitFor(() => expect(mount).toHaveBeenCalled());
    expect(stripeFactory).toHaveBeenCalledWith("pk_test_x", undefined);

    await user.click(screen.getByRole("button", { name: "Pay 300.00 USD" }));
    expect(confirmPayment).toHaveBeenCalledWith(expect.objectContaining({ redirect: "if_required", confirmParams: { return_url: `${window.location.origin}/pay/${G}` } }));
    expect(await screen.findByText(/Paid. Thank you./)).toBeInTheDocument();
  });

  it("shows a declined card in plain language and stays on the payment step", async () => {
    const confirmPayment = vi.fn(async () => ({ error: { type: "card_error", code: "card_declined", decline_code: "generic_decline", message: "Your card was declined." } }));
    window.Stripe = (() => ({ elements: () => ({ create: () => ({ mount: vi.fn(), destroy: vi.fn() }) }), confirmPayment })) as unknown as StripeFactory;
    routes["POST /api/v1/payment-intents"] = intent;
    const user = userEvent.setup();
    at(`/pay?items=${A}`, <Pay />);
    await user.click(await screen.findByRole("button", { name: "Continue to payment details" }));
    await user.click(await screen.findByRole("button", { name: "Pay 300.00 USD" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Your card was declined. Try another card or contact your bank.");
    expect(screen.getByRole("heading", { name: "Payment details" })).toBeInTheDocument();
  });

  it("offers bank account payment in US dollars only, and shows the service's refusal", async () => {
    routes["POST /api/v1/payment-intents"] = new Response(JSON.stringify({ code: "AR-0019", detail: "INV-1 has 40.00 USD left to pay; pay that much or less" }), { status: 422 });
    const user = userEvent.setup();
    at(`/pay?invoices=INV-3`, <Pay />);
    expect(await screen.findByLabelText("Pay INV-3")).toBeChecked();
    expect(screen.getByRole("radio", { name: "Bank account (ACH)" })).toBeDisabled();
    await user.click(screen.getByLabelText("Pay INV-3"));
    await user.click(screen.getByLabelText("Pay INV-1"));
    expect(screen.getByRole("radio", { name: "Bank account (ACH)" })).toBeEnabled();
    await user.click(screen.getByRole("radio", { name: "Bank account (ACH)" }));
    await user.click(screen.getByRole("button", { name: "Continue to payment details" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("INV-1 has 40.00 USD left to pay");
    expect(calls.find((c) => c.method === "POST")!.body).toMatchObject({ method: "ACH" });
  });
});

describe("leaving the payment step", () => {
  it("cancels the abandoned payment so the invoices are free again, and goes back to the amounts", async () => {
    window.Stripe = (() => ({ elements: () => ({ create: () => ({ mount: vi.fn(), destroy: vi.fn() }) }), confirmPayment: vi.fn() })) as unknown as StripeFactory;
    routes["POST /api/v1/payment-intents"] = intent;
    routes[`POST /api/v1/payment-intents/${G}/cancel`] = { ...intent, status: "FAILED", clientSecret: undefined };
    const user = userEvent.setup();
    at(`/pay?items=${A}`, <Pay />);
    await user.click(await screen.findByRole("button", { name: "Continue to payment details" }));
    await user.click(await screen.findByRole("button", { name: "Change the amounts" }));
    expect(await screen.findByRole("button", { name: "Continue to payment details" })).toBeInTheDocument();
    const cancel = calls.find((c) => c.url === `/api/v1/payment-intents/${G}/cancel`)!;
    expect(cancel.method).toBe("POST");
    expect(cancel.headers["idempotency-key"]).toMatch(/^p-/);
  });

  it("says so when the payment form cannot be shown, instead of loading forever", async () => {
    routes["POST /api/v1/payment-intents"] = { ...intent, publishableKey: undefined };
    const user = userEvent.setup();
    at(`/pay?items=${A}`, <Pay />);
    await user.click(await screen.findByRole("button", { name: "Continue to payment details" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("The secure payment form is not available right now.");
    expect(screen.queryByText("Loading the secure payment form…")).toBeNull();
  });
});

describe("payment status", () => {
  it("reads processing, paid and failed from the service in plain language", async () => {
    routes[`GET /api/v1/payment-intents/${G}`] = { ...intent, method: "ACH", status: "PROCESSING", bankName: "STRIPE TEST BANK", last4: "6789" };
    const first = at(`/pay/${G}`, <PaymentStatus />, "/pay/:id");
    expect(await screen.findByText(/Your bank payment is on its way/)).toBeInTheDocument();
    expect(screen.getByText("Bank account at STRIPE TEST BANK ending 6789")).toBeInTheDocument();
    await accessible(first.container);
    first.unmount();
    routes[`GET /api/v1/payment-intents/${G}`] = { ...intent, status: "FAILED", failureMessage: "There was not enough money in the account. Try another card or bank account." };
    at(`/pay/${G}`, <PaymentStatus />, "/pay/:id");
    expect(await screen.findByRole("alert")).toHaveTextContent("The payment did not go through. There was not enough money in the account.");
    expect(screen.getByRole("link", { name: "Try again" })).toHaveAttribute("href", "/pay?invoices=INV-1%2CINV-2%2F1");
  });
});

describe("invoices", () => {
  it("offer Pay only for invoices with something open", async () => {
    const inv = (id: string, number: string, type = "SHIPMENT") => ({ id, type, status: "POSTED", number, currency: "USD", invoiceDate: "2027-03-01", dueDate: "2027-03-31", paymentTermDays: 30, totals: { net: "1", charges: "0", tax: "0", gross: "1" } });
    routes["GET /api/v1/invoices"] = { items: [inv(A, "INV-1"), inv(B, "INV-2"), inv(E, "INV-9"), inv(G, "CN-1", "CREDIT_NOTE")] };
    const { container } = at("/invoices", <Invoices />, "/invoices");
    expect(await screen.findByRole("link", { name: "Pay INV-1" })).toHaveAttribute("href", "/pay?invoices=INV-1");
    expect(screen.getByRole("link", { name: "Pay INV-2" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Pay INV-9" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Pay CN-1" })).toBeNull();
    await accessible(container);
  });
});
