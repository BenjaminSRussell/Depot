import { randomUUID } from "node:crypto";
import type { ApprovalDetail, ApprovalReader, ApprovalRequest, ApprovalStore, RecordedDecision } from "../api/decision.js";
import type { Money } from "../domain/money.js";
import { approvalRequestedEvent } from "../domain/events.js";
import { enqueueOutbox } from "./outbox.js";
import type { PgAgentStore } from "./pg.js";

/** What the runtime stores when a run reaches the approval gate (FR-47.8). */
export interface NewApproval {
  tenantId: string;
  runId: string;
  seq: number;
  tool: string;
  invokerUserId: string;
  approvalDuty: string;
  amount: Money;
  selfApprovalLimit: Money;
  expiryHours?: number;
  /** The PII-free proposal shown in the inbox: tool, arguments with pseudonyms, confidence. */
  proposalJson: string;
  /** sha256 of the model's proposal; the commit refuses a proposal that does not match it. */
  proposalHash?: string;
  /** With these the request is announced to the shared inbox (`agent.approval.requested`) in the same transaction. */
  confidence?: number;
  riskScore?: number;
}

export interface PendingApproval {
  actionId: string;
  runId: string;
  seq: number;
  tool: string;
  requestedAt: string;
  approvalDuty: string;
}

interface Row {
  action_id: string;
  run_id: string;
  seq: number;
  tool_name: string;
  invoker_user_id: string;
  approval_duty: string;
  amount: string;
  currency: string;
  self_approval_limit: string;
  self_approval_currency: string;
  expiry_hours: number | null;
  requested_at: Date;
  decision: "APPROVED" | "REJECTED" | null;
  reason: "EXPIRED" | null;
  code: "AGT-0208" | null;
  approver_user_id: string | null;
  decided_at: Date | null;
  proposal_hash: string | null;
}

const asDecision = (r: Row): RecordedDecision | undefined =>
  r.decision === null || r.approver_user_id === null || r.decided_at === null
    ? undefined
    : {
        decision: r.decision,
        ...(r.reason !== null ? { reason: r.reason } : {}),
        ...(r.code !== null ? { code: r.code } : {}),
        approverUserId: r.approver_user_id,
        decidedAt: r.decided_at.toISOString(),
      };

const requestOf = (row: Row): ApprovalRequest => {
  const decided = asDecision(row);
  return {
    invokerUserId: row.invoker_user_id,
    requestedAtMs: row.requested_at.getTime(),
    ...(row.expiry_hours !== null ? { expiryHours: row.expiry_hours } : {}),
    approvalDuty: row.approval_duty,
    amount: { amount: row.amount, currency: row.currency },
    selfApprovalLimit: { amount: row.self_approval_limit, currency: row.self_approval_currency },
    ...(row.proposal_hash !== null ? { proposalHash: row.proposal_hash } : {}),
    ...(decided !== undefined ? { decided } : {}),
  };
};

/**
 * `approval_request` behind the decision endpoint (FR-47.8). Every statement runs in the caller's
 * tenant under row-level security; `record` is first-writer-wins.
 */
export class PgApprovalStore implements ApprovalStore, ApprovalReader {
  constructor(private readonly store: PgAgentStore) {}

  /** Opens a request and returns its action id (the id the decision endpoint takes). */
  async open(a: NewApproval): Promise<string> {
    // A step retried after a restart finds the request it already opened for this run and seq.
    const existing = await this.store.withTenant(a.tenantId, async (c) =>
      (await c.query<{ action_id: string; proposal_hash: string | null }>("select action_id, proposal_hash from approval_request where tenant_id = $1 and run_id = $2 and seq = $3", [a.tenantId, a.runId, a.seq])).rows[0],
    );
    if (existing !== undefined) {
      if (a.proposalHash !== undefined && existing.proposal_hash !== null && existing.proposal_hash !== a.proposalHash) {
        throw new Error("an approval request for this step already exists for a different proposal");
      }
      return existing.action_id;
    }
    const actionId = randomUUID();
    await this.store.withTenant(a.tenantId, (c) =>
      c.query(
        `insert into approval_request (tenant_id, action_id, run_id, seq, tool_name, invoker_user_id, approval_duty, amount, currency,
            self_approval_limit, self_approval_currency, expiry_hours, proposal_json, proposal_hash)
         values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
        [a.tenantId, actionId, a.runId, a.seq, a.tool, a.invokerUserId, a.approvalDuty, a.amount.amount, a.amount.currency,
          a.selfApprovalLimit.amount, a.selfApprovalLimit.currency, a.expiryHours ?? null, a.proposalJson, a.proposalHash ?? null],
      ).then(async () => {
        if (a.confidence === undefined) return;
        // The announcement is written with the request, so the inbox never misses a request that was stored (transactional outbox).
        const event = approvalRequestedEvent({
          tenantId: a.tenantId,
          runId: a.runId,
          actionId,
          toolName: a.tool,
          proposalRedacted: a.proposalJson,
          confidence: a.confidence,
          ...(a.riskScore !== undefined ? { riskScore: a.riskScore } : {}),
        });
        await enqueueOutbox(c, event, "approval_request", actionId);
      }),
    );
    return actionId;
  }

  async find(tenantId: string, runId: string, actionId: string): Promise<ApprovalRequest | undefined> {
    const row = await this.store.withTenant(tenantId, async (c) =>
      (await c.query<Row>("select * from approval_request where tenant_id = $1 and run_id = $2 and action_id = $3", [tenantId, runId, actionId])).rows[0],
    );
    return row === undefined ? undefined : requestOf(row);
  }

  async findByAction(tenantId: string, actionId: string): Promise<ApprovalDetail | undefined> {
    const row = await this.store.withTenant(tenantId, async (c) =>
      (await c.query<Row>("select * from approval_request where tenant_id = $1 and action_id = $2", [tenantId, actionId])).rows[0],
    );
    return row === undefined ? undefined : { ...requestOf(row), runId: row.run_id, actionId: row.action_id, tool: row.tool_name };
  }

  async record(tenantId: string, runId: string, actionId: string, d: RecordedDecision): Promise<RecordedDecision> {
    return this.store.withTenant(tenantId, async (c) => {
      await c.query(
        `update approval_request set decision = $4, reason = $5, code = $6, approver_user_id = $7, decided_at = $8
          where tenant_id = $1 and run_id = $2 and action_id = $3 and decided_at is null`,
        [tenantId, runId, actionId, d.decision, d.reason ?? null, d.code ?? null, d.approverUserId, d.decidedAt],
      );
      const row = (await c.query<Row>("select * from approval_request where tenant_id = $1 and run_id = $2 and action_id = $3", [tenantId, runId, actionId])).rows[0];
      const stored = row === undefined ? undefined : asDecision(row);
      if (stored === undefined) throw new Error(`approval ${actionId} does not exist or was not decided`);
      return stored;
    });
  }

  /** Open requests of a run, oldest first, for the run view and the inbox. */
  async pendingForRun(tenantId: string, runId: string): Promise<PendingApproval[]> {
    return this.store.withTenant(tenantId, async (c) =>
      (await c.query<Row>("select * from approval_request where tenant_id = $1 and run_id = $2 and decided_at is null order by seq", [tenantId, runId])).rows.map((r) => ({
        actionId: r.action_id,
        runId: r.run_id,
        seq: r.seq,
        tool: r.tool_name,
        requestedAt: r.requested_at.toISOString(),
        approvalDuty: r.approval_duty,
      })),
    );
  }
}
