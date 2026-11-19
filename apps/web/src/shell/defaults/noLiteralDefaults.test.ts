// @vitest-environment node
import { describe, expect, it } from "vitest";
import allowlist from "./noLiteralDefaultsAllowlist.json";

const LITERAL_CURRENCY_DEFAULT = /[cC]urrency\w*\s*[:=]\s*"[A-Z]{3}"|[cC]urrency\w*[^\n]{0,40}useState\(\s*"[A-Z]{3}"\s*\)/;

export function findLiteralCurrencyDefaults(source: string): number[] {
  return source
    .split("\n")
    .map((line, at) => (LITERAL_CURRENCY_DEFAULT.test(line) ? at + 1 : 0))
    .filter(Boolean);
}

export function allowlistProblem(value: unknown): string | null {
  if (typeof value !== "object" || value === null) return "the allowlist is not an object";
  const routes = (value as { taskRoutes?: unknown }).taskRoutes;
  if (!Array.isArray(routes)) return "taskRoutes is not an array";
  return routes.length === 0 ? null : "taskRoutes must stay empty";
}

const TASK_ROUTE_FORMS = [
  "../../modules/sales/NewOrderPage.tsx",
  "../../modules/purchasing/PurchaseOrderPages.tsx",
  "../../modules/ap-invoices/PostInvoicePage.tsx",
  "../../modules/receiving/NewArrivalPage.tsx",
  "../../modules/receivables/NewPaymentPage.tsx",
  "../../modules/billing/NewFreeTextInvoicePage.tsx",
];

const sources = import.meta.glob(
  [
    "../../modules/sales/NewOrderPage.tsx",
    "../../modules/purchasing/PurchaseOrderPages.tsx",
    "../../modules/ap-invoices/PostInvoicePage.tsx",
    "../../modules/receiving/NewArrivalPage.tsx",
    "../../modules/receivables/NewPaymentPage.tsx",
    "../../modules/billing/NewFreeTextInvoicePage.tsx",
  ],
  { query: "?raw", import: "default", eager: true },
) as Record<string, string>;

describe("no literal currency default on a task route", () => {
  it("finds a literal currency in a field default or a state default", () => {
    expect(findLiteralCurrencyDefaults('const a = {\n  currency: "USD",\n};')).toEqual([2]);
    expect(findLiteralCurrencyDefaults('const [currency, setCurrency] = useState("EUR");')).toEqual([1]);
    expect(findLiteralCurrencyDefaults('let displayCurrency = "GBP";')).toEqual([1]);
  });

  it("ignores a derived or empty default and currency codes in prose", () => {
    expect(findLiteralCurrencyDefaults('currency: "",')).toEqual([]);
    expect(findLiteralCurrencyDefaults("currency: values.currency || derived,")).toEqual([]);
    expect(findLiteralCurrencyDefaults('errors.currency = "Enter a code such as USD.";')).toEqual([]);
  });

  it("fails closed on a malformed or non-empty allowlist", () => {
    expect(allowlistProblem(null)).not.toBeNull();
    expect(allowlistProblem({})).not.toBeNull();
    expect(allowlistProblem({ taskRoutes: "all" })).not.toBeNull();
    expect(allowlistProblem({ taskRoutes: ["modules/sales/NewOrderPage.tsx"] })).not.toBeNull();
    expect(allowlistProblem(allowlist)).toBeNull();
  });

  it.each(TASK_ROUTE_FORMS)("%s carries no literal currency default", (path) => {
    const source = sources[path];
    expect(source, `${path} is not a source file`).toBeDefined();
    expect(findLiteralCurrencyDefaults(source), path).toEqual([]);
  });
});
