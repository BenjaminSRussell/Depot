import { AgentError } from "../errors.js";
import { type AiDataPolicy, type AiDataPolicyPatch, parsePolicyPatch } from "../domain/aiDataPolicy.js";
import { ApiError, type Principal, requireDuty } from "./principal.js";

export const AIPOLICY_READ_DUTY = "aipolicy.read";
export const AIPOLICY_WRITE_DUTY = "aipolicy.write";
export const TENANT_ADMIN_DUTY = "tenant.administer";

export interface AiPolicyWrite {
  tenantId: string;
  userId: string;
  expectedVersion: number;
  patch: AiDataPolicyPatch;
  now: Date;
}

export interface AiPolicyStore {
  read(tenantId: string): Promise<AiDataPolicy>;
  write(input: AiPolicyWrite): Promise<AiDataPolicy>;
}

const IF_MATCH = /^(?:W\/)?"?([0-9]{1,15})"?$/;

export function parseIfMatch(header: string | undefined): number {
  if (header === undefined) throw new ApiError(428, "AGT-0428", "If-Match with the policy version you read is required");
  const match = IF_MATCH.exec(header.trim());
  if (match === null) throw new ApiError(400, "AGT-0400", "If-Match must be the policy version");
  return Number(match[1]);
}

async function readPolicy(store: AiPolicyStore, tenantId: string): Promise<AiDataPolicy> {
  try {
    return await store.read(tenantId);
  } catch (err) {
    process.stderr.write(`${JSON.stringify({ level: "error", message: "AI data policy read failed", service: "agents-ts", error: String(err) })}\n`);
    throw new AgentError("AGT-0216", "the AI data policy could not be read");
  }
}

export async function handleGetAiDataPolicy(input: { principal: Principal; store: AiPolicyStore }): Promise<AiDataPolicy> {
  requireDuty(input.principal, AIPOLICY_READ_DUTY, "the AI data policy");
  return readPolicy(input.store, input.principal.tenantId);
}

export async function handlePutAiDataPolicy(input: {
  principal: Principal;
  ifMatch: string | undefined;
  body: unknown;
  store: AiPolicyStore;
  now: () => Date;
}): Promise<AiDataPolicy> {
  requireDuty(input.principal, AIPOLICY_WRITE_DUTY, "changing the AI data policy");
  requireDuty(input.principal, TENANT_ADMIN_DUTY, "changing the tenant's AI data policy");
  const expectedVersion = parseIfMatch(input.ifMatch);
  const patch = parsePolicyPatch(input.body);
  return input.store.write({
    tenantId: input.principal.tenantId,
    userId: input.principal.userId,
    expectedVersion,
    patch,
    now: input.now(),
  });
}
