import { findTable } from "../../test-utils";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { PrincipalProvider } from "../../shell/principal";
import type { RfidRead, RfidRule, TransportOrder, Work } from "./api/client";
import { heldByStation, identityText, validateRule } from "./AutomationPages";
import warehouse from "./module";
import { json, pageViolations, renderAt, server, useFakeServer } from "./testServer";

useFakeServer();

const WH = "0190b2a4-7c1e-7d3a-9b5f-2a4c6e8f0a11";
const RULE = "0190f0a0-0000-7000-8000-000000000701";
const ORDER = "0190f0a0-0000-7000-8000-000000000711";
const WORK = "0190f0a0-0000-7000-8000-000000000501";
const SSCC = "006141411000000008";
const MIA = "0190f0a0-0000-7000-8000-000000000e01";

const rule: RfidRule = {
  id: RULE,
  readerId: "dock-1",
  action: "RECEIVE_PALLET",
  warehouseId: WH,
  postedBy: "rfid-gateway",
  createdBy: "admin",
  createdAt: "2026-10-27T08:00:00Z",
  version: 3,
};

const read: RfidRead = {
  id: "0190f0a0-0000-7000-8000-000000000721",
  readerId: "dock-1",
  antenna: 2,
  epc: "3114141000000008",
  scheme: "SSCC-96",
  key: SSCC,
  rssi: -52,
  readAt: "2026-10-27T10:00:00Z",
  action: "RECEIVE_PALLET",
  actionOutcome: "DONE",
  actionDetail: "received 100 BOLT",
};

const order = (over: Partial<TransportOrder> = {}): TransportOrder => ({
  id: ORDER,
  workId: WORK,
  lineNo: 1,
  station: "PLC",
  loadUnit: SSCC,
  fromLocation: "INFEED-1",
  toLocation: "ASRS-01",
  priority: 5,
  status: "SENT",
  createdBy: "super",
  createdAt: "2026-10-27T09:00:00Z",
  ...over,
});

