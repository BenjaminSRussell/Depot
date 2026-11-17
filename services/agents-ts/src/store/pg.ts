import type { ModelEndpoint } from "../cloudprofile/index.js";
import { randomUUID } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";
import pg from "pg";
import type { ActionRecord, Decision } from "../domain/audit.js";
import { budgetStatus, type BudgetState, type TokenBudget } from "../domain/budget.js";
import { budgetThresholdEvent } from "../domain/events.js";
import { enqueueOutbox } from "./outbox.js";
import { AgentError } from "../errors.js";
import type {
  AuditPort,
  BudgetHold,
  BudgetPort,
  BudgetRequest,
  ModelUsage,
  RunRequest,
  RunResult,
} from "../runtime/engine.js";

export const MIGRATIONS_DIR = new URL("../../migrations/", import.meta.url);

/** Same contract as integration-hub-ts pgStore.migrate (dbx.Migrate: one transaction per file with its schema_migrations row,
 * under the advisory lock); the two are separate npm packages with no shared TS library to import it from. */
export async function migrate(pool: pg.Pool, dir: URL = MIGRATIONS_DIR): Promise<void> {
  const client = await pool.connect();
  let healthy = true;
  try {
    // The dbx.Migrate lock (platform/go/dbx): a session advisory lock keyed on the schema, taken by polling, so a Job retry and
    // a dev pod migrating on start take turns and every statement runs on the connection that holds it.
    const key = (await client.query<{ key: string }>("select hashtextextended('depot.dbx.migrate:' || current_schema(), 0)::text as key")).rows[0]!.key;
    while (!(await client.query<{ got: boolean }>("select pg_try_advisory_lock($1::bigint) as got", [key])).rows[0]!.got) {
      await new Promise((resolve) => setTimeout(resolve, 100 + Math.random() * 150));
    }
    try {
      await client.query("create table if not exists schema_migrations (name text primary key, applied_at timestamptz not null default now())");
      const names = (await readdir(dir)).filter((n) => n.endsWith(".sql")).sort();
      for (const name of names) {
        const done = await client.query("select 1 from schema_migrations where name = $1", [name]);
        if (done.rowCount) continue;
        const body = await readFile(new URL(name, dir), "utf8");
        try {
          await client.query("begin");
          await client.query(body);
          await client.query("insert into schema_migrations(name) values ($1)", [name]);
          await client.query("commit");
        } catch (err) {
          await client.query("rollback").catch(() => undefined);
          throw new Error(`migration ${name}: ${(err as Error).message}`, { cause: err });
        }
      }
    } finally {
      // Never return a connection that may still hold the lock to the pool.
      await client.query("select pg_advisory_unlock($1::bigint)", [key]).catch(() => {
        healthy = false;
      });
    }
  } catch (err) {
    healthy = false;
    throw err;
  } finally {
    client.release(!healthy);
  }
}

/**
 * The first argument that runs the image as its schema migration step (`node dist/main.js migrate`): the chart's owner-role
 * migration Job (D28).
 */
export const MIGRATE_COMMAND = "migrate";
/** The default serving role (MIGRATE_APP_ROLE names another): refused by migrateFromEnv unless MIGRATE_ALLOW_APP_ROLE=true. */
export const APP_ROLE = "depot_app";

/** The owner role's URL: MIGRATE_DATABASE_URL, else DATABASE_URL. */
export function migrateUrl(env: Record<string, string | undefined>): string {
  const url = env["MIGRATE_DATABASE_URL"] || env["DATABASE_URL"];
  if (url === undefined || url === "") throw new Error("MIGRATE_DATABASE_URL or DATABASE_URL is required to migrate");
  return url;
}

const DURATION_UNITS: Record<string, number> = { ns: 1e-6, us: 1e-3, "µs": 1e-3, ms: 1, s: 1000, m: 60_000, h: 3_600_000 };

/**
 * How long to retry an unreachable database, in ms: MIGRATE_RETRY_TIMEOUT as a Go duration (`90s`, `5m`, `1h30m`, `1.5s`,
 * `0`, as dbx.MigrateRetry parses it), else 5 minutes.
 */
