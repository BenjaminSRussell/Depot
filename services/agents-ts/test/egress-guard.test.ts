import { afterEach, describe, expect, it, vi } from "vitest";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import { Pseudonymizer, scanPii } from "../src/domain/redaction.js";
import { createCopilotService } from "../src/runtime/copilot.js";
import type { ModelResponse } from "../src/runtime/engine.js";
import { AMOUNT_WITHHELD_NOTICE } from "../src/domain/financialFields.js";
import { createProviderFactory, loadConfig, withEgressGuard } from "../src/config.js";
import { PROPOSE_TOOL_NAME } from "../src/providers/wire.js";
import { AgentError } from "../src/errors.js";
import { EGRESS_GUARD_TOOL, type Analyzer, type EgressInput, type GuardDecision } from "../src/runtime/egressGuard.js";
import { Metrics } from "../src/server.js";
import { openRouter, ScriptedProvider, testGuard, allowAiAccess } from "./fakes.js";
import { FakeMcp, headers, MemoryStore, serve, tool } from "./surface-fakes.js";

const GET = "depot.ap.invoice.get";
const DUTIES = "mcp.copilot.use,ap.invoice.read";
const CANARIES = {
  iban: "DE89370400440532013000",
  bankAccount: "00123456789",
  card: "5500 0000 0000 0004",
  ein: "12-3456789",
  ssn: "078-05-1120",
  salary: "85000.50",
  typedCard: "4111 1111 1111 1111",
};
const toolOutput = {
  id: "INV-7",
  total: "1200.00",
  iban: CANARIES.iban,
  bankAccount: CANARIES.bankAccount,
  paidWith: CANARIES.card,
  ein: CANARIES.ein,
  ssn: CANARIES.ssn,
  salary: CANARIES.salary,
};
const answer = (text: string): ModelResponse => ({ text, usage: { tokensIn: 100, tokensOut: 20 } });
const call = (): ModelResponse => ({
  text: "",
  proposal: { tool: GET, args: { invoiceId: "INV-7" }, confidence: 1, amount: { amount: "0", currency: "USD" } },
  usage: { tokensIn: 200, tokensOut: 10 },
});

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  vi.useRealTimers();
  await close?.();
  close = undefined;
});

const chatWith = async (analyzer?: Analyzer, observe?: (seconds: number, decision: GuardDecision) => void) => {
  const store = new MemoryStore();
  const provider = new ScriptedProvider([call(), answer("Done.")]);
  const copilot = createCopilotService({
    aiAccess: allowAiAccess(),
    router: openRouter(provider),
    mcp: new FakeMcp([tool(GET, "ap.invoice.read")], { [GET]: { ok: true, output: toolOutput, ref: "INV-7" } }),
    store,
    killSwitch: { isHalted: () => false },
    cache: new CopilotAnswerCache(),
    config: { tokenCap: 20_000, maxTurns: 3, pseudonymTtlMs: 60_000 },
    modelId: "claude-haiku-5-5",
    guard: testGuard({ ...(analyzer !== undefined ? { analyzer } : {}), ...(observe !== undefined ? { observe } : {}) }),
  });
  const srv = await serve({ approvals: store, copilot });
  close = srv.close;
  const res = await fetch(`${srv.base}/api/v1/copilot/chat`, {
    method: "POST",
    headers: headers(DUTIES),
    body: JSON.stringify({ mode: "ask", message: `Check invoice INV-7 paid with ${CANARIES.typedCard}` }),
  });
  const rows = [...store.actionRows.values()].flat();
  return { body: (await res.json()) as { answer: string; dataClass: string; refusal: { code: string } | null }, provider, rows };
};

