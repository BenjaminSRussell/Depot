import { describe, expect, it } from "vitest";
import { loadConfig, type Config } from "../src/config.js";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import { createServiceSigner } from "../src/api/serviceSigner.js";
import { createConfiguredRouter } from "../src/runtime/aiRouter.js";
import { createCopilotService, type ChatResponse } from "../src/runtime/copilot.js";
import { PROPOSE_TOOL_NAME } from "../src/providers/wire.js";
import { allowAiAccess } from "./fakes.js";
import { FakeMcp, MemoryStore, T1, tool, USER } from "./surface-fakes.js";

const GET = "depot.ap.invoice.get";
const CUI_MARKER = "CUI-MARKER-TEXT";
const OK_MARKER = "PERMITTED-TEXT";
const LE = "0192a2b4-0000-7000-8000-00000000000a";
const signer = createServiceSigner({ seedBase64: Buffer.alloc(32, 7).toString("base64") });
const claimsOf = (token: string): Record<string, unknown> => JSON.parse(Buffer.from(token.split(".")[0] ?? "", "base64url").toString("utf8")) as Record<string, unknown>;
const base = { MCP_URL: "http://mcp:8080", AGENTS_JWT_ISSUER: "https://idp.dev.depot.dev/realms/dev", AGENTS_JWKS_URL: "https://idp.dev.depot.dev/realms/dev/protocol/openid-connect/certs", AGENTS_COMPLIANCE_URL: "http://compliance.test" };

type AdapterName = "anthropic" | "bedrock" | "azure" | "vllm";
const adapters: Record<AdapterName, Record<string, string>> = {
  anthropic: { AGENTS_PROVIDER: "anthropic", ANTHROPIC_API_KEY: "k" },
  bedrock: { AGENTS_PROVIDER: "bedrock", AWS_REGION: "us-east-1", AWS_ACCESS_KEY_ID: "id", AWS_SECRET_ACCESS_KEY: "secret" },
  azure: { AGENTS_PROVIDER: "openai", OPENAI_API_KEY: "k", AGENTS_OPENAI_BASE_URL: "https://acme.openai.azure.com/openai/deployments/d", AGENTS_OPENAI_AUTH: "api-key" },
  vllm: { AGENTS_PROVIDER: "openai", OPENAI_API_KEY: "k", AGENTS_OPENAI_BASE_URL: "https://vllm.internal/v1" },
};

const wireAnswer = (adapter: AdapterName, input: Record<string, unknown>): unknown => {
  if (adapter === "anthropic") return { content: [{ type: "tool_use", name: PROPOSE_TOOL_NAME, input }], usage: { input_tokens: 10, output_tokens: 5 } };
  if (adapter === "bedrock") return { output: { message: { content: [{ toolUse: { name: PROPOSE_TOOL_NAME, input } }] } }, usage: { inputTokens: 10, outputTokens: 5 } };
  return { choices: [{ message: { tool_calls: [{ function: { name: PROPOSE_TOOL_NAME, arguments: JSON.stringify(input) } }] } }], usage: { prompt_tokens: 10, completion_tokens: 5 } };
};
const callTool = (name: string): Record<string, unknown> => ({ text: "", tool: name, confidence: 1, amount: { amount: "0", currency: "USD" }, args: {} });
const finalText = (text: string): Record<string, unknown> => ({ text });

type ResolverMode = "classes" | "http500" | "unreachable" | "mismatch";

function harness(adapter: AdapterName, script: Array<Record<string, unknown>>, resolver: ResolverMode = "classes") {
  const providerBodies: string[] = [];
  const resolverBodies: string[] = [];
  const resolverHeaders: Array<Record<string, string>> = [];
  const queue = [...script];
  const fetchFn = (async (url: string | URL, init?: RequestInit): Promise<Response> => {
    const body = String(init?.body ?? "");
    if (String(url).includes("data-designations:resolve")) {
      resolverBodies.push(body);
      resolverHeaders.push((init?.headers ?? {}) as Record<string, string>);
      if (resolver === "unreachable") throw new TypeError("fetch failed");
      if (resolver === "http500") return new Response("{}", { status: 500 });
      const refs = (JSON.parse(body) as { refs: Array<{ recordId: string }> }).refs;
      const items = refs.map((r) => ({ class: r.recordId === "" ? "UNKNOWN" : r.recordId.startsWith("cui") ? "CUI" : "NONE", source: "DEFAULT" }));
      return Response.json({ items: resolver === "mismatch" ? items.slice(1) : items });
    }
    providerBodies.push(body);
    const next = queue.shift();
    return Response.json(wireAnswer(adapter, next ?? finalText("done")));
  }) as unknown as typeof fetch;
  return { providerBodies, resolverBodies, resolverHeaders, fetchFn };
}

