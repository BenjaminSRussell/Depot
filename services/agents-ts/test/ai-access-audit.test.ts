import { describe, expect, it } from "vitest";
import type { Principal } from "../src/api/principal.js";
import { AUDIT_GRANT, createServiceSigner } from "../src/api/serviceSigner.js";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import { chunkIds, createAiAccessAudit, type AiAccessAudit, type AiAccessRow } from "../src/runtime/aiAccessAudit.js";
import { createCopilotService, type ChatResponse } from "../src/runtime/copilot.js";
import { GUARD_REFUSAL_MESSAGE, type EgressGuard } from "../src/runtime/egressGuard.js";
import type { ModelResponse } from "../src/runtime/engine.js";
import { allowAiAccess, openRouter, ScriptedProvider, testGuard } from "./fakes.js";
import { FakeMcp, MemoryStore, T1, tool, USER } from "./surface-fakes.js";

const LE = "2f6c3e0a-8d6b-4c1e-9a43-0c5f3b1d7e21";
const INVOICE = "depot.ap.invoice.get";
const VENDOR = "depot.ap.vendor.list";
const PAYMENT = "depot.ap.payment.list";
const SEARCH = "depot.knowledge.search";
const SECRET_TOTAL = "1200.00";
const PROMPT = "Which invoices are overdue for Acme Holdings?";
const DUTIES = new Set(["mcp.copilot.use", "ap.invoice.read", "ap.vendor.read", "ap.payment.read", "knowledge.read"]);
const caller: Principal = { tenantId: T1, userId: USER, duties: DUTIES, legalEntityId: LE };
const seed = Buffer.alloc(32, 7).toString("base64");
const signer = createServiceSigner({ seedBase64: seed });
const answer = (text: string): ModelResponse => ({ text, usage: { tokensIn: 100, tokensOut: 20 } });
const call = (toolName: string): ModelResponse => ({
  text: "",
  proposal: { tool: toolName, args: {}, confidence: 1, amount: { amount: "0", currency: "USD" } },
  usage: { tokensIn: 200, tokensOut: 10 },
});

const rowFor = (ids: string[]): AiAccessRow => ({
  agentId: "copilot",
  runId: "run-1",
  tool: INVOICE,
  entityType: "invoice",
  recordIds: ids,
  recordClasses: ids.map(() => "NONE"),
  highestClass: "P0",
  routeId: "test",
  redactionCounts: {},
  excludedCounts: {},
  policyVersion: 1,
  decision: "ALLOWED",
  traceId: "run-1",
  occurredAt: "2026-11-19T00:00:00.000Z",
});

const created = (init: { status?: number } = {}) => {
  const calls: Array<{ url: string; headers: Record<string, string>; body: Record<string, unknown> }> = [];
  const fetchFn = (async (url: string, req: { headers: Record<string, string>; body: string }) => {
    calls.push({ url, headers: req.headers, body: JSON.parse(req.body) as Record<string, unknown> });
    return new Response("{}", { status: init.status ?? 201 });
  }) as unknown as typeof fetch;
  return { calls, fetchFn };
};

