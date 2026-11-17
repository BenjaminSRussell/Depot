import { fireEvent, render, screen } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { visibleNav } from "../../shell/dutyGate";
import { plannedRoutes } from "../../shell/plannedRoutes";
import { PrincipalProvider } from "../../shell/principal";
import { json, pageViolations, renderRoutes, server, useFakeServer, type Reply } from "../../shell/testing/fakeServer";
import { loadModules } from "../registry";
import admin from "./module";
import { AdminHome } from "./pages";

useFakeServer();

const ENTITY = "22222222-2222-4222-8222-222222222222";

function sequence(code: string, extra: Record<string, unknown> = {}) {
  return { id: `id-${code}`, code, name: `${code} numbers`, format: `${code}-{######}`, legalEntityId: ENTITY, mode: "gap_free", startFrom: 1, ...extra };
}

function serve(sequences: (url: URL) => Reply) {
  server.handler = (url) => (url.pathname === "/api/v1/numbering/sequences" ? sequences(url) : undefined);
}

function renderAt(path: string, duties: string[] = []) {
  return renderRoutes(
    admin.routes.map((route) => ({ path: route.path, element: createElement(PrincipalProvider, { duties, children: route.element }) })),
    path,
  );
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

  it("gates Sequences on the read duty and opens Import to everyone who can reach it", () => {
    expect(admin.nav.find((n) => n.to === "/admin/sequences")).toMatchObject({ label: "Sequences", duty: "numbering.read" });
    expect(visibleNav(admin.nav, []).map((n) => n.to)).toContain("/admin/import");
    expect(visibleNav(admin.nav, []).map((n) => n.to)).not.toContain("/admin/sequences");
    expect(visibleNav(admin.nav, ["numbering.read"]).map((n) => n.to)).toContain("/admin/sequences");
  });

  it("serves no planned route", () => {
    const served = [...admin.routes.map((r) => r.path), ...admin.nav.map((n) => n.to)];
    expect(served.filter((path) => plannedRoutes.some((planned) => planned.path === path))).toEqual([]);
  });
});

describe("sequences page", () => {
  it("lists each sequence with its format, numbering and starting number", async () => {
    serve(() => json(200, { items: [sequence("SO"), sequence("ITM", { mode: "non_continuous", startFrom: 1000 })] }));
    const { container } = renderAt("/admin/sequences", ["numbering.read"]);
    await screen.findByText("SO numbers");
    const table = screen.getByRole("table", { name: "Number sequences" });
    expect(table).toHaveTextContent("SO-{######}");
    expect(table).toHaveTextContent("Gap-free");
    expect(table).toHaveTextContent("Non-continuous");
    expect(table).toHaveTextContent("1000");
    expect(screen.getByRole("heading", { level: 2, name: "Sequences" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("says there are no sequences only after a successful empty fetch", async () => {
    serve(() => json(200, { items: [] }));
    renderAt("/admin/sequences");
    expect(screen.queryByText("No number sequences yet")).not.toBeInTheDocument();
    expect(await screen.findByText("No number sequences yet")).toBeInTheDocument();
  });

  it.each([404, 500])("shows an error with a retry, never an empty state, on a %i", async (status) => {
    serve(() => json(status, { code: "COM-0500", detail: "numbering is unavailable" }));
    const { container } = renderAt("/admin/sequences");
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load the number sequences");
    expect(screen.getByRole("button", { name: "Try again" })).toBeInTheDocument();
    expect(screen.queryByText("No number sequences yet")).not.toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("retries after a failure and then shows the rows", async () => {
    let healthy = false;
    serve(() => (healthy ? json(200, { items: [sequence("PO")] }) : json(500, { code: "COM-0500", detail: "down" })));
    renderAt("/admin/sequences");
    await screen.findByRole("alert");
    healthy = true;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await screen.findByText("PO numbers");
    expect(screen.getByRole("table", { name: "Number sequences" })).toHaveTextContent("PO-{######}");
  });

  it("shows a calm notice, not an error or an empty state, on a 403", async () => {
    serve(() => json(403, { code: "COM-0403", detail: "forbidden" }));
    renderAt("/admin/sequences");
    expect(await screen.findByText("You can't see the number sequences")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByText("No number sequences yet")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
  });

  it.each([
    ["items null", { items: null }],
    ["items an object", { items: {} }],
    ["startFrom a string", { items: [sequence("SO", { startFrom: "1" })] }],
    ["an unknown mode", { items: [sequence("SO", { mode: "weird" })] }],
    ["a numeric legal entity", { items: [sequence("SO", { legalEntityId: 7 })] }],
    ["a non-string cursor", { items: [], next: 5 }],
  ])("rejects a body with %s and shows the failure", async (_name, body) => {
    serve(() => json(200, body));
    renderAt("/admin/sequences");
    expect(await screen.findByRole("alert")).toHaveTextContent("Could not load the number sequences");
    expect(screen.queryByText("No number sequences yet")).not.toBeInTheDocument();
  });

  it("loads the next page with the cursor and appends its rows", async () => {
    serve((url) => (url.searchParams.get("after") === "c2" ? json(200, { items: [sequence("PO")] }) : json(200, { items: [sequence("SO")], next: "c2" })));
    renderAt("/admin/sequences");
    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await screen.findByText("PO numbers");
    expect(screen.getByRole("table", { name: "Number sequences" })).toHaveTextContent("SO-{######}");
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });

  it("offers no Load more when the cursor is an empty string", async () => {
    serve(() => json(200, { items: [sequence("SO")], next: "" }));
    renderAt("/admin/sequences");
    await screen.findByText("SO numbers");
    expect(screen.queryByRole("button", { name: "Load more" })).not.toBeInTheDocument();
  });
});

describe("import page", () => {
  it("opens the setup import step for someone who can run imports", async () => {
    server.handler = (url) => (url.pathname.startsWith("/api/v1/integration") ? json(200, { items: [] }) : undefined);
    const { container } = renderAt("/admin/import", ["migration.run"]);
    expect(screen.getByRole("heading", { level: 2, name: "Import" })).toBeInTheDocument();
    expect(await screen.findByLabelText("What to import")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains the missing duty and asks the migration service for nothing", () => {
    server.handler = () => json(200, { items: [] });
    renderAt("/admin/import", []);
    expect(screen.getByText("Your account can't import files")).toBeInTheDocument();
    expect(server.calls.filter((c) => c.url.pathname.startsWith("/api/v1/integration"))).toHaveLength(0);
  });
});

describe("admin home", () => {
  it("links Sequences and Import and no planned route", () => {
    render(
      <MemoryRouter>
        <AdminHome />
      </MemoryRouter>,
    );
    expect(screen.getByRole("link", { name: "Sequences" })).toHaveAttribute("href", "/admin/sequences");
    expect(screen.getByRole("link", { name: "Import" })).toHaveAttribute("href", "/admin/import");
    const hrefs = screen.getAllByRole("link").map((a) => a.getAttribute("href"));
    expect(hrefs.filter((href) => plannedRoutes.some((planned) => planned.path === href))).toEqual([]);
  });
});
