import { describe, expect, it } from "vitest";
import { buildCatalog, CatalogError } from "../src/catalog.js";
import { compositeTools, HELP_SEARCH_NAME, SEARCH_TOOLS_NAME } from "../src/composite.js";
import { callTool } from "../src/gateway.js";
import { handleRpc, RPC } from "../src/jsonrpc.js";
import { principal, readTool, rig } from "./fixtures.js";

const COPILOT = "mcp.copilot.use";

const tools = () => [
  ...compositeTools(),
  readTool("depot.ar.invoice.list", "receivables.view", { description: "List open customer invoices. Filter by status." }),
  readTool("depot.ap.invoice.get", "ap.invoice.read", { description: "Fetch one supplier invoice by id." }),
  readTool("depot.hr.salary.list", "hr.salary.read", { description: "List salary invoice payments for payroll." }),
];

const search = (duties: string[], args: unknown) => {
  const r = rig(tools());
  return { r, result: callTool(r.deps, principal(duties), SEARCH_TOOLS_NAME, args) };
};

describe("depot.meta.search_tools", () => {
  it("ranks the caller's tools by name and description overlap", async () => {
    const { result } = search([COPILOT, "receivables.view", "ap.invoice.read"], { query: "invoice" });
    expect(await result).toMatchObject({
      kind: "ok",
      body: { tools: [{ name: "depot.ap.invoice.get" }, { name: "depot.ar.invoice.list" }] },
    });
  });

  it("does not leak tools the caller cannot see, even when the query matches them", async () => {
    const res = await search([COPILOT, "receivables.view"], { query: "salary payroll invoice" }).result;
    const names = (res as { body: { tools: { name: string }[] } }).body.tools.map((t) => t.name);
    expect(names).toEqual(["depot.ar.invoice.list"]);
    expect(JSON.stringify(res)).not.toContain("salary");
  });

  it("returns a one-sentence description", async () => {
    const res = await search([COPILOT, "receivables.view"], { query: "customer" }).result;
    expect(res).toMatchObject({ body: { tools: [{ description: "List open customer invoices." }] } });
  });

  it("is denied without the copilot duty and the audit entry says DENIED", async () => {
    const { r, result } = search(["receivables.view"], { query: "invoice" });
    expect(await result).toMatchObject({ kind: "error", status: 403, code: "AGT-0203" });
    expect(r.audit.entries).toMatchObject([{ tool: SEARCH_TOOLS_NAME, outcome: "DENIED" }]);
  });

  it.each([[{}], [{ query: null }], [{ query: "" }]])("refuses an absent or empty query %j with MCP-0422", async (args) => {
    expect(await search([COPILOT], args).result).toMatchObject({ kind: "error", status: 400, code: "MCP-0422" });
  });

  it.each([[{ query: 5 }], [{ query: {} }], [{ query: ["invoice"] }]])("refuses a mistyped query %j with MCP-0400", async (args) => {
    expect(await search([COPILOT], args).result).toMatchObject({ kind: "error", status: 400, code: "MCP-0400" });
  });

  it("refuses tenant_id in the arguments", async () => {
    expect(await search([COPILOT], { query: "invoice", tenant_id: "x" }).result).toMatchObject({ code: "MCP-0400" });
  });

  it.each([["5"], [0], [-3], [1e9], [Number.NaN], [{}]])("clamps limit %j without failing", async (limit) => {
    const res = await search([COPILOT, "receivables.view", "ap.invoice.read", "hr.salary.read"], { query: "invoice", limit }).result;
    expect(res.kind).toBe("ok");
    const found = (res as { body: { tools: unknown[] } }).body.tools.length;
    expect(found).toBeGreaterThanOrEqual(1);
    expect(found).toBeLessThanOrEqual(10);
  });

  it("returns exactly one tool for limit 1", async () => {
    const res = await search([COPILOT, "receivables.view", "ap.invoice.read"], { query: "invoice", limit: 1 }).result;
    expect((res as { body: { tools: unknown[] } }).body.tools).toHaveLength(1);
  });

  it("tokenises control characters and long queries instead of failing", async () => {
    const res = await search([COPILOT, "receivables.view"], { query: `customer\u0000\n${"x".repeat(900)}` }).result;
    expect(res).toMatchObject({ kind: "ok", body: { tools: [{ name: "depot.ar.invoice.list" }] } });
  });

  it("reports 500 MCP-0500 without internals when the handler throws, and never calls the upstream", async () => {
    const boom = { ...compositeTools()[0]!, local: () => { throw new Error("secret stack detail"); } };
    const r = rig([boom, ...compositeTools().slice(1)]);
    const res = await callTool(r.deps, principal([COPILOT]), SEARCH_TOOLS_NAME, { query: "x" });
    expect(res).toMatchObject({ kind: "error", status: 500, code: "MCP-0500" });
    expect(JSON.stringify(res)).not.toContain("secret");
    expect(r.audit.entries).toMatchObject([{ outcome: "UPSTREAM_ERROR", status: 500 }]);
    expect(r.upstream.calls).toHaveLength(0);
  });

  it("audits an OK call without the query text and forwards nothing upstream", async () => {
    const { r, result } = search([COPILOT, "receivables.view"], { query: "customer secret-query" });
    await result;
    expect(r.upstream.calls).toHaveLength(0);
    expect(r.audit.entries).toMatchObject([{ tool: SEARCH_TOOLS_NAME, outcome: "OK", status: 200 }]);
    expect(JSON.stringify(r.audit.entries)).not.toContain("secret-query");
  });

  it("is listed by tools/list only for holders of the copilot duty", async () => {
    const r = rig(tools());
    const names = async (duties: string[]) => {
      const res = (await handleRpc(r.deps, principal(duties), { jsonrpc: "2.0", id: 1, method: "tools/list" })) as { result: { tools: { name: string }[] } };
      return res.result.tools.map((t) => t.name);
    };
    expect(await names([COPILOT])).toEqual([HELP_SEARCH_NAME, SEARCH_TOOLS_NAME]);
    expect(await names([])).toEqual([]);
  });

  it("answers over JSON-RPC tools/call as text content", async () => {
    const r = rig(tools());
    const res = await handleRpc(r.deps, principal([COPILOT, "receivables.view"]), {
      jsonrpc: "2.0",
      id: 3,
      method: "tools/call",
      params: { name: SEARCH_TOOLS_NAME, arguments: { query: "customer" } },
    });
    expect(res).toMatchObject({ result: { isError: false, content: [{ type: "text" }] } });
  });

  it("refuses a duplicate name already supplied by the OpenAPI tools", () => {
    expect(() => buildCatalog([readTool(SEARCH_TOOLS_NAME, COPILOT), ...compositeTools()])).toThrow(CatalogError);
  });
});

