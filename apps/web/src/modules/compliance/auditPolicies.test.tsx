import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findAlert, findTable } from "../../test-utils";
import type { AuditPolicy } from "./api/client";
import { validatePolicy } from "./AuditPoliciesPage";
import { emptySpec } from "./auditSpec";
import compliance from "./module";

const ID = "33333333-0000-4000-8000-000000000003";
const policy: AuditPolicy = {
  policyId: ID,
  code: "GIFT-CARDS",
  name: "Gift cards in expenses",
  ruleType: "KEYWORD",
  spec: { type: "KEYWORD", keywords: ["gift card"] },
  active: true,
  version: 2,
  createdBy: "ana",
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-02T00:00:00Z",
};

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
        {compliance.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

describe("audit policies", () => {
  it("checks the code, the name and the rule together", () => {
    expect(validatePolicy("gift-cards", "Gift cards", { ...emptySpec, type: "KEYWORD", words: "gift card" }).input).toEqual({
      code: "GIFT-CARDS",
      name: "Gift cards",
      spec: { type: "KEYWORD", keywords: ["gift card"] },
    });
    expect(Object.keys(validatePolicy("x", "", { ...emptySpec, type: "KEYWORD" }).errors).sort()).toEqual(["code", "name", "words"]);
    expect(validatePolicy("", "Renamed", { ...emptySpec }, false).errors).toEqual({});
  });

  it("lists policies with their rule, version and status", async () => {
    handler = (url) => (url.pathname === "/api/v1/compliance/audit-policies" ? { status: 200, body: { items: [policy, { ...policy, policyId: "x", code: "OLD", name: "Retired rule", active: false }] } } : undefined);
    const { container } = renderAt("/compliance/audit/policies");
    const table = await findTable({ name: "Audit policies" });
    await waitFor(() => expect(table).toHaveTextContent("Gift cards in expenses"));
    expect(table).toHaveTextContent("Keyword");
    expect(table).toHaveTextContent("v2");
    expect(table).toHaveTextContent("Retired");
    expect(screen.getByRole("link", { name: "New audit policy" })).toHaveAttribute("href", "/compliance/audit/policies/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a policy with its rule under one Idempotency-Key", async () => {
    handler = (url, init) => (init.method === "POST" && url.pathname.endsWith("/audit-policies") ? { status: 201, body: policy } : { status: 200, body: { items: [policy] } });
    renderAt("/compliance/audit/policies/new");
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    expect((await screen.findAllByText(/Use 2 to 32 capital letters/)).length).toBeGreaterThan(0);
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "gift-cards" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Gift cards in expenses" } });
    fireEvent.change(screen.getByRole("combobox", { name: "Rule" }), { target: { value: "KEYWORD" } });
    fireEvent.change(screen.getByLabelText("Keywords"), { target: { value: "gift card, alcohol" } });
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/compliance/audit/policies"));
    const post = calls.find((c) => c.method === "POST")!;
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({ code: "GIFT-CARDS", name: "Gift cards in expenses", spec: { type: "KEYWORD", keywords: ["gift card", "alcohol"] } });
  });

  it("puts a code that is already used on the code field", async () => {
    handler = () => ({ status: 409, body: { code: "AUD-0721", detail: "an audit policy with this code already exists" } });
    renderAt("/compliance/audit/policies/new");
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "GIFT-CARDS" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Again" } });
    fireEvent.click(screen.getByRole("button", { name: "Save policy" }));
    expect((await screen.findAllByText("This code is already used by another audit policy.")).length).toBeGreaterThan(0);
  });

  it("changes only what changed: a rename sends no spec, a new rule sends it with If-Match", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 200, body: { ...policy, version: 3 }, headers: { ETag: '"3"' } };
      if (url.pathname === `/api/v1/compliance/audit-policies/${ID}`) return { status: 200, body: policy, headers: { ETag: '"2"' } };
      return { status: 200, body: { items: [policy] } };
    };
    renderAt(`/compliance/audit/policies/${ID}`);
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Gift cards and alcohol" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/compliance/audit/policies"));
    let patch = calls.find((c) => c.method === "PATCH")!;
    expect(new Headers(patch.init.headers).get("If-Match")).toBe('"2"');
    expect(patch.body).toEqual({ name: "Gift cards and alcohol", active: true });
  });

  it("sends the new rule when it changed", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 200, body: { ...policy, version: 3 } };
      return url.pathname === `/api/v1/compliance/audit-policies/${ID}` ? { status: 200, body: policy, headers: { ETag: '"2"' } } : { status: 200, body: { items: [policy] } };
    };
    renderAt(`/compliance/audit/policies/${ID}`);
    fireEvent.change(await screen.findByLabelText("Keywords"), { target: { value: "gift card, voucher" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PATCH")).toBe(true));
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({ name: "Gift cards in expenses", active: true, spec: { type: "KEYWORD", keywords: ["gift card", "voucher"] } });
  });

  it("says when someone else changed the policy first", async () => {
    handler = (url, init) => {
      if (init.method === "PATCH") return { status: 412, body: { code: "SOD-1109", detail: "the audit policy changed; reload it" } };
      return url.pathname === `/api/v1/compliance/audit-policies/${ID}` ? { status: 200, body: policy, headers: { ETag: '"2"' } } : undefined;
    };
    renderAt(`/compliance/audit/policies/${ID}`);
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Renamed" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect((await findAlert()).textContent).toContain("Someone else changed this policy");
  });
});
