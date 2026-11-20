import { approvalExpiresAtMs, decideApproval } from "../domain/approval.js";
import type { Money } from "../domain/money.js";
import { ApiError, asObject, type Principal, requireDuty, toApiError } from "./principal.js";

export type Decision = "APPROVED" | "REJECTED";

/** A decision as `agent_action` records it (approver_user_id, decision, decided_at). */
export interface RecordedDecision {
  decision: Decision;
  /** "EXPIRED" when BR-47.3 timed the request out (code AGT-0208). */
  reason?: "EXPIRED";
  code?: "AGT-0208";
  approverUserId: string;
  decidedAt: string;
}

/** An approval request as the store holds it (FR-47.8). */
export interface ApprovalRequest {
  invokerUserId: string;
  requestedAtMs: number;
  expiryHours?: number;
  approvalDuty: string;
  amount: Money;
  selfApprovalLimit: Money;
  /** sha256 of the proposal the request was opened for (workflow runs). */
  proposalHash?: string;
  /** Set once the action is decided; a decided action is never decided again. */
  decided?: RecordedDecision;
}

/**
 * The approval requests of the gated actions. Every call is scoped to the
 * caller's tenant, so one tenant can never read or decide another tenant's
 * action, whatever ids it sends (production: RLS on `agent_action`).
 */
export interface ApprovalStore {
  find(tenantId: string, runId: string, actionId: string): Promise<ApprovalRequest | undefined>;
  /**
   * Records the decision only if the action is still undecided, and returns
   * the decision that is stored afterwards: ours, or the one another approver
   * recorded first (production: `UPDATE ... WHERE decision IS NULL`).
   */
  record(tenantId: string, runId: string, actionId: string, decision: RecordedDecision): Promise<RecordedDecision>;
}

export interface DecisionResult extends RecordedDecision {
  runId: string;
  actionId: string;
}

export const MAX_REASON_CHARS = 500;

/** An optional free-text reason: absent, null or empty is null; anything but a string of at most 500 characters is 400. */
export function parseReason(value: unknown): string | null {
  if (value === undefined || value === null || value === "") return null;
  if (typeof value !== "string" || value.length > MAX_REASON_CHARS) {
    throw new ApiError(400, "AGT-0400", `reason must be a string of at most ${MAX_REASON_CHARS} characters`);
  }
  return value;
}

/** Validates the POST body of `.../actions/{id}/decision`. The reason is read by the decline of an ungated action only. */
export function parseDecision(raw: unknown): { decision: Decision; reason: string | null } {
  const body = asObject(raw, ["decision", "reason"]);
  const reason = parseReason(body.reason);
  if (body.decision === "APPROVED" || body.decision === "REJECTED") return { decision: body.decision, reason };
  throw new ApiError(400, "AGT-0400", "decision must be APPROVED or REJECTED");
}

/**
 * Answers a repeated decision: the same decision again returns the recorded
 * one (idempotent by action id, 9.47.5), and so does any decision on an
 * expired request (BR-47.3). A different decision on a decided action is 409.
 */
function replay(stored: RecordedDecision, requested: Decision): RecordedDecision {
  if (stored.decision === requested || stored.reason === "EXPIRED") return stored;
  throw new ApiError(409, "AGT-0409", `this action was already ${stored.decision.toLowerCase()}`);
}

/**
 * Records an approver's decision on a gated action (FR-47.8). The approver is
 * the caller and must hold the action's approval duty (AGT-0203) before
 * anything about the request is disclosed. An approver who started the run
 * may not approve above the self-approval limit (AGT-0209). An expired
 * request is recorded and answered as REJECTED with reason EXPIRED (BR-47.3):
 * the source document stays unposted.
 */
export async function handleApprovalDecision(input: {
  principal: Principal;
  runId: string;
  actionId: string;
  body: unknown;
  store: ApprovalStore;
  now: () => Date;
}): Promise<DecisionResult> {
  const { decision: requested } = parseDecision(input.body);
  const { tenantId, userId } = input.principal;
  const request = await input.store.find(tenantId, input.runId, input.actionId);
  if (request === undefined) {
    throw new ApiError(404, "AGT-0404", "no approval request for this action");
  }
  requireDuty(input.principal, request.approvalDuty, "this approval");
  let stored: RecordedDecision;
  if (request.decided !== undefined) {
    stored = replay(request.decided, requested);
  } else {
    const now = input.now();
    let outcome;
    try {
      outcome = decideApproval({
        requestedAtMs: request.requestedAtMs,
        nowMs: now.getTime(),
        expiryHours: request.expiryHours,
        invokerUserId: request.invokerUserId,
        approverUserId: userId,
        approverDuties: input.principal.duties,
        approvalDuty: request.approvalDuty,
        decision: requested,
        amount: request.amount,
        selfApprovalLimit: request.selfApprovalLimit,
      });
    } catch (err) {
      throw toApiError(err);
    }
    const ours: RecordedDecision = {
      decision: outcome.decision,
      ...(outcome.reason !== undefined ? { reason: outcome.reason } : {}),
      ...(outcome.code !== undefined ? { code: outcome.code } : {}),
      approverUserId: userId,
      decidedAt: now.toISOString(),
    };
    // A concurrent approver may have recorded first; theirs stands.
    stored = replay(await input.store.record(tenantId, input.runId, input.actionId, ours), requested);
  }
  return { runId: input.runId, actionId: input.actionId, ...stored };
}

