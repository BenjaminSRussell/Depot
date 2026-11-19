import type { Principal } from "../api/principal.js";
import type { LogFn } from "../logging/pseudonymize.js";

export const CUI_VIEW_DUTY = "cui.view";
export const NEED_TO_KNOW_CACHE_MS = 10_000;

export interface NeedToKnowClient {
  enforced(caller: Principal): Promise<boolean>;
}

export function createNeedToKnowClient(cfg: {
  baseUrl: string | undefined;
  authHeaders: (caller: Principal) => Record<string, string> | Promise<Record<string, string>>;
  fetchFn?: typeof fetch;
  now?: () => number;
  timeoutMs?: number;
  cacheMs?: number;
  log?: LogFn;
}): NeedToKnowClient {
  const fetchFn = cfg.fetchFn ?? fetch;
  const now = cfg.now ?? Date.now;
  const cache = new Map<string, { enabled: boolean; at: number }>();
  const read = async (caller: Principal): Promise<boolean> => {
    if (cfg.baseUrl === undefined || caller.legalEntityId === undefined) return true;
    const key = `${caller.tenantId}:${caller.legalEntityId}`;
    const hit = cache.get(key);
    if (hit !== undefined && now() - hit.at < (cfg.cacheMs ?? NEED_TO_KNOW_CACHE_MS)) return hit.enabled;
    try {
      const res = await fetchFn(`${cfg.baseUrl.replace(/\/$/, "")}/v1/compliance/need-to-know?legalEntityId=${encodeURIComponent(caller.legalEntityId)}`, {
        headers: await cfg.authHeaders(caller),
        signal: AbortSignal.timeout(cfg.timeoutMs ?? 5_000),
      });
      const body: unknown = res.ok ? await res.json() : undefined;
      const enabled = typeof body === "object" && body !== null ? (body as { enabled?: unknown }).enabled : undefined;
      if (typeof enabled !== "boolean") throw new Error(`need-to-know answer unreadable (${res.status})`);
      cache.set(key, { enabled, at: now() });
      return enabled;
    } catch (err) {
      cfg.log?.("warn", "need-to-know switch unreadable, treated as ON", { tenantId: caller.tenantId, error: err instanceof Error ? err.name : "unknown" });
      return true;
    }
  };
  return {
    async enforced(caller) {
      return caller.duties.has(CUI_VIEW_DUTY) ? false : read(caller);
    },
  };
}
