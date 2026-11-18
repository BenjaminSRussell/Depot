import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { setLookupFetcher } from "../../shell/lookup/entities";
import { plannedRoutes } from "../../shell/pages/PlannedPage";
import { PrincipalProvider } from "../../shell/principal";
import { json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import { findTable } from "../../test-utils";
import { loadModules } from "../registry";
import admin from "./module";
import { AdminHome } from "./pages";
import { listRouteMissing, modeLabel, nextNumber, PAGE_SIZE, SEQUENCES_PATH, type Sequence } from "./SequencesPage";

useFakeServer();

const ENTITY = "22222222-2222-4222-8222-222222222222";

function renderAt(path: string, duties: string[] = []) {
  return renderRoutes(
    admin.routes.map((route) => ({ path: route.path, element: createElement(PrincipalProvider, { duties, children: route.element }) })),
    path,
  );
}

let serial = 0;
const sequence = (code: string, name: string, mode: string, nextValue = 1): Sequence => ({
  id: `00000000-0000-4000-8000-${String((serial += 1)).padStart(12, "0")}`,
  code,
  name,
  format: `${code}-{000000}`,
  legalEntityId: ENTITY,
  mode,
  startFrom: 1,
  nextValue,
  nextFormatted: `${code}-${String(nextValue).padStart(6, "0")}`,
});

function answerSequences(reply: ReturnType<typeof json> | ((url: URL) => ReturnType<typeof json>)) {
  server.handler = (url) => {
    if (url.pathname === `/api/v1/org/legal-entities/${ENTITY}`) return json(200, { id: ENTITY, code: "DE01", name: "Depot GmbH" });
    if (url.pathname === SEQUENCES_PATH) return typeof reply === "function" ? reply(url) : reply;
    return undefined;
  };
}

describe("admin module", () => {
  it("uses its folder name as id and declares admin routes", () => {
    expect(admin.id).toBe("admin");
    expect(admin.routes.length).toBeGreaterThan(0);
    expect(admin.nav.every((n) => n.to.startsWith("/admin"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("admin");
  });

  it("gates the read-only sequences page on reading sequences", () => {
    expect(admin.nav.find((n) => n.to === "/admin/sequences")).toEqual({ label: "Number sequences", to: "/admin/sequences", duty: "numbering.read" });
  });

  it("links the admin home only to pages that work", () => {
    render(
      <MemoryRouter>
        <AdminHome />
      </MemoryRouter>,
    );
    const cards = within(screen.getByRole("list", { name: "Admin pages" }));
    expect(cards.getByRole("link", { name: "Number sequences" })).toHaveAttribute("href", "/admin/sequences");
    expect(cards.getByRole("link", { name: "Import records" })).toHaveAttribute("href", "/admin/import");
    for (const name of ["Audit", "Alerts", "Documents", "Standard texts"]) expect(cards.queryByRole("link", { name })).not.toBeInTheDocument();
  });
});

describe("planned admin pages", () => {
  it.each(["/admin/audit", "/admin/alerts", "/admin/documents", "/admin/texts"])("%s says it isn't available yet instead of a false empty state", (path) => {
    const { container } = renderAt(path);
    expect(screen.getByRole("heading", { level: 2, name: "This page isn't available yet" })).toBeInTheDocument();
    expect(screen.getByText(plannedRoutes.find((r) => r.path === path)?.description ?? "missing")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Go to home" })).toHaveAttribute("href", "/");
    expect(screen.queryByText(/No audit entries yet|No alerts right now|No documents yet|No standard texts yet/)).not.toBeInTheDocument();
    expect(server.calls).toEqual([]);
    expect(pageViolations(container)).toEqual([]);
  });
});

describe("number sequences", () => {
  it("lists the sequences the numbering service returns, with the next number each will give", async () => {
    answerSequences(json(200, { items: [sequence("INV", "Customer invoices", "gap_free", 31877), sequence("SO", "Sales orders", "continuous", 12)] }));
    // test-setup.ts answers every name lookup with nothing; this test reads the legal entity's name from the fake server.
    setLookupFetcher((url, init) => fetch(url, init));
    const { container } = renderAt("/admin/sequences", ["numbering.read"]);
    const table = await findTable({ name: "Number sequences" });
    const calls = server.calls.filter((c) => c.url.pathname === SEQUENCES_PATH);
    expect(calls.map((c) => `${c.method} ${c.url.search}`)).toEqual([`GET ?limit=${PAGE_SIZE}`]);
    expect(table).toHaveTextContent("Customer invoices");
    expect(table).toHaveTextContent("INV-{000000}");
    expect(table).toHaveTextContent("Gap-free");
    expect(table).toHaveTextContent("Continuous");
    expect(table).toHaveTextContent("INV-031877");
    expect(table).toHaveTextContent("SO-000012");
    expect(within(table).getByRole("columnheader", { name: /Next number/ })).toBeInTheDocument();
    await waitFor(() => expect(table).toHaveTextContent("Depot GmbH"));
    expect(table).not.toHaveTextContent(ENTITY);
    expect(screen.getByText(/You can't create or change sequences on this page yet/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Create|Save|Edit/ })).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("pages with the cursor the service returns", async () => {
    answerSequences((url) =>
      url.searchParams.get("cursor") === "c-1"
        ? json(200, { items: [sequence("PO", "Purchase orders", "non_continuous")] })
        : json(200, { items: [sequence("SO", "Sales orders", "continuous")], nextCursor: "c-1" }),
    );
    renderAt("/admin/sequences");
    await findTable({ name: "Number sequences" });
    await userEvent.setup().click(await screen.findByRole("button", { name: /Load more/ }));
    expect(await findTable({ name: "Number sequences" })).toHaveTextContent("Non-continuous");
    expect(server.calls.filter((c) => c.url.pathname === SEQUENCES_PATH).map((c) => c.url.searchParams.get("cursor"))).toEqual([null, "c-1"]);
  });

  it("says what the list is for when there are no sequences, and where to go next", async () => {
    answerSequences(json(200, { items: [] }));
    renderAt("/admin/sequences");
    expect(await screen.findByText("No number sequences yet")).toBeInTheDocument();
    expect(screen.getByText(/Creating them here isn't available yet/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open the setup checklist" })).toHaveAttribute("href", "/setup/checklist");
  });

  it("says plainly when the account may not read sequences", async () => {
    answerSequences(json(403, { code: "AUTH-0003", detail: "missing duty numbering.read" }));
    renderAt("/admin/sequences");
    expect(await screen.findByText("You can't view number sequences")).toBeInTheDocument();
    expect(screen.queryByText("No number sequences yet")).not.toBeInTheDocument();
  });

  it("shows a server failure with the catalog message and a way to try again", async () => {
    answerSequences(json(503, { code: "COM-0503", detail: "database unreachable" }));
    renderAt("/admin/sequences");
    expect(await screen.findByText("Could not load number sequences")).toBeInTheDocument();
    expect(screen.getByText(/This service is unavailable right now/)).toBeInTheDocument();
    answerSequences(json(200, { items: [sequence("SO", "Sales orders", "continuous")] }));
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    expect(await findTable({ name: "Number sequences" })).toHaveTextContent("Sales orders");
  });

  it("explains a numbering service without the list route, with no retry, search, views or export", async () => {
    answerSequences({ status: 405, body: "Method Not Allowed" });
    const { container } = renderAt("/admin/sequences");
    expect(await screen.findByRole("heading", { name: "Number sequences can't be listed yet" })).toBeInTheDocument();
    expect(screen.getByText(/Documents are still numbered as before/)).toBeInTheDocument();
    for (const name of ["Try again", "Export CSV", "Views", "Columns"]) expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    expect(screen.queryByRole("search")).not.toBeInTheDocument();
    expect(screen.queryByText(/Could not load|Something went wrong on our side/)).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("treats only a bodiless 404 or 405 as a missing list route", () => {
    expect(listRouteMissing({ status: 405, code: "COM-0500" })).toBe(true);
    expect(listRouteMissing({ status: 404 })).toBe(true);
    expect(listRouteMissing({ status: 404, code: "COM-0404" })).toBe(false);
    expect(listRouteMissing({ status: 500, code: "COM-0500" })).toBe(false);
    expect(listRouteMissing(undefined)).toBe(false);
  });

  it("labels every numbering mode in sentence case and falls back to the plain next value", () => {
    expect(["gap_free", "continuous", "non_continuous", "per_period"].map(modeLabel)).toEqual(["Gap-free", "Continuous", "Non-continuous", "Per period"]);
    const { nextFormatted: _formatted, ...exhausted } = sequence("BIG", "Exhausted", "gap_free", 1000000);
    expect(nextNumber(exhausted)).toBe("1000000");
  });
});

describe("import records", () => {
  it("opens the setup guide's working import instead of a stub", () => {
    renderAt("/admin/import");
    expect(screen.getByRole("heading", { name: "Import records" })).toBeInTheDocument();
    expect(screen.getByText("Your account can't import files")).toBeInTheDocument();
    expect(screen.queryByText(/Coming soon/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Validate" })).not.toBeInTheDocument();
  });

  it("reads the file imports set up for the chosen records", async () => {
    server.handler = (url) => (url.pathname.startsWith("/api/v1/integration/") ? json(200, { items: [] }) : undefined);
    renderAt("/admin/import", ["migration.run", "migration.connection.write"]);
    expect(await screen.findByText("No file import is set up for vendors yet")).toBeInTheDocument();
    expect(screen.getByLabelText("What to import")).toHaveValue("vendor");
    expect(server.calls.some((c) => c.url.pathname.startsWith("/api/v1/integration/"))).toBe(true);
  });
});
