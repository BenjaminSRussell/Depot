import { render, screen } from "@testing-library/react";
import { createElement } from "react";
import { MemoryRouter } from "react-router-dom";
import { renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import { plannedRoutes } from "../../shell/pages/PlannedPage";
import { loadModules } from "../registry";
import workflow from "./module";
import { WorkflowHome } from "./pages";

useFakeServer();

describe("workflow module", () => {
  it("uses its folder name as id and declares workflow routes", () => {
    expect(workflow.id).toBe("workflow");
    expect(workflow.routes.length).toBeGreaterThan(0);
    expect(workflow.nav.every((n) => n.to.startsWith("/workflow"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("workflow");
  });

  it("links to My work and shows no invented job counts", () => {
    render(
      <MemoryRouter>
        <WorkflowHome />
      </MemoryRouter>,
    );
    expect(screen.getByRole("heading", { name: "Workflow" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open My work" })).toHaveAttribute("href", "/workflow/inbox");
    expect(screen.queryByRole("list", { name: "Workflow tiles" })).not.toBeInTheDocument();
    expect(screen.queryByText(/Sample data|Running jobs|Failed runs/)).not.toBeInTheDocument();
  });

  it.each(["/workflow/jobs", "/workflow/designer", "/workflow/templates"])("%s says it isn't available yet instead of showing sample rows", (path) => {
    renderRoutes(workflow.routes.map((r) => ({ path: r.path, element: r.element ?? createElement("p") })), path);
    expect(screen.getByRole("heading", { level: 2, name: "This page isn't available yet" })).toBeInTheDocument();
    expect(screen.getByText(plannedRoutes.find((r) => r.path === path)?.description ?? "missing")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
    expect(screen.queryByText(/rows-1m|outbox\.sweep|workflow\.workitem|Manager role/)).not.toBeInTheDocument();
    expect(server.calls).toEqual([]);
  });
});