function fill(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("RFID rules", () => {
  it("lists the rules and opens one to edit", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/warehouse/rfid-rules" ? json(200, { items: [rule] }) : undefined);
    const { container } = renderAt("/warehouse/rfid-rules");
    const table = await findTable({ name: "RFID rules" });
    await waitFor(() => expect(table).toHaveTextContent("dock-1"));
    expect(table).toHaveTextContent("Every antenna");
    expect(table).toHaveTextContent("Receive the pallet at the dock");
    expect(table).toHaveTextContent("rfid-gateway");
    expect(container.textContent).not.toContain(WH);
    expect(pageViolations(container)).toEqual([]);
    fireEvent.change(screen.getByLabelText("Reader"), { target: { value: "dock-1" } });
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("readerId")).toBe("dock-1"));
  });

  it("validates a rule like the service does", () => {
    const base = { readerId: "dock-1", antenna: "", action: "RECEIVE_PALLET" as const, warehouseId: WH, locationCode: "", postedBy: "gw" };
    expect(validateRule(base).input).toEqual({ readerId: "dock-1", action: "RECEIVE_PALLET", warehouseId: WH, postedBy: "gw" });
    expect(validateRule({ ...base, antenna: "2", action: "CONFIRM_PUTAWAY", locationCode: "A-01" }).input).toEqual({
      readerId: "dock-1",
      antenna: 2,
      action: "CONFIRM_PUTAWAY",
      warehouseId: WH,
      locationCode: "A-01",
      postedBy: "gw",
    });
    const { errors } = validateRule({ readerId: " ", antenna: "300", action: "CONFIRM_PUTAWAY", warehouseId: "x", locationCode: "", postedBy: "" });
    expect(Object.keys(errors).sort()).toEqual(["antenna", "locationCode", "postedBy", "readerId", "warehouseId"]);
  });

  it("creates a rule and returns to the list", async () => {
    server.handler = (url, init) => {
      if (url.pathname === "/api/v1/warehouse/rfid-rules" && init.method === "POST") return json(201, rule);
      if (url.pathname === "/api/v1/warehouse/rfid-rules") return json(200, { items: [rule] });
      return undefined;
    };
    const { container } = renderAt("/warehouse/rfid-rules/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create rule" }));
    expect((await screen.findAllByText("Enter the reader, up to 64 characters.")).length).toBeGreaterThan(0);
    fill("Reader", "dock-1");
    fill("Warehouse", WH);
    fill("Reads posted as", "rfid-gateway");
    fireEvent.click(screen.getByRole("button", { name: "Create rule" }));
    expect(await findTable({ name: "RFID rules" })).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ readerId: "dock-1", action: "RECEIVE_PALLET", warehouseId: WH, postedBy: "rfid-gateway" });
  });

  it("edits a rule with PUT and shows a conflict in words", async () => {
    let conflict = true;
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/warehouse/rfid-rules/${RULE}` && init.method === "PUT") {
        return conflict ? json(409, { code: "WMS-RFID-RULE-EXISTS", detail: "reader dock-1 has a CONFIRM_PUTAWAY rule for that antenna already" }) : json(200, rule);
      }
      if (url.pathname === "/api/v1/warehouse/rfid-rules") return json(200, { items: [rule] });
      return undefined;
    };
    renderAt(`/warehouse/rfid-rules/${RULE}`);
    expect(await screen.findByRole("heading", { name: "Rule for reader dock-1" })).toBeInTheDocument();
    expect((screen.getByLabelText("Reader") as HTMLInputElement).value).toBe("dock-1");
    fill("What a read does", "CONFIRM_PUTAWAY");
    fill("Location", "A-01");
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await screen.findByText("This reader already has that rule")).toBeInTheDocument();
    conflict = false;
    fireEvent.click(screen.getByRole("button", { name: "Save rule" }));
    expect(await findTable({ name: "RFID rules" })).toBeInTheDocument();
    const puts = server.calls.filter((c) => c.method === "PUT");
    expect(new Headers(puts.at(-1)?.init.headers).get("If-Match")).toBe('"3"');
    expect(puts.at(-1)?.body).toEqual({ readerId: "dock-1", action: "CONFIRM_PUTAWAY", warehouseId: WH, locationCode: "A-01", postedBy: "rfid-gateway" });
  });
});

describe("RFID reads", () => {
  it("lists reads with what the rule did, and filters", async () => {
    server.handler = (url) => (url.pathname === "/api/v1/warehouse/rfid-reads" ? json(200, { items: [read, { ...read, id: "r2", action: undefined, actionOutcome: undefined, actionDetail: undefined }] }) : undefined);
    const { container } = renderAt("/warehouse/rfid-reads");
    const table = await findTable({ name: "RFID reads" });
    await waitFor(() => expect(table).toHaveTextContent(`SSCC-96 ${SSCC}`));
    expect(table).toHaveTextContent("Done");
    expect(table).toHaveTextContent("received 100 BOLT");
    expect(table).toHaveTextContent("No rule ran");
    expect(table).toHaveTextContent("-52 dBm");
    expect(pageViolations(container)).toEqual([]);
    fill("Pallet number or item number", SSCC);
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("key")).toBe(SSCC));
  });

  it("shows the GS1 TDS 2.3 schemes with their key and serial", async () => {
    const cpi: RfidRead = { ...read, id: "r3", epc: "3D76451FD75411DEF6B4CC00000003039000", scheme: "CPI-var", key: "95211415PQ7/Z43", serial: "12345", actionOutcome: "SKIPPED", actionDetail: "the tag is CPI-var, not a pallet's SSCC" };
    const adi: RfidRead = { ...read, id: "r4", epc: "3B0E0CF5E76C9047759AD00373DC7602E7200", scheme: "ADI-var", key: "35962PQ7VZ4", serial: "M37GXB92", action: undefined, actionOutcome: undefined, actionDetail: undefined };
    server.handler = (url) => (url.pathname === "/api/v1/warehouse/rfid-reads" ? json(200, { items: [cpi, adi] }) : undefined);
    renderAt("/warehouse/rfid-reads");
    const table = await findTable({ name: "RFID reads" });
    await waitFor(() => expect(table).toHaveTextContent("CPI-var 95211415PQ7/Z43 / 12345"));
    expect(table).toHaveTextContent("ADI-var 35962PQ7VZ4 / M37GXB92");
    expect(table).toHaveTextContent("the tag is CPI-var, not a pallet's SSCC");
  });
});

describe("Automation orders", () => {
  it("lists orders, filters, and opens one", async () => {
    server.handler = (url) => {
      if (url.pathname === "/api/v1/warehouse/automation/transport-orders") return json(200, { items: [order()] });
      if (url.pathname === `/api/v1/warehouse/automation/transport-orders/${ORDER}`) return json(200, order());
      return undefined;
    };
    const { container } = renderAt("/warehouse/automation-orders");
    const table = await findTable({ name: "Automation orders" });
    await waitFor(() => expect(table).toHaveTextContent("INFEED-1 to ASRS-01"));
    expect(table).toHaveTextContent("Sent");
    expect(pageViolations(container)).toEqual([]);
    fill("Status", "FAULT");
    await waitFor(() => expect(server.calls.at(-1)?.url.searchParams.get("status")).toBe("FAULT"));
    fireEvent.click(within(table).getByRole("button", { name: new RegExp(SSCC) }));
    expect(await screen.findByRole("heading", { name: `Order for pallet ${SSCC}` })).toBeInTheDocument();
  });

  it("cancels an order the PLC may hold only with the move cleared, and sends plcCleared", async () => {
    let cancelled = false;
    server.handler = (url, init) => {
      if (url.pathname.endsWith(`${ORDER}:cancel`) && init.method === "POST") {
        cancelled = true;
        return json(200, order({ status: "CANCELLED", reason: "wrong pallet" }));
      }
      if (url.pathname === `/api/v1/warehouse/automation/transport-orders/${ORDER}`) return json(200, order());
      return undefined;
    };
    const { container } = renderAt(`/warehouse/automation-orders/${ORDER}`);
    expect(await screen.findByRole("heading", { name: `Order for pallet ${SSCC}` })).toBeInTheDocument();
    expect(container.textContent).not.toContain(ORDER);
    expect(pageViolations(container)).toEqual([]);
    fill("Reason", "wrong pallet");
    fireEvent.click(screen.getByRole("button", { name: "Cancel order" }));
    expect((await screen.findAllByText("Confirm that the move is cleared on the PLC before cancelling.")).length).toBeGreaterThan(0);
    expect(cancelled).toBe(false);
    fireEvent.click(screen.getByLabelText(/I cleared this move on the PLC/));
    fireEvent.click(screen.getByRole("button", { name: "Cancel order" }));
    await waitFor(() => expect(screen.getByText("Cancelled")).toBeInTheDocument());
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ reason: "wrong pallet", plcCleared: true });
    expect(screen.queryByRole("button", { name: "Cancel order" })).not.toBeInTheDocument();
  });

  it("needs no plcCleared for an order the PLC already gave up, and offers no cancel for an arrived one", async () => {
    server.handler = (url) => (url.pathname.endsWith(ORDER) ? json(200, order({ status: "FAULT", reason: "PLC fault: E12" })) : undefined);
    const first = renderAt(`/warehouse/automation-orders/${ORDER}`);
    expect(await screen.findByText("PLC fault: E12")).toBeInTheDocument();
    expect(screen.queryByLabelText(/I cleared this move on the PLC/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel order" })).toBeInTheDocument();
    first.unmount();
    server.handler = (url) => (url.pathname.endsWith(ORDER) ? json(200, order({ status: "ARRIVED" })) : undefined);
    renderAt(`/warehouse/automation-orders/${ORDER}`);
    expect(await screen.findByRole("heading", { name: `Order for pallet ${SSCC}` })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Cancel order" })).not.toBeInTheDocument();
  });

  it("explains a refused cancel", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(409, { code: "WMS-AUTO-ORDER-STATE", detail: "the order has arrived; its move is done" });
      return url.pathname.endsWith(ORDER) ? json(200, order()) : undefined;
    };
    renderAt(`/warehouse/automation-orders/${ORDER}`);
    await screen.findByRole("heading", { name: `Order for pallet ${SSCC}` });
    fill("Reason", "x");
    fireEvent.click(screen.getByLabelText(/I cleared this move on the PLC/));
    fireEvent.click(screen.getByRole("button", { name: "Cancel order" }));
    expect(await screen.findByText("The order could not be cancelled")).toBeInTheDocument();
  });
});

describe("Hand a station's work to a person", () => {
  const stationWork: Work = {
    id: WORK,
    warehouseId: WH,
    workType: "PURCHASE_PUTAWAY",
    status: "IN_PROGRESS",
    priority: 20,
    assignedUser: "automation:PLC",
    assignedAt: "2026-10-27T09:00:00Z",
    version: 4,
    createdAt: "2026-10-27T08:00:00Z",
    updatedAt: "2026-10-27T09:00:00Z",
    lines: [
      { lineNo: 1, itemId: "PUMP", direction: "PUT", qty: "1", confirmedQty: "1", weight: "1", volume: "0", lps: 0, status: "COMPLETED" },
      { lineNo: 2, itemId: "PUMP", direction: "PUT", qty: "1", confirmedQty: "0", weight: "1", volume: "0", lps: 0, status: "OPEN", reason: "PLC fault: E12 jam at infeed" },
    ],
  };

  const mountWork = (duties: string[]) =>
    render(
      <PrincipalProvider duties={duties}>
        <MemoryRouter initialEntries={[`/warehouse/work/${WORK}`]}>
          <Routes>
            {warehouse.routes.map((r) => (
              <Route key={r.path} path={r.path} element={r.element} />
            ))}
          </Routes>
        </MemoryRouter>
      </PrincipalProvider>,
    );

  it("knows a work a station holds", () => {
    expect(heldByStation(stationWork)).toBe(true);
    expect(heldByStation({ ...stationWork, status: "COMPLETED" })).toBe(false);
    expect(heldByStation({ ...stationWork, assignedUser: "mia" })).toBe(false);
    expect(identityText("automation:PLC")).toBe("Station PLC");
    expect(identityText("rfid:dock-1")).toBe("Reader dock-1");
  });

  it("offers the hand-over to a supervisor and sends the person and the reason with If-Match", async () => {
    const handed: Work = { ...stationWork, assignedUser: MIA, version: 5 };
    server.handler = (url, init) => {
      if (url.pathname === `/api/v1/warehouse/work/${WORK}:reassign` && init.method === "POST") return json(200, handed);
      if (url.pathname === `/api/v1/warehouse/work/${WORK}`) return json(200, stationWork);
      if (url.pathname === "/api/v1/identity/users") return json(200, { items: [{ userId: MIA, displayName: "Mia Ortiz", email: "mia@example.com", status: "active" }] });
      return undefined;
    };
    const { container } = mountWork(["warehouse.work.supervise"]);
    expect(await screen.findByRole("heading", { name: "Hand the work to a person" })).toBeInTheDocument();
    expect(screen.getByText("Station PLC")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Hand to this person" }));
    expect((await screen.findAllByText("Choose the person who takes the work.")).length).toBeGreaterThan(0);
    fill("Person", MIA);
    fill("Reason", "PLC fault at infeed");
    fireEvent.click(screen.getByRole("button", { name: "Hand to this person" }));
    expect(await screen.findByText("The work now belongs to the person you chose.")).toBeInTheDocument();
    const post = server.calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ toUser: MIA, reason: "PLC fault at infeed" });
    expect(new Headers(post?.init.headers).get("If-Match")).toBe('"4"');
    expect(screen.queryByRole("heading", { name: "Hand the work to a person" })).not.toBeInTheDocument();
  });

  it("says why an order still with the PLC blocks the hand-over", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(409, { code: "WMS-AUTO-ORDER-STATE", detail: "an automation order of the work is still with the PLC" });
      return url.pathname === `/api/v1/warehouse/work/${WORK}` ? json(200, stationWork) : undefined;
    };
    mountWork(["warehouse.work.supervise"]);
    await screen.findByRole("heading", { name: "Hand the work to a person" });
    fill("Person", MIA);
    fill("Reason", "fault");
    fireEvent.click(screen.getByRole("button", { name: "Hand to this person" }));
    expect(await screen.findByText("An order of this work is still with the PLC")).toBeInTheDocument();
  });

  it("hides the hand-over from a user without the supervise duty", async () => {
    server.handler = (url) => (url.pathname === `/api/v1/warehouse/work/${WORK}` ? json(200, stationWork) : undefined);
    mountWork(["warehouse.inquiry.view"]);
    expect(await screen.findByRole("heading", { name: "Purchase putaway work" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Hand the work to a person" })).not.toBeInTheDocument();
  });
});
