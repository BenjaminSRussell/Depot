import type { RouteResponse } from "../../modules/copilot/api/client";
import type { AvailabilityState } from "./availability";

export const AGENTS_INVOKE_DUTY = "agents.invoke";

export type PromptKind = "ask" | "navigate" | "act";
export type PromptMode = PromptKind;
export type ChooserOptions = readonly [PromptKind, PromptKind];

export type AskHandler = (text: string) => void | Promise<void>;
export type NavigateHandler = (text: string) => void | Promise<void>;
export type ActHandler = (text: string) => void | Promise<void>;

export interface PromptHandlers {
  ask: AskHandler;
  navigate?: NavigateHandler;
  act?: ActHandler;
}

export type RouterOutcome =
  | { status: "dispatched"; kind: PromptKind; handled: "own" | "ask-fallback" }
  | { status: "clarify"; options: ChooserOptions }
  | { status: "blocked"; reason: "no-duty"; alternative: "navigate" }
  | { status: "refused"; code: string; message: string }
  | { status: "search"; reason: "ai-unavailable" | "error" }
  | { status: "empty" };

export interface PromptRouterDeps {
  handlers: PromptHandlers;
  getAvailability: () => AvailabilityState;
  hasDuty: (duty: string) => boolean;
  postRoute: (message: string) => Promise<RouteResponse>;
  searchFallback: (text: string) => void | Promise<void>;
}

export interface PromptRouter {
  submit: (text: string, mode?: PromptMode) => Promise<RouterOutcome>;
}

const MODES: readonly string[] = ["ask", "navigate", "act"];
const DEFAULT_OPTIONS: readonly PromptKind[] = ["navigate", "ask"];

function chooserOptions(candidates: readonly PromptKind[] | undefined): ChooserOptions {
  const distinct = [...new Set([...(candidates ?? []), ...DEFAULT_OPTIONS])];
  return [distinct[0] ?? "navigate", distinct[1] ?? "ask"];
}

export function createPromptRouter(deps: PromptRouterDeps): PromptRouter {
  const { handlers } = deps;

  async function dispatch(kind: PromptKind, text: string): Promise<RouterOutcome> {
    if (kind === "act" && !deps.hasDuty(AGENTS_INVOKE_DUTY)) return { status: "blocked", reason: "no-duty", alternative: "navigate" };
    const own = handlers[kind];
    await (own ?? handlers.ask)(text);
    return { status: "dispatched", kind, handled: own === undefined ? "ask-fallback" : "own" };
  }

  async function searchInstead(text: string, reason: "ai-unavailable" | "error"): Promise<RouterOutcome> {
    await deps.searchFallback(text);
    return { status: "search", reason };
  }

  async function submit(text: string, mode?: PromptMode): Promise<RouterOutcome> {
    const message = text.trim();
    if (message === "") return { status: "empty" };
    if (deps.getAvailability().status !== "enabled") return searchInstead(message, "ai-unavailable");
    if (mode !== undefined && MODES.includes(mode)) return dispatch(mode, message);
    let route: RouteResponse;
    try {
      route = await deps.postRoute(message);
    } catch {
      return searchInstead(message, "error");
    }
    if ("refusal" in route) return { status: "refused", code: route.refusal.code, message: route.refusal.message };
    if (route.kind === "clarify") return { status: "clarify", options: chooserOptions(route.candidates) };
    return dispatch(route.kind, message);
  }

  return { submit };
}
