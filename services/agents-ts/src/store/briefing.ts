import { randomUUID } from "node:crypto";
import { AGENT_IDS, type AgentId } from "../agents/catalog.js";
import { ApiError } from "../api/principal.js";
import {
  encodeCursor,
  FACT_ROW_CAP,
  RATE_LIMIT,
  STALE_RUNNING_MINUTES,
  type BriefingContent,
  type BriefingFacts,
  type BriefingFactsReader,
  type BriefingPage,
  type BriefingStore,
  type Cursor,
  type StoredBriefing,
} from "../domain/briefing.js";
import type { PgAgentStore } from "./pg.js";

const UNIQUE_VIOLATION = "23505";
const INVALID_DATETIME = new Set(["22007", "22008"]);
const COLUMNS = "id, status, narrative, content, created_at, created_at::text as created_text, completed_at, read_at";

interface BriefingRow {
  id: string;
  status: StoredBriefing["status"];
  narrative: boolean;
  content: BriefingContent | null;
  created_at: Date;
  created_text: string;
  completed_at: Date | null;
  read_at: Date | null;
}

const toStored = (r: BriefingRow): StoredBriefing => ({
  id: r.id,
  status: r.status,
  narrative: r.narrative,
  createdAt: r.created_at.toISOString(),
  completedAt: r.completed_at?.toISOString() ?? null,
  readAt: r.read_at?.toISOString() ?? null,
  content: r.content,
});

const codeOf = (err: unknown): string | undefined => (typeof err === "object" && err !== null ? (err as { code?: string }).code : undefined);

export class PgBriefingStore implements BriefingStore {
  constructor(private readonly store: PgAgentStore) {}

  async create(tenantId: string, userId: string): Promise<string> {
    const id = randomUUID();
    try {
      await this.store.withTenant(tenantId, async (c) => {
        await c.query("select pg_advisory_xact_lock(hashtextextended($1, 0))", [`${tenantId}:${userId}`]);
        await c.query(
          `update agent_briefing set status = 'FAILED', completed_at = now()
           where tenant_id = $1 and user_id = $2 and status = 'RUNNING' and created_at < now() - make_interval(mins => $3)`,
          [tenantId, userId, STALE_RUNNING_MINUTES],
        );
        const window = await c.query<{ n: string; wait: number | null }>(
          `select count(*) as n, ceil(extract(epoch from min(created_at) + interval '1 hour' - now()))::int as wait
           from agent_briefing where tenant_id = $1 and user_id = $2 and created_at > now() - interval '1 hour'`,
          [tenantId, userId],
        );
        if (Number(window.rows[0]?.n) >= RATE_LIMIT) {
          const wait = Math.max(1, window.rows[0]?.wait ?? 1);
          throw new ApiError(429, "AGT-0230", `at most ${RATE_LIMIT} briefings can be generated per hour`, { headers: { "retry-after": String(wait) } });
        }
        await c.query("insert into agent_briefing (tenant_id, id, user_id, status) values ($1, $2, $3, 'RUNNING')", [tenantId, id, userId]);
      });
    } catch (err) {
      if (codeOf(err) !== UNIQUE_VIOLATION) throw err;
      const running = await this.store.withTenant(tenantId, (c) =>
        c.query<{ id: string }>("select id from agent_briefing where tenant_id = $1 and user_id = $2 and status = 'RUNNING'", [tenantId, userId]),
      );
      throw new ApiError(409, "AGT-0231", "a briefing is already being generated", { members: { briefingId: running.rows[0]?.id ?? null } });
    }
    return id;
  }

  async finish(tenantId: string, id: string, content: BriefingContent): Promise<void> {
    await this.store.withTenant(tenantId, (c) =>
      c.query("update agent_briefing set status = 'READY', narrative = false, content = $3::jsonb, completed_at = now() where tenant_id = $1 and id = $2 and status = 'RUNNING'", [
        tenantId,
        id,
        JSON.stringify(content),
      ]),
    );
  }

  async fail(tenantId: string, id: string): Promise<void> {
    await this.store.withTenant(tenantId, (c) =>
      c.query("update agent_briefing set status = 'FAILED', completed_at = now() where tenant_id = $1 and id = $2 and status = 'RUNNING'", [tenantId, id]),
    );
  }

  async get(tenantId: string, userId: string, id: string): Promise<StoredBriefing | undefined> {
    const res = await this.store.withTenant(tenantId, (c) =>
      c.query<BriefingRow>(`select ${COLUMNS} from agent_briefing where tenant_id = $1 and user_id = $2 and id = $3`, [tenantId, userId, id]),
    );
    return res.rows[0] === undefined ? undefined : toStored(res.rows[0]);
  }

