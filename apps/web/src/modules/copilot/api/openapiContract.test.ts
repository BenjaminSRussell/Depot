import { describe, expect, it } from "vitest";
import spec from "../../../../../../services/agents-ts/api/openapi.yaml?raw";
import { BRIEFING_AGENT_IDS, briefingListSchema } from "./briefings";
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
const briefingBlock = schemaBlock("Briefing");
const briefingItemBlock = schemaBlock("BriefingItem");
const briefingListBlock = schemaBlock("BriefingList");
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

  it("parses a BriefingList fixture carrying every key the spec requires and keeps the agent enum in step", () => {
    const item = { agentId: "cash_app", factId: "f1", text: "Apply payment", actionId: "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11", route: "/assistant/proposals/6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11" };
    const briefing = {
      id: "0b9a6b5e-2f4c-4f6e-9c1d-3d2b1a0f9e88",
      status: "READY",
      narrative: false,
      createdAt: "2026-11-19T10:00:00.000Z",
      completedAt: null,
      readAt: null,
      changes: [item],
      risks: [],
      recommendedActions: [],
      notes: [],
    };
    const body = { items: [briefing], nextCursor: null, nextScheduledAt: null };
    for (const key of requiredOf(briefingBlock)) expect(briefing).toHaveProperty(key);
    for (const key of requiredOf(briefingListBlock)) expect(body).toHaveProperty(key);
    for (const key of requiredOf(briefingItemBlock)) expect(item).toHaveProperty(key);
    expect(briefingListSchema.parse(body).items).toHaveLength(1);
    expect(new Set(enumAfter(briefingItemBlock, "agentId"))).toEqual(new Set(BRIEFING_AGENT_IDS));
    expect(new Set(enumAfter(briefingBlock, "status"))).toEqual(new Set(["RUNNING", "READY", "FAILED"]));
  });
});
