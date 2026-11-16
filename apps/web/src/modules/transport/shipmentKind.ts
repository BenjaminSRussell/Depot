import type { LookupEntityKey } from "../../shell/lookup/entities";

/** What a transport shipment id refers to: a sales shipment released by fulfillment, a warehouse transfer, or an inbound notice. */
export type ShipmentKind = "SALES" | "TRANSFER" | "INBOUND";

export const SHIPMENT_KINDS: readonly { value: ShipmentKind; label: string; entity: LookupEntityKey; hint: string }[] = [
  { value: "SALES", label: "Sales shipment", entity: "shipment", hint: "A shipment released by fulfillment." },
  { value: "TRANSFER", label: "Transfer order", entity: "transferOrder", hint: "A stock transfer between warehouses." },
  { value: "INBOUND", label: "Inbound notice", entity: "inboundNotice", hint: "A vendor's advance ship notice." },
];

export const shipmentKindEntity = (kind: ShipmentKind): LookupEntityKey => SHIPMENT_KINDS.find((k) => k.value === kind)?.entity ?? "shipment";
