import type pg from "pg";
import type { AiPolicyStore, AiPolicyWrite } from "../api/aiDataPolicy.js";
import { ApiError } from "../api/principal.js";
import { aiPolicyChangedEvent } from "../domain/events.js";
import { type AiDataPolicy, applyPatch, defaultPolicy, policyFromStored } from "../domain/aiDataPolicy.js";
import { enqueueOutbox } from "./outbox.js";
import type { PgAgentStore } from "./pg.js";

const UNIQUE_VIOLATION = "23505";

const STALE = new ApiError(412, "AGT-0412", "the policy changed since you read it: reload and retry");

async function readCurrent(c: Pick<pg.PoolClient, "query">, tenantId: string): Promise<AiDataPolicy> {
  const res = await c.query<{ version: number; after_modules: unknown }>(
    "select version, after_modules from ai_data_policy_change where tenant_id = $1 and legal_entity_id is null order by version desc limit 1",
    [tenantId],
  );
  const row = res.rows[0];
  return row === undefined ? defaultPolicy() : policyFromStored(row.version, row.after_modules);
}

export class PgAiDataPolicyStore implements AiPolicyStore {
  constructor(private readonly store: PgAgentStore) {}

  read(tenantId: string): Promise<AiDataPolicy> {
    return this.store.withTenant(tenantId, (c) => readCurrent(c, tenantId));
  }

  write(input: AiPolicyWrite): Promise<AiDataPolicy> {
    return this.store.withTenant(input.tenantId, async (c) => {
      const current = await readCurrent(c, input.tenantId);
      if (current.version !== input.expectedVersion) throw STALE;
      const applied = applyPatch(current, input.patch);
      if (!applied.changed) return current;
      const next = { version: current.version + 1, modules: applied.modules };
      const changedAt = input.now.toISOString();
      try {
        await c.query(
          `insert into ai_data_policy_change (tenant_id, version, before_modules, after_modules, by_user, loosening, reason, changed_at)
           values ($1, $2, $3::jsonb, $4::jsonb, $5, $6, $7, $8)`,
          [input.tenantId, next.version, JSON.stringify(current.modules), JSON.stringify(next.modules), input.userId, applied.loosening, input.patch.reason ?? null, changedAt],
        );
      } catch (err) {
        if ((err as { code?: string }).code === UNIQUE_VIOLATION) throw STALE;
        throw err;
      }
      const event = aiPolicyChangedEvent({ tenantId: input.tenantId, version: next.version, loosening: applied.loosening, byUser: input.userId, changedAt });
      await enqueueOutbox(c, event, "ai_data_policy", input.tenantId);
      return next;
    });
  }
}