export type ApprovalState = "PENDING" | "APPROVED" | "REJECTED" | "EXPIRED";

export interface ApprovalDetail extends ApprovalRequest {
  runId: string;
  actionId: string;
  tool: string;
}

export interface PendingApprovalDetail extends ApprovalDetail {
  agentId: string;
}

export interface ApprovalReader {
  findByAction(tenantId: string, actionId: string): Promise<ApprovalDetail | undefined>;
  listPending(tenantId: string, duties: readonly string[], excludedAgents: readonly string[], notExpiredAtMs: number, limit: number): Promise<PendingApprovalDetail[]>;
}

export const DEFAULT_APPROVAL_LIST_LIMIT = 50;
export const MAX_APPROVAL_LIST_LIMIT = 200;

export function parseApprovalListLimit(params: URLSearchParams): number {
  const raw = params.get("limit");
  if (raw === null) return DEFAULT_APPROVAL_LIST_LIMIT;
  const limit = /^[0-9]{1,3}$/.test(raw) ? Number(raw) : 0;
  if (limit < 1 || limit > MAX_APPROVAL_LIST_LIMIT) throw new ApiError(400, "AGT-0400", `limit must be an integer from 1 to ${MAX_APPROVAL_LIST_LIMIT}`);
  return limit;
}

export interface ApprovalView {
  runId: string;
  actionId: string;
  tool: string;
  state: ApprovalState;
  amount: Money;
  requestedAt: string;
  expiresAt: string;
  decided?: { decision: Decision; reason?: "EXPIRED"; decidedAt: string };
}

export function approvalStateOf(request: ApprovalRequest, nowMs: number): ApprovalState {
  if (request.decided !== undefined) return request.decided.reason === "EXPIRED" ? "EXPIRED" : request.decided.decision;
  return nowMs >= approvalExpiresAtMs(request.requestedAtMs, request.expiryHours) ? "EXPIRED" : "PENDING";
}

export interface PendingApprovalView extends ApprovalView {
  agentId: string;
}

export async function handleListApprovals(input: {
  principal: Principal;
  params: URLSearchParams;
  reader: ApprovalReader;
  disabledAgents: (tenantId: string) => Promise<ReadonlySet<string>>;
  now: () => Date;
}): Promise<{ items: PendingApprovalView[] }> {
  const limit = parseApprovalListLimit(input.params);
  const { tenantId, duties } = input.principal;
  const nowMs = input.now().getTime();
  const disabled = await input.disabledAgents(tenantId);
  const rows = await input.reader.listPending(tenantId, [...duties], [...disabled], nowMs, limit);
  const items = rows
    .filter((r) => r.decided === undefined && duties.has(r.approvalDuty) && !disabled.has(r.agentId))
    .map((r) => ({
      agentId: r.agentId,
      runId: r.runId,
      actionId: r.actionId,
      tool: r.tool,
      state: approvalStateOf(r, nowMs),
      amount: r.amount,
      requestedAt: new Date(r.requestedAtMs).toISOString(),
      expiresAt: new Date(approvalExpiresAtMs(r.requestedAtMs, r.expiryHours)).toISOString(),
    }))
    .filter((r) => r.state === "PENDING");
  return { items };
}

export async function handleGetApproval(input: {
  principal: Principal;
  actionId: string;
  reader: ApprovalReader;
  now: () => Date;
}): Promise<ApprovalView> {
  const detail = await input.reader.findByAction(input.principal.tenantId, input.actionId);
  if (detail === undefined || !input.principal.duties.has(detail.approvalDuty)) {
    throw new ApiError(404, "AGT-0404", "no approval request for this action");
  }
  const { decided } = detail;
  return {
    runId: detail.runId,
    actionId: detail.actionId,
    tool: detail.tool,
    state: approvalStateOf(detail, input.now().getTime()),
    amount: detail.amount,
    requestedAt: new Date(detail.requestedAtMs).toISOString(),
    expiresAt: new Date(approvalExpiresAtMs(detail.requestedAtMs, detail.expiryHours)).toISOString(),
    ...(decided !== undefined
      ? { decided: { decision: decided.decision, ...(decided.reason !== undefined ? { reason: decided.reason } : {}), decidedAt: decided.decidedAt } }
      : {}),
  };
}
