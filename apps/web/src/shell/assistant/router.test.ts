import { describe, expect, it, vi } from "vitest";
import type { RouteResponse } from "../../modules/copilot/api/client";
import type { AvailabilityState } from "./availability";
import { CANONICAL_PHRASES } from "./phrases";
import { AGENTS_INVOKE_DUTY, createPromptRouter, type PromptKind } from "./router";

const ENABLED: AvailabilityState = { status: "enabled", budget: "OK" };

function setup(options: { route?: RouteResponse | Error; availability?: AvailabilityState; duties?: string[]; register?: PromptKind[] } = {}) {
  const register = options.register ?? ["navigate", "act"];
  const handlers = { ask: vi.fn(), navigate: vi.fn(), act: vi.fn() };
  const postRoute = vi.fn(async () => {
    if (options.route instanceof Error) throw options.route;
    return options.route ?? { kind: "ask" as const, confidence: 1 };
  });
  const searchFallback = vi.fn();
  const duties = options.duties ?? [AGENTS_INVOKE_DUTY];
  const router = createPromptRouter({
    handlers: { ask: handlers.ask, ...(register.includes("navigate") ? { navigate: handlers.navigate } : {}), ...(register.includes("act") ? { act: handlers.act } : {}) },
    getAvailability: () => options.availability ?? ENABLED,
    hasDuty: (duty) => duties.includes(duty),
    postRoute,
    searchFallback,
  });
  const calls = () => [handlers.ask, handlers.navigate, handlers.act].map((h) => h.mock.calls.length);
  return { router, handlers, postRoute, searchFallback, calls };
}

describe("prompt router dispatch", () => {
  it.each(Object.entries(CANONICAL_PHRASES).flatMap(([kind, phrases]) => phrases.map((phrase) => [kind as PromptKind, phrase] as const)))(
    "sends the %s phrase %j to exactly one handler, once",
    async (kind, phrase) => {
      const { router, handlers, postRoute, calls } = setup({ route: { kind, confidence: 0.9 } });
      const outcome = await router.submit(phrase);
      expect(outcome).toEqual({ status: "dispatched", kind, handled: "own" });
      expect(postRoute).toHaveBeenCalledWith(phrase);
      expect(handlers[kind]).toHaveBeenCalledExactlyOnceWith(phrase);
      expect(calls().reduce((a, b) => a + b, 0)).toBe(1);
    },
  );

  it.each(["navigate", "act"] as const)("falls back to the ask handler once when no %s handler is registered", async (kind) => {
    const { router, handlers, calls } = setup({ route: { kind, confidence: 0.9 }, register: [] });
    expect(await router.submit("anything")).toEqual({ status: "dispatched", kind, handled: "ask-fallback" });
    expect(handlers.ask).toHaveBeenCalledExactlyOnceWith("anything");
    expect(calls()).toEqual([1, 0, 0]);
  });

  it("answers an empty prompt without any call", async () => {
    const { router, postRoute, searchFallback, calls } = setup();
    expect(await router.submit("   ")).toEqual({ status: "empty" });
    expect(postRoute).not.toHaveBeenCalled();
    expect(searchFallback).not.toHaveBeenCalled();
    expect(calls()).toEqual([0, 0, 0]);
  });
});

describe("prompt router clarify and explicit mode", () => {
  it("dispatches nothing on clarify and offers the server's two candidates", async () => {
    const { router, calls } = setup({ route: { kind: "clarify", confidence: 0.4, candidates: ["act", "ask"] } });
    expect(await router.submit("do invoices")).toEqual({ status: "clarify", options: ["act", "ask"] });
    expect(calls()).toEqual([0, 0, 0]);
  });

  it.each([
    ["absent", undefined, ["navigate", "ask"]],
    ["a repeated kind", ["ask", "ask"], ["ask", "navigate"]],
  ] as const)("offers two distinct options when candidates are %s", async (_name, candidates, options) => {
    const { router } = setup({ route: { kind: "clarify", confidence: 0.4, ...(candidates ? { candidates: [...candidates] as ["ask", "ask"] } : {}) } });
    expect(await router.submit("x")).toEqual({ status: "clarify", options });
  });

  it.each(["ask", "navigate", "act"] as const)("uses the explicit %s mode and makes no classification call", async (mode) => {
    const { router, handlers, postRoute, calls } = setup();
    expect(await router.submit("text", mode)).toEqual({ status: "dispatched", kind: mode, handled: "own" });
    expect(postRoute).not.toHaveBeenCalled();
    expect(handlers[mode]).toHaveBeenCalledExactlyOnceWith("text");
    expect(calls().reduce((a, b) => a + b, 0)).toBe(1);
  });

  it("treats an unknown mode as no mode and classifies", async () => {
    const { router, postRoute } = setup();
    await router.submit("text", "route" as never);
    expect(postRoute).toHaveBeenCalledOnce();
  });
});