export function migrateRetryMs(env: Record<string, string | undefined>): number {
  const raw = env["MIGRATE_RETRY_TIMEOUT"];
  if (raw === undefined || raw === "") return 5 * 60_000;
  if (raw === "0") return 0;
  const part = /(\d+(?:\.\d*)?|\.\d+)(ns|us|µs|ms|s|m|h)/gy;
  let total = 0;
  let at = 0;
  for (let m = part.exec(raw); m !== null; m = part.exec(raw)) {
    total += Number(m[1]) * DURATION_UNITS[m[2]!]!;
    at = part.lastIndex;
  }
  if (at === 0 || at !== raw.length) throw new Error(`MIGRATE_RETRY_TIMEOUT must be a Go duration such as 90s, 5m or 1h30m, got ${JSON.stringify(raw)}`);
  return total;
}

const CONNECTION_CODES = new Set(["ECONNREFUSED", "ECONNRESET", "ENOTFOUND", "EAI_AGAIN", "ETIMEDOUT", "EHOSTUNREACH", "ENETUNREACH", "EPIPE", "57P01", "57P02", "57P03", "53300"]);
// Node's TLS verification failures (and a server without TLS where the URL requires it): no retry fixes them.
const TLS_CODES = /^(UNABLE_TO_|SELF_SIGNED_CERT|DEPTH_ZERO_SELF_SIGNED_CERT|CERT_|ERR_TLS_|ERR_SSL_|HOSTNAME_MISMATCH)/;

/** Whether err (or its cause) is a TLS failure no retry fixes. */
export function isTlsError(err: unknown): boolean {
  for (let e = err as { code?: unknown; message?: unknown; cause?: unknown } | null | undefined, depth = 0; e && depth < 5; e = e.cause as typeof e, depth++) {
    if (typeof e.code === "string" && TLS_CODES.test(e.code)) return true;
    if (typeof e.message === "string" && /does not support SSL connections/.test(e.message)) return true;
  }
  return false;
}

/**
 * Whether err (or its cause) means the database could not be reached or dropped the session (retried), not a statement's
 * own error, a rejected login (28xxx), a missing database (3D000) or a TLS failure.
 */
export function isConnectionError(err: unknown): boolean {
  if (isTlsError(err)) return false;
  for (let e = err as { code?: unknown; message?: unknown; cause?: unknown } | null | undefined, depth = 0; e && depth < 5; e = e.cause as typeof e, depth++) {
    const code = typeof e.code === "string" ? e.code : "";
    if (CONNECTION_CODES.has(code) || code.startsWith("08")) return true;
    if (code !== "") return false;
    if (typeof e.message === "string" && /Connection terminated|timeout exceeded when trying to connect|connect ECONNREFUSED/.test(e.message)) return true;
  }
  return false;
}

/**
 * True when the session is tied to the app role ($1) either way round: current_user or session_user is it, the current or
 * session role is a member of it (superusers left out), or it is a member of current_user and so inherits the owner's
 * rights. The same query as dbx.AppRoleQuery in the Go services.
 */
const APP_ROLE_QUERY = `select session_user::text = $1 or current_user::text = $1 or (app.oid is not null and (
    (not cur.rolsuper and pg_has_role(cur.oid, app.oid, 'MEMBER'))
    or (not sess.rolsuper and pg_has_role(sess.oid, app.oid, 'MEMBER'))
    or pg_has_role(app.oid, cur.oid, 'MEMBER'))) as app, current_user::text as name, session_user::text as session
from pg_roles cur
join pg_roles sess on sess.rolname = session_user
left join pg_roles app on app.rolname = $1
where cur.rolname = current_user`;

async function migrateOnce(pool: pg.Pool, dir: URL, appRole: string | null): Promise<void> {
  await pool.query("select 1");
  if (appRole !== null) {
    const who = (await pool.query<{ app: boolean; name: string; session: string }>(APP_ROLE_QUERY, [appRole])).rows[0];
    if (who?.app) {
      throw new Error(
        `refusing to migrate as ${JSON.stringify(who.name)} (session ${JSON.stringify(who.session)}): it is the serving role ${appRole}, a member of it, or a role ${appRole} inherits. Use the owner role's URL (MIGRATE_DATABASE_URL), or set MIGRATE_ALLOW_APP_ROLE=true where it owns the schema (dev only)`,
      );
    }
  }
  await migrate(pool, dir);
}

