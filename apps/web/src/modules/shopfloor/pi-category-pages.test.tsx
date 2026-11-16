import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import type { PICategory } from "./api/client";
import shopfloor from "./module";
import { validatePiCategory } from "./PiCategoryPages";

const WEIGH = "0195f0a0-0000-7000-8000-0000000000c1";

type Reply = { status: number; body: unknown };
type Handler = (url: URL, init: RequestInit) => Reply | undefined;
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));
const json = (status: number, body: unknown): Reply => ({ status, body });

let handler: Handler;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

const ENGINEER = ["shopfloor.view", "shopfloor.picategory.maintain"];
function renderAt(path: string, duties: readonly string[] = ENGINEER) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {shopfloor.routes.map((route) => (
            <Route key={route.path} path={route.path} element={route.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}
const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);
const sent = (method: string, suffix: string) => calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

const weigh: PICategory = { id: WEIGH, code: "WEIGH", name: "Weighing", description: "Scale steps", active: true, version: 2, createdBy: "admin", createdAt: "2026-10-20T08:00:00Z", updatedAt: "2026-10-21T08:00:00Z" };

describe("PI categories list", () => {
  it("lists the categories and offers the create link to a maintainer only", async () => {
    handler = (url) => (url.pathname.endsWith("/pi-categories") ? json(200, { items: [weigh] }) : undefined);
    const { container } = renderAt("/shopfloor/pi-categories");
    const table = await findTable({ name: "PI categories" });
    await waitFor(() => expect(table).toHaveTextContent("Weighing"));
    expect(table).toHaveTextContent("Scale steps");
    expect(table).toHaveTextContent("Active");
    expect(screen.getByRole("link", { name: "New PI category" })).toHaveAttribute("href", "/shopfloor/pi-categories/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("hides the create link without the duty and shows the empty state", async () => {
    handler = () => json(200, { items: [] });
    renderAt("/shopfloor/pi-categories", ["shopfloor.view"]);
    expect(await screen.findByText("No PI categories yet")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "New PI category" })).toBeNull();
  });
});

describe("New PI category", () => {
  it("validates like the service", () => {
    const base = { code: " WEIGH ", name: "Weighing", description: "", active: true };
    expect(validatePiCategory(base, true).input).toEqual({ code: "WEIGH", name: "Weighing", description: "", active: true });
    expect(validatePiCategory({ ...base, code: "weigh" }, true).errors.code).toBeDefined();
    expect(validatePiCategory({ ...base, code: "" }, false).errors.code).toBeUndefined();
    expect(validatePiCategory({ ...base, name: " " }, true).errors.name).toBeDefined();
    expect(validatePiCategory({ ...base, description: "x".repeat(501) }, true).errors.description).toBeDefined();
  });

  it("creates the category with an Idempotency-Key and returns to the list", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, weigh);
      return url.pathname.endsWith("/pi-categories") ? json(200, { items: [weigh] }) : undefined;
    };
    const { container } = renderAt("/shopfloor/pi-categories/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Code", "weigh");
    fill("Name", "Weighing");
    fireEvent.click(screen.getByRole("button", { name: "Create PI category" }));
    await waitFor(() => expect(sent("POST", "/pi-categories")).toHaveLength(1));
    expect(sent("POST", "/pi-categories")[0].body).toEqual({ code: "WEIGH", name: "Weighing", description: "", active: true });
    expect(header(sent("POST", "/pi-categories")[0], "Idempotency-Key")).toMatch(/^[0-9a-f-]{36}$/);
    expect(await screen.findByRole("heading", { name: "PI categories" })).toBeInTheDocument();
  });

  it("does not send an invalid form and shows a used code", async () => {
    renderAt("/shopfloor/pi-categories/new");
    fireEvent.click(screen.getByRole("button", { name: "Create PI category" }));
    expect((await screen.findAllByText(/upper-case letters/)).length).toBeGreaterThan(0);
    expect(sent("POST", "/pi-categories")).toHaveLength(0);
    handler = () => json(409, { code: "SF-0142", detail: "code is already used" });
    fill("Code", "WEIGH");
    fill("Name", "Weighing");
    fireEvent.click(screen.getByRole("button", { name: "Create PI category" }));
    expect(await screen.findByText(/A category with this code exists/)).toBeInTheDocument();
  });
});

describe("PI category detail", () => {
  it("saves with the version read as If-Match and keeps the code read-only", async () => {
    handler = (url, init) => {
      if (init.method === "PUT") return json(200, { ...weigh, name: "Weighing and dosing", version: 3 });
      return url.pathname.endsWith(`/pi-categories/${WEIGH}`) ? json(200, weigh) : undefined;
    };
    renderAt(`/shopfloor/pi-categories/${WEIGH}`);
    expect(await screen.findByRole("heading", { name: "PI category WEIGH" })).toBeInTheDocument();
    expect(screen.getByLabelText("Code")).toHaveAttribute("readonly");
    fill("Name", "Weighing and dosing");
    fireEvent.click(screen.getByRole("button", { name: "Save category" }));
    await waitFor(() => expect(sent("PUT", `/pi-categories/${WEIGH}`)).toHaveLength(1));
    const [put] = sent("PUT", `/pi-categories/${WEIGH}`);
    expect(put.body).toMatchObject({ name: "Weighing and dosing", description: "Scale steps", active: true });
    expect(header(put, "If-Match")).toBe('"2"');
  });

  it("shows a refused change and has no save button without the duty", async () => {
    handler = (url, init) => {
      if (init.method === "PUT") return json(412, { code: "SF-0143", detail: "PI category was changed by someone else" });
      return url.pathname.endsWith(`/pi-categories/${WEIGH}`) ? json(200, weigh) : undefined;
    };
    renderAt(`/shopfloor/pi-categories/${WEIGH}`);
    fireEvent.click(await screen.findByRole("button", { name: "Save category" }));
    expect(await screen.findByText(/changed by someone else/)).toBeInTheDocument();
  });

  it("is read-only for a viewer", async () => {
    handler = (url) => (url.pathname.endsWith(`/pi-categories/${WEIGH}`) ? json(200, weigh) : undefined);
    renderAt(`/shopfloor/pi-categories/${WEIGH}`, ["shopfloor.view"]);
    expect(await screen.findByRole("heading", { name: "PI category WEIGH" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save category" })).toBeNull();
    expect(within(screen.getByRole("form", { name: "PI category" })).getByLabelText("Name")).toHaveAttribute("readonly");
  });
});
