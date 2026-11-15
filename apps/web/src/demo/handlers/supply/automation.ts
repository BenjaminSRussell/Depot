// Warehouse RFID and automation (/api/v1/warehouse) for the demo: the reader rules, the stored tag reads with what each
// rule did, and the orders given to the ASRS station. The pallets are the SSCCs of the received ASN pallets.
import type { RfidRead, RfidRule, RfidRuleInput, TransportOrder } from "../../../modules/warehouse/api/client";
import { WH_FG, WH_RAW } from "../../company";
import { reply, route } from "../../backend";
import { at, table, uid } from "../../store";
import { ME } from "../finance/people";
import { STATION_WORK_ID } from "./warehouse";

const BASE = "/api/v1/warehouse";
const K_RULE = 440;
const K_READ = 441;
const K_ORDER = 442;
const GATEWAY = "rfid-gateway";

// ---- Rules ------------------------------------------------------------------------------------------------------

let ruleSeq = 0;
const rule = (readerId: string, action: RfidRule["action"], warehouseId: string, extra: Partial<RfidRule> = {}): RfidRule => ({
  id: uid(K_RULE, ++ruleSeq),
  readerId,
  action,
  warehouseId,
  postedBy: GATEWAY,
  createdBy: ME.id,
  createdAt: at(-120),
  ...extra,
});
const rules = table<RfidRule>([
  rule("dock-r1", "RECEIVE_PALLET", WH_RAW.id),
  rule("dock-r2", "RECEIVE_PALLET", WH_RAW.id, { antenna: 2 }),
  rule("asrs-in", "CONFIRM_PUTAWAY", WH_RAW.id, { locationCode: "RM-B-01" }),
  rule("ship-s1", "CONFIRM_PUTAWAY", WH_FG.id, { locationCode: "FG-A-01", postedBy: "rfid-fg-gateway" }),
]);

route("GET", `${BASE}/rfid-rules`, ({ query }) => {
  const reader = query.get("readerId");
  return { items: rules.all().filter((r) => !reader || r.readerId === reader).sort((a, b) => a.readerId.localeCompare(b.readerId) || (a.antenna ?? -1) - (b.antenna ?? -1)) };
});

function ruleBody(body: unknown): { input?: RfidRuleInput; problem?: ReturnType<typeof reply.problem> } {
  const b = (body ?? {}) as Partial<RfidRuleInput>;
  if (!b.readerId || !b.warehouseId || !b.postedBy || (b.action === "CONFIRM_PUTAWAY" && !b.locationCode)) {
    return { problem: reply.problem(400, "COM-0001", "request failed validation") };
  }
  return { input: b as RfidRuleInput };
}
const clash = (input: RfidRuleInput, except?: string) =>
  rules.all().some((r) => r.id !== except && r.readerId === input.readerId && r.antenna === input.antenna && r.action === input.action);

route("POST", `${BASE}/rfid-rules`, ({ body }) => {
  const { input, problem } = ruleBody(body);
  if (!input) return problem!;
  if (clash(input)) return reply.problem(409, "WMS-RFID-RULE-EXISTS", `reader ${input.readerId} has a ${input.action} rule for that antenna already`);
  return reply.created(rules.insert({ ...input, id: crypto.randomUUID(), createdBy: ME.id, createdAt: at(0) }));
});
route("PUT", `${BASE}/rfid-rules/{id}`, ({ params, body }) => {
  const current = rules.get(params.id);
  if (!current) return reply.problem(404, "WMS-RFID-RULE-NOT-FOUND", "RFID rule not found");
  const { input, problem } = ruleBody(body);
  if (!input) return problem!;
  if (clash(input, current.id)) return reply.problem(409, "WMS-RFID-RULE-EXISTS", `reader ${input.readerId} has a ${input.action} rule for that antenna already`);
  return reply.ok(rules.update(current.id, { ...input, antenna: input.antenna, locationCode: input.locationCode }));
});

// ---- Reads ------------------------------------------------------------------------------------------------------