async function ask(cfg: Config, fetchFn: typeof fetch, results: Record<string, unknown>, opts: { signer?: typeof signer | undefined; legalEntityId?: string | undefined } = { signer, legalEntityId: LE }) {
  const store = new MemoryStore();
  const tools = Object.keys(results);
  const mcp = new FakeMcp(
    tools.map((t) => tool(t, "ap.invoice.read")),
    Object.fromEntries(tools.map((t) => [t, { ok: true as const, output: results[t], ref: t }])),
  );
  const copilot = createCopilotService({
    aiAccess: allowAiAccess(),
    router: createConfiguredRouter(cfg, { fetchFn, ...(opts.signer !== undefined ? { signer: opts.signer } : {}) }),
    mcp,
    store,
    killSwitch: { isHalted: () => false },
    cache: new CopilotAnswerCache(),
    config: { tokenCap: 20_000, maxTurns: 4, pseudonymTtlMs: 60_000 },
    modelId: "claude-haiku-5-5",
  });
  const caller = { userId: USER, tenantId: T1, duties: new Set(["mcp.copilot.use", "ap.invoice.read"]), ...(opts.legalEntityId !== undefined ? { legalEntityId: opts.legalEntityId } : {}) };
  const run = (): Promise<ChatResponse> => copilot.chat(caller, "caller-token", { mode: "ask", message: "what is open?" });
  return { store, run };
}

