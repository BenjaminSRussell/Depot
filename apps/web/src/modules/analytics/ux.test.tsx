import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import type { NavEntry } from "../../shell/workspaces/model";
import { findTable } from "../../test-utils";
import type { UxFunnel, UxSignals } from "./api/client";
import analytics from "./module";
import { biggestDrop, checkFilter, daysBefore, duration, funnelRows, nameRoute } from "./UxTelemetryPage";

// UX telemetry (#510): the funnel and friction signals over GET /v1/analytics/ux-funnels and /ux-signals.

useFakeServer();

const render = () => renderRoutes(analytics.routes, "/analytics/ux");

const funnel: UxFunnel = {
  flow: "procure_to_pay",
  from: "2026-10-18",
  to: "2026-11-16",
  steps: [
    { step: "requisition_created", sessions: 400, conversionFromPrevious: 1 },
    { step: "requisition_submitted", sessions: 360, conversionFromPrevious: 0.9 },
    { step: "purchase_order_created", sessions: 300, conversionFromPrevious: 300 / 360 },
    { step: "receipt_posted", sessions: 280, conversionFromPrevious: 280 / 300 },
    { step: "vendor_invoice_posted", sessions: 240, conversionFromPrevious: 240 / 280 },
    { step: "payment_proposal_created", sessions: 200, conversionFromPrevious: 200 / 240 },
  ],
  secondsToLastStep: 90_000,
};

const signals: UxSignals = {
  from: "2026-10-18",
  to: "2026-11-16",
  validationErrors: [
    { route: "/ap-invoices/post", field: "vendorInvoiceNumber", count: 46 },
    { route: "/purchasing/requisitions/new", field: "lines[0].quantity", count: 31 },
  ],
  noResultSearches: [{ query: "impeller 4in bronze", count: 27 }],
  abandonedForms: [{ route: "/purchasing/requisitions/new", count: 38 }],
};

const calls = (path: string) => server.calls.filter((c) => c.url.pathname === `/api/v1/analytics/${path}`);

describe("UX telemetry page", () => {
  it("shows the funnel with drop-off and the signals by page name", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/analytics/ux-funnels") return json(200, funnel);
      if (url.pathname === "/api/v1/analytics/ux-signals") return json(200, signals);
      return undefined;
    };
    const { container } = render();
    const steps = await findTable({ name: "Procure to pay funnel steps" });
    const invoiced = within(steps).getByText("Vendor invoice posted").closest("tr")!;
    expect(within(invoiced).getByText("60%")).toBeInTheDocument(); // of the first step
    expect(within(invoiced).getByText("85.7%")).toBeInTheDocument(); // from the step before
    expect(within(invoiced).getByText("40")).toBeInTheDocument(); // dropped off
    expect(screen.getByText("1 d 1 h")).toBeInTheDocument();
    expect(screen.getByText(/Most sessions are lost before purchase order created: 60/)).toBeInTheDocument();

    const validation = await findTable({ name: "Validation errors by field" });
    expect(within(validation).getByText("vendorInvoiceNumber")).toBeInTheDocument();
    expect(within(validation).getByText("New requisition")).toBeInTheDocument();
    expect(within(await findTable({ name: "Searches without results" })).getByText("impeller 4in bronze")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);

    const query = Object.fromEntries(calls("ux-funnels")[0].url.searchParams);
    expect(query.flow).toBe("procure_to_pay");
    expect(query.release).toBeUndefined();
    expect(daysBefore(query.to, 29)).toBe(query.from);
  });

  it("sends the flow, period and release, and narrows the signals to a workspace", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/analytics/ux-funnels") return json(200, { ...funnel, flow: "onboarding", steps: [] });
      if (url.pathname === "/api/v1/analytics/ux-signals") return json(200, signals);
      return undefined;
    };
    render();
    await findTable({ name: "Validation errors by field" });
    const filter = screen.getByRole("form", { name: "Telemetry filter" });
    fireEvent.change(within(filter).getByLabelText("Flow"), { target: { value: "onboarding" } });
    fireEvent.change(within(filter).getByLabelText("From"), { target: { value: "2026-11-01" } });
    fireEvent.change(within(filter).getByLabelText("To"), { target: { value: "2026-11-10" } });
    fireEvent.change(within(filter).getByLabelText(/Release/), { target: { value: "2026.11.1" } });
    fireEvent.click(within(filter).getByRole("button", { name: "Show telemetry" }));
    await waitFor(() => expect(calls("ux-funnels")).toHaveLength(2));
    expect(Object.fromEntries(calls("ux-funnels")[1].url.searchParams)).toEqual({ flow: "onboarding", from: "2026-11-01", to: "2026-11-10", release: "2026.11.1" });
    await waitFor(() => expect(calls("ux-signals")).toHaveLength(2));
    expect(await screen.findByText("No sessions in this period")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(/^Workspace/), { target: { value: "Purchasing" } });
    const validation = await findTable({ name: "Validation errors by field" });
    expect(within(validation).queryByText("vendorInvoiceNumber")).toBeNull();
    expect(within(validation).getByText("lines[0].quantity")).toBeInTheDocument();
  });

  it("refuses a bad period or release before calling the service", async () => {
    server.handler = (url) => (url.pathname.includes("/ux-") ? json(200, url.pathname.endsWith("funnels") ? funnel : signals) : undefined);
    render();
    await findTable({ name: "Validation errors by field" });
    const filter = screen.getByRole("form", { name: "Telemetry filter" });
    fireEvent.change(within(filter).getByLabelText("From"), { target: { value: "2026-11-10" } });
    fireEvent.change(within(filter).getByLabelText("To"), { target: { value: "2026-11-01" } });
    fireEvent.click(within(filter).getByRole("button", { name: "Show telemetry" }));
    expect(await screen.findByText("The last day must be on or after the first day.")).toBeInTheDocument();
    expect(calls("ux-funnels")).toHaveLength(1);
  });

  it("says why when the service refuses the request", async () => {
    server.handler = (url) =>
      url.pathname.includes("/ux-") ? json(403, { code: "AUTH-0003", detail: "You do not hold the analytics.ux.view duty." }) : undefined;
    render();
    expect(await screen.findByText("Could not load the funnel")).toBeInTheDocument();
    expect(screen.getByText("Could not load the friction signals")).toBeInTheDocument();
  });
});