describe("egress guard before the provider", () => {
  it("replaces every financial canary in tool results and typed text with a stable pseudonym", async () => {
    const { body, provider, rows } = await chatWith();
    expect(provider.seen).toHaveLength(2);
    for (const seen of provider.seen) {
      for (const value of Object.values(CANARIES)) expect(seen.user).not.toContain(value);
      expect(scanPii(seen.user)).toEqual([]);
    }
    const second = provider.seen[1]?.user ?? "";
    for (const pseudonym of ["[IBAN-1]", "[BANKACCT-1]", "[CARD-1]", "[CARD-2]", "[TAXID-1]", "[SSN-1]"]) expect(second).toContain(pseudonym);
    expect(second).not.toContain("salary");
    expect(body.answer).toContain(AMOUNT_WITHHELD_NOTICE);
    expect(provider.seen[0]?.user).toContain("[CARD-1]");
    expect(body.dataClass).toBe("P3");
    const guardRows = rows.filter((r) => r.toolName === EGRESS_GUARD_TOOL);
    expect(guardRows.map((r) => r.guardResult?.decision)).toEqual(["REDACTED", "REDACTED"]);
    expect(guardRows[1]?.guardResult?.counts).toEqual({ CARD: 2, IBAN: 1, BANKACCT: 1, TAXID: 1, SSN: 1 });
    expect(guardRows[1]?.guardResult?.excluded).toEqual({ AMOUNT_WITHHELD: 1 });
    const stored = JSON.stringify(rows);
    for (const value of Object.values(CANARIES)) expect(stored).not.toContain(value);
    expect(scanPii(stored)).toEqual([]);
  });

  it("blocks with ANALYZER_ERROR and calls no provider when the analyzer throws", async () => {
    const { body, provider, rows } = await chatWith({
      scan: () => {
        throw new Error(`down ${CANARIES.iban}`);
      },
    });
    expect(body.refusal?.code).toBe("AGT-0219");
    expect(provider.seen).toHaveLength(0);
    expect(rows[0]?.guardResult).toMatchObject({ decision: "BLOCKED", errorCode: "ANALYZER_ERROR", counts: {} });
    expect(JSON.stringify(rows)).not.toContain(CANARIES.iban);
  });

  it("blocks with RESIDUAL_PII when a finding survives redaction", async () => {
    const { provider, rows } = await chatWith({ scan: () => [{ category: "IBAN", value: "kept" }] });
    expect(provider.seen).toHaveLength(0);
    expect(rows[0]?.guardResult).toMatchObject({ decision: "BLOCKED", errorCode: "RESIDUAL_PII" });
  });

  it("exports the duration histogram by decision and never a value", async () => {
    const metrics = new Metrics();
    await chatWith(undefined, (seconds, decision) => metrics.observeGuard(seconds, decision));
    const text = metrics.render();
    expect(text).toContain('agents_egress_guard_duration_seconds_count{decision="REDACTED"} 2');
    expect(text).toContain('agents_egress_guard_duration_seconds_bucket{decision="REDACTED",le="+Inf"} 2');
    for (const value of Object.values(CANARIES)) expect(text).not.toContain(value);
  });
});