describe("prompt router act duty", () => {
  it.each([["no duties", []], ["only agentapproval.decide", ["agentapproval.decide"]]])("blocks act and offers navigate with %s", async (_name, duties) => {
    const { router, calls } = setup({ route: { kind: "act", confidence: 0.9 }, duties });
    expect(await router.submit(CANONICAL_PHRASES.act[0] ?? "")).toEqual({ status: "blocked", reason: "no-duty", alternative: "navigate" });
    expect(calls()).toEqual([0, 0, 0]);
  });

  it("blocks an explicit act mode without the duty and makes no classification call", async () => {
    const { router, postRoute, calls } = setup({ duties: [] });
    expect(await router.submit("cancel this planned order", "act")).toMatchObject({ status: "blocked" });
    expect(postRoute).not.toHaveBeenCalled();
    expect(calls()).toEqual([0, 0, 0]);
  });

  it("lets the navigate alternative through for a user without the duty", async () => {
    const { router, handlers } = setup({ duties: [] });
    await router.submit("cancel this planned order", "navigate");
    expect(handlers.navigate).toHaveBeenCalledOnce();
  });
});

describe("prompt router when AI is unavailable", () => {
  it.each<AvailabilityState>([
    { status: "checking" },
    { status: "degraded", reason: "budget" },
    { status: "degraded", reason: "unreachable" },
    { status: "unavailable", reason: "paused" },
    { status: "unavailable", reason: "off" },
  ])("searches instead and makes no classification call in state %j", async (availability) => {
    const { router, postRoute, searchFallback, calls } = setup({ availability });
    expect(await router.submit("show me overdue invoices")).toEqual({ status: "search", reason: "ai-unavailable" });
    expect(postRoute).not.toHaveBeenCalled();
    expect(searchFallback).toHaveBeenCalledExactlyOnceWith("show me overdue invoices");
    expect(calls()).toEqual([0, 0, 0]);
  });

  it("searches instead even when a mode is chosen", async () => {
    const { router, searchFallback, calls } = setup({ availability: { status: "unavailable", reason: "paused" } });
    expect(await router.submit("text", "ask")).toEqual({ status: "search", reason: "ai-unavailable" });
    expect(searchFallback).toHaveBeenCalledOnce();
    expect(calls()).toEqual([0, 0, 0]);
  });
});

describe("prompt router classification failure", () => {
  it("shows a refusal with the server's code and message and dispatches nothing", async () => {
    const { router, searchFallback, calls } = setup({ route: { refusal: { code: "AGT-0202", message: "The assistant has reached its usage limit." } } });
    expect(await router.submit("anything")).toEqual({ status: "refused", code: "AGT-0202", message: "The assistant has reached its usage limit." });
    expect(searchFallback).not.toHaveBeenCalled();
    expect(calls()).toEqual([0, 0, 0]);
  });

  it.each([
    ["timeout", new Error("Copilot did not answer in time.")],
    ["network error", new TypeError("Failed to fetch")],
    ["HTTP error", new Error("Copilot is unavailable (HTTP 503).")],
    ["schema failure", new Error("invalid")],
  ])("falls back to search on a %s and never dispatches act", async (_name, failure) => {
    const { router, searchFallback, handlers, calls } = setup({ route: failure });
    expect(await router.submit(CANONICAL_PHRASES.act[0] ?? "")).toEqual({ status: "search", reason: "error" });
    expect(searchFallback).toHaveBeenCalledOnce();
    expect(handlers.act).not.toHaveBeenCalled();
    expect(calls()).toEqual([0, 0, 0]);
  });
});
