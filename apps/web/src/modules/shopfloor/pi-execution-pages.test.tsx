import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { checkAxeAA } from "../../shell/a11y";
import { PrincipalProvider } from "../../shell/principal";
import { resetOrderCache } from "../mfg-shared/orders";
import { resetResourceCache } from "../mfg-shared/resources";
import type { PIExecution, PIExecutionDetail } from "./api/client";
import shopfloor from "./module";

const ID = "0195f0a0-0000-7000-8000-0000000000e1";
type Reply = { status: number; body: unknown };
const json = (status: number, body: unknown): Reply => ({ status, body });
const shellRules = new Set(["page-has-heading-one", "landmark-unique", "region", "bypass"]);
let handler: (url: URL, init: RequestInit) => Reply | undefined;
let calls: { method: string; url: URL }[];

beforeEach(() => {
  calls = [];
  handler = () => undefined;
  resetOrderCache();
  resetResourceCache();
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, "http://localhost");
      calls.push({ method: init.method ?? "GET", url });
      const lookup = url.pathname.includes("/production/orders") || url.pathname.includes("/engineering/resources") || url.pathname.includes("/identity/") ? json(200, { items: [] }) : undefined;
      const reply = handler(url, init) ?? lookup ?? json(404, { code: "TEST-404", detail: `no fake for ${url.pathname}` });
      return new Response(JSON.stringify(reply.body), { status: reply.status, headers: { "Content-Type": "application/json" } });
    }),
  );
});
afterEach(() => vi.unstubAllGlobals());

function renderAt(path: string, duties: readonly string[]) {
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

const header: PIExecution = {
  id: ID, recipeId: "r", orderId: "o", resourceId: "res", piSheetId: null, sheetCode: "PUMP-ASSY", sheetVersion: 2, executedBy: "u1",
  startedAt: "2026-11-15T09:00:00Z", completedAt: "2026-11-15T09:30:00Z", stepCount: 2, deviationCount: 1, headHash: "a".repeat(64), recordHash: "b".repeat(64), createdAt: "2026-11-15T09:30:00Z",
};
const detail: PIExecutionDetail = {
  ...header,
  steps: [
    { seq: 1, key: "U/O/P/1", phasePath: "B/U/O/P", kind: "process-data-request", text: "Clearance.", template: null, status: "recorded", values: [{ name: "Clearance", type: "numeric", value: "0.19", unit: "mm", outOfLimit: true }, { name: "comment", type: "text", value: "re-zeroed" }], occurredAt: "2026-11-15T09:10:00Z", signerUser: "", signerAuthTime: "", signatureMeaning: "", comment: "re-zeroed", deviation: true, deviationReasons: ["Clearance is outside its limits"], prevHash: "c".repeat(64), hash: "d".repeat(64) },
    { seq: 2, key: "U/O/P/2", phasePath: "B/U/O/P", kind: "signature-request", text: "Sign.", template: null, status: "missing", values: [], occurredAt: "", signerUser: "", signerAuthTime: "", signatureMeaning: "", comment: "", deviation: true, deviationReasons: ["the step was not recorded"], prevHash: "d".repeat(64), hash: "e".repeat(64) },
  ],
};

describe("PI execution records", () => {
  it("lists records read only", async () => {
    handler = (url) => (url.pathname.endsWith("/pi-executions") ? json(200, { items: [header] }) : undefined);
    const { container } = renderAt("/shopfloor/pi-executions", ["shopfloor.view"]);
    const table = await findTable({ name: "PI execution records" });
    await waitFor(() => expect(table).toHaveTextContent("PUMP-ASSY version 2"));
    expect(table).toHaveTextContent("1");
    expect(screen.queryByRole("link", { name: /New/ })).toBeNull();
    expect(checkAxeAA(container).filter((v) => !shellRules.has(v.id))).toEqual([]);
  });

  it("shows the steps, deviations and checks the chain on request", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(200, { valid: true, checkedSteps: 2, brokenAtSeq: null, reason: "", recordHash: header.recordHash });
      return url.pathname.endsWith(`/pi-executions/${ID}`) ? json(200, detail) : undefined;
    };
    renderAt(`/shopfloor/pi-executions/${ID}`, ["shopfloor.view", "shopfloor.piexecution.verify"]);
    const table = await findTable({ name: "Steps of the record" });
    expect(table).toHaveTextContent("Clearance: 0.19 mm");
    expect(table).toHaveTextContent("Clearance is outside its limits; comment: re-zeroed");
    expect(table).toHaveTextContent("Not recorded");
    expect(screen.getByText("b".repeat(64))).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Check the record" }));
    expect(await screen.findByText("The record is intact")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST" && c.url.pathname.endsWith(`/pi-executions/${ID}:verify`))).toBe(true);
  });

  it("reports a record that does not match, and offers no check without the duty", async () => {
    handler = (url, init) => {
      if (init.method === "POST") return json(200, { valid: false, checkedSteps: 1, brokenAtSeq: 2, reason: "the step's content does not match its hash", recordHash: header.recordHash });
      return url.pathname.endsWith(`/pi-executions/${ID}`) ? json(200, detail) : undefined;
    };
    const first = renderAt(`/shopfloor/pi-executions/${ID}`, ["shopfloor.view", "shopfloor.piexecution.verify"]);
    fireEvent.click(await screen.findByRole("button", { name: "Check the record" }));
    expect(await screen.findByText("The record does not match what was sealed")).toBeInTheDocument();
    expect(screen.getByText(/content does not match its hash \(step 2\)/)).toBeInTheDocument();
    first.unmount();
    renderAt(`/shopfloor/pi-executions/${ID}`, ["shopfloor.view"]);
    await findTable({ name: "Steps of the record" });
    expect(screen.queryByRole("button", { name: "Check the record" })).toBeNull();
  });
});
