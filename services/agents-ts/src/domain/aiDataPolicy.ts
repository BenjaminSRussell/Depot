import { AgentError } from "../errors.js";
import { ApiError, asObject } from "../api/principal.js";

export const AI_POLICY_MODULES = ["ap", "ar", "platform"] as const;
export const DATA_CLASSES = ["P0", "P1", "P2", "P3", "CUI"] as const;
export const MAX_REASON_LENGTH = 500;

export type PolicyModule = (typeof AI_POLICY_MODULES)[number];
export type DataClass = (typeof DATA_CLASSES)[number];
export type ModulePolicy = Record<DataClass, boolean>;
export type PolicyModules = Record<PolicyModule, ModulePolicy>;

export interface AiDataPolicy {
  version: number;
  modules: PolicyModules;
}

export interface AiDataPolicyPatch {
  modules: Partial<Record<PolicyModule, Partial<ModulePolicy>>>;
  reason?: string;
}

export interface PolicyDecision {
  allowed: boolean;
  policyVersion: number;
  reason: string;
}

const DEFAULT_CELLS: ModulePolicy = { P0: true, P1: true, P2: false, P3: false, CUI: false };
const NEVER_ALLOWED: readonly DataClass[] = ["P3", "CUI"];

export function defaultModules(): PolicyModules {
  return Object.fromEntries(AI_POLICY_MODULES.map((m) => [m, { ...DEFAULT_CELLS }])) as PolicyModules;
}

export function defaultPolicy(): AiDataPolicy {
  return { version: 1, modules: defaultModules() };
}

export function policyFromStored(version: number, stored: unknown): AiDataPolicy {
  const modules = defaultModules();
  const byModule = typeof stored === "object" && stored !== null ? (stored as Record<string, unknown>) : {};
  for (const m of AI_POLICY_MODULES) {
    const cells = byModule[m];
    if (typeof cells !== "object" || cells === null) continue;
    for (const c of DATA_CLASSES) {
      const cell = (cells as Record<string, unknown>)[c];
      if (typeof cell === "boolean") modules[m][c] = cell;
    }
  }
  return { version, modules };
}

function parseCells(module: string, raw: unknown): Partial<ModulePolicy> {
  const cells = asObject(raw, DATA_CLASSES);
  const out: Partial<ModulePolicy> = {};
  for (const c of DATA_CLASSES) {
    const cell = cells[c];
    if (cell === undefined) continue;
    if (typeof cell !== "boolean") throw new ApiError(400, "AGT-0400", `${module}.${c} must be a boolean`);
    out[c] = cell;
  }
  return out;
}

export function parsePolicyPatch(raw: unknown): AiDataPolicyPatch {
  const body = asObject(raw, ["modules", "reason"]);
  const modules = asObject(body["modules"] ?? {}, AI_POLICY_MODULES);
  const patch: AiDataPolicyPatch = { modules: {} };
  for (const m of AI_POLICY_MODULES) {
    if (modules[m] !== undefined) patch.modules[m] = parseCells(m, modules[m]);
  }
  if (Object.values(patch.modules).every((cells) => Object.keys(cells).length === 0)) {
    throw new ApiError(400, "AGT-0400", "nothing to change");
  }
  const reason = body["reason"];
  if (reason !== undefined) {
    if (typeof reason !== "string" || reason.length > MAX_REASON_LENGTH) {
      throw new ApiError(400, "AGT-0400", `reason must be a string of at most ${MAX_REASON_LENGTH} characters`);
    }
    patch.reason = reason;
  }
  return patch;
}

export interface AppliedPatch {
  modules: PolicyModules;
  changed: boolean;
  loosening: boolean;
}

export function applyPatch(policy: AiDataPolicy, patch: AiDataPolicyPatch): AppliedPatch {
  const modules = structuredClone(policy.modules);
  let changed = false;
  let loosening = false;
  for (const m of AI_POLICY_MODULES) {
    for (const c of DATA_CLASSES) {
      const cell = patch.modules[m]?.[c];
      if (cell === undefined) continue;
      if (cell && NEVER_ALLOWED.includes(c)) {
        throw new AgentError("AGT-0217", `${c} cannot be allowed for ${m} in this version of the AI data policy`);
      }
      if (cell === modules[m][c]) continue;
      changed = true;
      loosening ||= cell;
      modules[m][c] = cell;
    }
  }
  return { modules, changed, loosening };
}

export function decide(policy: AiDataPolicy, module: string, dataClass: string): PolicyDecision {
  const policyVersion = policy.version;
  const known = (AI_POLICY_MODULES as readonly string[]).includes(module) && (DATA_CLASSES as readonly string[]).includes(dataClass);
  if (!known) return { allowed: false, policyVersion, reason: "unknown module or data class" };
  const allowed = policy.modules[module as PolicyModule][dataClass as DataClass] === true;
  return { allowed, policyVersion, reason: allowed ? "allowed by the tenant policy" : "denied by the tenant policy" };
}