describe("UX telemetry helpers", () => {
  it("computes drop-off and the share of the first step", () => {
    const rows = funnelRows(funnel);
    expect(rows.map((r) => r.dropped)).toEqual([0, 40, 60, 20, 40, 40]);
    expect(rows[5].ofStart).toBe(0.5);
    expect(biggestDrop(rows)?.step).toBe("purchase_order_created");
    expect(biggestDrop(funnelRows({ ...funnel, steps: [] }))).toBeUndefined();
  });

  it("checks the filter the way the service does", () => {
    expect(checkFilter({ flow: "onboarding", from: "2026-01-01", to: "2026-11-16", release: "" })).toEqual({});
    expect(checkFilter({ flow: "onboarding", from: "2024-01-01", to: "2026-11-16", release: "" }).to).toMatch(/one year/);
    expect(checkFilter({ flow: "onboarding", from: "", to: "2026-11-16", release: "bad release!" })).toEqual({
      from: "Choose the first day.",
      release: "Use a release such as 2026.11.1.",
    });
  });

  it("names a route by its nav entry or help title and its workspace", () => {
    const entries = [
      { to: "/ap-invoices/post", label: "Post vendor invoice", moduleTitle: "Vendor invoices" },
      { to: "/purchasing/requisitions", label: "Requisitions", moduleTitle: "Purchasing" },
    ] as NavEntry[];
    expect(nameRoute("/ap-invoices/post", entries)).toEqual({ page: "Post vendor invoice", module: "Vendor invoices" });
    expect(nameRoute("/purchasing/requisitions/new", entries)).toEqual({ page: "New requisition", module: "Purchasing" });
    expect(nameRoute("/receivables/unknown", entries)).toEqual({ page: "/receivables/unknown", module: "Receivables" });
  });

  it("formats durations", () => {
    expect(duration(42)).toBe("42 s");
    expect(duration(250)).toBe("4 min 10 s");
    expect(duration(7500)).toBe("2 h 5 min");
    expect(duration(90_000)).toBe("1 d 1 h");
  });
});
