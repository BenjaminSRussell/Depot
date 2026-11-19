import type { AgentDefinitionStore, DisabledRow } from "../api/agentDefinitions.js";
import type { PgAgentStore } from "./pg.js";

export class PgAgentDefinitions implements AgentDefinitionStore {
  constructor(private readonly store: PgAgentStore) {}

  disabledAgents(tenantId: string): Promise<ReadonlyMap<string, DisabledRow>> {
    return this.store.withTenant(tenantId, async (c) => {
      const res = await c.query<{ agent_id: string; disabled_by: string; disabled_at: Date; reason: string | null }>(
        "select agent_id, disabled_by, disabled_at, reason from agent_disabled where tenant_id = $1",
        [tenantId],
      );
      return new Map(res.rows.map((r) => [r.agent_id, { disabledBy: r.disabled_by, disabledAt: r.disabled_at.toISOString(), reason: r.reason }]));
    });
  }

  async setDisabled(tenantId: string, agentId: string, disabled: boolean, changedBy: string, reason: string | null): Promise<void> {
    await this.store.withTenant(tenantId, async (c) => {
      await c.query("select pg_advisory_xact_lock(hashtextextended($1, 0))", [`agent_disabled:${tenantId}:${agentId}`]);
      const changed = disabled
        ? await c.query(
            "insert into agent_disabled (tenant_id, agent_id, disabled_by, reason) values ($1, $2, $3, $4) on conflict do nothing",
            [tenantId, agentId, changedBy, reason],
          )
        : await c.query("delete from agent_disabled where tenant_id = $1 and agent_id = $2", [tenantId, agentId]);
      if (changed.rowCount === 0) return;
      await c.query("insert into agent_disabled_change (tenant_id, agent_id, disabled, changed_by, reason) values ($1, $2, $3, $4, $5)", [
        tenantId,
        agentId,
        disabled,
        changedBy,
        reason,
      ]);
    });
  }
}
