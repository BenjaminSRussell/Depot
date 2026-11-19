import { AGENT_IDS } from "../agents/catalog.js";
import { budgetStatus, costUsd, type BudgetState, type TokenBudget } from "../domain/budget.js";
import { ApiError, asObject, requireDuty, type Principal } from "./principal.js";

export const BUDGET_READ_DUTY = "agentbudget.read";
export const BUDGET_WRITE_DUTY = "agentbudget.write";

export interface TokenCap {
  limitIn: number;
  limitOut: number;
}

export interface TokenPair {
  tokensIn: number;
  tokensOut: number;
}

export interface BudgetEntry {
  scope: "TENANT" | "AGENT";
  agentId: string | null;
  cap: TokenPair;
  tokensUsed: TokenPair;
  costUsed: string;
  percentConsumed: number;
  status: BudgetState;
}

export interface BudgetList {
  month: string;
  tenant: BudgetEntry;
  agents: BudgetEntry[];
}

export interface AgentBudgetStore {
  snapshot(tenantId: string, month: string): Promise<TokenBudget>;
  agentCaps(tenantId: string): Promise<ReadonlyMap<string, TokenCap>>;
  agentUsage(tenantId: string, month: string): Promise<ReadonlyMap<string, TokenPair>>;
  setAgentCap(tenantId: string, agentId: string, cap: TokenCap, changedBy: string): Promise<void>;
}

export interface AgentBudgetService {
  list(principal: Principal): Promise<BudgetList>;
  setCap(principal: Principal, agentId: string, body: unknown): Promise<BudgetEntry>;
}

const NO_USE: TokenPair = { tokensIn: 0, tokensOut: 0 };

function parseLimit(body: Record<string, unknown>, field: "limitIn" | "limitOut"): number {
  const value = body[field];
  if (value === undefined) throw new ApiError(422, "AGT-0422", `${field} is required`);
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) {
    throw new ApiError(422, "AGT-0422", `${field} must be a non-negative integer`);
  }
  return value;
}

export function parseCapBody(raw: unknown): TokenCap {
  const body = asObject(raw, ["limitIn", "limitOut"]);
  return { limitIn: parseLimit(body, "limitIn"), limitOut: parseLimit(body, "limitOut") };
}

function entry(scope: BudgetEntry["scope"], agentId: string | null, cap: TokenCap, used: TokenPair, softPct: number): BudgetEntry {
  const { state, pct } = budgetStatus({ limitIn: cap.limitIn, limitOut: cap.limitOut, usedIn: used.tokensIn, usedOut: used.tokensOut, softPct });
  return {
    scope,
    agentId,
    cap: { tokensIn: cap.limitIn, tokensOut: cap.limitOut },
    tokensUsed: used,
    costUsed: costUsd(used.tokensIn, used.tokensOut),
    percentConsumed: Math.floor(pct * 100) / 100,
    status: state,
  };
}

export function createAgentBudgetService(deps: { store: AgentBudgetStore; now?: () => Date }): AgentBudgetService {
  const now = deps.now ?? (() => new Date());

  async function build(tenantId: string): Promise<BudgetList> {
    const month = now().toISOString().slice(0, 7);
    const [tenant, caps, usage] = await Promise.all([
      deps.store.snapshot(tenantId, month),
      deps.store.agentCaps(tenantId),
      deps.store.agentUsage(tenantId, month),
    ]);
    const tenantCap: TokenCap = { limitIn: tenant.limitIn, limitOut: tenant.limitOut };
    return {
      month,
      tenant: entry("TENANT", null, tenantCap, { tokensIn: tenant.usedIn, tokensOut: tenant.usedOut }, tenant.softPct),
      agents: AGENT_IDS.map((id) => entry("AGENT", id, caps.get(id) ?? tenantCap, usage.get(id) ?? NO_USE, tenant.softPct)),
    };
  }

  return {
    async list(principal) {
      requireDuty(principal, BUDGET_READ_DUTY, "the agent budgets");
      return build(principal.tenantId);
    },
    async setCap(principal, agentId, body) {
      requireDuty(principal, BUDGET_WRITE_DUTY, "the agent budget cap");
      if (!(AGENT_IDS as readonly string[]).includes(agentId)) throw new ApiError(404, "AGT-0404", "no such agent");
      await deps.store.setAgentCap(principal.tenantId, agentId, parseCapBody(body), principal.userId);
      const list = await build(principal.tenantId);
      return list.agents.find((a) => a.agentId === agentId)!;
    },
  };
}