/**
 * Applies the migrations as the owner role (MIGRATE_DATABASE_URL, else DATABASE_URL) and closes the pool, as
 * dbx.RunMigrateCommand does in the Go services: each attempt (connect, app-role check, migrate()) is retried with back-off
 * from 1 s to 30 s for MIGRATE_RETRY_TIMEOUT when the database cannot be reached or drops the session; the app role
 * (MIGRATE_APP_ROLE, default depot_app) is refused unless MIGRATE_ALLOW_APP_ROLE=true; migrate() holds the dbx advisory
 * lock. A migration's SQL error, a rejected login, a missing database or a TLS failure fails at once.
 */
export async function migrateFromEnv(
  env: Record<string, string | undefined>,
  dir: URL = MIGRATIONS_DIR,
  log: (message: string, fields: Record<string, unknown>) => void = (message, fields) =>
    void process.stderr.write(`${JSON.stringify({ level: "warn", message, ...fields })}\n`),
): Promise<void> {
  const url = migrateUrl(env);
  const deadline = Date.now() + migrateRetryMs(env);
  const appRole = env["MIGRATE_ALLOW_APP_ROLE"] === "true" ? null : env["MIGRATE_APP_ROLE"] || APP_ROLE;
  const pool = new pg.Pool({ connectionString: url, max: 1, connectionTimeoutMillis: 10_000 });
  // A dropped session errors on the idle client; the attempt that used it fails and is retried.
  pool.on("error", () => undefined);
  try {
    let wait = 1000;
    for (let attempt = 1; ; attempt++) {
      try {
        await migrateOnce(pool, dir, appRole);
        return;
      } catch (err) {
        if (!isConnectionError(err) || Date.now() + wait > deadline) throw err;
        log("migration database not reachable; retrying", { attempt, retryInMs: wait, error: String(err) });
        await new Promise((resolve) => setTimeout(resolve, wait));
        wait = Math.min(2 * wait, 30_000);
      }
    }
  } finally {
    await pool.end();
  }
}

/** An `agent_run` row. Status is RUNNING until the run ends (a run waiting for approval is RUNNING). */
export interface StoredRun {
  runId: string;
  agentId: string;
  agentVersion: number;
  invokerUserId: string;
  status: string;
  code?: string;
  reason?: string;
  tokensIn: number;
  tokensOut: number;
  estimated: boolean;
  postCommitError?: string;
  /** Where the model call was served; absent when the run made none (#605). */
  modelEndpoint?: ModelEndpoint;
  createdAt: string;
}

export interface BudgetLimits {
  limitIn: number;
  limitOut: number;
  softPct: number;
}

/** FR-47.11 default monthly budget per tenant: 50M input and 10M output tokens, soft alert at 80% (ai.budget.soft_pct). */
export const DEFAULT_BUDGET: BudgetLimits = { limitIn: 50_000_000, limitOut: 10_000_000, softPct: 80 };

const SEVERITY: Record<BudgetState, number> = { OK: 0, SOFT: 1, HARD: 2 };

interface ActionRow {
  run_id: string;
  seq: number;
  tool_name: string;
  input_json: string;
  output_hash: string;
  output_json: string;
  output_truncated: boolean;
  approver_user_id: string | null;
  decision: Decision;
  decided_at: Date;
  committed_entity_ref: string | null;
  reason: string | null;
}

interface BudgetRow {
  limit_in: string;
  limit_out: string;
  used_in: string;
  used_out: string;
  soft_pct: number;
  alert_state: BudgetState;
}

const asTokenBudget = (r: BudgetRow): TokenBudget => ({
  limitIn: Number(r.limit_in),
  limitOut: Number(r.limit_out),
  usedIn: Number(r.used_in),
  usedOut: Number(r.used_out),
  softPct: r.soft_pct,
});

