import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import type { DataDesignation } from "./api/client";
import { DataDesignationPanel, reasonError } from "./DataDesignationPanel";

const ROW: DataDesignation = {
  designationId: "d-1",
  scope: "RECORD",
  entityType: "project",
  recordId: "p-1",
  class: "CUI",
  reason: "contract data",
  designatedBy: "ana",
  designatedAt: "2026-11-01T00:00:00Z",
  version: 3,
};

interface Fake {
  effective: { class: string; source: string };
  rows: DataDesignation[];
}

let fake: Fake;
let calls: { method: string; path: string; headers: Headers; body: unknown }[];

beforeEach(() => {
  calls = [];
  fake = { effective: { class: "NONE", source: "DEFAULT" }, rows: [] };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const method = init.method ?? "GET";
      calls.push({ method, path: url.pathname, headers: new Headers(init.headers), body: init.body ? JSON.parse(String(init.body)) : undefined });
      const reply = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
        new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });
      if (url.pathname.endsWith(":resolve")) return reply({ items: [{ entityType: "project", recordId: "p-1", ...fake.effective }] });
      if (url.pathname.endsWith("/history")) return reply({ items: [{ auditId: "a-1", action: "DESIGNATE", scope: "RECORD", actor: "ana", occurredAt: "2026-11-01T00:00:00Z", reason: "contract data", after: { class: "CUI" } }] });
      if (method === "GET") return reply({ items: fake.rows });
      return reply({ ...ROW, version: ROW.version + 1 }, method === "POST" ? 201 : 200, { ETag: '"4"' });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function mount(duties: string[]) {
  return render(
    <PrincipalProvider duties={duties}>
      <main>
        <h1>Project</h1>
        <DataDesignationPanel entityType="project" recordId="p-1" />
      </main>
    </PrincipalProvider>,
  );
}

describe("reasonError", () => {
  it("needs a reason of at most 500 characters", () => {
    expect([reasonError(""), reasonError("   "), reasonError("ok"), reasonError("x".repeat(501))]).toEqual(["Give a reason.", "Give a reason.", undefined, "Use at most 500 characters."]);
  });
});

describe("DataDesignationPanel", () => {
  it("renders nothing without the read duty and calls nothing", () => {
    const { container } = mount([]);
    expect(container.querySelector("h3")).toBeNull();
    expect(calls).toEqual([]);
  });

  it("hides the change action without the write duty", async () => {
    fake.effective = { class: "CUI", source: "RECORD" };
    fake.rows = [ROW];
    mount(["datadesignation.read"]);
    await screen.findByText("Designated on this record.");
    expect(screen.queryByRole("button", { name: /Designate as CUI|Downgrade/ })).toBeNull();
  });

  it("shows inherited state when the legal entity is designated", async () => {
    fake.effective = { class: "CUI", source: "LEGAL_ENTITY" };
    mount(["datadesignation.read", "datadesignation.write"]);
    expect(await screen.findByText(/Inherited: this record is CUI because its legal entity is designated/)).toBeTruthy();
  });

  it("designates with a required reason and an idempotency key", async () => {
    mount(["datadesignation.read", "datadesignation.write"]);
    fireEvent.click(await screen.findByRole("button", { name: "Designate as CUI" }));
    fireEvent.click(screen.getAllByRole("button", { name: "Designate as CUI" })[1]!);
    expect((await screen.findAllByText("Give a reason.")).length).toBeGreaterThan(0);
    expect(calls.some((c) => c.method === "POST" && !c.path.endsWith(":resolve"))).toBe(false);
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "export controlled" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Designate as CUI" })[1]!);
    await waitFor(() => expect(calls.some((c) => c.method === "POST" && c.path === "/api/v1/compliance/data-designations")).toBe(true));
    const post = calls.find((c) => c.method === "POST" && c.path === "/api/v1/compliance/data-designations")!;
    expect(post.body).toEqual({ scope: "RECORD", entityType: "project", recordId: "p-1", class: "CUI", reason: "export controlled" });
    expect(post.headers.get("Idempotency-Key")).toBeTruthy();
  });

  it("downgrades with If-Match and the reason, keeping the row", async () => {
    fake.effective = { class: "CUI", source: "RECORD" };
    fake.rows = [ROW];
    mount(["datadesignation.read", "datadesignation.write"]);
    fireEvent.click(await screen.findByRole("button", { name: "Downgrade the designation" }));
    fireEvent.change(screen.getByLabelText(/Reason/), { target: { value: "released" } });
    fireEvent.click(screen.getByRole("button", { name: "Downgrade" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    const patch = calls.find((c) => c.method === "PATCH")!;
    expect(patch.path).toBe("/api/v1/compliance/data-designations/d-1");
    expect(patch.headers.get("If-Match")).toBe('"3"');
    expect(patch.body).toEqual({ class: "NONE", reason: "released" });
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
  });

  it("shows the history on request", async () => {
    fake.effective = { class: "CUI", source: "RECORD" };
    fake.rows = [ROW];
    mount(["datadesignation.read"]);
    fireEvent.click(await screen.findByRole("button", { name: "Show designation history" }));
    expect(await screen.findByText("contract data")).toBeTruthy();
  });

  it("has no accessibility violations with the dialog open", async () => {
    mount(["datadesignation.read", "datadesignation.write"]);
    fireEvent.click(await screen.findByRole("button", { name: "Designate as CUI" }));
    await screen.findByRole("alertdialog");
    const shell = new Set(["region", "landmark-unique", "bypass", "page-has-heading-one"]);
    expect(checkAxeAA(document.body).filter((v) => !shell.has(v.id))).toEqual([]);
  });
});
