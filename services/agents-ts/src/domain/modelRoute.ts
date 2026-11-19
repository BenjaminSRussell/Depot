import type { DataRef, EffectiveClass } from "../runtime/cuiDesignation.js";

export type Boundary = "standard" | "cui";
export type RouteAdapter = "anthropic" | "bedrock" | "azure" | "vllm";

export interface Attestation {
  accreditation: string;
  evidenceRef: string;
  approvedBy: string;
  approvedAt: string;
}

export interface ModelRoute {
  routeId: string;
  adapter: RouteAdapter;
  endpointId: string;
  region?: string;
  attestation?: Attestation;
}

export const NO_ROUTE_MESSAGE = "I cannot use that data: no approved AI route exists for it in this deployment. The manual flow is unaffected.";

const isAttested = (route: ModelRoute): boolean => {
  const a = route.attestation;
  return a !== undefined && [a.accreditation, a.evidenceRef, a.approvedBy, a.approvedAt].every((v) => v.trim() !== "");
};

export function routeCarries(boundary: Boundary, route: ModelRoute, dataClass: EffectiveClass): boolean {
  if (boundary === "cui") return isAttested(route);
  return dataClass === "NONE";
}

export const selectRoute = (boundary: Boundary, routes: readonly ModelRoute[], dataClass: EffectiveClass): ModelRoute | undefined =>
  routes.find((r) => routeCarries(boundary, r, dataClass));

const refOf = (entityType: string, value: unknown): DataRef => ({
  entityType,
  recordId: typeof value === "object" && value !== null && typeof (value as { id?: unknown }).id === "string" ? (value as { id: string }).id : "",
});

export function recordsOf(tool: string, output: unknown): DataRef[] {
  const entityType = tool.split(".")[2] ?? "";
  const record = output as { id?: unknown; items?: unknown } | null | undefined;
  if (typeof record === "object" && record !== null && !Array.isArray(record) && typeof record.id === "string" && record.id !== "") return [refOf(entityType, record)];
  const list = Array.isArray(output) ? output : Array.isArray(record?.items) ? (record.items as unknown[]) : undefined;
  return list === undefined ? [refOf(entityType, undefined)] : list.map((element) => refOf(entityType, element));
}

export function exclusionNotice(kinds: readonly string[], count: number): string {
  const what = kinds.length === 0 ? "records" : `${kinds.join(", ")} records`;
  return `I left out ${count} of the ${what} it found because no approved AI route exists for that data. The rest of the answer uses only data that is allowed.`;
}
