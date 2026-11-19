import type { ActionDecline, ActionRecord } from "../domain/audit.js";
import { actionDeclinedEvent } from "../domain/events.js";
import { enqueueOutbox } from "./outbox.js";
import { type ActionRow, rowToAction, type PgAgentStore } from "./pg.js";

/** The audit rows of the actions, read and declined by action id. Every call is scoped to the caller's tenant. */
export interface ActionStore {
  find(tenantId: string, runId: string, actionId: string): Promise<ActionRecord | undefined>;
  /**
   * Declines an AUTO action first-writer-wins and returns the row as stored afterwards (ours, or the decline another
   * caller recorded first); undefined when the tenant's run has no such action.
   */
  decline(tenantId: string, runId: string, actionId: string, d: ActionDecline): Promise<ActionRecord | undefined>;
}

const SELECT_ONE = "select * from agent_action where tenant_id = $1 and run_id = $2 and action_id = $3";

export class PgActionStore implements ActionStore {
  constructor(private readonly store: PgAgentStore) {}

  async find(tenantId: string, runId: string, actionId: string): Promise<ActionRecord | undefined> {
    return this.store.withTenant(tenantId, async (c) => {
      const row = (await c.query<ActionRow>(SELECT_ONE, [tenantId, runId, actionId])).rows[0];
      return row === undefined ? undefined : rowToAction(row);
    });
  }

  async decline(tenantId: string, runId: string, actionId: string, d: ActionDecline): Promise<ActionRecord | undefined> {
    return this.store.withTenant(tenantId, async (c) => {
      const changed = (
        await c.query<ActionRow>(
          `update agent_action set declined_at = $4, declined_by = $5, decline_reason = $6
            where tenant_id = $1 and run_id = $2 and action_id = $3 and declined_at is null and decision = 'AUTO'
        returning *`,
          [tenantId, runId, actionId, d.at, d.by, d.reason],
        )
      ).rows[0];
      if (changed !== undefined) {
        await enqueueOutbox(c, actionDeclinedEvent({ tenantId, runId, actionId, toolName: changed.tool_name, declinedBy: d.by, declinedAt: d.at }), "agent_action", actionId);
        return rowToAction(changed);
      }
      const row = (await c.query<ActionRow>(SELECT_ONE, [tenantId, runId, actionId])).rows[0];
      return row === undefined ? undefined : rowToAction(row);
    });
  }
}
