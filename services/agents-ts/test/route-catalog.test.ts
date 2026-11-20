import { describe, expect, it } from "vitest";
import { CLARIFY_FALLBACK, NO_PAGE_ACCESS, ROUTE_CATALOG, taskBody, taskInstruction, TEXT_LIMIT } from "../src/domain/routeCatalog.js";

const ALL = new Set(ROUTE_CATALOG.map((e) => e.duty));
const answer = (value: unknown) => ({ answer: typeof value === "string" ? value : JSON.stringify(value), refusal: null });
const intentOf = (body: ReturnType<typeof taskBody>) => ("navigationIntent" in body ? body.navigationIntent : undefined);

describe("taskBody", () => {
  it("builds the invoice intent from the catalog with validated params and a summary", () => {
    const body = taskBody(answer({ routeKey: "receivables.openItems", params: { overdue: true, amountGt: 10000 }, draft: { vendor: "ignored" } }), ALL);
    expect(body).toEqual({
      navigationIntent: { routeKey: "receivables.openItems", path: "/receivables/open-items", params: { overdue: true, amountGt: 10000 }, draft: {}, summary: "Showing open invoices that are overdue and above 10000." },
      generated: true,
    });
  });

  it("builds the purchase order intent with draft values", () => {
    const intent = intentOf(taskBody(answer({ routeKey: "purchasing.purchaseOrder.new", draft: { vendor: "Acme", product: "widgets", qty: "50" } }), ALL));
    expect(intent).toMatchObject({ path: "/purchasing/purchase-orders/new", params: {}, draft: { vendor: "Acme", product: "widgets", qty: 50 } });
    expect(intent?.summary).toBe("Opening a new purchase order for 50 widgets from Acme. Nothing is saved until you save it yourself.");
  });

  it("never reads a path, url or href from model text", () => {
    const text = JSON.stringify({ routeKey: "receivables.openItems", path: "https://evil.example", url: "//evil", href: "https://evil.example", params: { overdue: true, path: "https://evil.example" } });
    const body = taskBody(answer(text), ALL);
    expect(intentOf(body)?.path).toBe("/receivables/open-items");
    expect(JSON.stringify(body)).not.toContain("evil");
  });

  it.each([
    ["not JSON", "show invoices please"],
    ["an array", "[1]"],
    ["null", "null"],
    ["an empty object", "{}"],
    ["an unknown route key", JSON.stringify({ routeKey: "admin.deleteEverything" })],
    ["a wrong-typed route key", JSON.stringify({ routeKey: 5 })],
    ["a question with a route key", JSON.stringify({ routeKey: "receivables.openItems", clarifyingQuestion: "" })],
  ])("returns a clarifying question for %s and no path", (_name, text) => {
    const body = taskBody(answer(text), ALL);
    expect(body).toEqual({ clarifyingQuestion: CLARIFY_FALLBACK, generated: true });
    expect(JSON.stringify(body)).not.toContain("path");
  });

  it("passes on the model's own question, cleaned and clipped, and does not navigate", () => {
    const body = taskBody(answer({ routeKey: "receivables.openItems", clarifyingQuestion: `Which\ncustomer? ${"x".repeat(300)}` }), ALL);
    expect(body).not.toHaveProperty("navigationIntent");
    expect(body).toMatchObject({ clarifyingQuestion: expect.stringMatching(/^Which customer\? x+$/) });
    expect((body as { clarifyingQuestion: string }).clarifyingQuestion).toHaveLength(200);
  });

  it("refuses a screen the caller lacks the duty for without naming the page, a record or the vendor", () => {
    const body = taskBody(answer({ routeKey: "purchasing.purchaseOrder.new", draft: { vendor: "Acme" } }), new Set(["receivables.view"]));
    expect(body).toEqual({ refusal: { code: "AGT-0203", message: NO_PAGE_ACCESS }, generated: true });
    expect(JSON.stringify(body)).not.toMatch(/Acme|purchase|purchasing/i);
  });

  it("passes a model-run refusal through unchanged", () => {
    const refusal = { code: "AGT-0219", message: "blocked" };
    expect(taskBody({ answer: "{}", refusal }, ALL)).toEqual({ refusal, generated: true });
  });

  it.each([
    ["overdue yes", { overdue: "yes" }, {}],
    ["overdue 1", { overdue: 1 }, {}],
    ["overdue object", { overdue: {} }, {}],
    ["overdue string true", { overdue: "true" }, { overdue: true }],
    ["overdue false", { overdue: false }, { overdue: false }],
    ["amountGt text", { amountGt: "abc" }, {}],
    ["amountGt 10k", { amountGt: "10k" }, {}],
    ["amountGt negative", { amountGt: -5 }, {}],
    ["amountGt Infinity", { amountGt: "Infinity" }, {}],
    ["amountGt object", { amountGt: {} }, {}],
    ["amountGt numeric string", { amountGt: "10000" }, { amountGt: 10000 }],
    ["amountGt zero", { amountGt: 0 }, { amountGt: 0 }],
  ])("coerces params: %s", (_name, params, expected) => {
    expect(intentOf(taskBody(answer({ routeKey: "receivables.openItems", params }), ALL))?.params).toEqual(expected);
  });

  it.each([
    ["50", { qty: 50 }, { qty: 50 }],
    ["numeric string", { qty: "50" }, { qty: 50 }],
    ["zero", { qty: 0 }, {}],
    ["negative", { qty: "-5" }, {}],
    ["word", { qty: "fifty" }, {}],
    ["overflow", { qty: 1e999 }, {}],
    ["empty vendor", { vendor: "" }, {}],
    ["numeric vendor", { vendor: 5 }, {}],
    ["object product", { product: {} }, {}],
    ["control characters", { vendor: "Ac\nme\u0000" }, { vendor: "Ac me" }],
    ["long vendor", { vendor: "v".repeat(500) }, { vendor: "v".repeat(TEXT_LIMIT) }],
  ])("coerces draft: %s", (_name, draft, expected) => {
    expect(intentOf(taskBody(answer({ routeKey: "purchasing.purchaseOrder.new", draft }), ALL))?.draft).toEqual(expected);
  });

  it("keeps only declared names", () => {
    const intent = intentOf(taskBody(answer({ routeKey: "receivables.openItems", params: { overdue: true, evil: "x" } }), ALL));
    expect(intent?.params).toEqual({ overdue: true });
  });
});

describe("catalog", () => {
  it("holds only clean absolute app paths and the model instruction names every key and forbids URLs", () => {
    for (const e of ROUTE_CATALOG) expect(e.path).toMatch(/^\/(?!\/)[^?#:]*$/);
    const text = taskInstruction();
    for (const e of ROUTE_CATALOG) expect(text).toContain(e.routeKey);
    expect(text).toContain("Never write a URL");
  });
});
