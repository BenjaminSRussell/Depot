import type { AgentBudgetStore, TokenCap, TokenPair } from "../api/agentBudgets.js";
import type { TokenBudget } from "../domain/budget.js";
import type { PgAgentStore } from "./pg.js";

export class PgAgentBudgets implements AgentBudgetStore {
  constructor(private readonly store: PgAgentStore) {}

  snapshot(tenantId: string, month: string): Promise<TokenBudget> {
    return this.store.budgetSnapshot(tenantId, month);
  }

  agentCaps(tenantId: string): Promise<ReadonlyMap<string, TokenCap>> {
    return this.store.withTenant(tenantId, async (c) => {
      const res = await c.query<{ agent_id: string; limit_in: string; limit_out: string }>(
        `select distinct on (agent_id) agent_id, limit_in, limit_out from agent_budget_change
          where tenant_id = $1 order by agent_id, change_seq desc`,
        [tenantId],
      );
      return new Map(res.rows.map((r) => [r.agent_id, { limitIn: Number(r.limit_in), limitOut: Number(r.limit_out) }]));
    });
  }

  agentUsage(tenantId: string, month: string): Promise<ReadonlyMap<string, TokenPair>> {
    return this.store.withTenant(tenantId, async (c) => {
      const res = await c.query<{ agent_id: string; tokens_in: string; tokens_out: string }>(
        `select agent_id, sum(tokens_in) as tokens_in, sum(tokens_out) as tokens_out from agent_run
          where tenant_id = $1 and run_month = $2::date group by agent_id`,
        [tenantId, `${month}-01`],
      );
      return new Map(res.rows.map((r) => [r.agent_id, { tokensIn: Number(r.tokens_in), tokensOut: Number(r.tokens_out) }]));
    });
  }

  async setAgentCap(tenantId: string, agentId: string, cap: TokenCap, changedBy: string): Promise<void> {
    await this.store.withTenant(tenantId, async (c) => {
      await c.query("select pg_advisory_xact_lock(hashtextextended($1, 0))", [`agent_budget_change:${tenantId}:${agentId}`]);
      await c.query(
        `insert into agent_budget_change (tenant_id, agent_id, limit_in, limit_out, previous_limit_in, previous_limit_out, changed_by)
         select $1::uuid, $2::text, $3::bigint, $4::bigint, p.limit_in, p.limit_out, $5::text
           from (select 1) one
           left join lateral (select limit_in, limit_out from agent_budget_change
                               where tenant_id = $1::uuid and agent_id = $2::text order by change_seq desc limit 1) p on true`,
        [tenantId, agentId, cap.limitIn, cap.limitOut, changedBy],
      );
    });
  }
}
