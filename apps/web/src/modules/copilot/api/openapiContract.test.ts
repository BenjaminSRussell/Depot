import { describe, expect, it } from "vitest";
import spec from "../../../../../../services/agents-ts/api/openapi.yaml?raw";
import { chatResponseSchema, citationSchema, conversationSchema, toolCallSchema } from "./client";

function schemaBlock(name: string): string {
  const start = spec.indexOf(`\n    ${name}:\n`);
  expect(start).toBeGreaterThan(-1);
  const rest = spec.slice(start + 1);
  const next = rest.slice(1).search(/\n {4}\S/);
  return next === -1 ? rest : rest.slice(0, next + 1);
}

function list(source: string): string[] {
  return source.split(",").map((item) => item.trim());
}

function requiredOf(block: string): string[] {
  const match = /^ {6}required: \[([^\]]*)\]/m.exec(block);
  return match?.[1] === undefined ? [] : list(match[1]);
}

function enumAfter(block: string, property: string): string[] {
  const match = new RegExp(`${property}: \\{[^}]*enum: \\[([^\\]]*)\\]`).exec(block);
  return match?.[1] === undefined ? [] : list(match[1]);
}

const chat = schemaBlock("ChatResponse");
const refusal = schemaBlock("RefusalResponse");

const fixture = {
  answer: "Open invoices total 10.",
  citations: [{ sourceType: "RECORD", sourceRef: "record:ledger/ap-invoice/INV-1", label: "INV-1" }],
  toolCalls: [{ tool: "depot.ap.invoice.get", status: "OK" }],
  refusal: null,
  generated: true,
};

describe("web client against services/agents-ts/api/openapi.yaml", () => {
  it("parses a create-conversation fixture shaped like the 201 body, and rejects a non-uuid id", () => {
    expect(spec).toMatch(/required: \[conversationId\]\n\s+properties:\n\s+conversationId: \{type: string, format: uuid\}/);
    expect(conversationSchema.parse({ conversationId: "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11" }).conversationId).toBeDefined();
    expect(conversationSchema.safeParse({ conversationId: "not-a-uuid" }).success).toBe(false);
  });

  it("parses a ChatResponse fixture carrying every key the spec requires", () => {
    for (const key of requiredOf(chat)) expect(fixture).toHaveProperty(key);
    expect(chatResponseSchema.parse(fixture).answer).toBe(fixture.answer);
  });

  it("rejects generated:false and a missing generated, as the spec's enum [true] demands", () => {
    expect(enumAfter(chat, "generated")).toEqual(["true"]);
    expect(chatResponseSchema.safeParse({ ...fixture, generated: false }).success).toBe(false);
    const { generated: _generated, ...withoutGenerated } = fixture;
    expect(chatResponseSchema.safeParse(withoutGenerated).success).toBe(false);
  });

  it("keeps the citation and tool-call enums in step with the spec", () => {
    expect(new Set(enumAfter(chat, "sourceType"))).toEqual(new Set(citationSchema.shape.sourceType.options));
    expect(new Set(enumAfter(chat, "status"))).toEqual(new Set(toolCallSchema.shape.status.options));
  });

  it("parses a refusal carried by a ChatResponse and matches the RefusalResponse shape", () => {
    expect(requiredOf(refusal)).toEqual(["refusal", "generated"]);
    const refused = { ...fixture, answer: "", citations: [], toolCalls: [], refusal: { code: "AGT-0203", message: "Not allowed." } };
    expect(chatResponseSchema.parse(refused).refusal?.code).toBe("AGT-0203");
  });
});