const assertTokens = (what: string, n: number, min: number): void => {
  if (!Number.isSafeInteger(n) || n < min) throw new Error(`${what} must be a safe integer of at least ${min}, got ${n}`);
};

async function insertAction(c: pg.PoolClient, tenantId: string, runMonth: string, a: ActionRecord): Promise<void> {
  await c.query(
    `insert into agent_action (tenant_id, run_id, run_month, seq, tool_name, input_json, output_hash, output_json, output_truncated, approver_user_id, decision, decided_at, committed_entity_ref, reason)
     values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
     on conflict (tenant_id, run_id, seq, run_month) do nothing`,
    [tenantId, a.runId, runMonth, a.seq, a.toolName, a.inputJson, a.outputHash, a.outputJson, a.outputTruncated, a.approverUserId, a.decision, a.decidedAt, a.committedEntityRef, a.reason],
  );
}

/** Postgres store behind the audit and budget ports. Every statement runs in a tenant-scoped transaction under row-level security. */
export class PgAgentStore implements AuditPort, BudgetPort {
  /** `defaults` null means a tenant month with no configured row cannot run. */
  constructor(
    private readonly pool: pg.Pool,
    private readonly defaults: BudgetLimits | null = DEFAULT_BUDGET,
  ) {}

  async withTenant<T>(tenantId: string, fn: (client: pg.PoolClient) => Promise<T>): Promise<T> {
    const client = await this.pool.connect();
    try {
      await client.query("begin");
      await client.query("select set_config('depot.tenant_id', $1, true)", [tenantId]);
      const out = await fn(client);
      await client.query("commit");
      return out;
    } catch (err) {
      await client.query("rollback").catch(() => undefined);
      throw err;
    } finally {
      client.release();
    }
  }

  async startRun(req: RunRequest): Promise<void> {
    await this.withTenant(req.tenantId, (c) =>
      c.query(
        `insert into agent_run (tenant_id, run_id, agent_id, agent_version, invoker_user_id, status, tokens_in, tokens_out, estimated)
         values ($1, $2, $3, $4, $5, 'RUNNING', 0, 0, false)`,
        [req.tenantId, req.runId, req.agent.agentId, req.agent.version, req.invokerUserId],
      ),
    );
  }

  async runStatus(req: RunRequest): Promise<string | undefined> {
    return this.withTenant(req.tenantId, async (c) => {
      const res = await c.query<{ status: string }>("select status from agent_run where tenant_id = $1 and run_id = $2", [req.tenantId, req.runId]);
      return res.rows[0]?.status;
    });
  }

  /** One run of the tenant as the run view shows it, or undefined when the tenant has no such run. */
  async getRun(tenantId: string, runId: string): Promise<StoredRun | undefined> {
    return this.withTenant(tenantId, async (c) => {
      const r = (
        await c.query<{
          run_id: string; agent_id: string; agent_version: number; invoker_user_id: string; status: string; code: string | null;
          reason: string | null; tokens_in: string; tokens_out: string; estimated: boolean; post_commit_error: string | null; created_at: Date;
          model_provider: string | null; model_host: string | null; model_region: string | null;
        }>("select * from agent_run where tenant_id = $1 and run_id = $2", [tenantId, runId])
      ).rows[0];
      if (r === undefined) return undefined;
      return {
        runId: r.run_id,
        agentId: r.agent_id,
        agentVersion: r.agent_version,
        invokerUserId: r.invoker_user_id,
        status: r.status,
        ...(r.code !== null ? { code: r.code } : {}),
        ...(r.reason !== null ? { reason: r.reason } : {}),
        tokensIn: Number(r.tokens_in),
        tokensOut: Number(r.tokens_out),
        estimated: r.estimated,
        ...(r.post_commit_error !== null ? { postCommitError: r.post_commit_error } : {}),
        ...(r.model_provider !== null && r.model_host !== null && r.model_region !== null
          ? { modelEndpoint: { provider: r.model_provider, host: r.model_host, region: r.model_region } }
          : {}),
        createdAt: r.created_at.toISOString(),
      };
    });
  }