describe("the AI data-access audit client", () => {
  it("posts each row with a signed service token and splits more than 200 ids into several rows", async () => {
    const { calls, fetchFn } = created();
    const audit = createAiAccessAudit({ baseUrl: "http://audit.test/", signer, fetchFn });
    const ids = Array.from({ length: 450 }, (_, i) => `inv-${i}`);
    await audit.record(caller, [rowFor(ids)]);
    expect(calls.map((c) => (c.body["recordIds"] as string[]).length)).toEqual([200, 200, 50]);
    expect(calls.map((c) => (c.body["recordClasses"] as string[]).length)).toEqual([200, 200, 50]);
    expect(calls[0]).toMatchObject({ url: "http://audit.test/v1/audit/ai-data-access", body: { kind: "READ", agentId: "copilot", tool: INVOICE } });
    const claims = JSON.parse(Buffer.from((calls[0]?.headers["x-depot-service-token"] ?? "").split(".")[0] ?? "", "base64url").toString()) as Record<string, unknown>;
    expect(claims).toMatchObject({ iss: "agents", aud: "audit", tid: T1, le: LE, act: USER, duties: [...AUDIT_GRANT] });
    expect(chunkIds(rowFor(ids))).toHaveLength(3);
  });

  it("throws, and logs only the error name, when the audit service answers anything but 201", async () => {
    for (const status of [200, 400, 403, 500]) {
      const logs: unknown[][] = [];
      const audit = createAiAccessAudit({ baseUrl: "http://audit.test", signer, fetchFn: created({ status }).fetchFn, log: (...args) => logs.push(args) });
      await expect(audit.record(caller, [rowFor(["inv-1"])])).rejects.toThrow(String(status));
      expect(JSON.stringify(logs)).not.toContain("inv-1");
      expect(logs).toHaveLength(1);
    }
  });

  it("throws on a network failure, an unset URL, no signing key and a caller with no legal entity", async () => {
    const down = (async () => {
      throw new TypeError("fetch failed");
    }) as unknown as typeof fetch;
    const row = [rowFor(["inv-1"])];
    await expect(createAiAccessAudit({ baseUrl: "http://audit.test", signer, fetchFn: down }).record(caller, row)).rejects.toThrow("fetch failed");
    await expect(createAiAccessAudit({ baseUrl: undefined, signer }).record(caller, row)).rejects.toThrow("AGENTS_AUDIT_URL");
    await expect(createAiAccessAudit({ baseUrl: "http://audit.test", signer: undefined }).record(caller, row)).rejects.toThrow("signing key");
    const { legalEntityId: _omitted, ...noEntity } = caller;
    await expect(createAiAccessAudit({ baseUrl: "http://audit.test", signer, fetchFn: created().fetchFn }).record(noEntity, row)).rejects.toThrow("legal entity");
  });

  it("mints an audit token only for one real legal entity", () => {
    const subject = { tenantId: T1, legalEntityId: LE, actor: USER };
    expect(() => signer.mintForAudit({ ...subject, legalEntityId: "" })).toThrow(TypeError);
    expect(() => signer.mintForAudit({ ...subject, legalEntityId: "00000000-0000-0000-0000-000000000000" })).toThrow(TypeError);
    expect(() => signer.mintForAudit({ ...subject, tenantId: "" })).toThrow(TypeError);
    expect(() => signer.mintForAudit({ ...subject, actor: "" })).toThrow(TypeError);
  });
});

const harness = (script: ModelResponse[], aiAccess: AiAccessAudit, results: Record<string, unknown>, guard?: EgressGuard) => {
  const provider = new ScriptedProvider(script);
  const mcp = new FakeMcp(
    [tool(INVOICE, "ap.invoice.read"), tool(VENDOR, "ap.vendor.read"), tool(PAYMENT, "ap.payment.read"), tool(SEARCH, "knowledge.read")],
    Object.fromEntries(Object.entries(results).map(([name, output]) => [name, { ok: true as const, output, ref: name }])),
  );
  const copilot = createCopilotService({
    aiAccess,
    guard: guard ?? testGuard(),
    router: openRouter(provider),
    mcp,
    store: new MemoryStore(),
    killSwitch: { isHalted: () => false },
    cache: new CopilotAnswerCache(),
    config: { tokenCap: 50_000, maxTurns: 5, pseudonymTtlMs: 60_000 },
    modelId: "claude-haiku-5-5",
  });
  const chat = (who: Principal = caller): Promise<ChatResponse> => copilot.chat(who, "token", { mode: "ask", message: PROMPT });
  return { provider, chat };
};

const READS = {
  [INVOICE]: { id: "INV-7", total: SECRET_TOTAL },
  [VENDOR]: { items: [{ id: "V-1", name: "Acme Holdings" }, { id: "V-2", name: "Globex" }] },
  [PAYMENT]: { items: [{ id: "P-1", amount: SECRET_TOTAL }] },
};