describe("guard inspect", () => {
  const input = (pseudo: Pseudonymizer, over: Partial<EgressInput> = {}): EgressInput => ({
    tenantId: "t",
    callId: "c",
    routeId: "r",
    pseudo,
    system: "system",
    typed: "hello",
    results: [],
    ...over,
  });

  it("allows clean text unchanged with an empty record", async () => {
    const verdict = await testGuard().inspect(input(new Pseudonymizer("t", 1000, () => 0), { results: ["Result of x: INV-2026-0042 1,200.50"] }));
    expect(verdict.result).toMatchObject({ decision: "ALLOWED", highestClass: "P0", counts: {}, excluded: {}, routeId: "r", policyVersion: 1 });
    expect(verdict.user).toBe("hello\n\nResult of x: INV-2026-0042 1,200.50");
  });

  it("keeps one pseudonym per value across calls with the same pseudonymizer", async () => {
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard();
    const first = await guard.inspect(input(pseudo, { typed: `iban ${CANARIES.iban}` }));
    const second = await guard.inspect(input(pseudo, { results: [`other ${CANARIES.iban}`] }));
    expect(first.user).toBe("iban [IBAN-1]");
    expect(second.user).toBe("hello\n\nother [IBAN-1]");
    expect(first.typedReplaced).toBe(1);
  });

  it("carries this turn's tokens, record ids and highest class in the context", async () => {
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const verdict = await testGuard().inspect(input(pseudo, { typed: `iban ${CANARIES.iban}`, recordIds: new Set(["R-1"]) }));
    expect([...verdict.context.inputTokens]).toEqual(["[IBAN-1]"]);
    expect([...verdict.context.inputRecordIds]).toEqual(["R-1"]);
    expect(verdict.context.highestClass).toBe("P3");
  });

  it("scans each distinct text once per pseudonymizer across turns and keeps the full-scan verdict", async () => {
    const seen: string[] = [];
    const analyzer: Analyzer = { scan: (text) => (seen.push(text), scanPii(text)) };
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard({ analyzer });
    const first = `a ${CANARIES.iban}`;
    const second = `b ${CANARIES.ssn}`;
    await guard.inspect(input(pseudo, { results: [first] }));
    const turn = await guard.inspect(input(pseudo, { results: [first, second] }));
    expect(seen.filter((t) => t === first)).toHaveLength(1);
    expect(seen.filter((t) => t === second)).toHaveLength(1);
    expect(turn.result).toMatchObject({ decision: "REDACTED", counts: { IBAN: 1, SSN: 1 } });
    expect(turn.user).toBe("hello\n\na [IBAN-1]\n\nb [SSN-1]");
  });

  it("does not memoise a failed scan, so a retry scans again", async () => {
    let calls = 0;
    const analyzer: Analyzer = { scan: (text) => { calls += 1; if (calls === 1) throw new Error("down"); return scanPii(text); } };
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard({ analyzer });
    const failed = await guard.inspect(input(pseudo, { results: [`x ${CANARIES.iban}`] }));
    const retry = await guard.inspect(input(pseudo, { results: [`x ${CANARIES.iban}`] }));
    expect(failed.result.errorCode).toBe("ANALYZER_ERROR");
    expect(retry.result).toMatchObject({ decision: "REDACTED", counts: { IBAN: 1 } });
  });

  it("still blocks on residual PII when the memoised texts were clean", async () => {
    let residual = false;
    const analyzer: Analyzer = { scan: (text) => (residual && text.startsWith("system") ? [{ category: "IBAN", value: "kept" }] : []) };
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = testGuard({ analyzer });
    await guard.inspect(input(pseudo));
    residual = true;
    const verdict = await guard.inspect(input(pseudo));
    expect(verdict.result).toMatchObject({ decision: "BLOCKED", errorCode: "RESIDUAL_PII" });
  });

  it("blocks with ANALYZER_TIMEOUT when the analyzer misses the deadline", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const guard = testGuard({ analyzer: { scan: () => new Promise(() => undefined) }, deadlineMs: 50 });
    const pending = guard.inspect(input(new Pseudonymizer("t", 1000, () => 0)));
    await vi.advanceTimersByTimeAsync(50);
    const verdict = await pending;
    expect(verdict.result).toMatchObject({ decision: "BLOCKED", errorCode: "ANALYZER_TIMEOUT" });
    expect(verdict.user).toBe("");
  });

  it("logs a detail code and a scrubbed, truncated message, never the raw value, when inspect fails", async () => {
    const logs: Record<string, unknown>[] = [];
    const failing: Analyzer = { scan: () => { throw Object.assign(new Error(`down ${CANARIES.iban} 1234567 ${"x".repeat(300)}`), { code: "ECONNREFUSED" }); } };
    await testGuard({ analyzer: failing, log: (_l, _m, extra) => logs.push(extra ?? {}) }).inspect(input(new Pseudonymizer("t", 1000, () => 0)));
    const record = logs[0] ?? {};
    expect(record).toMatchObject({ errorCode: "ANALYZER_ERROR", error: "Error", errorDetailCode: "ECONNREFUSED" });
    const message = String(record.errorMessage);
    expect(message).toContain("down [REDACTED] [N]");
    expect(message.length).toBeLessThanOrEqual(160);
    expect(JSON.stringify(record)).not.toContain(CANARIES.iban);
  });

  it("logs the scrubbed message for admit and omits the detail code when it is not a code", async () => {
    const logs: Record<string, unknown>[] = [];
    const failing: Analyzer = { scan: () => { throw Object.assign(new Error(`down ${CANARIES.iban}`), { code: "not a code" }); } };
    const admission = await testGuard({ analyzer: failing, log: (_l, _m, extra) => logs.push(extra ?? {}) }).admit({ tenantId: "t", tool: GET, text: "x" });
    expect(admission).toMatchObject({ pass: false, failure: "ANALYZER_ERROR" });
    expect(logs[0]).toMatchObject({ errorMessage: "down [REDACTED]" });
    expect(logs[0]).not.toHaveProperty("errorDetailCode");
    expect(JSON.stringify(logs)).not.toContain(CANARIES.iban);
  });

  it("logs only the name for a non-Error throw", async () => {
    const logs: Record<string, unknown>[] = [];
    const failing: Analyzer = { scan: () => { throw "boom " + CANARIES.iban; } };
    await testGuard({ analyzer: failing, log: (_l, _m, extra) => logs.push(extra ?? {}) }).inspect(input(new Pseudonymizer("t", 1000, () => 0)));
    expect(logs[0]).toMatchObject({ error: "unknown" });
    expect(logs[0]).not.toHaveProperty("errorMessage");
  });

  it("returns an empty context when the analyzer fails", async () => {
    const failing: Analyzer = { scan: () => { throw new Error("down"); } };
    const verdict = await testGuard({ analyzer: failing }).inspect(input(new Pseudonymizer("t", 1000, () => 0), { recordIds: new Set(["R-1"]) }));
    expect(verdict.result.decision).toBe("BLOCKED");
    expect(verdict.context.inputTokens.size).toBe(0);
    expect(verdict.context.inputRecordIds.size).toBe(0);
  });
});

