import { describe, expect, it } from "vitest";
import { classifyByRules, decideRoute, type ModelKind, type PromptKind, type Rule } from "../src/domain/promptClass.js";

const model = (kind: string, confidence: unknown, runnerUp?: { kind: string; confidence: unknown }): string => JSON.stringify({ kind, confidence, ...(runnerUp !== undefined ? { runnerUp } : {}) });
const LOW = model("ask", 0.4);

const GOLDEN: Array<[string, PromptKind, string?]> = [
  ["show me overdue invoices over $10k", "navigate"],
  ["list open purchase orders", "navigate"],
  ["open vendor Acme", "navigate"],
  ["go to the trial balance", "navigate"],
  ["find invoice INV-1042", "navigate"],
  ["create a PO for 50 widgets from Acme", "navigate"],
  ["new journal entry", "navigate"],
  ["start a physical inventory count", "navigate"],
  ["show me last month's sales orders", "navigate"],
  ["find customers in Texas", "navigate"],
  ["what is the total of our overdue invoices over $10k?", "ask"],
  ["which vendors have open balances", "ask"],
  ["who approved invoice INV-1042", "ask"],
  ["why is the cash balance down", "ask"],
  ["how many open purchase orders do we have", "ask"],
  ["how much did we spend on freight in Q3", "ask"],
  ["when is the next payment run", "ask"],
  ["compare revenue this quarter to last", "ask"],
  ["explain the variance on account 4100", "ask"],
  ["trend of days sales outstanding", "ask"],
  ["apply this payment to invoice INV-1042", "act"],
  ["cancel purchase order PO-2231", "act"],
  ["release production order 5521", "act"],
  ["pay bill BILL-88 today", "act"],
  ["post journal 4410", "act"],
  ["approve the expense report EXP-19", "act"],
  ["reject invoice INV-77", "act"],
  ["reverse journal JE-9", "act"],
  ["settle the balance of $2,000", "act"],
  ["send invoice INV-1042 to Acme", "act"],
  ["invoices", "clarify", LOW],
  ["Acme", "clarify", model("navigate", 0.5)],
  ["the thing from yesterday", "clarify", "not json"],
  ["hmm", "clarify", model("ask", 0.9, { kind: "navigate", confidence: 0.8 })],
  ["help", "clarify", model("clarify", 0.99)],
  ["numbers please", "clarify", model("ask", "0.9")],
  ["last week", "clarify", model("act", 1.5)],
  ["that vendor", "clarify", ""],
  ["ok", "clarify", model("navigate", 0.59)],
  ["can you do it", "clarify", model("ask", 0.7, { kind: "act", confidence: 0.6 })],
];

const decide = (prompt: string, modelText = ""): PromptKind => decideRoute(classifyByRules(prompt), modelText).kind;

describe("classifyByRules", () => {
  it("answers the four ticket examples by rule alone", () => {
    expect(classifyByRules("show me overdue invoices over $10k")).toEqual({ status: "decided", kind: "navigate" });
    expect(classifyByRules("what is the total of our overdue invoices over $10k?")).toEqual({ status: "decided", kind: "ask" });
    expect(classifyByRules("apply this payment to invoice INV-1042")).toEqual({ status: "decided", kind: "act" });
    expect(classifyByRules("create a PO for 50 widgets from Acme")).toEqual({ status: "decided", kind: "navigate" });
  });

  it("checks act before navigate before ask and lets the first match win", () => {
    expect(classifyByRules("show me how to pay invoice INV-5")).toEqual({ status: "decided", kind: "act" });
    expect(classifyByRules("find out what we owe?")).toEqual({ status: "decided", kind: "navigate" });
  });

  it("needs a record or amount after an action verb", () => {
    expect(classifyByRules("approve")).toEqual({ status: "none" });
    expect(classifyByRules("how do I pay?")).toEqual({ status: "decided", kind: "ask" });
    expect(classifyByRules("released invoices")).toEqual({ status: "none" });
  });

  it("answers none for an empty prompt and ambiguous when two kinds match at one position", () => {
    expect(classifyByRules("   ")).toEqual({ status: "none" });
    const rules: Rule[] = [
      { kind: "navigate", re: /^open/ },
      { kind: "ask", re: /^open/ },
    ];
    expect(classifyByRules("open it", rules)).toEqual({ status: "ambiguous", kinds: ["navigate", "ask"] });
  });
});

describe("decideRoute", () => {
  it("keeps a rule decision at confidence 1 without reading the model text", () => {
    expect(decideRoute({ status: "decided", kind: "act" }, "garbage")).toEqual({ kind: "act", confidence: 1 });
  });

  it("takes a confident, clear model answer", () => {
    expect(decideRoute({ status: "none" }, model("ask", 0.9, { kind: "navigate", confidence: 0.3 }))).toEqual({ kind: "ask", confidence: 0.9 });
  });

  it("clarifies below 0.6 and when the top two are within 0.15, with the top two kinds", () => {
    expect(decideRoute({ status: "none" }, model("act", 0.4, { kind: "ask", confidence: 0.3 }))).toEqual({ kind: "clarify", confidence: 0.4, candidates: ["act", "ask"] });
    expect(decideRoute({ status: "none" }, model("ask", 0.8, { kind: "navigate", confidence: 0.7 }))).toEqual({ kind: "clarify", confidence: 0.8, candidates: ["ask", "navigate"] });
    expect(decideRoute({ status: "none" }, model("ask", 0.8, { kind: "navigate", confidence: 0.65 })).kind).toBe("ask");
  });

  it.each([
    ["non-JSON", "nope"],
    ["empty", ""],
    ["array", "[1]"],
    ["clarify as the model kind", model("clarify", 0.99)],
    ["unknown kind", model("delete", 0.99)],
    ["numeric kind", model("1", 0.99).replace('"1"', "1")],
    ["string confidence", model("ask", "0.9")],
    ["confidence above 1", model("ask", 1.5)],
    ["negative confidence", model("ask", -0.1)],
    ["infinite confidence", '{"kind":"ask","confidence":1e999}'],
  ])("clarifies when the model answer is %s", (_name, text) => {
    const result = decideRoute({ status: "none" }, text);
    expect(result.kind).toBe("clarify");
    expect(result.candidates).toHaveLength(2);
    expect(new Set(result.candidates).size).toBe(2);
  });

  it("ignores a malformed runnerUp and fills the second candidate from ambiguous rule kinds", () => {
    expect(decideRoute({ status: "none" }, '{"kind":"act","confidence":0.3,"runnerUp":"ask"}').candidates).toEqual(["act", "navigate"]);
    expect(decideRoute({ status: "ambiguous", kinds: ["ask", "act"] }, LOW).candidates).toEqual(["ask", "act"]);
  });
});

describe("golden set", () => {
  it("has ten phrases per kind", () => {
    const kinds: PromptKind[] = ["ask", "navigate", "act", "clarify"];
    for (const kind of kinds) expect(GOLDEN.filter(([, k]) => k === kind)).toHaveLength(10);
  });

  it("is classified with at least 90 percent accuracy by the rules plus the scripted fallback", () => {
    const right = GOLDEN.filter(([prompt, kind, modelText]) => decide(prompt, modelText) === kind);
    expect(right.length / GOLDEN.length).toBeGreaterThanOrEqual(0.9);
  });

  it("decides every non-clarify phrase by rule alone, so the fallback is never needed for it", () => {
    for (const [prompt, kind] of GOLDEN.filter(([, k]) => k !== "clarify")) expect(classifyByRules(prompt), prompt).toEqual({ status: "decided", kind: kind as ModelKind });
  });
});
