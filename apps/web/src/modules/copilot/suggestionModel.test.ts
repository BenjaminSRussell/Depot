import { describe, expect, it } from "vitest";
import { runTraceSchema } from "./api/runTrace";
import { toolActionLabel } from "./domain";
import { suggestionFixtures, UNRENDERED_MARKER } from "./suggestionFixtures";
import { answerViewModel, citationRoute, resolveRecord, toSuggestionViewModels } from "./suggestionModel";

const run = { runId: "r1", agentId: "ap_coding", agentVersion: 1 };
const action = {
  seq: 1,
  toolName: "depot.ap.invoice.propose_coding",
  actionId: "a1",
};
const invoiceRecord = {
  entityType: "ap-invoice",
  recordId: "inv 7",
  label: "Invoice INV-7",
};

describe("runTraceSchema", () => {
  it("rejects a confidence outside 0 to 1 or not a number", () => {
    for (const confidence of [-0.1, 1.2, "high", Number.NaN]) {
      expect(
        runTraceSchema.safeParse({ run, actions: [{ ...action, confidence }] })
          .success,
      ).toBe(false);
    }
  });

  it("rejects a data used list that is not an array, an item without a record id, or a missing agent version", () => {
    expect(
      runTraceSchema.safeParse({ run, actions: [{ ...action, dataUsed: {} }] })
        .success,
    ).toBe(false);
    expect(
      runTraceSchema.safeParse({
        run,
        actions: [
          { ...action, dataUsed: [{ entityType: "ap-invoice", label: "x" }] },
        ],
      }).success,
    ).toBe(false);
    expect(
      runTraceSchema.safeParse({
        run: { runId: "r1", agentId: "ap_coding" },
        actions: [],
      }).success,
    ).toBe(false);
  });
});

describe("toSuggestionViewModels", () => {
  it("maps rationale, confidence and version when the trace carries them", () => {
    expect(suggestionFixtures.answer.suggestion).toMatchObject({
      agentLabel: "Journal review agent",
      agentVersion: 3,
      rationale: "The posting differs from the usual pattern for this account.",
      confidencePercent: 82,
    });
  });

  it("leaves rationale and confidence unset and records empty when the trace omits them", () => {
    const [model] = toSuggestionViewModels(
      runTraceSchema.parse({ run, actions: [action] }),
    );
    expect(model).toMatchObject({
      rationale: undefined,
      confidencePercent: undefined,
      records: [],
    });
  });

  it("treats a blank rationale as absent", () => {
    const [model] = toSuggestionViewModels(
      runTraceSchema.parse({ run, actions: [{ ...action, rationale: "   " }] }),
    );
    expect(model?.rationale).toBeUndefined();
  });

  it("lists each tool once in sequence order with a plain label", () => {
    const tools = toSuggestionViewModels(
      runTraceSchema.parse({
        run,
        actions: [
          { seq: 2, toolName: "depot.ap.invoice.post", actionId: "a2" },
          {
            seq: 1,
            toolName: "depot.ap.invoice.propose_coding",
            actionId: "a1",
          },
          { seq: 3, toolName: "depot.ap.invoice.post", actionId: "a3" },
        ],
      }),
    )[0]?.tools;
    expect(tools).toEqual([
      "Propose coding on ap invoice",
      "Post on ap invoice",
    ]);
  });

  it("never carries the prompt, redacted or committed fields into the view model", () => {
    for (const { suggestion } of Object.values(suggestionFixtures)) {
      expect(JSON.stringify(suggestion)).not.toContain(UNRENDERED_MARKER);
    }
  });
});

describe("toolActionLabel", () => {
  it("falls back for a name that is not shaped like a tool name", () => {
    expect(toolActionLabel("whatever")).toBe("A tool call");
  });
});

describe("resolveRecord", () => {
  it("links the record page when the user holds its duty", () => {
    expect(resolveRecord(invoiceRecord, ["receiving.ap.inquire"])).toEqual({
      entityLabel: "Vendor invoice",
      label: "Invoice INV-7",
      href: "/ap-invoices/inv%207",
    });
  });

  it("names only the entity type, without id or label, when the duty is missing", () => {
    const resolved = resolveRecord(invoiceRecord, ["receivables.view"]);
    expect(resolved).toEqual({
      entityLabel: "Vendor invoice",
      label: undefined,
      href: undefined,
    });
    expect(JSON.stringify(resolved)).not.toContain("INV-7");
  });

  it("renders an unknown entity type as plain text, even for an inherited object key", () => {
    for (const entityType of ["mystery-record", "constructor"]) {
      expect(
        resolveRecord({ entityType, recordId: "x", label: "Secret" }, [
          "receiving.ap.inquire",
        ]),
      ).toEqual({
        entityLabel: "A record",
        label: undefined,
        href: undefined,
      });
    }
  });
});

describe("citationRoute", () => {
  const duties = ["receiving.ap.inquire"];

  it("links a record the user may open to its page, and nothing else", () => {
    expect(citationRoute({ sourceRef: "record:ledger/ap-invoice/INV-1042" }, duties)).toBe("/ap-invoices/INV-1042");
  });

  it.each([
    "https://evil.example/x",
    "//evil.example/x",
    "javascript:alert(1)",
    "![x](http://a/b.png)",
    "/ap-invoices/INV-1042",
    "INV-1042",
    "depot.ledger.journal.get",
    "metric:revenue",
    "audit:12",
    "record:ledger/ap-invoice",
    "record:ledger/ap-invoice/INV-1/extra",
    "record:ledger/ap-invoice/",
    "record:ledger/ap-invoice/a%2Fb",
    "record:ledger/ap-invoice/a b",
    "record:ledger/ap-invoice/a\nb",
    "record:ledger/unknown-entity/1",
    "record:ledger/__proto__/1",
    "record:ledger/constructor/1",
  ])("renders %j as plain text", (sourceRef) => {
    expect(citationRoute({ sourceRef }, duties)).toBeUndefined();
  });

  it("gives no link when the user lacks the duty for the entity", () => {
    expect(citationRoute({ sourceRef: "record:ledger/ap-invoice/INV-1042" }, [])).toBeUndefined();
    expect(citationRoute({ sourceRef: "record:ledger/journal-entry/1" }, duties)).toBeUndefined();
  });
});

describe("answerViewModel", () => {
  it("labels the copilot with no version and lists each tool call", () => {
    const model = answerViewModel(
      { answer: "x", citations: [], toolCalls: [{ tool: "metric.search", status: "OK" }, { tool: "analytics.query", status: "DENIED", code: "AGT-0203" }], generated: true, dataClass: "CUI" },
      "turn-1",
    );
    expect(model).toMatchObject({ actionId: "turn-1", agentLabel: "Copilot", tools: ["metric.search: OK", "analytics.query: DENIED (AGT-0203)"], records: [], dataClass: "CUI" });
    expect(model.agentVersion).toBeUndefined();
  });
});
