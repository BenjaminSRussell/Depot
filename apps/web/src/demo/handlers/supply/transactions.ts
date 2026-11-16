// Inventory transactions and their status changes (/api/v1/inventory/transactions, /status-changes) for the demo: a few
// registered receipts and issues of the demo company's items, so the transaction lookup and the status change audit
// have rows to show. Reads only.
import type { StatusChange, Transaction } from "../../../modules/inventory/api/client";
import { ITEMS, LEGAL_ENTITY } from "../../company";
import { reply, route } from "../../backend";
import { at, day, dec, uid } from "../../store";

const BASE = "/api/v1/inventory";
const K_TXN = 916;
const K_CHANGE = 917;

const item = (n: number) => ITEMS[n % ITEMS.length];

const transactions: Transaction[] = [
  { n: 1, ref: "PO-1042", source: "PURCHASE_LINE", qty: 40, receipt: "REGISTERED" as const, ago: 5 },
  { n: 2, ref: "PO-1043", source: "PURCHASE_LINE", qty: 12, receipt: "RECEIVED" as const, ago: 3 },
  { n: 3, ref: undefined, source: "SALES_LINE", qty: -6, issue: "PICKED" as const, ago: 2 },
  { n: 4, ref: "WO-2208", source: "PROD_LINE", qty: 25, receipt: "REGISTERED" as const, ago: 1 },
].map((t): Transaction => ({
  id: uid(K_TXN, t.n),
  legalEntityId: LEGAL_ENTITY.id,
  originId: uid(K_TXN, 100 + t.n),
  sourceType: t.source as Transaction["sourceType"],
  sourceId: uid(K_TXN, 200 + t.n),
  reference: t.ref,
  productId: item(t.n).id,
  dimensions: {},
  qty: dec(t.qty, 2),
  enteredQty: dec(Math.abs(t.qty), 2),
  enteredUnit: item(t.n).unit,
  ...(t.receipt ? { receiptStatus: t.receipt } : {}),
  ...(t.issue ? { issueStatus: t.issue } : {}),
  physicalDate: day(-t.ago),
  currency: "USD",
  costAmountPhysical: dec(Math.abs(t.qty) * item(t.n).cost, 2),
  createdAt: at(-t.ago),
}));

route("GET", `${BASE}/transactions`, ({ query }) => {
  const rows = transactions.filter((t) => (!query.get("product") || t.productId === query.get("product")) && (!query.get("status") || t.receiptStatus === query.get("status") || t.issueStatus === query.get("status")));
  return { items: rows.slice(0, Number(query.get("limit") ?? 50)) };
});
route("GET", `${BASE}/transactions/{id}`, ({ params }) => transactions.find((t) => t.id === params.id) ?? reply.problem(404, "INV-TXN-001", "inventory transaction not found"));

const change = (n: number, txn: number, command: StatusChange["command"], from: StatusChange["fromStatus"], to: NonNullable<StatusChange["toStatus"]>, qty: string, ago: number, by: string): StatusChange => ({
  id: uid(K_CHANGE, n),
  legalEntityId: LEGAL_ENTITY.id,
  transactionId: uid(K_TXN, txn),
  command,
  ...(from ? { fromStatus: from } : {}),
  toStatus: to,
  qty,
  date: day(-ago),
  createdBy: by,
  occurredAt: at(-ago),
});

const changes: StatusChange[] = [
  change(1, 1, "CREATE", undefined, "REGISTERED", "40.00", 5, "purchasing"),
  change(2, 2, "CREATE", undefined, "REGISTERED", "12.00", 4, "receiving"),
  change(3, 2, "STEP", "REGISTERED", "RECEIVED", "12.00", 3, "receiving"),
  change(4, 3, "CREATE", undefined, "PICKED", "6.00", 2, "warehouse"),
];

route("GET", `${BASE}/status-changes`, ({ query }) => ({
  items: changes.filter((c) => (!query.get("transaction") || c.transactionId === query.get("transaction")) && (!query.get("command") || c.command === query.get("command"))),
}));
