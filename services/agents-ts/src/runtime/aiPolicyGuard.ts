import { AgentError } from "../errors.js";
import { type AiDataPolicy, decide, type PolicyDecision } from "../domain/aiDataPolicy.js";
import { isUuid } from "../api/principal.js";

export const AI_POLICY_CACHE_TTL_MS = 2_000;
export const AI_POLICY_READ_TIMEOUT_MS = 2_000;

export interface AiPolicyGuard {
  check(input: { tenantId: string; module: string; dataClass: string }): Promise<PolicyDecision>;
  invalidate(tenantId: string): void;
}

function withTimeout<T>(work: Promise<T>, ms: number): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error("AI data policy read timed out")), ms);
  });
  return Promise.race([work, timeout]).finally(() => clearTimeout(timer));
}

export function createAiPolicyGuard(deps: {
  read: (tenantId: string) => Promise<AiDataPolicy>;
  now?: () => number;
  ttlMs?: number;
  readTimeoutMs?: number;
  onError?: (err: unknown) => void;
}): AiPolicyGuard {
  const now = deps.now ?? Date.now;
  const ttlMs = deps.ttlMs ?? AI_POLICY_CACHE_TTL_MS;
  const cache = new Map<string, { policy: AiDataPolicy; at: number }>();

  async function policyOf(tenantId: string): Promise<AiDataPolicy> {
    const hit = cache.get(tenantId);
    if (hit !== undefined && now() - hit.at < ttlMs) return hit.policy;
    try {
      const policy = await withTimeout(deps.read(tenantId), deps.readTimeoutMs ?? AI_POLICY_READ_TIMEOUT_MS);
      cache.set(tenantId, { policy, at: now() });
      return policy;
    } catch (err) {
      cache.delete(tenantId);
      deps.onError?.(err);
      throw new AgentError("AGT-0216", "the AI data policy could not be read, so the call is refused");
    }
  }

  return {
    async check({ tenantId, module, dataClass }) {
      if (typeof tenantId !== "string" || !isUuid(tenantId)) return { allowed: false, policyVersion: 0, reason: "unknown tenant" };
      return decide(await policyOf(tenantId.toLowerCase()), module, dataClass);
    },
    invalidate(tenantId) {
      cache.delete(tenantId.toLowerCase());
    },
  };
}
