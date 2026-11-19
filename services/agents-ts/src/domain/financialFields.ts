import type { Pseudonymizer } from "./redaction.js";

export type FinancialFieldClass = "DOCUMENT_AMOUNT" | "BALANCE" | "CREDIT_LIMIT" | "BANK_BALANCE" | "COMPENSATION";
export type FinancialFieldAction = "allow" | "withhold" | "tokenize";
export type FinancialFieldPolicy = Record<FinancialFieldClass, FinancialFieldAction>;

export const FINANCIAL_POLICY_VERSION = 1;
export const AMOUNT_WITHHELD = "AMOUNT_WITHHELD";
export const AMOUNT_WITHHELD_NOTICE = "An amount was withheld from the data sent to the AI model, so this answer does not state it.";

const DEFAULT_POLICY: FinancialFieldPolicy = {
  DOCUMENT_AMOUNT: "allow",
  BALANCE: "allow",
  CREDIT_LIMIT: "allow",
  BANK_BALANCE: "tokenize",
  COMPENSATION: "withhold",
};

export const financialFieldPolicy = (): FinancialFieldPolicy => DEFAULT_POLICY;

const REGISTRY: Readonly<Record<FinancialFieldClass, readonly string[]>> = {
  DOCUMENT_AMOUNT: ["amount", "total", "gross", "net", "openamount", "taxamount", "discount", "subtotal", "totalamount", "grossamount", "netamount", "paymentamount", "invoiceamount", "orderamount"],
  BALANCE: ["balance", "unappliedamount", "customerbalance", "vendorbalance", "openbalance"],
  CREDIT_LIMIT: ["creditlimit"],
  BANK_BALANCE: ["bankbalance", "cashposition", "accountbalance", "cashbalance"],
  COMPENSATION: ["salary", "wage", "payrate", "hourlyrate", "basepay", "bonus", "compensation"],
};

const CLASS_BY_KEY = new Map<string, FinancialFieldClass>(
  (Object.keys(REGISTRY) as FinancialFieldClass[]).flatMap((cls) => REGISTRY[cls].map((key): [string, FinancialFieldClass] => [key, cls])),
);

export const fieldClassOf = (key: string): FinancialFieldClass | undefined => CLASS_BY_KEY.get(key.toLowerCase().replace(/[_-]/g, ""));

export interface GovernedOutput {
  value: unknown;
  withheld: number;
}

export function applyFinancialPolicy(output: unknown, pseudo: Pseudonymizer, policy: FinancialFieldPolicy = financialFieldPolicy()): GovernedOutput {
  let withheld = 0;
  const walk = (node: unknown): unknown => {
    if (Array.isArray(node)) return node.map(walk);
    if (typeof node !== "object" || node === null) return node;
    const out: Record<string, unknown> = {};
    for (const [key, value] of Object.entries(node)) {
      const cls = fieldClassOf(key);
      if (cls === undefined) {
        out[key] = walk(value);
        continue;
      }
      const action = cls === "COMPENSATION" ? "withhold" : (policy[cls] ?? "withhold");
      if (action === "allow") out[key] = value;
      else if (action === "tokenize" && (typeof value === "number" || typeof value === "string")) out[key] = pseudo.tokenize("AMOUNT", String(value));
      else withheld += 1;
    }
    return out;
  };
  const value = walk(output);
  return { value, withheld };
}
