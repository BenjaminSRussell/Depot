import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findAlert, findTable } from "../../test-utils";
import type { Outcome } from "./api/client";
import quality from "./module";
import { validateOutcome } from "./OutcomesPage";

const OK: Outcome = { id: "0195f0a0-0000-7000-8000-0000000000a1", code: "NO-DEFECT", name: "No visible defect", active: true, version: 1 };
const OLD: Outcome = { id: "0195f0a0-0000-7000-8000-0000000000a2", code: "LEGACY", name: "Old finish rating", active: false, version: 3 };

type Reply = { status: number; body: unknown; headers?: Record<string, string> };
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (container: HTMLElement) => checkAxeAA(container).filter((v) => !shellRules.has(v.id));

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = handler(url, init) ?? { status: 404, body: { code: "TEST-404", detail: `no fake for ${init.method ?? "GET"} ${url.pathname}` } };
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json", ...reply.headers } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function Where() {
  const location = useLocation();
  return <p data-testid="location">{location.pathname}</p>;
}

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {quality.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

const header = (call: { init: RequestInit }, name: string) => new Headers(call.init.headers).get(name);

describe("outcome master", () => {
  it("checks the code and the name like the service", () => {
    expect(validateOutcome("no-defect", "No visible defect")).toEqual({});
    expect(validateOutcome("x", "").code).toMatch(/2 to 32/);
    expect(validateOutcome("OK", " ").name).toBe("Name the outcome in up to 120 characters.");
  });

  it("lists outcomes by code with their status and an edit link", async () => {
    handler = (url) => (url.pathname === "/api/v1/quality/outcomes" ? { status: 200, body: { items: [OK, OLD] } } : undefined);
    const { container } = renderAt("/quality/outcomes");
    const table = await findTable({ name: "Outcomes" });
    await waitFor(() => expect(table).toHaveTextContent("No visible defect"));
    expect(table).toHaveTextContent("NO-DEFECT");
    expect(table).toHaveTextContent("Retired");
    expect(screen.getByRole("link", { name: "Edit No visible defect" })).toHaveAttribute("href", `/quality/outcomes/${OK.id}`);
    expect(screen.getByRole("link", { name: "New outcome" })).toHaveAttribute("href", "/quality/outcomes/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates an outcome with one Idempotency-Key and returns to the list", async () => {
    handler = (url, init) => (init.method === "POST" && url.pathname === "/api/v1/quality/outcomes" ? { status: 201, body: OK } : { status: 200, body: { items: [OK] } });
    renderAt("/quality/outcomes/new");
    fireEvent.click(screen.getByRole("button", { name: "Save outcome" }));
    expect((await screen.findAllByText(/Use 2 to 32 capital letters/)).length).toBeGreaterThan(0);
    expect(calls).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "no-defect" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: " No visible defect " } });
    fireEvent.click(screen.getByRole("button", { name: "Save outcome" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/quality/outcomes"));
    const post = calls.find((c) => c.method === "POST")!;
    expect(header(post, "Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({ code: "NO-DEFECT", name: "No visible defect" });
  });

  it("puts a code that is already used on the code field", async () => {
    handler = () => ({ status: 409, body: { code: "QM-0191", detail: "an outcome with this code already exists" } });
    renderAt("/quality/outcomes/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "NO-DEFECT" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Again" } });
    fireEvent.click(screen.getByRole("button", { name: "Save outcome" }));
    expect((await screen.findAllByText("This code is already used by another outcome.")).length).toBeGreaterThan(0);
    expect(screen.getByTestId("location")).toHaveTextContent("/quality/outcomes/new");
  });

  it("renames and retires an outcome with If-Match", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 200, body: { ...OK, active: false, name: "Clean", version: 2 }, headers: { ETag: '"2"' } };
      if (url.pathname === `/api/v1/quality/outcomes/${OK.id}`) return { status: 200, body: OK, headers: { ETag: '"1"' } };
      return { status: 200, body: { items: [OK] } };
    };
    renderAt(`/quality/outcomes/${OK.id}`);
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Clean" } });
    fireEvent.click(screen.getByLabelText("Active"));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/quality/outcomes"));
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(header(patch, "If-Match")).toBe('"1"');
    expect(patch.body).toEqual({ name: "Clean", active: false });
  });

  it("says when someone else changed the outcome first", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 412, body: { code: "conflict_version", detail: "current version is 2, not 1" } };
      return url.pathname === `/api/v1/quality/outcomes/${OK.id}` ? { status: 200, body: OK } : undefined;
    };
    renderAt(`/quality/outcomes/${OK.id}`);
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Clean" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect((await findAlert()).textContent).toContain("Someone else changed this outcome");
  });
});
