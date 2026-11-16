import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { findTable } from "../../test-utils";
import type { CarrierService } from "./api/client";
import { validateService } from "./CarrierServicesPage";
import transport from "./module";

const CARRIER = "0195f0a0-0000-7000-8000-0000000000c1";
const GROUND: CarrierService = { id: "0195f0a0-0000-7000-8000-0000000000e1", carrierId: CARRIER, code: "GROUND", name: "Ground freight", mode: "LTL", active: true };
const OLD: CarrierService = { ...GROUND, id: "0195f0a0-0000-7000-8000-0000000000e2", code: "2DAY", name: "Two-day parcel", mode: "PARCEL", active: false };

type Reply = { status: number; body: unknown };
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
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
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
        {transport.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
      <Where />
    </MemoryRouter>,
  );
}

describe("carrier services", () => {
  it("checks the carrier, code and name", () => {
    expect(validateService({ carrierId: CARRIER, code: "ground", name: "Ground" })).toEqual({});
    expect(Object.keys(validateService({ carrierId: "x", code: "", name: " " })).sort()).toEqual(["carrierId", "code", "name"]);
  });

  it("lists the services of every carrier with their mode and status", async () => {
    handler = (url) => (url.pathname === "/api/v1/transport/carrier-services" ? { status: 200, body: { items: [GROUND, OLD] } } : undefined);
    const { container } = renderAt("/transport/carrier-services");
    const table = await findTable({ name: "Carrier services" });
    await waitFor(() => expect(table).toHaveTextContent("Ground freight"));
    expect(table).toHaveTextContent("GROUND");
    expect(table).toHaveTextContent("Retired");
    expect(screen.getByRole("link", { name: "New carrier service" })).toHaveAttribute("href", "/transport/carrier-services/new");
    expect(pageViolations(container)).toEqual([]);
  });

  it("creates a service with one Idempotency-Key and returns to the list", async () => {
    handler = (url, init) => (init.method === "POST" && url.pathname.endsWith("/carrier-services") ? { status: 201, body: GROUND } : { status: 200, body: { items: [GROUND] } });
    renderAt(`/transport/carrier-services/new?carrierId=${CARRIER}`);
    fireEvent.click(screen.getByRole("button", { name: "Save service" }));
    expect((await screen.findAllByText(/Use 1 to 20 capital letters/)).length).toBeGreaterThan(0);
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "ground" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Ground freight" } });
    fireEvent.change(screen.getByLabelText("Mode"), { target: { value: "LTL" } });
    fireEvent.click(screen.getByRole("button", { name: "Save service" }));
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/transport/carrier-services"));
    const post = calls.find((c) => c.method === "POST")!;
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toBeTruthy();
    expect(post.body).toEqual({ carrierId: CARRIER, code: "GROUND", name: "Ground freight", mode: "LTL" });
  });

  it("puts a code the carrier already uses on the code field", async () => {
    handler = () => ({ status: 409, body: { code: "TMS-0140", detail: "this carrier already has a service with that code" } });
    renderAt(`/transport/carrier-services/new?carrierId=${CARRIER}`);
    fireEvent.change(screen.getByLabelText("Code"), { target: { value: "GROUND" } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Again" } });
    fireEvent.click(screen.getByRole("button", { name: "Save service" }));
    expect((await screen.findAllByText("This carrier already has a service with this code.")).length).toBeGreaterThan(0);
    expect(screen.getByTestId("location")).toHaveTextContent("/transport/carrier-services/new");
  });
});
