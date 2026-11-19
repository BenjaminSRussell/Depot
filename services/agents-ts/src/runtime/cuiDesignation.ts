import type { Principal } from "../api/principal.js";
import type { LogFn } from "../logging/pseudonymize.js";

export type EffectiveClass = "CUI" | "NONE" | "UNKNOWN";

export interface DataRef {
  entityType: string;
  recordId: string;
}

export interface ResolvedClass extends DataRef {
  class: EffectiveClass;
  source: string;
}

export interface DesignationClient {
  resolve(caller: Principal, refs: readonly DataRef[]): Promise<ResolvedClass[]>;
}

const MAX_REFS = 500;
const UNRESOLVED = "UNRESOLVED";

export const treatAsCui = (effective: EffectiveClass): boolean => effective !== "NONE";

export const aiMayUse = (effective: EffectiveClass): boolean => effective === "NONE";

const unknown = (ref: DataRef): ResolvedClass => ({ entityType: ref.entityType, recordId: ref.recordId, class: "UNKNOWN", source: UNRESOLVED });

function parseItems(body: unknown, refs: readonly DataRef[]): ResolvedClass[] {
  const items = typeof body === "object" && body !== null ? (body as { items?: unknown }).items : undefined;
  if (!Array.isArray(items) || items.length !== refs.length) return refs.map(unknown);
  return refs.map((ref, i) => {
    const item = items[i] as { class?: unknown; source?: unknown } | null;
    const effective = item?.class;
    if (effective !== "CUI" && effective !== "NONE") return unknown(ref);
    return { entityType: ref.entityType, recordId: ref.recordId, class: effective, source: typeof item?.source === "string" ? item.source : UNRESOLVED };
  });
}

/**
 * The effective data class of records from services/compliance (POST /v1/compliance/data-designations:resolve), as an AI consumer
 * needs it: any failure to learn the class (timeout, refusal, unreachable, an answer that does not line up with the refs) is
 * UNKNOWN, and a consumer must treat UNKNOWN as CUI (treatAsCui). Only NONE is safe for AI (aiMayUse). The call never throws.
 * `authHeaders` supplies the credentials the resolver authorizes: the caller's own, so its legal-entity scope bounds the answer.
 */
export function createDesignationClient(cfg: {
  baseUrl: string;
  authHeaders: (caller: Principal) => Record<string, string> | Promise<Record<string, string>>;
  fetchFn?: typeof fetch;
  timeoutMs?: number;
  log?: LogFn;
}): DesignationClient {
  const fetchFn = cfg.fetchFn ?? fetch;
  const url = `${cfg.baseUrl.replace(/\/$/, "")}/v1/compliance/data-designations:resolve`;
  const batch = async (caller: Principal, refs: readonly DataRef[]): Promise<ResolvedClass[]> => {
    try {
      const res = await fetchFn(url, {
        method: "POST",
        headers: { "content-type": "application/json", ...(await cfg.authHeaders(caller)) },
        body: JSON.stringify({ refs }),
        signal: AbortSignal.timeout(cfg.timeoutMs ?? 5_000),
      });
      if (!res.ok) {
        cfg.log?.("warn", "data designation resolver refused, refs are UNKNOWN", { tenantId: caller.tenantId, status: res.status, refs: refs.length });
        return refs.map(unknown);
      }
      return parseItems(await res.json(), refs);
    } catch (err) {
      cfg.log?.("warn", "data designation resolver unavailable, refs are UNKNOWN", { tenantId: caller.tenantId, refs: refs.length, error: err instanceof Error ? err.name : "unknown" });
      return refs.map(unknown);
    }
  };
  return {
    async resolve(caller, refs) {
      const out: ResolvedClass[] = [];
      for (let start = 0; start < refs.length; start += MAX_REFS) out.push(...(await batch(caller, refs.slice(start, start + MAX_REFS))));
      return out;
    },
  };
}
