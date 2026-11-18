import type { Me } from "./api";
import type { Po, PoSummary } from "./pages/PurchaseOrders";

declare global {
  interface Window {
    __depotDemoMisses?: string[];
  }
}

type Answer = { status: number; body: unknown };

const LE = "00000000-0000-0000-0000-0000000000e1";
const day = (offset: number): string => new Date(Date.now() + offset * 86_400_000).toISOString().slice(0, 10);
const id = (n: number): string => `00000050-0000-4000-8000-${n.toString(16).padStart(12, "0")}`;

const me: Me = { userId: "demo-vendor-contact", name: "Priya Raman, Ironbridge Foundry", vendorId: id(900), legalEntityIds: [LE], isAdmin: false, csrfToken: "demo-csrf" };

const orders: Po[] = [
  {
    id: id(1), number: "PO-1042", currency: "USD", approvalStatus: "APPROVED", versionNo: 1, total: "6300.00", acknowledgements: [],
    lines: [
      { lineNo: 1, description: "Pump casing, cast iron", qty: "20", unit: "EA", unitPrice: "275.00", lineAmount: "5500.00", deliveryDate: day(14), receivedQty: "0", status: "OPEN" },
      { lineNo: 2, description: "Impeller blank", qty: "10", unit: "EA", unitPrice: "80.00", lineAmount: "800.00", deliveryDate: day(21), receivedQty: "0", status: "OPEN" },
    ],
  },
  {
    id: id(2), number: "PO-1043", currency: "USD", approvalStatus: "APPROVED", versionNo: 1, total: "1250.00", acknowledgements: [],
    lines: [{ lineNo: 1, description: "Bearing housing", qty: "25", unit: "EA", unitPrice: "50.00", lineAmount: "1250.00", deliveryDate: day(30), receivedQty: "0", status: "OPEN" }],
  },
];

const summary = (o: Po, n: number): PoSummary => ({
  id: o.id, number: o.number, currency: o.currency, approvalStatus: o.approvalStatus, versionNo: o.versionNo, total: o.total, lineCount: o.lines.length,
  acknowledgement: o.acknowledgements[0]?.outcome ?? "NONE", createdAt: `${day(-3 - n)}T09:00:00Z`,
});

const ok = (body: unknown): Answer => ({ status: 200, body });
const missing = (detail: string): Answer => ({ status: 404, body: { code: "DEMO-0404", detail } });

function handle(method: string, path: string): Answer | undefined {
  if (method === "POST" && path === "/bff/logout") return ok({ logoutUrl: "/" });
  if (method !== "GET") return undefined;
  if (path === "/bff/me") return ok(me);
  if (path === "/api/v1/purchase-orders") return ok({ items: orders.map(summary) });
  if (path === "/api/v1/requests") return ok({ items: [] });
  const detail = /^\/api\/v1\/purchase-orders\/([^/]+)$/.exec(path);
  if (detail) {
    const found = orders.find((o) => o.id === detail[1]);
    return found ? ok(found) : missing("purchase order not found");
  }
  return undefined;
}

export function installDemo(): void {
  const misses: string[] = [];
  window.__depotDemoMisses = misses;
  const real = window.fetch.bind(window);
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, window.location.origin);
    if (url.origin !== window.location.origin || !/^\/(api|bff)\//.test(url.pathname)) return real(input, init);
    const method = (init?.method ?? "GET").toUpperCase();
    const answer = handle(method, url.pathname);
    if (!answer) {
      misses.push(`${method} ${url.pathname}`);
      return new Response(JSON.stringify({ code: "DEMO-0404", detail: "This part of the portal has no demo data." }), { status: 404, headers: { "content-type": "application/problem+json" } });
    }
    return new Response(JSON.stringify(answer.body), { status: answer.status, headers: { "content-type": answer.status >= 400 ? "application/problem+json" : "application/json" } });
  };
}
