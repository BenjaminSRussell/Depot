import type { PseudonymSnapshot } from "../domain/redaction.js";
import type { PgAgentStore } from "./pg.js";

/** What a step acts with: a verified caller's token and identity (never stored in Temporal history). */
export interface RunCredential {
  kind: "starter" | "approver";
  userId: string;
  duties: string[];
  token: string;
}

/** Durable state of one workflow-mode run; `input` is fixed at start, `state` and `pseudonyms` advance with the steps. */
export interface RunStateRow<I, S> {
  input: I;
  state: S;
  pseudonyms: PseudonymSnapshot;
}

export interface RunStateStore {
  create<I, S>(tenantId: string, runId: string, input: I, state: S): Promise<void>;
  load<I, S>(tenantId: string, runId: string): Promise<RunStateRow<I, S> | undefined>;
  save<S>(tenantId: string, runId: string, state: S, pseudonyms: PseudonymSnapshot): Promise<void>;
  putCredential(tenantId: string, runId: string, cred: RunCredential, expiresAtMs: number): Promise<void>;
  /** The credential if it is still unexpired; undefined otherwise. */
  credential(tenantId: string, runId: string, kind: RunCredential["kind"]): Promise<RunCredential | undefined>;
  purgeCredentials(tenantId: string, runId: string): Promise<void>;
}

/** The `exp` claim (ms) of a JWT already verified by the API layer; absent when unreadable. */
export function tokenExpiryMs(token: string): number | undefined {
  try {
    const claims = JSON.parse(Buffer.from(token.split(".")[1] ?? "", "base64url").toString("utf8")) as { exp?: unknown };
    return typeof claims.exp === "number" ? claims.exp * 1000 : undefined;
  } catch {
    return undefined;
  }
}

export class PgRunStateStore implements RunStateStore {
  constructor(private readonly store: PgAgentStore) {}

  async create<I, S>(tenantId: string, runId: string, input: I, state: S): Promise<void> {
    await this.store.withTenant(tenantId, (c) =>
      c.query("insert into agent_run_state (tenant_id, run_id, input, state) values ($1, $2, $3::jsonb, $4::jsonb)", [tenantId, runId, JSON.stringify(input), JSON.stringify(state)]),
    );
  }

  async load<I, S>(tenantId: string, runId: string): Promise<RunStateRow<I, S> | undefined> {
    return this.store.withTenant(tenantId, async (c) => {
      const r = (await c.query<{ input: I; state: S; pseudonyms: PseudonymSnapshot }>("select input, state, pseudonyms from agent_run_state where tenant_id = $1 and run_id = $2", [tenantId, runId])).rows[0];
      return r === undefined ? undefined : { input: r.input, state: r.state, pseudonyms: r.pseudonyms };
    });
  }

  async save<S>(tenantId: string, runId: string, state: S, pseudonyms: PseudonymSnapshot): Promise<void> {
    await this.store.withTenant(tenantId, (c) =>
      c.query("update agent_run_state set state = $3::jsonb, pseudonyms = $4::jsonb, updated_at = now() where tenant_id = $1 and run_id = $2", [tenantId, runId, JSON.stringify(state), JSON.stringify(pseudonyms)]),
    );
  }

  async putCredential(tenantId: string, runId: string, cred: RunCredential, expiresAtMs: number): Promise<void> {
    await this.store.withTenant(tenantId, (c) =>
      c.query(
        `insert into agent_run_credential (tenant_id, run_id, kind, user_id, duties, token, expires_at) values ($1, $2, $3, $4, $5, $6, $7)
         on conflict (tenant_id, run_id, kind) do update set user_id = $4, duties = $5, token = $6, expires_at = $7`,
        [tenantId, runId, cred.kind, cred.userId, cred.duties.join(","), cred.token, new Date(expiresAtMs)],
      ),
    );
  }

  async credential(tenantId: string, runId: string, kind: RunCredential["kind"]): Promise<RunCredential | undefined> {
    return this.store.withTenant(tenantId, async (c) => {
      const r = (
        await c.query<{ user_id: string; duties: string; token: string }>(
          "select user_id, duties, token from agent_run_credential where tenant_id = $1 and run_id = $2 and kind = $3 and expires_at > now()",
          [tenantId, runId, kind],
        )
      ).rows[0];
      return r === undefined ? undefined : { kind, userId: r.user_id, duties: r.duties === "" ? [] : r.duties.split(","), token: r.token };
    });
  }

  async purgeCredentials(tenantId: string, runId: string): Promise<void> {
    await this.store.withTenant(tenantId, (c) => c.query("delete from agent_run_credential where tenant_id = $1 and run_id = $2", [tenantId, runId]));
  }
}