const read = (n: number, ago: number, readerId: string, antenna: number, key: string, extra: Partial<RfidRead> = {}): RfidRead => ({
  id: uid(K_READ, n),
  readerId,
  antenna,
  epc: `3114141${key.slice(8, 17)}0000`,
  scheme: "SSCC-96",
  key,
  rssi: -48 - n,
  readAt: at(-ago),
  ...extra,
});
const reads = table<RfidRead>([
  read(1, 0.02, "dock-r1", 1, "006141411000000015", { action: "RECEIVE_PALLET", actionOutcome: "DONE", actionDetail: "received the pallet of ASN-5120 at the announced quantity" }),
  read(2, 0.05, "asrs-in", 1, "006141411000000015", { locationCode: "RM-B-01", action: "CONFIRM_PUTAWAY", actionOutcome: "DONE", actionDetail: "confirmed the put-away of the pallet at RM-B-01" }),
  read(3, 0.2, "dock-r2", 2, "006141411000000022", { action: "RECEIVE_PALLET", actionOutcome: "SKIPPED", actionDetail: "no expected ASN pallet has this SSCC" }),
  read(4, 0.4, "asrs-in", 1, "006141411000000039", { locationCode: "RM-B-01", action: "CONFIRM_PUTAWAY", actionOutcome: "REFUSED", actionDetail: "the location RM-B-01 has no room for the pallet" }),
  read(5, 0.9, "dock-r1", 3, "006141411000000046"),
  read(6, 1.1, "dock-r1", 1, "006141411000000053", { action: "RECEIVE_PALLET", actionOutcome: "SKIPPED", actionDetail: "the read was not posted by the identity this reader's rule accepts" }),
]);

route("GET", `${BASE}/rfid-reads`, ({ query }) => {
  const key = query.get("key");
  const reader = query.get("readerId");
  const limit = Number(query.get("limit") ?? 50);
  return { items: reads.all().filter((r) => (!key || r.key === key) && (!reader || r.readerId === reader)).sort((a, b) => b.readAt.localeCompare(a.readAt)).slice(0, limit) };
});

// ---- Automation orders --------------------------------------------------------------------------------------------

let orderSeq = 0;
const order = (status: TransportOrder["status"], ago: number, loadUnit: string, to: string, extra: Partial<TransportOrder> = {}): TransportOrder => ({
  id: uid(K_ORDER, ++orderSeq),
  workId: STATION_WORK_ID,
  lineNo: 1,
  station: "ASRS",
  loadUnit,
  fromLocation: "INFEED-1",
  toLocation: to,
  priority: 5,
  status,
  createdBy: ME.id,
  createdAt: at(-ago),
  ...extra,
});
const orders = table<TransportOrder>([
  order("ARRIVED", 0.42, "006141411000000015", "RM-B-02", { sentAt: at(-0.41), doneAt: at(-0.38) }),
  order("FAULT", 0.35, "006141411000000022", "RM-B-02", { lineNo: 2, sentAt: at(-0.34), doneAt: at(-0.3), reason: "PLC fault: E12 jam at infeed" }),
  order("SENT", 0.05, "006141411000000039", "RM-B-03", { lineNo: 2, sentAt: at(-0.04) }),
  order("PENDING", 0.01, "006141411000000046", "RM-B-04", { lineNo: 2 }),
]);

route("GET", `${BASE}/automation/transport-orders`, ({ query }) => {
  const station = query.get("station");
  const status = query.get("status");
  return { items: orders.all().filter((o) => (!station || o.station === station) && (!status || o.status === status)).sort((a, b) => a.createdAt.localeCompare(b.createdAt)) };
});
route("GET", `${BASE}/automation/transport-orders/{id}`, ({ params }) => orders.get(params.id) ?? reply.problem(404, "WMS-AUTO-ORDER-NOT-FOUND", "automation order not found"));
route("POST", `${BASE}/automation/transport-orders/{id}:cancel`, ({ params, body }) => {
  const current = orders.get(params.id);
  if (!current) return reply.problem(404, "WMS-AUTO-ORDER-NOT-FOUND", "automation order not found");
  const input = (body ?? {}) as { reason?: string; plcCleared?: boolean };
  if (!input.reason) return reply.problem(400, "COM-0001", "request failed validation");
  if (current.status === "ARRIVED") return reply.problem(409, "WMS-AUTO-ORDER-STATE", "the order has arrived; its move is done");
  if ((current.status === "SENT" || current.status === "PENDING") && !input.plcCleared) {
    return reply.problem(409, "WMS-AUTO-ORDER-STATE", "the order may already be with the PLC: clear the move on the PLC first, then cancel it with plcCleared");
  }
  return reply.ok(orders.update(current.id, { status: "CANCELLED", reason: input.reason, doneAt: at(0) }));
});
