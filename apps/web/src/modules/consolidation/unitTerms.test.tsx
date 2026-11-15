import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearResolvedOptions, setLookupFetcher } from "../../shell/lookup/entities";
import consolidation from "./module";
import { emptyTerms, validateTerms } from "./UnitTermsPage";

const GROUP = "g-1";
const UNIT = "u-2";
const groupDetail = {
  id: GROUP,
  code: "CRP",
  name: "Cedar Ridge Pumps group",
  currency: "USD",
  createdBy: "u-9",
  createdAt: "2026-01-01T00:00:00Z",
  units: [{ id: UNIT, legalEntityId: "le-2", ledgerId: "CRP-DE-GL", currency: "EUR", currentPercent: "80.0000", ownership: [] }],
};

let calls: { method: string; path: string; body: unknown }[];
let stored: unknown[];

beforeEach(() => {
  clearResolvedOptions();
  setLookupFetcher(async (url) => {
    const id = decodeURIComponent(url.split("/").pop() ?? "");
    return new Response(JSON.stringify({ id, name: "Cedar Ridge Pumps Europe GmbH", code: "DE" }), { status: 200, headers: { "Content-Type": "application/json" } });
  });
  calls = [];
  stored = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      const method = init.method ?? "GET";
      calls.push({ method, path: url.pathname, body: init.body ? JSON.parse(String(init.body)) : undefined });
      const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
      if (url.pathname.endsWith(`/groups/${GROUP}/unit-terms`)) return json(200, { items: stored });
      if (url.pathname.endsWith(`/groups/${GROUP}/units/${UNIT}/terms`) && method === "PUT") return json(200, { unitId: UNIT, ...JSON.parse(String(init.body)) });
      if (url.pathname.endsWith(`/groups/${GROUP}`)) return json(200, groupDetail);
      return json(404, { code: "TEST-404", detail: `no fake for ${url.pathname}` });
    }),
  );
});
afterEach(() => {
  vi.unstubAllGlobals();
  setLookupFetcher(null);
  clearResolvedOptions();
});

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        {consolidation.routes.map((route) => (
          <Route key={route.path} path={route.path} element={route.element} />
        ))}
      </Routes>
    </MemoryRouter>,
  );
}

describe("validateTerms", () => {
  it("keeps amounts as decimal strings and rejects what the service rejects", () => {
    const ok = validateTerms({ ...emptyTerms, investmentCost: "2400000.00", shareCapital: "1500000.00", preAcquisitionRe: "-12.50", historicalRate: "1.0840", icReceivableAccount: " 1300 " });
    expect(ok.errors).toEqual({});
    expect(ok.request).toMatchObject({ investmentCost: "2400000.00", preAcquisitionRe: "-12.50", historicalRate: "1.0840", icReceivableAccount: "1300", method: "full" });
    const bad = validateTerms({ ...emptyTerms, investmentCost: "-1", shareCapital: "1e3", preAcquisitionRe: "", historicalRate: "0", fxGainLossAccount: "bad account!" });
    expect(Object.keys(bad.errors).sort()).toEqual(["fxGainLossAccount", "historicalRate", "investmentCost", "preAcquisitionRe", "shareCapital"]);
    expect(bad.request).toBeUndefined();
  });
});

describe("UnitTermsPage", () => {
  it("shows the unit by name, saves the terms and returns to the group", async () => {
    renderAt(`/consolidation/groups/${GROUP}/units/${UNIT}/terms`);
    expect(await screen.findByText(/Cedar Ridge Pumps Europe GmbH/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Cost of the investment"), { target: { value: "2400000.00" } });
    fireEvent.change(screen.getByLabelText("Share capital at acquisition"), { target: { value: "1500000.00" } });
    fireEvent.change(screen.getByLabelText("Retained earnings at acquisition"), { target: { value: "620000.00" } });
    fireEvent.change(screen.getByLabelText("Exchange rate on the acquisition day"), { target: { value: "1.0840" } });
    fireEvent.click(screen.getByRole("button", { name: "Store terms" }));
    await waitFor(() => expect(calls.some((c) => c.method === "PUT")).toBe(true));
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.path).toBe(`/api/v1/consol/groups/${GROUP}/units/${UNIT}/terms`);
    expect(put.body).toMatchObject({ investmentCost: "2400000.00", shareCapital: "1500000.00", preAcquisitionRe: "620000.00", historicalRate: "1.0840", method: "full", sellerIsSubsidiary: false, icNetInvestment: false });
  });

  it("refuses an empty form without calling the service and names what to fix", async () => {
    renderAt(`/consolidation/groups/${GROUP}/units/${UNIT}/terms`);
    await screen.findByText(/Cedar Ridge Pumps Europe GmbH/);
    fireEvent.click(screen.getByRole("button", { name: "Store terms" }));
    expect((await screen.findAllByText(/Enter the cost of the investment/)).length).toBeGreaterThan(0);
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("opens with the stored terms filled in", async () => {
    stored = [{ unitId: UNIT, investmentCost: "2400000.00", shareCapital: "1500000.00", preAcquisitionRe: "620000.00", historicalRate: "1.0840", sellerIsSubsidiary: true, icNetInvestment: false, method: "equity" }];
    renderAt(`/consolidation/groups/${GROUP}/units/${UNIT}/terms`);
    await screen.findByDisplayValue("2400000.00");
    expect(screen.getByLabelText("Exchange rate on the acquisition day")).toHaveValue("1.0840");
    expect(screen.getByRole("button", { name: "Save terms" })).toBeInTheDocument();
    expect((screen.getByLabelText("Consolidation method") as HTMLSelectElement).value).toBe("equity");
  });
});
