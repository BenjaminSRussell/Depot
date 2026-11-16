import { fireEvent, render as renderPage, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import { loadWorkspaceConfig, type NavEntry } from "../../shell/workspaces/model";
import { WorkspaceNavProvider } from "../../shell/workspaces/WorkspaceNav";
import { findTable } from "../../test-utils";
import type { UxFunnel, UxSignals } from "./api/client";
import analytics from "./module";
import { biggestDrop, checkFilter, daysBefore, duration, fieldLabel, funnelRows, funnelSummary, nameRoute, UxTelemetryPage, wordsOf } from "./UxTelemetryPage";

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

const navEntry = (to: string, label: string, moduleTitle: string) => ({ to, label, moduleTitle }) as NavEntry;
const NAV = [
  navEntry("/ap-invoices/post", "Post vendor invoice", "Vendor invoices"),
  navEntry("/purchasing/requisitions", "Requisitions", "Purchasing"),
  navEntry("/receiving/receipts", "Receipts", "Receiving"),
];
const config = loadWorkspaceConfig();
const page = (entries: NavEntry[]) => (
  <MemoryRouter initialEntries={["/analytics/ux"]}>
    <WorkspaceNavProvider entries={entries} config={config}>
      <UxTelemetryPage />
    </WorkspaceNavProvider>
  </MemoryRouter>
);

const withSteps = (counts: number[]): UxFunnel => ({
  ...funnel,
  steps: funnel.steps.map((s, i) => ({ ...s, sessions: counts[i] ?? 0, conversionFromPrevious: i === 0 ? 1 : counts[i - 1] ? Math.min((counts[i] ?? 0) / counts[i - 1], 1) : 0 })),
});

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
    expect(within(validation).getByText("Vendor invoice number")).toHaveAttribute("title", "vendorInvoiceNumber");
    expect(within(validation).getByText("Line 1 – Quantity")).toBeInTheDocument();
    expect(within(validation).getByText("New requisition")).toBeInTheDocument();
    expect(within(validation).getByText("AP invoices")).toBeInTheDocument();
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

  });

  it("asks the service for one workspace's signals, so its top 20 is taken within it", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/analytics/ux-funnels") return json(200, funnel);
      if (url.pathname === "/api/v1/analytics/ux-signals") {
        const only = url.searchParams.get("routePrefix");
        return json(200, {
          ...signals,
          ...(only ? { routePrefix: only } : {}),
          validationErrors: signals.validationErrors.filter((v) => !only || v.route.startsWith(`${only}/`)),
          abandonedForms: signals.abandonedForms.filter((v) => !only || v.route.startsWith(`${only}/`)),
        });
      }
      return undefined;
    };
    renderPage(page(NAV));
    await findTable({ name: "Validation errors by field" });
    const select = screen.getByLabelText(/^Workspace/) as HTMLSelectElement;
    expect([...select.options].map((o) => o.text)).toEqual(["Every workspace", "Purchasing", "Receiving", "Vendor invoices"]);
    expect(calls("ux-signals")[0].url.searchParams.get("routePrefix")).toBeNull();

    fireEvent.change(select, { target: { value: "/purchasing" } });
    await waitFor(() => expect(calls("ux-signals")).toHaveLength(2));
    expect(calls("ux-signals")[1].url.searchParams.get("routePrefix")).toBe("/purchasing");
    const validation = await findTable({ name: "Validation errors by field" });
    await waitFor(() => expect(within(validation).queryByText("Vendor invoice number")).toBeNull());
    expect(within(validation).getByText("Line 1 – Quantity")).toBeInTheDocument();
    expect(screen.getAllByText("Showing the 20 most frequent in Purchasing.")).toHaveLength(2);

    // Another period keeps the workspace: the options come from the navigation, not from the rows of the last answer.
    const filter = screen.getByRole("form", { name: "Telemetry filter" });
    fireEvent.change(within(filter).getByLabelText("From"), { target: { value: "2026-11-01" } });
    fireEvent.click(within(filter).getByRole("button", { name: "Show telemetry" }));
    await waitFor(() => expect(calls("ux-signals")).toHaveLength(3));
    expect(calls("ux-signals")[2].url.searchParams.get("routePrefix")).toBe("/purchasing");
    expect(select.value).toBe("/purchasing");
  });

  it("drops a chosen workspace the navigation no longer offers, so the select and the lists agree", async () => {
    server.handler = (url) => (url.pathname.includes("/ux-") ? json(200, url.pathname.endsWith("funnels") ? funnel : signals) : undefined);
    const view = renderPage(page(NAV));
    await findTable({ name: "Validation errors by field" });
    fireEvent.change(screen.getByLabelText(/^Workspace/), { target: { value: "/purchasing" } });
    await waitFor(() => expect(calls("ux-signals")).toHaveLength(2));

    view.rerender(page(NAV.filter((e) => e.moduleTitle !== "Purchasing")));
    await waitFor(() => expect((screen.getByLabelText(/^Workspace/) as HTMLSelectElement).value).toBe(""));
    await waitFor(() => expect(calls("ux-signals")).toHaveLength(3));
    expect(calls("ux-signals")[2].url.searchParams.get("routePrefix")).toBeNull();
    const validation = await findTable({ name: "Validation errors by field" });
    expect(await within(validation).findByText("Vendor invoice number")).toBeInTheDocument();
  });

  it("shows every step when the first has no sessions, and caps finished of started at 100%", async () => {
    let steps = [0, 4, 6, 5, 5, 3];
    server.handler = (url) => (url.pathname.includes("/ux-") ? json(200, url.pathname.endsWith("funnels") ? withSteps(steps) : signals) : undefined);
    const view = render();
    const table = await findTable({ name: "Procure to pay funnel steps" });
    expect(await within(table).findByText("Payment proposal created")).toBeInTheDocument();
    expect(screen.queryByText("No sessions in this period")).toBeNull();
    expect(screen.getByText("No session reached the first step in this period")).toBeInTheDocument();
    expect(screen.getByText(/A later step counts more sessions than the first/)).toBeInTheDocument();
    view.unmount();

    steps = [2, 3, 3, 4, 4, 5];
    render();
    await findTable({ name: "Procure to pay funnel steps" });
    expect(await screen.findByText("Capped at 100%: some sessions began before this period or release")).toBeInTheDocument();
    expect(screen.queryByText("250%")).toBeNull();
    expect(screen.getAllByText("100%").length).toBeGreaterThan(0);
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

  it("caps shares at 100% and leaves them out when the first step is empty", () => {
    const empty = funnelRows(withSteps([0, 4, 6, 5, 5, 3]));
    expect(empty.map((r) => r.ofStart)).toEqual([null, null, null, null, null, null]);
    expect(empty[2].ofLargest).toBe(1);
    expect(funnelSummary(empty)).toEqual({ started: 0, finished: 3, finishedShare: null, laterExceedsFirst: true, any: true });
    const grows = funnelRows(withSteps([2, 3, 3, 4, 4, 5]));
    expect(grows[5].ofStart).toBe(1);
    expect(funnelSummary(grows).finishedShare).toBe(1);
    expect(funnelSummary(funnelRows(withSteps([0, 0, 0, 0, 0, 0]))).any).toBe(false);
  });

  it("labels fields and route segments in words", () => {
    expect(fieldLabel("lines[0].unitPrice")).toBe("Line 1 – Unit price");
    expect(fieldLabel("vendorInvoiceNumber")).toBe("Vendor invoice number");
    expect(fieldLabel("addresses[2].postalCode")).toBe("Address 3 – Postal code");
    expect(wordsOf("ap-invoices")).toBe("AP invoices");
    expect(wordsOf("eam")).toBe("EAM");
  });

  it("names a route by its nav entry or help title and its workspace", () => {
    const entries = [
      { to: "/ap-invoices/post", label: "Post vendor invoice", moduleTitle: "Vendor invoices" },
      { to: "/purchasing/requisitions", label: "Requisitions", moduleTitle: "Purchasing" },
    ] as NavEntry[];
    expect(nameRoute("/ap-invoices/post", entries)).toEqual({ page: "Post vendor invoice", module: "Vendor invoices" });
    expect(nameRoute("/purchasing/requisitions/new", entries)).toEqual({ page: "New requisition", module: "Purchasing" });
    expect(nameRoute("/receivables/unknown", entries)).toEqual({ page: "/receivables/unknown", module: "Receivables" });
    expect(nameRoute("/product/items/new", entries)).toEqual({ page: "New product", module: "Product" });
    expect(nameRoute("/ap-invoices/grir", [])).toEqual({ page: "Goods received, not invoiced", module: "AP invoices" });
  });

  it("formats durations", () => {
    expect(duration(42)).toBe("42 s");
    expect(duration(250)).toBe("4 min 10 s");
    expect(duration(7500)).toBe("2 h 5 min");
    expect(duration(90_000)).toBe("1 d 1 h");
  });
});
