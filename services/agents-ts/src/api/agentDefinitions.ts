import type { AgentVersion } from "../domain/registry.js";
import { ApiError, asObject, requireDuty, type Principal } from "./principal.js";

export const AGENT_ADMIN_DUTY = "agents.admin";
const REASON_MAX = 500;

export type DisabledAgents = (tenantId: string) => Promise<ReadonlySet<string>>;

export interface DisabledRow {
  disabledBy: string;
  disabledAt: string;
  reason: string | null;
}

export interface AgentDefinitionStore {
  disabledAgents(tenantId: string): Promise<ReadonlyMap<string, DisabledRow>>;
  setDisabled(tenantId: string, agentId: string, disabled: boolean, changedBy: string, reason: string | null): Promise<void>;
}

export interface AgentDefinition {
  agentId: string;
  version: number;
  enabled: boolean;
  disabledBy: string | null;
  disabledAt: string | null;
  reason: string | null;
}

export interface AgentDefinitionService {
  list(principal: Principal): Promise<{ items: AgentDefinition[] }>;
  disable(principal: Principal, agentId: string, body: unknown): Promise<AgentDefinition>;
  enable(principal: Principal, agentId: string, body: unknown): Promise<AgentDefinition>;
}

export function parseDisableBody(raw: unknown): string | null {
  const reason = asObject(raw, ["reason"])["reason"];
  if (reason === undefined) return null;
  if (typeof reason !== "string" || reason.length > REASON_MAX) {
    throw new ApiError(422, "AGT-0422", `reason must be a string of at most ${REASON_MAX} characters`);
  }
  return reason === "" ? null : reason;
}

export function createAgentDefinitionService(deps: { agents: readonly Pick<AgentVersion, "agentId" | "version">[]; store: AgentDefinitionStore }): AgentDefinitionService {
  async function definitions(tenantId: string): Promise<AgentDefinition[]> {
    const disabled = await deps.store.disabledAgents(tenantId);
    return deps.agents.map((a) => {
      const row = disabled.get(a.agentId);
      return { agentId: a.agentId, version: a.version, enabled: row === undefined, disabledBy: row?.disabledBy ?? null, disabledAt: row?.disabledAt ?? null, reason: row?.reason ?? null };
    });
  }

  async function change(principal: Principal, agentId: string, disabled: boolean, reason: () => string | null): Promise<AgentDefinition> {
    requireDuty(principal, AGENT_ADMIN_DUTY, "the agent administration");
    if (!deps.agents.some((a) => a.agentId === agentId)) throw new ApiError(404, "AGT-0404", "no such agent");
    await deps.store.setDisabled(principal.tenantId, agentId, disabled, principal.userId, reason());
    return (await definitions(principal.tenantId)).find((d) => d.agentId === agentId)!;
  }

  return {
    async list(principal) {
      requireDuty(principal, AGENT_ADMIN_DUTY, "the agent administration");
      return { items: await definitions(principal.tenantId) };
    },
    disable: (principal, agentId, body) => change(principal, agentId, true, () => parseDisableBody(body)),
    enable: (principal, agentId, body) =>
      change(principal, agentId, false, () => {
        asObject(body, []);
        return null;
      }),
  };
}
