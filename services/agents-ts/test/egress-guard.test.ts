import { afterEach, describe, expect, it } from "vitest";
import { CopilotAnswerCache } from "../src/domain/copilot.js";
import { Pseudonymizer, scanPii } from "../src/domain/redaction.js";
import { createCopilotService } from "../src/runtime/copilot.js";
import type { ModelResponse } from "../src/runtime/engine.js";
import { createEgressGuard, EGRESS_GUARD_TOOL, type Analyzer, type EgressInput, type GuardDecision } from "../src/runtime/egressGuard.js";
import { Metrics } from "../src/server.js";
import { openRouter, ScriptedProvider } from "./fakes.js";
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
  await close?.();
  close = undefined;
});

const chatWith = async (analyzer?: Analyzer, observe?: (seconds: number, decision: GuardDecision) => void) => {
  const store = new MemoryStore();
  const provider = new ScriptedProvider([call(), answer("Done.")]);
  const copilot = createCopilotService({
    router: openRouter(provider),
    mcp: new FakeMcp([tool(GET, "ap.invoice.read")], { [GET]: { ok: true, output: toolOutput, ref: "INV-7" } }),
    store,
    killSwitch: { isHalted: () => false },
    cache: new CopilotAnswerCache(),
    config: { tokenCap: 20_000, maxTurns: 3, pseudonymTtlMs: 60_000 },
    modelId: "claude-haiku-5-5",
    guard: createEgressGuard({ ...(analyzer !== undefined ? { analyzer } : {}), deadlineMs: 50, ...(observe !== undefined ? { observe } : {}) }),
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
    for (const pseudonym of ["[IBAN-1]", "[BANKACCT-1]", "[CARD-1]", "[CARD-2]", "[TAXID-1]", "[SSN-1]", "[PAY-1]"]) expect(second).toContain(pseudonym);
    expect(provider.seen[0]?.user).toContain("[CARD-1]");
    expect(body.dataClass).toBe("P3");
    const guardRows = rows.filter((r) => r.toolName === EGRESS_GUARD_TOOL);
    expect(guardRows.map((r) => r.guardResult?.decision)).toEqual(["REDACTED", "REDACTED"]);
    expect(guardRows[1]?.guardResult?.counts).toEqual({ CARD: 2, IBAN: 1, BANKACCT: 1, TAXID: 1, SSN: 1, PAY: 1 });
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

  it("blocks with ANALYZER_TIMEOUT when the analyzer misses the deadline", async () => {
    const { body, provider, rows } = await chatWith({ scan: () => new Promise(() => undefined) });
    expect(body.refusal?.code).toBe("AGT-0219");
    expect(provider.seen).toHaveLength(0);
    expect(rows[0]?.guardResult).toMatchObject({ decision: "BLOCKED", errorCode: "ANALYZER_TIMEOUT" });
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
    const verdict = await createEgressGuard().inspect(input(new Pseudonymizer("t", 1000, () => 0), { results: ["Result of x: INV-2026-0042 1,200.50"] }));
    expect(verdict.result).toMatchObject({ decision: "ALLOWED", highestClass: "P0", counts: {}, excluded: {}, routeId: "r", policyVersion: 1 });
    expect(verdict.user).toBe("hello\n\nResult of x: INV-2026-0042 1,200.50");
  });

  it("keeps one pseudonym per value across calls with the same pseudonymizer", async () => {
    const pseudo = new Pseudonymizer("t", 1000, () => 0);
    const guard = createEgressGuard();
    const first = await guard.inspect(input(pseudo, { typed: `iban ${CANARIES.iban}` }));
    const second = await guard.inspect(input(pseudo, { results: [`other ${CANARIES.iban}`] }));
    expect(first.user).toBe("iban [IBAN-1]");
    expect(second.user).toBe("hello\n\nother [IBAN-1]");
    expect(first.typedReplaced).toBe(1);
  });
});
