import { afterEach, describe, expect, it, vi } from "vitest";
import { AI_REQUEST_TIMEOUT_MS } from "../../../shell/assistant/availability";
import { COPILOT_TIMEOUT_MESSAGE, CopilotError, postRoute, routeResponseSchema } from "./client";

const reply = (status: number, body: unknown) => async () => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

afterEach(() => vi.useRealTimers());

describe("postRoute", () => {
  it("posts the route mode without caching and strips unknown fields", async () => {
    const fetchFn = vi.fn(reply(200, { kind: "navigate", confidence: 0.9, generated: true }));
    const result = await postRoute("show me overdue invoices", fetchFn as unknown as typeof fetch);
    expect(result).toEqual({ kind: "navigate", confidence: 0.9 });
    const [path, init] = fetchFn.mock.calls[0] as unknown as [string, RequestInit];
    expect(path).toBe("/api/v1/copilot/chat");
    expect(init.method).toBe("POST");
    expect(init.cache).toBe("no-store");
    expect(JSON.parse(String(init.body))).toEqual({ mode: "route", message: "show me overdue invoices" });
  });

  it("returns a refusal body as given", async () => {
    const refusal = { code: "AGT-0202", message: "Usage limit reached." };
    expect(await postRoute("x", reply(200, { refusal, generated: true }) as unknown as typeof fetch)).toEqual({ refusal });
  });

  it("throws the problem code on a non-OK status", async () => {
    const err = await postRoute("x", reply(403, { code: "AGT-0203" }) as unknown as typeof fetch).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(CopilotError);
    expect((err as CopilotError).code).toBe("AGT-0203");
  });

  it("gives up with the timeout message after the shared deadline", async () => {
    vi.useFakeTimers();
    const hang = (_path: string, init: RequestInit) => new Promise<Response>((_, reject) => init.signal?.addEventListener("abort", () => reject(new Error("aborted"))));
    const pending = postRoute("x", hang as unknown as typeof fetch).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(AI_REQUEST_TIMEOUT_MS);
    expect(await pending).toMatchObject({ message: COPILOT_TIMEOUT_MESSAGE });
  });
});

describe("routeResponseSchema", () => {
  it.each([
    ["a kind outside the four", { kind: "delete", confidence: 0.9 }],
    ["a missing kind", { confidence: 0.9 }],
    ["a string confidence", { kind: "ask", confidence: "0.9" }],
    ["a confidence above one", { kind: "ask", confidence: 1.5 }],
    ["candidates that are not a pair of kinds", { kind: "clarify", confidence: 0.3, candidates: { a: 1 } }],
    ["candidates outside the three kinds", { kind: "clarify", confidence: 0.3, candidates: ["ask", "clarify"] }],
    ["a null refusal", { refusal: null }],
    ["a refusal without a message", { refusal: { code: "AGT-0202" } }],
  ])("rejects %s", (_name, body) => {
    expect(routeResponseSchema.safeParse(body).success).toBe(false);
  });

  it("accepts clarify with and without candidates", () => {
    expect(routeResponseSchema.safeParse({ kind: "clarify", confidence: 0.3, candidates: ["navigate", "ask"] }).success).toBe(true);
    expect(routeResponseSchema.safeParse({ kind: "clarify", confidence: 0 }).success).toBe(true);
  });
});