  async finishRun(req: RunRequest, result: RunResult, afterCommitError?: string): Promise<void> {
    await this.withTenant(req.tenantId, async (c) => {
      const res = await c.query<{ run_month: string }>(
        `update agent_run
            set status = $3, code = $4, reason = $5, tokens_in = $6, tokens_out = $7, estimated = $8, post_commit_error = $9,
                model_provider = $10, model_host = $11, model_region = $12
          where tenant_id = $1 and run_id = $2 and status = 'RUNNING'
          returning run_month::text`,
        [
          req.tenantId,
          result.runId,
          afterCommitError === undefined ? result.status : "FAILED_AFTER_COMMIT",
          result.code ?? null,
          result.reason ?? null,
          result.tokensIn,
          result.tokensOut,
          result.estimated,
          afterCommitError ?? null,
          result.modelEndpoint?.provider ?? null,
          result.modelEndpoint?.host ?? null,
          result.modelEndpoint?.region ?? null,
        ],
      );
      const runMonth = res.rows[0]?.run_month;
      if (runMonth === undefined) {
        // Already finished (a retried finish after the first one landed) is success; no row at all is an error.
        const done = await c.query("select 1 from agent_run where tenant_id = $1 and run_id = $2 and status <> 'RUNNING'", [req.tenantId, result.runId]);
        if (done.rowCount) return;
        throw new Error(`run ${result.runId} is not open for audit`);
      }
      for (const a of result.actions) {
        if (a.runId !== result.runId) throw new Error(`action ${a.seq} belongs to run ${a.runId}, not ${result.runId}`);
        // Every action lands in its run's monthly partition (migration 0006). A row a resumable run already recorded is kept.
        await insertAction(c, req.tenantId, runMonth, a);
      }
    });
  }

  /**
   * Records one action as its step completes (resumable runs). Idempotent per (run, seq): a step retried after a
   * restart finds the row and leaves it, so the audit trail never holds a duplicate or loses the first write.
   */
  async recordAction(tenantId: string, a: ActionRecord): Promise<void> {
    await this.withTenant(tenantId, async (c) => {
      const res = await c.query<{ run_month: string }>("select run_month::text from agent_run where tenant_id = $1 and run_id = $2", [tenantId, a.runId]);
      const runMonth = res.rows[0]?.run_month;
      if (runMonth === undefined) throw new Error(`run ${a.runId} is not open for audit`);
      await insertAction(c, tenantId, runMonth, a);
    });
  }

  async loadActions(tenantId: string, runId: string): Promise<ActionRecord[]> {
    return this.withTenant(tenantId, async (c) => {
      const res = await c.query<ActionRow>("select * from agent_action where run_id = $1 order by seq", [runId]);
      return res.rows.map((r) => ({
        runId: r.run_id,
        seq: r.seq,
        toolName: r.tool_name,
        inputJson: r.input_json,
        outputHash: r.output_hash,
        outputJson: r.output_json,
        outputTruncated: r.output_truncated,
        approverUserId: r.approver_user_id,
        decision: r.decision,
        decidedAt: r.decided_at.toISOString(),
        committedEntityRef: r.committed_entity_ref,
        reason: r.reason,
      }));
    });
  }

  /**
   * Creates this month's partition of `agent_action` and the next `monthsAhead` (migration 0006). Idempotent and
   * safe from every pod at once; returns how many partitions were created.
   */
  async ensureActionPartitions(monthsAhead = 2): Promise<number> {
    const res = await this.pool.query<{ made: number }>("select agent_action_ensure_partitions($1) as made", [monthsAhead]);
    return res.rows[0]?.made ?? 0;
  }

