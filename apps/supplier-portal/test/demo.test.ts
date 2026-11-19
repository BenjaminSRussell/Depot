import { beforeEach, describe, expect, it } from "vitest";
import { installDemo } from "../src/demo";

async function call(path: string, method = "GET") {
  const res = await fetch(path, { method });
  return { status: res.status, body: (await res.json()) as Record<string, unknown> };
}

describe("supplier portal demo mode", () => {
  beforeEach(() => installDemo());

  it("answers the session with a fictional vendor contact", async () => {
    const me = await call("/bff/me");
    expect(me.status).toBe(200);
    expect(me.body).toMatchObject({ name: "Priya Raman, Ironbridge Foundry", isAdmin: false, csrfToken: "demo-csrf" });
  });

  it("lists the purchase orders and opens one", async () => {
    const list = await call("/api/v1/purchase-orders?limit=100");
    const items = list.body.items as Array<{ id: string; number: string; acknowledgement: string; lineCount: number }>;
    expect(items.map((o) => o.number)).toEqual(["PO-1042", "PO-1043"]);
    expect(items[0]).toMatchObject({ acknowledgement: "NONE", lineCount: 2 });
    const detail = await call(`/api/v1/purchase-orders/${items[0]?.id}`);
    expect(detail.body).toMatchObject({ number: "PO-1042" });
  });

  it("answers 404 for an unknown purchase order and for a path nothing answers, and records the miss", async () => {
    expect((await call("/api/v1/purchase-orders/nope")).status).toBe(404);
    const unknown = await call("/api/v1/invoices");
    expect(unknown.status).toBe(404);
    expect(unknown.body.code).toBe("DEMO-0404");
    expect(window.__depotDemoMisses).toEqual(["GET /api/v1/invoices"]);
  });

  it("does not answer a write", async () => {
    expect((await call("/api/v1/purchase-orders", "POST")).status).toBe(404);
    expect(window.__depotDemoMisses).toEqual(["POST /api/v1/purchase-orders"]);
  });
});