describe("depot.help.search", () => {
  const helpRig = () => rig(compositeTools());
  const call = (r: ReturnType<typeof helpRig>, duties: string[], args: unknown) => callTool(r.deps, principal(duties), HELP_SEARCH_NAME, args);

  it("forwards the bearer to the knowledge search API and returns the chunks", async () => {
    const r = helpRig();
    r.upstream.responses.push({ status: 200, body: { chunks: [{ chunkId: "c1" }] } });
    expect(await call(r, [COPILOT], { query: "how do I post an invoice" })).toMatchObject({ kind: "ok", body: { chunks: [{ chunkId: "c1" }] } });
    expect(r.upstream.calls).toHaveLength(1);
    expect(r.upstream.calls[0]).toMatchObject({
      tool: { method: "POST", path: "/api/v1/knowledge/search" },
      args: { query: "how do I post an invoice" },
      headers: { authorization: "Bearer user-token-for-test" },
    });
    expect(r.upstream.calls[0]?.args).not.toHaveProperty("limit");
  });

  it("is denied without the copilot duty and never reaches the upstream", async () => {
    const r = helpRig();
    expect(await call(r, [], { query: "x" })).toMatchObject({ kind: "error", status: 403, code: "AGT-0203" });
    expect(r.upstream.calls).toHaveLength(0);
  });

  it.each([[{}], [{ query: "" }]])("refuses a missing or empty query %j with MCP-0422", async (args) => {
    const r = helpRig();
    expect(await call(r, [COPILOT], args)).toMatchObject({ status: 400, code: "MCP-0422" });
    expect(r.upstream.calls).toHaveLength(0);
  });

  it("answers 503 AGT-0214 after retries when the upstream is down", async () => {
    const r = helpRig();
    for (let i = 0; i < 4; i += 1) r.upstream.responses.push({ status: 503, body: null });
    expect(await call(r, [COPILOT], { query: "x" })).toMatchObject({ kind: "error", status: 503, code: "AGT-0214" });
    expect(r.upstream.calls).toHaveLength(4);
  });

  it("answers 502 MCP-0502 when chunks is not an array", async () => {
    const r = helpRig();
    r.upstream.responses.push({ status: 200, body: { chunks: "nope" } });
    expect(await call(r, [COPILOT], { query: "x" })).toMatchObject({ kind: "error", status: 502, code: "MCP-0502" });
  });

  it("refuses tenant_id in the arguments", async () => {
    const r = helpRig();
    expect(await call(r, [COPILOT], { query: "x", tenant_id: "t" })).toMatchObject({ code: "MCP-0400" });
    expect(r.upstream.calls).toHaveLength(0);
  });
});

it("maps the forbidden RPC code for a search_tools denial", async () => {
  const r = rig(tools());
  const res = await handleRpc(r.deps, principal([]), { jsonrpc: "2.0", id: 9, method: "tools/call", params: { name: SEARCH_TOOLS_NAME, arguments: { query: "x" } } });
  expect(res).toMatchObject({ error: { code: RPC.forbidden } });
});
