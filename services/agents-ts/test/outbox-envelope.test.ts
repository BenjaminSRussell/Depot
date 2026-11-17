import { describe, expect, it } from "vitest";
import { type AgentEvent, approvalRequestedEvent, budgetThresholdEvent, killSwitchChangedEvent } from "../src/domain/events.js";
import { buildKillSwitchChange } from "../src/domain/killswitch.js";
import { EVENT_SOURCE, enqueueOutbox, outboxEnvelope } from "../src/store/outbox.js";

const T1 = "11111111-1111-4111-8111-111111111111";
const NOW = new Date("2026-11-17T08:00:00.000Z");

// What deploy/kafka-connect/enricher CloudEventsEnricher requires of the relayed payload: a conforming type, an id, a
// time and specversion 1.0, and a tenant (from the value or the tenantid header).
const ENRICHER_TYPE = /^dev\.depot\.[a-z][a-z0-9]*\.[a-z][a-z0-9]*\.[a-z][a-z0-9]*\.v[0-9]+$/;
function acceptedByEnricher(env: Record<string, unknown>): boolean {
  return (
    typeof env["type"] === "string" &&
    ENRICHER_TYPE.test(env["type"]) &&
    typeof env["id"] === "string" &&
    env["id"] !== "" &&
    typeof env["time"] === "string" &&
    env["specversion"] === "1.0" &&
    typeof env["tenantid"] === "string"
  );
}

describe("outbox payloads are CloudEvents envelopes (outbox-agents-ts relays them as is)", () => {
  const events: AgentEvent<unknown>[] = [
    approvalRequestedEvent({ tenantId: T1, runId: "run-1", actionId: "act-1", toolName: "depot.ap.invoice.propose_coding", proposalRedacted: "{}", confidence: 0.5 }),
    budgetThresholdEvent({ tenantId: T1, month: "2026-10", previous: "OK", current: { state: "SOFT", pct: 80 } })!,
    killSwitchChangedEvent(buildKillSwitchChange({ scope: "GLOBAL", active: true, byUser: T1, version: 1, changedAt: NOW.toISOString() }), T1),
  ];

  it.each(events.map((e) => [e.type, e] as const))("%s carries the envelope with its data", (_type, event) => {
    const env = outboxEnvelope(event, "subject-1", "evt-1", NOW);
    expect(env).toEqual({
      specversion: "1.0",
      id: "evt-1",
      source: EVENT_SOURCE,
      type: event.type,
      subject: "subject-1",
      time: "2026-11-17T08:00:00.000Z",
      datacontenttype: "application/json",
      data: event.data,
      tenantid: T1,
    });
    expect(acceptedByEnricher(env)).toBe(true);
  });

  it("copies a top-level legal entity into legalentityid, as platform/go/events.New does", () => {
    const env = outboxEnvelope({ type: "dev.depot.agent.run.completed.v1", tenantId: T1, data: { legal_entity_id: "le-1" } }, "s");
    expect(env["legalentityid"]).toBe("le-1");
  });

  it("refuses a type that breaks the naming rule", () => {
    expect(() => outboxEnvelope({ type: "agent.run.completed", tenantId: T1, data: {} }, "s")).toThrow(/invalid event type/);
  });

  it("enqueueOutbox writes the envelope as the payload, keyed by its id", async () => {
    const calls: unknown[][] = [];
    await enqueueOutbox({ query: async (_text, values) => void calls.push(values) }, events[0]!, "approval_request", "act-1");
    const [id, tenant, aggregateType, aggregateId, type, payload] = calls[0]!;
    const env = JSON.parse(payload as string) as Record<string, unknown>;
    expect([tenant, aggregateType, aggregateId, type]).toEqual([T1, "approval_request", "act-1", events[0]!.type]);
    expect(env["id"]).toBe(id);
    expect(env["data"]).toEqual(events[0]!.data);
    expect(acceptedByEnricher(env)).toBe(true);
  });
});
