import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import type { PIStepTemplate } from "./api/client";
import shopfloor from "./module";
import { newSpec, validateStep } from "./PiStepLibraryPages";

const STEP = "0195f0a0-0000-7000-8000-0000000000d1";
type Reply = { status: number; body: unknown };
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
const pageViolations = (c: HTMLElement) => checkAxeAA(c).filter((v) => !shellRules.has(v.id));
const json = (status: number, body: unknown): Reply => ({ status, body });
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL; init: RequestInit; body: unknown }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url, init, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = (url.pathname.endsWith("/pi-categories") ? json(200, { items: [] }) : undefined) ?? handler(url, init) ?? json(404, { code: "TEST-404", detail: `no fake for ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

const ENGINEER = ["shopfloor.view", "shopfloor.pistep.maintain"];
function renderAt(path: string, duties: readonly string[] = ENGINEER) {
  return render(
    <PrincipalProvider duties={duties}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          {shopfloor.routes.map((r) => (
            <Route key={r.path} path={r.path} element={r.element} />
          ))}
        </Routes>
      </MemoryRouter>
    </PrincipalProvider>,
  );
}
const sent = (method: string, suffix: string) => calls.filter((c) => c.method === method && c.url.pathname.endsWith(suffix));
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

const base: PIStepTemplate = {
  id: STEP, code: "CURE-HOLD", version: 2, name: "Cure hold", categoryId: null, kind: "timer",
  spec: { category: "timer", text: "Let it cure.", durationSeconds: 900, blocking: true },
  status: "draft", rev: 3, createdBy: "u", createdAt: "2026-10-20T08:00:00Z", updatedBy: "u", updatedAt: "2026-10-21T08:00:00Z", publishedBy: null, publishedAt: null,
};

describe("step template validation", () => {
  const v = (spec: ReturnType<typeof newSpec>, extra = {}) => validateStep({ code: "A1", name: "Step", categoryId: "", spec, ...extra }, true);
  it("needs what each kind needs", () => {
    expect(v({ category: "timer", text: "w", durationSeconds: 60 }).input).toBeDefined();
    expect(v({ category: "timer", text: "w", durationSeconds: 0 }).spec?.join()).toMatch(/1 to 86400/);
    expect(v({ category: "calculation", text: "n", expression: "" }).spec?.join()).toMatch(/formula/);
    expect(v({ category: "calculation", text: "n", expression: "a +" }).spec?.length).toBeGreaterThan(0);
    expect(v({ category: "calculation", text: "n", expression: "a > 1" }).spec?.join()).toMatch(/number/);
    expect(v({ category: "branch", text: "q", condition: "a > 1" }).input).toBeDefined();
    expect(v({ category: "branch", text: "q", condition: "a + 1" }).spec?.join()).toMatch(/true or false/);
    expect(v({ category: "machine-readout", node: "", characteristic: { name: "T", type: "numeric" } }).spec?.join()).toMatch(/node/);
    expect(v({ category: "machine-readout", node: "n", characteristic: { name: "T", type: "numeric" }, tolerance: 1 }).spec?.join()).toMatch(/expected reading/);
    expect(validateStep({ code: "bad", name: "", categoryId: "", spec: newSpec("instruction-text") }, true).errors).toMatchObject({ code: expect.any(String), name: expect.any(String) });
  });
});

describe("PI step library pages", () => {
  it("lists templates with status and offers create to a maintainer", async () => {
    handler = (url) => (url.pathname.endsWith("/pi-step-templates") ? json(200, { items: [base, { ...base, id: "x", version: 1, status: "superseded" }] }) : undefined);
    const { container } = renderAt("/shopfloor/pi-step-templates");
    const table = await findTable({ name: "PI step library" });
    await waitFor(() => expect(table).toHaveTextContent("Cure hold"));
    expect(table).toHaveTextContent("Timer");
    expect(table).toHaveTextContent("Draft");
    expect(table).toHaveTextContent("Superseded");
    expect(screen.getByRole("link", { name: "New step template" })).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a draft and opens it", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(201, base);
      return url.pathname.endsWith(`/pi-step-templates/${STEP}`) ? json(200, base) : undefined;
    };
    const { container } = renderAt("/shopfloor/pi-step-templates/new");
    expect(pageViolations(container)).toEqual([]);
    fill("Code", "cure-hold");
    fill("Name", "Cure hold");
    fireEvent.change(screen.getByLabelText("Type of step"), { target: { value: "timer" } });
    fill("Duration (seconds)", "900");
    fill("What to do while it runs", "Let it cure.");
    fireEvent.click(screen.getByRole("button", { name: "Create draft" }));
    await waitFor(() => expect(sent("POST", "/pi-step-templates")).toHaveLength(1));
    expect(sent("POST", "/pi-step-templates")[0]!.body).toMatchObject({ code: "CURE-HOLD", name: "Cure hold", categoryId: null, spec: { category: "timer", durationSeconds: 900, blocking: true } });
    expect(await screen.findByRole("heading", { name: "CURE-HOLD version 2" })).toBeInTheDocument();
  });

  it("saves a draft with the rev as If-Match and publishes it", async () => {
    handler = (url, init) => {
      if (init.method === "PUT") return json(200, { ...base, rev: 4 });
      if (init.method === "POST") return json(200, { ...base, status: "published", rev: 4 });
      return url.pathname.endsWith(`/pi-step-templates/${STEP}`) ? json(200, base) : undefined;
    };
    renderAt(`/shopfloor/pi-step-templates/${STEP}`);
    fireEvent.click(await screen.findByRole("button", { name: "Save draft" }));
    await waitFor(() => expect(sent("PUT", `/pi-step-templates/${STEP}`)).toHaveLength(1));
    expect(new Headers(sent("PUT", `/pi-step-templates/${STEP}`)[0]!.init.headers).get("If-Match")).toBe('"3"');
    fireEvent.click(await screen.findByRole("button", { name: "Publish this version" }));
    await waitFor(() => expect(sent("POST", `/pi-step-templates/${STEP}:publish`)).toHaveLength(1));
  });

  it("shows a published version read-only with a way to make a new version", async () => {
    handler = (url) => (url.pathname.endsWith(`/pi-step-templates/${STEP}`) ? json(200, { ...base, status: "published" }) : undefined);
    renderAt(`/shopfloor/pi-step-templates/${STEP}`);
    expect(await screen.findByRole("button", { name: "Make a new version" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Save draft" })).toBeNull();
    expect(screen.getByLabelText("Name")).toHaveAttribute("readonly");
  });

  it("shows a refused change", async () => {
    handler = (url, init) => (init.method === "PUT" ? json(412, { code: "SF-0143", detail: "PI step template was changed by someone else" }) : url.pathname.endsWith(`/pi-step-templates/${STEP}`) ? json(200, base) : undefined);
    renderAt(`/shopfloor/pi-step-templates/${STEP}`);
    fireEvent.click(await screen.findByRole("button", { name: "Save draft" }));
    expect(await screen.findByText(/changed by someone else/)).toBeInTheDocument();
  });
});