  /** Locks the tenant's budget row for the month, creating it from the default when none exists. */
  private async lockBudget(c: pg.PoolClient, tenantId: string, month: string): Promise<BudgetRow> {
    if (this.defaults !== null) {
      await c.query(
        "insert into token_budget (tenant_id, month, limit_in, limit_out, soft_pct) values ($1, $2, $3, $4, $5) on conflict do nothing",
        [tenantId, month, this.defaults.limitIn, this.defaults.limitOut, this.defaults.softPct],
      );
    }
    const res = await c.query<BudgetRow>("select * from token_budget where tenant_id = $1 and month = $2 for update", [tenantId, month]);
    const row = res.rows[0];
    if (row === undefined) throw new Error(`no token budget is configured for ${month} and defaults are disabled`);
    return row;
  }

  /** A read: no lock and no insert. A month with no row reports the defaults unused; reserve is the authority on live holds. */
  async budgetSnapshot(tenantId: string, month: string): Promise<TokenBudget> {
    return this.withTenant(tenantId, async (c) => {
      const row = (await c.query<BudgetRow>("select * from token_budget where tenant_id = $1 and month = $2", [tenantId, month])).rows[0];
      if (row !== undefined) return asTokenBudget(row);
      if (this.defaults === null) throw new Error(`no token budget is configured for ${month} and defaults are disabled`);
      return { ...this.defaults, usedIn: 0, usedOut: 0 };
    });
  }

  /** Sets a month's limits. Raising the limit lowers alert_state to the new status, so the next crossing alerts again. */
  async setBudget(tenantId: string, month: string, limits: BudgetLimits): Promise<void> {
    await this.withTenant(tenantId, async (c) => {
      await c.query(
        `insert into token_budget (tenant_id, month, limit_in, limit_out, soft_pct)
         values ($1, $2, $3, $4, $5)
         on conflict (tenant_id, month)
         do update set limit_in = $3, limit_out = $4, soft_pct = $5, updated_at = now()`,
        [tenantId, month, limits.limitIn, limits.limitOut, limits.softPct],
      );
      const row = await this.lockBudget(c, tenantId, month);
      const status = budgetStatus(asTokenBudget(row));
      if (SEVERITY[status.state] < SEVERITY[row.alert_state]) {
        await c.query("update token_budget set alert_state = $3 where tenant_id = $1 and month = $2", [tenantId, month, status.state]);
      }
    });
  }

  /**
   * The row lock serializes concurrent runs of a tenant. Expired holds (a worker that died before settling)
   * are swept first, charged in full and judged by the database clock; the hold lands only while used plus live holds plus this hold fit both limits (AGT-0202).
   */
  async reserve(r: BudgetRequest): Promise<BudgetHold> {
    assertTokens("tokensIn", r.tokensIn, 1);
    assertTokens("tokensOut", r.tokensOut, 1);
    return this.withTenant(r.tenantId, async (c) => {
      await this.lockBudget(c, r.tenantId, r.month);
      // One hold per run: a step retried after the hold was taken gets the same hold, not a second one that would be charged in full later.
      const existing = (await c.query<{ hold_id: string; month: string }>("select hold_id, month from budget_hold where tenant_id = $1 and run_id = $2", [r.tenantId, r.runId])).rows[0];
      if (existing !== undefined) return { holdId: existing.hold_id, tenantId: r.tenantId, month: existing.month };
      await c.query(
        `with swept as (delete from budget_hold where tenant_id = $1 and expires_at <= now() returning month, tokens_in, tokens_out),
              spent as (select month, sum(tokens_in) as tokens_in, sum(tokens_out) as tokens_out from swept group by month)
         update token_budget b set used_in = b.used_in + spent.tokens_in, used_out = b.used_out + spent.tokens_out, updated_at = now()
           from spent where b.tenant_id = $1 and b.month = spent.month`,
        [r.tenantId],
      );
      const row = (await c.query<BudgetRow>("select * from token_budget where tenant_id = $1 and month = $2", [r.tenantId, r.month])).rows[0]!;
      const held = (
        await c.query<{ held_in: string; held_out: string }>(
          "select coalesce(sum(tokens_in), 0) as held_in, coalesce(sum(tokens_out), 0) as held_out from budget_hold where tenant_id = $1 and month = $2",
          [r.tenantId, r.month],
        )
      ).rows[0]!;
      const b = asTokenBudget(row);
      if (b.usedIn + Number(held.held_in) + r.tokensIn > b.limitIn || b.usedOut + Number(held.held_out) + r.tokensOut > b.limitOut) {
        throw new AgentError("AGT-0202", `token budget for ${r.month} cannot cover a run of ${r.tokensIn} in / ${r.tokensOut} out`);
      }
      const holdId = randomUUID();
      await c.query(
        "insert into budget_hold (tenant_id, hold_id, month, agent_id, run_id, tokens_in, tokens_out, expires_at) values ($1, $2, $3, $4, $5, $6, $7, now() + $8 * interval '1 millisecond')",
        [r.tenantId, holdId, r.month, r.agentId, r.runId, r.tokensIn, r.tokensOut, r.ttlMs],
      );
      return { holdId, tenantId: r.tenantId, month: r.month };
    });
  }

