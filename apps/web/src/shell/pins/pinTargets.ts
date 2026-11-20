import { matchPath } from "react-router-dom";
import type { LookupEntityKey } from "../lookup/entities";
import type { RecordType } from "../search/records";
import type { Pin, PinTarget } from "./pinStore";

export const RECORD_ROUTES: Record<RecordType, string> = {
  party: "/party/:id",
  product: "/product/:id",
  purchase_order: "/purchasing/purchase-orders/:id",
  receipt: "/receiving/receipts/:id",
  vendor_invoice: "/ap-invoices/:id",
  sales_order: "/sales/orders/:id",
  packing_slip: "/fulfillment/packing-slips/:id",
  customer_invoice: "/billing/invoices/:id",
  production_order: "/production/:id",
  worker: "/hr/workers/:id",
};

export const RECORD_LOOKUP: Partial<Record<RecordType, LookupEntityKey>> = {
  party: "party",
  product: "product",
  purchase_order: "purchaseOrder",
  receipt: "receipt",
  vendor_invoice: "vendorInvoice",
  sales_order: "salesOrder",
  packing_slip: "packingSlip",
  worker: "worker",
};

export function pinTargetFor(pathname: string, hasPage: boolean, literalRoutes: ReadonlySet<string> = new Set()): PinTarget | null {
  if (hasPage) return { kind: "page", route: pathname };
  if (literalRoutes.has(pathname)) return null;
  for (const [recordType, path] of Object.entries(RECORD_ROUTES) as [RecordType, string][]) {
    const id = matchPath({ path, end: true }, pathname)?.params.id;
    if (id !== undefined && id !== "new") return { kind: "record", recordType, id };
  }
  return null;
}

export function pinHref(pin: Pin | PinTarget): string {
  return pin.kind === "page" ? pin.route : RECORD_ROUTES[pin.recordType].replace(":id", encodeURIComponent(pin.id));
}
