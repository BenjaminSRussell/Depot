import { z } from "zod";
import type { DepotModule } from "../../modules/registry";
import { killSwitchStatusSchema } from "../../modules/copilot/api/agents";
import { defaultFetch, type FetchLike } from "../api/http";
import { COPILOT_PATH } from "../home/assistantRouter";

export const AVAILABILITY_POLL_MS = 10_000;
export const AI_REQUEST_TIMEOUT_MS = 10_000;
export const AI_FIRST_TOKEN_TIMEOUT_MS = 30_000;
export const AVAILABILITY_PATH = "/api/v1/agents/availability";
export const BUDGET_REFUSAL_CODE = "AGT-0202";

export const availabilitySchema = z.object({
  killSwitch: killSwitchStatusSchema.pick({ halted: true, stale: true }),
  budgetStatus: z.enum(["OK", "SOFT", "HARD"]),
  copilotEnabled: z.boolean(),
});

export type Availability = z.infer<typeof availabilitySchema>;

export type AvailabilityRead = { ok: true; data: Availability } | { ok: false; cause: "network" | "timeout" | "http" | "invalid" };

export type AvailabilityState =
  | { status: "checking" }
  | { status: "enabled"; budget: "OK" | "SOFT" }
  | { status: "degraded"; reason: "budget" | "unreachable" }
  | { status: "unavailable"; reason: "paused" | "off" | "noAccess" };

export const CHECKING: AvailabilityState = { status: "checking" };
export const NO_ACCESS: AvailabilityState = { status: "unavailable", reason: "noAccess" };

export function withDeadline(ms: number, parent?: AbortSignal) {
  const controller = new AbortController();
  let expired = false;
  const timer = setTimeout(() => {
    expired = true;
    controller.abort();
  }, ms);
  if (parent?.aborted) controller.abort();
  else parent?.addEventListener("abort", () => controller.abort(), { once: true });
  return { signal: controller.signal, clear: () => clearTimeout(timer), expired: () => expired };
}

export async function getAvailability(signal?: AbortSignal, fetchFn: FetchLike = defaultFetch): Promise<AvailabilityRead> {
  const deadline = withDeadline(AI_REQUEST_TIMEOUT_MS, signal);
  try {
    const res = await fetchFn(AVAILABILITY_PATH, { cache: "no-store", headers: { accept: "application/json" }, signal: deadline.signal });
    if (!res.ok) return { ok: false, cause: "http" };
    const parsed = availabilitySchema.safeParse(await res.json().catch(() => undefined));
    return parsed.success ? { ok: true, data: parsed.data } : { ok: false, cause: "invalid" };
  } catch {
    return { ok: false, cause: deadline.expired() ? "timeout" : "network" };
  } finally {
    deadline.clear();
  }
}

export function deriveAvailability(read: AvailabilityRead): AvailabilityState {
  if (!read.ok) return { status: "degraded", reason: "unreachable" };
  const { killSwitch, budgetStatus, copilotEnabled } = read.data;
  if (killSwitch.halted || killSwitch.stale) return { status: "unavailable", reason: "paused" };
  if (!copilotEnabled) return { status: "unavailable", reason: "off" };
  if (budgetStatus === "HARD") return { status: "degraded", reason: "budget" };
  return { status: "enabled", budget: budgetStatus };
}

export function applyRefusal(state: AvailabilityState, code: string): AvailabilityState {
  return code === BUDGET_REFUSAL_CODE ? { status: "degraded", reason: "budget" } : state;
}

export function hideAssistantEntries(modules: DepotModule[]): DepotModule[] {
  const hidden = (to: string) => to === COPILOT_PATH || to.startsWith(`${COPILOT_PATH}/tools`);
  return modules.map((m) => (m.nav.some((n) => hidden(n.to)) ? { ...m, nav: m.nav.filter((n) => !hidden(n.to)) } : m));
}