  async list(tenantId: string, userId: string, limit: number, cursor: Cursor | undefined): Promise<BriefingPage> {
    let rows: BriefingRow[];
    try {
      rows = (
        await this.store.withTenant(tenantId, (c) =>
          c.query<BriefingRow>(
            `select ${COLUMNS} from agent_briefing
             where tenant_id = $1 and user_id = $2 and ($3::boolean or (created_at, id) < ($4::timestamptz, $5::uuid))
             order by created_at desc, id desc limit $6`,
            [tenantId, userId, cursor === undefined, cursor?.[0] ?? null, cursor?.[1] ?? null, limit + 1],
          ),
        )
      ).rows;
    } catch (err) {
      if (INVALID_DATETIME.has(codeOf(err) ?? "")) throw new ApiError(400, "AGT-0400", "cursor is not valid");
      throw err;
    }
    const page = rows.slice(0, limit);
    const last = page[page.length - 1];
    return {
      items: page.map(toStored),
      nextCursor: rows.length > limit && last !== undefined ? encodeCursor([last.created_text, last.id]) : null,
    };
  }

  async markRead(tenantId: string, userId: string, id: string): Promise<StoredBriefing | undefined> {
    const res = await this.store.withTenant(tenantId, (c) =>
      c.query<BriefingRow>(
        `update agent_briefing set read_at = coalesce(read_at, now()) where tenant_id = $1 and user_id = $2 and id = $3 returning ${COLUMNS}`,
        [tenantId, userId, id],
      ),
    );
    return res.rows[0] === undefined ? undefined : toStored(res.rows[0]);
  }
}

export class PgBriefingFacts implements BriefingFactsReader {
  constructor(private readonly store: PgAgentStore) {}

  async read(tenantId: string, userId: string, duties: readonly string[], now: Date): Promise<BriefingFacts> {
    const visible = "r.tenant_id = $1 and a.agent_id = any($4::text[]) and (a.invoker_user_id = $2 or r.approval_duty = any($3::text[]))";
    const approvals = `from approval_request r join agent_run a on a.tenant_id = r.tenant_id and a.run_id = r.run_id where ${visible}`;
    return this.store.withTenant(tenantId, async (c) => {
      const args = [tenantId, userId, [...duties], [...AGENT_IDS]];
      const pending = await c.query(
        `select a.agent_id, r.action_id, r.tool_name, r.amount, r.currency, r.self_approval_limit, r.self_approval_currency, r.requested_at, r.expiry_hours
         ${approvals} and r.decision is null order by r.requested_at limit ${FACT_ROW_CAP}`,
        args,
      );
      const decided = await c.query(
        `select a.agent_id, r.action_id, r.tool_name, r.decision, r.decided_at
         ${approvals} and r.decision is not null and r.decided_at > $5::timestamptz - interval '24 hours' order by r.decided_at desc limit ${FACT_ROW_CAP}`,
        [...args, now],
      );
      const runs = await c.query(
        `select agent_id, run_id, status, created_at from agent_run
         where tenant_id = $1 and invoker_user_id = $2 and agent_id = any($3::text[]) and status in ('COMMITTED', 'FAILED')
           and created_at > $4::timestamptz - interval '24 hours' order by created_at desc limit ${FACT_ROW_CAP}`,
        [tenantId, userId, [...AGENT_IDS], now],
      );
      return {
        pending: pending.rows.map((r) => ({
          agentId: r.agent_id as AgentId,
          actionId: r.action_id as string,
          tool: r.tool_name as string,
          amount: r.amount as string,
          currency: r.currency as string,
          selfApprovalLimit: r.self_approval_limit as string,
          selfApprovalCurrency: r.self_approval_currency as string,
          requestedAt: (r.requested_at as Date).toISOString(),
          expiryHours: r.expiry_hours as number | null,
        })),
        decided: decided.rows.map((r) => ({
          agentId: r.agent_id as AgentId,
          actionId: r.action_id as string,
          tool: r.tool_name as string,
          decision: r.decision as string,
          decidedAt: (r.decided_at as Date).toISOString(),
        })),
        runs: runs.rows.map((r) => ({
          agentId: r.agent_id as AgentId,
          runId: r.run_id as string,
          status: r.status as string,
          createdAt: (r.created_at as Date).toISOString(),
        })),
      };
    });
  }
}
