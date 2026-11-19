import type { Principal } from "../api/principal.js";
import type { ServiceSigner } from "../api/serviceSigner.js";
import type { LogFn } from "../logging/pseudonymize.js";
import type { DataClass, GuardDecision } from "./egressGuard.js";

export const MAX_IDS_PER_ROW = 200;

export interface AiAccessRow {
  agentId: string;
  runId: string;
  tool: string;
  entityType: string;
  recordIds: string[];
  recordClasses: string[];
  highestClass: DataClass;
  routeId: string;
  redactionCounts: Record<string, number>;
  excludedCounts: Record<string, number>;
  policyVersion: number;
  decision: GuardDecision | "WITHHELD";
  reason?: string;
  traceId: string;
  occurredAt: string;
}

export interface AiAccessAudit {
  record(caller: Principal, rows: readonly AiAccessRow[]): Promise<void>;
}

export function chunkIds(row: AiAccessRow): AiAccessRow[] {
  const chunks: AiAccessRow[] = [];
  for (let start = 0; start < row.recordIds.length; start += MAX_IDS_PER_ROW) {
    chunks.push({ ...row, recordIds: row.recordIds.slice(start, start + MAX_IDS_PER_ROW), recordClasses: row.recordClasses.slice(start, start + MAX_IDS_PER_ROW) });
  }
  return chunks;
}

export function createAiAccessAudit(cfg: { baseUrl: string | undefined; signer: ServiceSigner | undefined; fetchFn?: typeof fetch; timeoutMs?: number; log?: LogFn }): AiAccessAudit {
  const fetchFn = cfg.fetchFn ?? fetch;
  return {
    async record(caller, rows) {
      try {
        if (cfg.baseUrl === undefined) throw new Error("AGENTS_AUDIT_URL is not set");
        if (cfg.signer === undefined) throw new Error("no service signing key is configured");
        if (caller.legalEntityId === undefined) throw new Error("the caller's token names no legal entity");
        const url = `${cfg.baseUrl.replace(/\/$/, "")}/v1/audit/ai-data-access`;
        for (const row of rows.flatMap(chunkIds)) {
          const token = cfg.signer.mintForAudit({ tenantId: caller.tenantId, legalEntityId: caller.legalEntityId, actor: caller.userId });
          const res = await fetchFn(url, {
            method: "POST",
            headers: { "content-type": "application/json", "x-depot-service-token": token },
            body: JSON.stringify({ kind: "READ", ...row }),
            signal: AbortSignal.timeout(cfg.timeoutMs ?? 5_000),
          });
          if (res.status !== 201) throw new Error(`the audit service answered ${res.status}`);
        }
      } catch (err) {
        cfg.log?.("error", "AI data-access audit write failed, the model call is refused", { tenantId: caller.tenantId, rows: rows.length, error: err instanceof Error ? err.name : "unknown" });
        throw err;
      }
    },
  };
}