describe("copilot routing in the standard deployment", () => {
  it.each(Object.keys(adapters) as AdapterName[])("%s adapter never receives a CUI record", async (adapter) => {
    const cfg = loadConfig({ ...base, ...adapters[adapter] });
    const h = harness(adapter, [callTool(GET), finalText("Nothing I can use.")]);
    const { run } = await ask(cfg, h.fetchFn, { [GET]: { id: "cui-1", note: CUI_MARKER } });
    const res = await run();
    expect(res.excluded).toEqual({ count: 1, kinds: ["invoice"], reason: "NO_APPROVED_ROUTE" });
    expect(res.answer).toContain("no approved AI route");
    expect(res.citations).toEqual([]);
    expect(res.refusal).toBeNull();
    expect(h.providerBodies).toHaveLength(2);
    for (const body of h.providerBodies) expect(body).not.toContain(CUI_MARKER);
    expect(h.providerBodies[1]).toContain("withheld");
    expect(h.resolverBodies[0]).toContain("cui-1");
  });

  it("filters only the CUI record out of a mixed list and counts just that record", async () => {
    const cfg = loadConfig({ ...base, ...adapters.anthropic });
    const h = harness("anthropic", [callTool("depot.ap.invoice.get"), callTool("depot.ap.vendor.list"), finalText("Invoice summary.")]);
    const mcpResults = { "depot.ap.invoice.get": { id: "ok-1", note: OK_MARKER }, "depot.ap.vendor.list": { items: [{ id: "cui-2", note: CUI_MARKER }, { id: "ok-3", note: OK_MARKER }] } };
    const { run, store } = await ask(cfg, h.fetchFn, mcpResults);
    const res = await run();
    expect(h.providerBodies).toHaveLength(3);
    expect(h.providerBodies[1]).toContain(OK_MARKER);
    for (const body of h.providerBodies) expect(body).not.toContain(CUI_MARKER);
    expect(h.providerBodies[2]).toContain("ok-3");
    expect(res.excluded).toEqual({ count: 1, kinds: ["vendor"], reason: "NO_APPROVED_ROUTE" });
    expect(res.citations.map((c) => c.sourceRef)).toEqual(["depot.ap.invoice.get", "depot.ap.vendor.list"]);
    expect(JSON.stringify([...store.actionRows.values()])).not.toContain(CUI_MARKER);
  });

  it.each<ResolverMode>(["http500", "unreachable", "mismatch"])("treats records as CUI when the resolver fails (%s)", async (mode) => {
    const cfg = loadConfig({ ...base, ...adapters.anthropic });
    const h = harness("anthropic", [callTool(GET), finalText("ok")], mode);
    const { run } = await ask(cfg, h.fetchFn, { [GET]: { items: [{ id: "ok-1", note: OK_MARKER }, { id: "ok-2", note: OK_MARKER }] } });
    const res = await run();
    expect(res.excluded?.count).toBe(2);
    for (const body of h.providerBodies) expect(body).not.toContain(OK_MARKER);
  });

  it("resolves classes with agents' own service token for the caller's tenant and legal entity, never the caller's bearer", async () => {
    const cfg = loadConfig({ ...base, ...adapters.anthropic });
    const h = harness("anthropic", [callTool(GET), finalText("ok")]);
    const { run } = await ask(cfg, h.fetchFn, { [GET]: { id: "ok-1", note: OK_MARKER } });
    const res = await run();
    expect(res.excluded).toBeUndefined();
    expect(h.providerBodies[1]).toContain(OK_MARKER);
    const headers = h.resolverHeaders[0] ?? {};
    expect(headers["authorization"]).toBeUndefined();
    expect(JSON.stringify(headers)).not.toContain("caller-token");
    expect(claimsOf(headers["x-depot-service-token"] ?? "")).toMatchObject({ iss: "agents", aud: "compliance", tid: T1, le: LE, act: USER, duties: ["compliance.data-designation.resolve"] });
  });

  it.each([
    ["no service signing key", { signer: undefined, legalEntityId: LE }],
    ["a caller token that names no legal entity", { signer, legalEntityId: undefined }],
    ["a caller token that names several legal entities", { signer, legalEntityId: `${LE},0192a2b4-0000-7000-8000-00000000000b` }],
  ])("withholds every record without calling compliance when there is %s", async (_name, opts) => {
    const cfg = loadConfig({ ...base, ...adapters.anthropic });
    const h = harness("anthropic", [callTool(GET), finalText("ok")]);
    const { run } = await ask(cfg, h.fetchFn, { [GET]: { id: "ok-1", note: OK_MARKER } }, opts);
    const res = await run();
    expect(res.excluded?.count).toBe(1);
    expect(h.resolverBodies).toEqual([]);
    for (const body of h.providerBodies) expect(body).not.toContain(OK_MARKER);
  });

  it("withholds every record result when the compliance URL is unset, without calling a resolver", async () => {
    const { AGENTS_COMPLIANCE_URL: _unset, ...env } = base;
    const cfg = loadConfig({ ...env, ...adapters.anthropic });
    const h = harness("anthropic", [callTool(GET), finalText("ok")]);
    const { run } = await ask(cfg, h.fetchFn, { [GET]: { id: "ok-1", note: OK_MARKER } });
    const res = await run();
    expect(res.excluded?.count).toBe(1);
    expect(h.resolverBodies).toEqual([]);
    for (const body of h.providerBodies) expect(body).not.toContain(OK_MARKER);
  });

  it("withholds a result that names no record", async () => {
    const cfg = loadConfig({ ...base, ...adapters.anthropic });
    const h = harness("anthropic", [callTool(GET), finalText("ok")]);
    const { run } = await ask(cfg, h.fetchFn, { [GET]: { total: OK_MARKER } });
    const res = await run();
    expect(res.excluded).toEqual({ count: 1, kinds: ["invoice"], reason: "NO_APPROVED_ROUTE" });
    for (const body of h.providerBodies) expect(body).not.toContain(OK_MARKER);
  });

  it("passes a result with no records and does not report an exclusion", async () => {
    const cfg = loadConfig({ ...base, ...adapters.anthropic });
    const h = harness("anthropic", [callTool(GET), finalText("None open.")]);
    const { run } = await ask(cfg, h.fetchFn, { [GET]: [] });
    const res = await run();
    expect(res.excluded).toBeUndefined();
    expect(res.answer).toBe("None open.");
    expect(h.resolverBodies).toEqual([]);
  });
});

describe("copilot routing in the cui boundary", () => {
  it("refuses with AGT-0218 when no route is attested: no provider request, no budget hold", async () => {
    const cfg = loadConfig({ ...base, ...adapters.anthropic, AGENTS_BOUNDARY: "cui" });
    const h = harness("anthropic", [finalText("never")]);
    const { run, store } = await ask(cfg, h.fetchFn, { [GET]: { id: "cui-1", note: CUI_MARKER } });
    const res = await run();
    expect(res.refusal).toMatchObject({ code: "AGT-0218", message: expect.stringContaining("no approved AI route") });
    expect(h.providerBodies).toEqual([]);
    expect(h.resolverBodies).toEqual([]);
    expect(store.reserved).toEqual([]);
    expect(store.runs.size).toBe(0);
  });
});