describe("run provider wrapper", () => {
  const response: ModelResponse = { text: "ok" };
  const make = (calls: string[]) => withEgressGuard({ complete: async (req) => (calls.push(req.user), response) }, testGuard());

  it("forwards a prompt with no sensitive data", async () => {
    const calls: string[] = [];
    await make(calls).complete({ system: "s", user: "hello [IBAN-1]", allowedTools: [], maxOutputTokens: 10 });
    expect(calls).toEqual(["hello [IBAN-1]"]);
  });

  it("refuses with AGT-0219 and makes no call when a raw IBAN reaches the provider layer", async () => {
    const calls: string[] = [];
    const err = await make(calls)
      .complete({ system: "s", user: "pay DE89 3704 0044 0532 0130 00", allowedTools: [], maxOutputTokens: 10 })
      .catch((e: unknown) => e);
    expect(err).toBeInstanceOf(AgentError);
    expect((err as AgentError).code).toBe("AGT-0219");
    expect(calls).toEqual([]);
  });
});

const wireAnswer = (adapter: string, input: Record<string, unknown>): unknown => {
  if (adapter === "anthropic") return { content: [{ type: "tool_use", name: PROPOSE_TOOL_NAME, input }], usage: { input_tokens: 10, output_tokens: 5 } };
  if (adapter === "bedrock") return { output: { message: { content: [{ toolUse: { name: PROPOSE_TOOL_NAME, input } }] } }, usage: { inputTokens: 10, outputTokens: 5 } };
  return { choices: [{ message: { tool_calls: [{ function: { name: PROPOSE_TOOL_NAME, arguments: JSON.stringify(input) } }] } }], usage: { prompt_tokens: 10, completion_tokens: 5 } };
};

describe("adapter parity", () => {
  const base = { MCP_URL: "http://mcp:8080", AGENTS_JWT_ISSUER: "https://idp.dev.depot.dev/realms/dev", AGENTS_JWKS_URL: "https://idp.dev.depot.dev/realms/dev/protocol/openid-connect/certs" };
  const adapters: Record<string, Record<string, string>> = {
    anthropic: { AGENTS_PROVIDER: "anthropic", ANTHROPIC_API_KEY: "k" },
    bedrock: { AGENTS_PROVIDER: "bedrock", AWS_REGION: "us-east-1", AWS_ACCESS_KEY_ID: "id", AWS_SECRET_ACCESS_KEY: "secret" },
    azure: { AGENTS_PROVIDER: "openai", OPENAI_API_KEY: "k", AGENTS_OPENAI_BASE_URL: "https://acme.openai.azure.com/openai/deployments/d", AGENTS_OPENAI_AUTH: "api-key" },
    vllm: { AGENTS_PROVIDER: "openai", OPENAI_API_KEY: "k", AGENTS_OPENAI_BASE_URL: "https://vllm.internal/v1" },
  };
  const typed = `Pay ${CANARIES.iban} with card ${CANARIES.typedCard}, EIN ${CANARIES.ein}, ssn ${CANARIES.ssn}`;

  it("sends the same redacted prompt through all four adapters and no canary", async () => {
    const verdict = await testGuard().inspect({ tenantId: "t", callId: "c", routeId: "r", pseudo: new Pseudonymizer("t", 60_000, () => 0), system: "s", typed, results: [] });
    expect(verdict.result.decision).toBe("REDACTED");
    const escaped = JSON.stringify(verdict.user).slice(1, -1);
    for (const [name, env] of Object.entries(adapters)) {
      const bodies: string[] = [];
      const fetchFn = (async (_url: string | URL, init?: RequestInit): Promise<Response> => {
        bodies.push(String(init?.body ?? ""));
        return Response.json(wireAnswer(name, { text: "ok" }));
      }) as unknown as typeof fetch;
      const provider = createProviderFactory(loadConfig({ ...base, ...env }), fetchFn)("m");
      await provider.complete({ system: "s", user: verdict.user, allowedTools: [GET], maxOutputTokens: 10 });
      expect(bodies, name).toHaveLength(1);
      expect(bodies[0], name).toContain(escaped);
      for (const value of [CANARIES.iban, CANARIES.typedCard, CANARIES.ein, CANARIES.ssn]) expect(bodies[0], name).not.toContain(value);
    }
  });
});
