import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { loadModules } from "../registry";
import workflow from "./module";
import { WorkflowHome } from "./pages";

describe("workflow module", () => {
  it("uses its folder name as id and declares workflow routes", () => {
    expect(workflow.id).toBe("workflow");
    expect(workflow.routes.length).toBeGreaterThan(0);
    expect(workflow.nav.every((n) => n.to.startsWith("/workflow"))).toBe(true);
  });

  it("is auto-discovered by the registry", () => {
    expect(loadModules().map((m) => m.id)).toContain("workflow");
  });

  it("serves only the home page and My work until the designer, jobs and templates are built", () => {
    expect(workflow.routes.map((r) => r.path)).toEqual(["/workflow", "/workflow/inbox"]);
    expect(workflow.nav.map((n) => n.to)).toEqual(["/workflow", "/workflow/inbox"]);
  });

  it("links to My work and shows no sample data", () => {
    render(
      <MemoryRouter>
        <WorkflowHome />
      </MemoryRouter>,
    );
    expect(screen.getByRole("heading", { name: "Workflow and batch" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Open My work" })).toHaveAttribute("href", "/workflow/inbox");
    expect(screen.queryByText("Sample data")).not.toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Workflow tiles" })).not.toBeInTheDocument();
  });
});