describe("copilot chat and the AI data-access audit", () => {
  it("writes one row per read tool with ids, classes, route, counts and policy version, and no values or prompt text", async () => {
    const audit = allowAiAccess();
    const h = harness([call(INVOICE), call(VENDOR), call(PAYMENT), answer("done")], audit, READS);
    const res = await h.chat();
    expect(res.refusal).toBeNull();
    expect(audit.rows.map((r) => [r.tool, r.entityType, r.recordIds])).toEqual([
      [INVOICE, "invoice", ["INV-7"]],
      [VENDOR, "vendor", ["V-1", "V-2"]],
      [PAYMENT, "payment", ["P-1"]],
    ]);
    expect(audit.rows[1]).toMatchObject({ agentId: "copilot", recordClasses: ["NONE", "NONE"], routeId: "test", highestClass: "P2", policyVersion: 1, decision: "ALLOWED", redactionCounts: {}, excludedCounts: {} });
    expect(audit.rows[0]?.traceId).toBe(audit.rows[0]?.runId);
    const stored = JSON.stringify(audit.rows);
    for (const value of [SECRET_TOTAL, "Acme Holdings", "Globex", PROMPT, "Which invoices"]) expect(stored).not.toContain(value);
  });

  it("makes no model call that would carry a result when the audit write fails, and gives the standard refusal", async () => {
    const failing: AiAccessAudit = {
      record: async () => {
        throw new Error("audit down");
      },
    };
    const h = harness([call(INVOICE), answer("INV-7 totals 1200.00")], failing, READS);
    const res = await h.chat();
    expect(res.refusal).toEqual({ code: "AGT-0232", message: GUARD_REFUSAL_MESSAGE });
    expect(res.answer).toBe(GUARD_REFUSAL_MESSAGE);
    expect(h.provider.seen).toHaveLength(1);
    expect(h.provider.seen.some((s) => s.user.includes(SECRET_TOTAL))).toBe(false);
  });

  it("refuses with AGT-0232 through the real client when the caller's token names no legal entity", async () => {
    const { calls, fetchFn } = created();
    const h = harness([call(INVOICE), answer("x")], createAiAccessAudit({ baseUrl: "http://audit.test", signer, fetchFn }), READS);
    const { legalEntityId: _omitted, ...noEntity } = caller;
    const res = await h.chat(noEntity);
    expect(res.refusal?.code).toBe("AGT-0232");
    expect(calls).toHaveLength(0);
    expect(h.provider.seen).toHaveLength(1);
  });

  it("records the guard's decision, and still writes the rows when the guard blocks the call", async () => {
    const verdicts = [
      { user: "ok", decision: "ALLOWED" as const },
      { user: "", decision: "BLOCKED" as const },
    ];
    const guard: EgressGuard = {
      ...testGuard(),
      inspect: async () => {
        const v = verdicts.shift() ?? { user: "", decision: "BLOCKED" as const };
        return { user: v.user, typedReplaced: 0, context: { inputTokens: new Set(), inputRecordIds: new Set(), highestClass: "P2" }, result: { callId: "c", highestClass: "P2", routeId: "test", policyVersion: 4, counts: { EMAIL: 2 }, excluded: {}, decision: v.decision } };
      },
    };
    const audit = allowAiAccess();
    const h = harness([call(INVOICE), answer("never")], audit, READS, guard);
    const res = await h.chat();
    expect(res.refusal?.code).toBe("AGT-0219");
    expect(audit.rows).toEqual([expect.objectContaining({ tool: INVOICE, decision: "BLOCKED", highestClass: "P2", policyVersion: 4, redactionCounts: { EMAIL: 2 } })]);
  });

  it("splits a read of more than 200 ids into rows of at most 200 and still writes an id-less row for an empty list", async () => {
    const many = { items: Array.from({ length: 450 }, (_, i) => ({ id: `V-${i}` })) };
    const audit = allowAiAccess();
    const h = harness([call(VENDOR), call(PAYMENT), answer("done")], audit, { ...READS, [VENDOR]: many, [PAYMENT]: { items: [] } });
    await h.chat();
    expect(audit.rows.map((r) => [r.tool, r.recordIds.length])).toEqual([[VENDOR, 200], [VENDOR, 200], [VENDOR, 50], [PAYMENT, 0]]);
  });

  it("audits an id-less result and a knowledge search, and refuses without a provider call when the write fails", async () => {
    const summary = { total: 3 };
    const search = { chunks: [] };
    const audit = allowAiAccess();
    const ok = harness([call(INVOICE), call(SEARCH), answer("done")], audit, { ...READS, [INVOICE]: summary, [SEARCH]: search });
    await ok.chat();
    expect(audit.rows.map((r) => [r.tool, r.entityType, r.recordIds])).toEqual([
      [INVOICE, "invoice", []],
      [SEARCH, "knowledge", []],
    ]);
    const failing: AiAccessAudit = {
      record: async () => {
        throw new Error("audit down");
      },
    };
    for (const [name, output] of [[INVOICE, summary], [SEARCH, search]] as const) {
      const h = harness([call(name), answer("leak")], failing, { ...READS, [name]: output });
      const res = await h.chat();
      expect(res.refusal?.code).toBe("AGT-0232");
      expect(h.provider.seen).toHaveLength(1);
    }
  });

  it("sends ids such as INV/2026/001 and over-long ids in a form the audit service accepts", async () => {
    const audit = allowAiAccess();
    const h = harness([call(VENDOR), answer("done")], audit, { ...READS, [VENDOR]: { items: [{ id: "INV/2026/001" }, { id: "a b" }, { id: "x".repeat(300) }] } });
    await h.chat();
    expect(audit.rows[0]?.recordIds).toEqual(["INV/2026/001", "a_b", "x".repeat(128)]);
  });
});