  /** Drops the hold and charges the actual usage (a hold already swept was charged in full, so nothing more), and writes `budget.threshold` to the outbox in the same transaction when the budget moves up into SOFT or HARD. */
  async settle(hold: BudgetHold, actual: ModelUsage): Promise<BudgetState> {
    assertTokens("tokensIn", actual.tokensIn, 0);
    assertTokens("tokensOut", actual.tokensOut, 0);
    return this.withTenant(hold.tenantId, async (c) => {
      const locked = await c.query("select 1 from token_budget where tenant_id = $1 and month = $2 for update", [hold.tenantId, hold.month]);
      if (locked.rowCount === 0) throw new Error(`no token budget to settle for ${hold.month}`);
      const dropped = await c.query("delete from budget_hold where tenant_id = $1 and hold_id = $2", [hold.tenantId, hold.holdId]);
      const charge = dropped.rowCount === 0 ? { tokensIn: 0, tokensOut: 0 } : actual;
      const res = await c.query<BudgetRow>(
        `update token_budget
            set used_in = used_in + $3, used_out = used_out + $4, updated_at = now()
          where tenant_id = $1 and month = $2
      returning limit_in, limit_out, used_in, used_out, soft_pct, alert_state`,
        [hold.tenantId, hold.month, charge.tokensIn, charge.tokensOut],
      );
      const row = res.rows[0]!;
      const current = budgetStatus(asTokenBudget(row));
      const event = budgetThresholdEvent({ tenantId: hold.tenantId, month: hold.month, previous: row.alert_state, current });
      if (event === undefined) return current.state;
      await c.query("update token_budget set alert_state = $3 where tenant_id = $1 and month = $2", [hold.tenantId, hold.month, event.data.state]);
      await enqueueOutbox(c, event, "token_budget", hold.month);
      return current.state;
    });
  }
}

/**
 * Production wiring, the same shape as integration-hub-ts main: DATABASE_URL is required and
 * MIGRATE_ON_START=true runs the migrations (as the owner role) before the store is used.
 * AGENTS_BUDGET_DEFAULTS=off requires every tenant month's row to be configured.
 */
export async function openAgentStore(env: Record<string, string | undefined>): Promise<{ pool: pg.Pool; store: PgAgentStore }> {
  const url = env["DATABASE_URL"];
  if (url === undefined || url === "") throw new Error("DATABASE_URL is required: agent runs are not audited or budgeted without the store");
  const limit = (key: string, fallback: number): number => {
    const raw = env[key];
    if (raw === undefined || raw === "") return fallback;
    const n = Number(raw);
    assertTokens(key, n, 1);
    return n;
  };
  const max = limit("AGENTS_DB_POOL", 10);
  const defaults =
    env["AGENTS_BUDGET_DEFAULTS"] === "off"
      ? null
      : {
          limitIn: limit("AGENTS_BUDGET_DEFAULT_IN", DEFAULT_BUDGET.limitIn),
          limitOut: limit("AGENTS_BUDGET_DEFAULT_OUT", DEFAULT_BUDGET.limitOut),
          softPct: DEFAULT_BUDGET.softPct,
        };
  const pool = new pg.Pool({ connectionString: url, max });
  if (env["MIGRATE_ON_START"] === "true") await migrate(pool);
  return { pool, store: new PgAgentStore(pool, defaults) };
}
