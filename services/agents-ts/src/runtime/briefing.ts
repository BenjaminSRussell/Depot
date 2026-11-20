import type { DisabledAgents } from "../api/agentDefinitions.js";
import type { Principal } from "../api/principal.js";
import { buildBriefing, type BriefingFactsReader, type BriefingStore, type Cursor, type StoredBriefing } from "../domain/briefing.js";
import type { LogFn } from "../logging/pseudonymize.js";

export interface BriefingDeps {
  store: BriefingStore;
  facts: BriefingFactsReader;
  disabledAgents: DisabledAgents;
  log?: LogFn;
  now?: () => Date;
}

export function createBriefingService(deps: BriefingDeps) {
  const now = deps.now ?? (() => new Date());

  async function build(principal: Principal, id: string): Promise<void> {
    try {
      const at = now();
      const [facts, disabled] = await Promise.all([deps.facts.read(principal.tenantId, principal.userId, [...principal.duties], at), deps.disabledAgents(principal.tenantId)]);
      await deps.store.finish(principal.tenantId, id, buildBriefing(facts, at, disabled));
    } catch (err) {
      deps.log?.("error", "briefing build failed", { briefingId: id, tenantId: principal.tenantId, error: err instanceof Error ? err.name : "unknown" });
      await deps.store.fail(principal.tenantId, id).catch(() => undefined);
    }
  }

  return {
    async generate(principal: Principal): Promise<{ id: string; status: "RUNNING" }> {
      const id = await deps.store.create(principal.tenantId, principal.userId);
      void build(principal, id);
      return { id, status: "RUNNING" };
    },
    list: (principal: Principal, limit: number, cursor: Cursor | undefined) => deps.store.list(principal.tenantId, principal.userId, limit, cursor),
    get: (principal: Principal, id: string): Promise<StoredBriefing | undefined> => deps.store.get(principal.tenantId, principal.userId, id),
    markRead: (principal: Principal, id: string): Promise<StoredBriefing | undefined> => deps.store.markRead(principal.tenantId, principal.userId, id),
  };
}

export type BriefingService = ReturnType<typeof createBriefingService>;
