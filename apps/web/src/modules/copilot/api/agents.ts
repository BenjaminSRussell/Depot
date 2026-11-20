import { z } from "zod";
import { readProblem, type FetchLike, defaultFetch } from "../../../shell/api/http";

/**
 * Client for the agent runtime's kill switch (services/agents-ts/api/openapi.yaml,
 * GET and POST /api/v1/agents/kill-switch). The edge sets the caller's tenant,
 * user and duties; the browser never sends them.
 */
export const KILL_SWITCH_PATH = "/api/v1/agents/kill-switch";

const stateSchema = z.object({
  active: z.boolean(),
  version: z.number().int().positive().nullable(),
  changedAt: z.string().nullable(),
});

export const killSwitchStatusSchema = z.object({
  tenantId: z.string(),
  halted: z.boolean(),
  stale: z.boolean(),
  tenant: stateSchema.extend({ byUser: z.string().nullable() }),
  global: stateSchema,
});

export type KillSwitchStatus = z.infer<typeof killSwitchStatusSchema>;

export const killSwitchResultSchema = z.object({
  scope: z.enum(["GLOBAL", "TENANT"]),
  tenantId: z.string().nullable(),
  active: z.boolean(),
  version: z.number().int().positive(),
  changedAt: z.string(),
  byUser: z.string(),
});

export type KillSwitchResult = z.infer<typeof killSwitchResultSchema>;

export type KillSwitchCommand = { scope: "TENANT"; tenantId: string; active: boolean } | { scope: "GLOBAL"; active: boolean };

async function parse<T>(res: Response, schema: z.ZodType<T>): Promise<T> {
  if (!res.ok) throw await readProblem(res);
  const parsed = schema.safeParse(await res.json());
  if (!parsed.success) throw new Error("The agent runtime sent a response we couldn't read.");
  return parsed.data;
}

/** The caller's tenant switch and the global switch. */
export async function getKillSwitch(signal?: AbortSignal, fetchFn: FetchLike = defaultFetch): Promise<KillSwitchStatus> {
  const res = await fetchFn(KILL_SWITCH_PATH, { cache: "no-store", headers: { accept: "application/json" }, ...(signal ? { signal } : {}) });
  return parse(res, killSwitchStatusSchema);
}

/** Halts or resumes agent runs. Idempotent by scope on the server. */
export async function setKillSwitch(cmd: KillSwitchCommand, fetchFn: FetchLike = defaultFetch): Promise<KillSwitchResult> {
  const res = await fetchFn(KILL_SWITCH_PATH, {
    method: "POST",
    cache: "no-store",
    headers: { "content-type": "application/json", accept: "application/json" },
    body: JSON.stringify(cmd),
  });
  return parse(res, killSwitchResultSchema);
}

const tokenPairSchema = z.object({ tokensIn: z.number().int().nonnegative(), tokensOut: z.number().int().nonnegative() });

export const budgetEntrySchema = z.object({
  scope: z.enum(["TENANT", "AGENT"]),
  agentId: z.string().nullable(),
  cap: tokenPairSchema,
  tokensUsed: tokenPairSchema,
  costUsed: z.string(),
  percentConsumed: z.number().nonnegative(),
  status: z.enum(["OK", "SOFT", "HARD"]),
});

export const budgetListSchema = z.object({ month: z.string(), tenant: budgetEntrySchema, agents: z.array(budgetEntrySchema) });

export const agentDefinitionSchema = z.object({
  agentId: z.string(),
  version: z.number().int().positive(),
  enabled: z.boolean(),
  disabledBy: z.string().nullable(),
  disabledAt: z.string().nullable(),
  reason: z.string().nullable(),
});

const agentDefinitionListSchema = z.object({ items: z.array(agentDefinitionSchema) });

const cellsSchema = z.object({ P0: z.boolean(), P1: z.boolean(), P2: z.boolean(), P3: z.boolean(), CUI: z.boolean() });

export const aiDataPolicySchema = z.object({ version: z.number().int().positive(), modules: z.object({ ap: cellsSchema, ar: cellsSchema }) });

export type BudgetEntry = z.infer<typeof budgetEntrySchema>;
export type BudgetList = z.infer<typeof budgetListSchema>;
export type AgentDefinition = z.infer<typeof agentDefinitionSchema>;
export type AiDataPolicy = z.infer<typeof aiDataPolicySchema>;
export interface BudgetCap {
  limitIn: number;
  limitOut: number;
}

export const BUDGETS_PATH = "/api/v1/agents/budgets";
export const DEFINITIONS_PATH = "/api/v1/agents/definitions";
export const AI_DATA_POLICY_PATH = "/api/v1/agents/ai-data-policy";

function readInit(signal?: AbortSignal): RequestInit {
  return { cache: "no-store", headers: { accept: "application/json" }, ...(signal ? { signal } : {}) };
}

function writeInit(body?: unknown): RequestInit {
  return {
    method: "POST",
    cache: "no-store",
    headers: { "content-type": "application/json", accept: "application/json" },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  };
}

export async function getBudgets(signal?: AbortSignal, fetchFn: FetchLike = defaultFetch): Promise<BudgetList> {
  return parse(await fetchFn(BUDGETS_PATH, readInit(signal)), budgetListSchema);
}

export async function putBudgetCap(agentId: string, cap: BudgetCap, fetchFn: FetchLike = defaultFetch): Promise<BudgetEntry> {
  const res = await fetchFn(`${BUDGETS_PATH}/${encodeURIComponent(agentId)}`, { ...writeInit(cap), method: "PUT" });
  return parse(res, budgetEntrySchema);
}

export async function getAgentDefinitions(signal?: AbortSignal, fetchFn: FetchLike = defaultFetch): Promise<AgentDefinition[]> {
  return (await parse(await fetchFn(DEFINITIONS_PATH, readInit(signal)), agentDefinitionListSchema)).items;
}

export async function setAgentEnabled(agentId: string, enabled: boolean, fetchFn: FetchLike = defaultFetch): Promise<AgentDefinition> {
  const res = await fetchFn(`${DEFINITIONS_PATH}/${encodeURIComponent(agentId)}:${enabled ? "enable" : "disable"}`, writeInit(enabled ? undefined : {}));
  return parse(res, agentDefinitionSchema);
}

export async function getAiDataPolicy(signal?: AbortSignal, fetchFn: FetchLike = defaultFetch): Promise<AiDataPolicy> {
  return parse(await fetchFn(AI_DATA_POLICY_PATH, readInit(signal)), aiDataPolicySchema);
}
