import { describe, expect, it } from "vitest";
import { defaultCatalog } from "../src/agents/catalog.js";
import { ACT_TOOL_MAP, navigateAlternative, resolveActTool } from "../src/domain/actMap.js";
import { schemaViolations, unresolvedIds } from "../src/domain/schemaCheck.js";
import { evalResults, parseEvalReport } from "../src/cli/record-eval.js";
import { readFileSync } from "node:fs";

const golden = JSON.parse(readFileSync(new URL("./fixtures/act-golden.json", import.meta.url), "utf8")) as Array<{ message: string; tool: string | null }>;

describe("act tool table", () => {
  it("maps each propose tool to its catalog agent and holds no anomaly tool", () => {
    const catalog = defaultCatalog("claude-sonnet-5-5");
    for (const entry of ACT_TOOL_MAP) {
      expect(catalog.find((a) => a.agentId === entry.agent)?.allowedTools).toContain(entry.tool);
    }
    expect(ACT_TOOL_MAP.map((e) => e.agent)).toEqual(["ap_coding", "cash_app", "mrp_triage", "collections"]);
    expect(ACT_TOOL_MAP.some((e) => e.tool.includes("flag") || e.agent === "anomaly")).toBe(false);
  });

  it("resolves none, mapped and ambiguous requests", () => {
    expect(resolveActTool("flag this journal as an anomaly")).toEqual({ status: "none" });
    expect(resolveActTool("")).toEqual({ status: "none" });
    expect(resolveActTool("apply this payment to invoice INV-1042")).toMatchObject({ status: "mapped", entry: { agent: "cash_app" } });
    const both = resolveActTool("apply this payment and code the invoice");
    expect(both.status === "ambiguous" && both.entries.map((e) => e.agent)).toEqual(["ap_coding", "cash_app"]);
  });

  it("offers a navigate alternative built from the route result", () => {
    expect(navigateAlternative("show me invoices")).toEqual({ kind: "navigate", route: { kind: "navigate", confidence: 1 } });
  });

  it("scores the golden phrase set per agent and records it through the eval-report flow", () => {
    const hits = new Map<string, { total: number; correct: number }>();
    for (const g of golden) {
      const got = resolveActTool(g.message);
      const resolved = got.status === "mapped" ? got.entry.tool : null;
      if (resolved === null && g.tool === null) continue;
      const agent = ACT_TOOL_MAP.find((e) => e.tool === (g.tool ?? resolved))?.agent ?? "";
      const row = hits.get(agent) ?? { total: 0, correct: 0 };
      hits.set(agent, { total: row.total + 1, correct: row.correct + (resolved === g.tool ? 1 : 0) });
    }
    const report = {
      datasets: Object.fromEntries([...hits.keys()].map((a) => [a, { id: "act-golden", version: "1" }])),
      results: Object.fromEntries([...hits].map(([a, r]) => [a, { toolAccuracy: r.correct / r.total }])),
      verdicts: Object.fromEntries([...hits].map(([a, r]) => [a, { passed: r.correct === r.total, failures: r.correct === r.total ? [] : ["tool accuracy"] }])),
    };
    const { results } = evalResults(parseEvalReport(report), defaultCatalog("claude-sonnet-5-5"), "test");
    expect(results).toHaveLength(4);
    expect(results.every((r) => r.failures.length === 0)).toBe(true);
    for (const g of golden.filter((p) => p.tool === null)) expect(resolveActTool(g.message).status).toBe("none");
  });
});

describe("schemaViolations and unresolvedIds", () => {
  const schema = { type: "object", required: ["invoiceId", "amount"], properties: { invoiceId: { type: "string" }, amount: { type: "number" }, action: { enum: ["cancel", "postpone"] }, lines: { type: "array", items: { type: "integer" } } } };

  it("accepts a conforming object and lists every violation otherwise", () => {
    expect(schemaViolations(schema, { invoiceId: "INV-1", amount: 12, action: "cancel", lines: [1, 2] })).toEqual([]);
    expect(schemaViolations(schema, { invoiceId: "INV-1" })).toEqual(["$.amount is required"]);
    expect(schemaViolations(schema, { invoiceId: "INV-1", amount: "12" })).toEqual(["$.amount must be number"]);
    expect(schemaViolations(schema, { invoiceId: "INV-1", amount: Number.NaN })).toHaveLength(1);
    expect(schemaViolations(schema, { invoiceId: "INV-1", amount: 1, action: "delete", lines: [1.5] })).toHaveLength(2);
    expect(schemaViolations(schema, "x")).toEqual(["$ must be object"]);
    expect(schemaViolations({}, { anything: true })).toEqual([]);
  });

  it("treats an id that is not in the request text as unresolved", () => {
    const source = "apply this payment to invoice INV-1042\npayment:PAY-9";
    expect(unresolvedIds({ invoiceId: "INV-1042", paymentRef: "pay-9" }, source)).toEqual([]);
    expect(unresolvedIds({ invoiceId: "INV-9999" }, source)).toEqual(["$.invoiceId"]);
    expect(unresolvedIds({ invoice_id: "", id: null, lines: [{ lineId: "L1" }] }, source)).toEqual(["$.invoice_id", "$.id", "$.lines[0].lineId"]);
    expect(unresolvedIds({ action: "cancel", note: "INV-9999" }, source)).toEqual([]);
  });
});
