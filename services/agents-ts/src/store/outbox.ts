import { randomUUID } from "node:crypto";
import type { AgentEvent } from "../domain/events.js";

/** CloudEvents source of every event this service writes. */
export const EVENT_SOURCE = "depot/agents";

const TYPE_RE = /^dev\.depot\.[a-z][a-z0-9]*\.[a-z][a-z0-9]*\.[a-z][a-z0-9]*\.v[0-9]+$/;

/** The part of a pg client the outbox insert needs (pg.PoolClient inside a transaction). */
export interface OutboxClient {
  query(text: string, values: unknown[]): Promise<unknown>;
}

/**
 * The CloudEvents 1.0 envelope an outbox row carries as its payload, the shape platform/go/outbox.Enqueue and
 * integration-hub-ts envelope() write: the `outbox-agents-ts` connector relays the payload as the Kafka value, and its
 * CloudEventsEnricher rejects a row without `type`, `id`, `time` or `specversion` 1.0, which stops the connector.
 * `legalentityid` is copied from the data's top-level legal_entity_id or legalEntityId when there is one, as
 * platform/go/events.New does.
 */
export function outboxEnvelope<T>(event: AgentEvent<T>, subject: string, id: string = randomUUID(), now: Date = new Date()): Record<string, unknown> {
  if (!TYPE_RE.test(event.type)) throw new Error(`invalid event type ${event.type}`);
  const le = legalEntityOf(event.data);
  return {
    specversion: "1.0",
    id,
    source: EVENT_SOURCE,
    type: event.type,
    subject,
    time: now.toISOString(),
    datacontenttype: "application/json",
    data: event.data,
    tenantid: event.tenantId,
    ...(le === undefined ? {} : { legalentityid: le }),
  };
}

/** Writes the event to the outbox inside the caller's transaction; the row id is the envelope id. */
export async function enqueueOutbox<T>(c: OutboxClient, event: AgentEvent<T>, aggregateType: string, aggregateId: string): Promise<void> {
  const env = outboxEnvelope(event, aggregateId);
  await c.query(
    "insert into outbox (id, tenant_id, aggregate_type, aggregate_id, type, payload) values ($1, $2, $3, $4, $5, $6::jsonb)",
    [env["id"], event.tenantId, aggregateType, aggregateId, event.type, JSON.stringify(env)],
  );
}

function legalEntityOf(data: unknown): string | undefined {
  if (typeof data !== "object" || data === null || Array.isArray(data)) return undefined;
  const d = data as Record<string, unknown>;
  for (const key of ["legalEntityId", "legal_entity_id"]) {
    if (typeof d[key] === "string" && d[key] !== "") return d[key] as string;
  }
  return undefined;
}
