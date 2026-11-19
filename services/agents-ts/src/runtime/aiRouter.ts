import type { Principal } from "../api/principal.js";
import type { ServiceSigner } from "../api/serviceSigner.js";
import { createProviderFactory, type Config } from "../config.js";
import { NO_ROUTE_MESSAGE, selectRoute, type Boundary, type ModelRoute, type RouteAdapter } from "../domain/modelRoute.js";
import { AgentError } from "../errors.js";
import type { LogFn } from "../logging/pseudonymize.js";
import { aiMayUse, createDesignationClient, treatAsCui, type DataRef, type DesignationClient, type EffectiveClass } from "./cuiDesignation.js";
import type { ModelProvider } from "./engine.js";

export interface PlanInput {
  caller: Principal;
  modelId: string;
  refs: readonly DataRef[];
}

export interface AiPlan {
  routeId: string;
  provider: ModelProvider;
  keep: DataRef[];
  excluded: DataRef[];
  classes: EffectiveClass[];
}

export interface AiRouter {
  plan(input: PlanInput): Promise<AiPlan>;
}

export interface AiRouterDeps {
  boundary: Boundary;
  routes: readonly ModelRoute[];
  providerFor: (modelId: string) => ModelProvider;
  designation: () => DesignationClient | undefined;
  log?: LogFn;
}

export function createAiRouter(deps: AiRouterDeps): AiRouter {
  const classify = async (caller: Principal, refs: readonly DataRef[]): Promise<EffectiveClass[]> => {
    const client = deps.designation();
    if (refs.length === 0) return [];
    if (client === undefined) return refs.map(() => "UNKNOWN");
    try {
      const resolved = await client.resolve(caller, refs);
      if (resolved.length !== refs.length) return refs.map(() => "UNKNOWN");
      return refs.map((_, i) => resolved[i]?.class ?? "UNKNOWN");
    } catch (err) {
      deps.log?.("warn", "data designation resolve failed, refs are UNKNOWN", { tenantId: caller.tenantId, error: err instanceof Error ? err.name : "unknown" });
      return refs.map(() => "UNKNOWN");
    }
  };

  return {
    async plan({ caller, modelId, refs }) {
      const classes = await classify(caller, refs);
      const excluded = refs.filter((_, i) => treatAsCui(classes[i] ?? "UNKNOWN"));
      const permitted = refs.filter((_, i) => aiMayUse(classes[i] ?? "UNKNOWN"));
      const cuiRoute = excluded.length > 0 ? selectRoute(deps.boundary, deps.routes, "CUI") : undefined;
      const route = cuiRoute ?? selectRoute(deps.boundary, deps.routes, "NONE");
      if (route === undefined) throw new AgentError("AGT-0218", NO_ROUTE_MESSAGE);
      const dropped = cuiRoute === undefined ? excluded : [];
      if (dropped.length > 0) {
        deps.log?.("warn", "records withheld from the AI call: no approved route for their data class", {
          tenantId: caller.tenantId,
          count: dropped.length,
          kinds: [...new Set(dropped.map((r) => r.entityType))],
        });
      }
      return { routeId: route.routeId, provider: deps.providerFor(modelId), keep: cuiRoute === undefined ? permitted : [...refs], excluded: dropped, classes };
    },
  };
}

export function assertBoundarySupported(cfg: Pick<Config, "boundary">): void {
  if (cfg.boundary === "cui") throw new Error("AGENTS_BOUNDARY=cui is refused: agent runs and run steps are not routed through an attested route yet (DEPOT-303 slice 2)");
}

export function envRoute(cfg: Pick<Config, "provider" | "openai" | "providerRegion" | "bedrock">): ModelRoute {
  const adapter: RouteAdapter =
    cfg.provider === "anthropic" ? "anthropic" : cfg.provider === "bedrock" ? "bedrock" : cfg.openai.auth === "api-key" || /\.openai\.azure\./.test(cfg.openai.baseUrl) ? "azure" : "vllm";
  const region = cfg.providerRegion ?? (cfg.bedrock.region === "" ? undefined : cfg.bedrock.region);
  return { routeId: `env:${adapter}`, adapter, endpointId: "env", ...(region !== undefined ? { region } : {}) };
}

export function createConfiguredRouter(cfg: Config, deps: { fetchFn?: typeof fetch; log?: LogFn; signer?: ServiceSigner } = {}): AiRouter {
  const signer = deps.signer;
  const complianceUrl = cfg.complianceUrl;
  return createAiRouter({
    boundary: cfg.boundary,
    routes: [envRoute(cfg)],
    providerFor: createProviderFactory(cfg, deps.fetchFn),
    designation: () =>
      complianceUrl === undefined
        ? undefined
        : createDesignationClient({
            baseUrl: complianceUrl,
            authHeaders: (caller) => {
              if (signer === undefined) throw new Error("no service signing key is configured");
              if (caller.legalEntityId === undefined) throw new Error("the caller's token names no legal entity");
              return { "x-depot-service-token": signer.mintForCompliance({ tenantId: caller.tenantId, legalEntityId: caller.legalEntityId, actor: caller.userId }) };
            },
            ...(deps.fetchFn !== undefined ? { fetchFn: deps.fetchFn } : {}),
            ...(deps.log !== undefined ? { log: deps.log } : {}),
          }),
    ...(deps.log !== undefined ? { log: deps.log } : {}),
  });
}
