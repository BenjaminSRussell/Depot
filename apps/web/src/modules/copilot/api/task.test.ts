import { afterEach, describe, expect, it, vi } from "vitest";
import { AI_REQUEST_TIMEOUT_MS } from "../../../shell/assistant/availability";
import { COPILOT_TIMEOUT_MESSAGE, CopilotError, postTask, taskResponseSchema } from "./client";

const reply = (status: number, body: unknown) => async () => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
const intent = { routeKey: "receivables.openItems", path: "/receivables/open-items", params: { overdue: true, amountGt: 10000 }, draft: {}, summary: "Showing open invoices." };

afterEach(() => vi.useRealTimers());

describe("postTask", () => {
  it("posts the task mode without caching and returns the navigation intent", async () => {
    const fetchFn = vi.fn(reply(200, { navigationIntent: intent, generated: true }));
    const result = await postTask("show me overdue invoices over $10k", fetchFn as unknown as typeof fetch);
    expect(result).toEqual({ navigationIntent: intent, generated: true });
    const [path, init] = fetchFn.mock.calls[0] as unknown as [string, RequestInit];
    expect(path).toBe("/api/v1/copilot/chat");
    expect(init.method).toBe("POST");
    expect(init.cache).toBe("no-store");
    expect(JSON.parse(String(init.body))).toEqual({ mode: "task", message: "show me overdue invoices over $10k" });
  });

  it("returns a clarifying question and a refusal as given", async () => {
    const question = { clarifyingQuestion: "Which page do you want?", generated: true };
    expect(await postTask("x", reply(200, question) as unknown as typeof fetch)).toEqual(question);
    const refusal = { refusal: { code: "AGT-0203", message: "You do not have access to that page." }, generated: true };
    expect(await postTask("x", reply(200, refusal) as unknown as typeof fetch)).toEqual(refusal);
  });

  it("throws the problem code on a non-OK status", async () => {
    const err = await postTask("x", reply(403, { code: "AGT-0203" }) as unknown as typeof fetch).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(CopilotError);
    expect((err as CopilotError).code).toBe("AGT-0203");
  });

  it("rejects a body that does not match the schema", async () => {
    await expect(postTask("x", reply(200, { kind: "navigate" }) as unknown as typeof fetch)).rejects.toThrow();
  });

  it("gives up with the timeout message after the shared deadline", async () => {
    vi.useFakeTimers();
    const hang = (_path: string, init: RequestInit) => new Promise<Response>((_, reject) => init.signal?.addEventListener("abort", () => reject(new Error("aborted"))));
    const pending = postTask("x", hang as unknown as typeof fetch).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(AI_REQUEST_TIMEOUT_MS);
    expect(await pending).toMatchObject({ message: COPILOT_TIMEOUT_MESSAGE });
  });
});

describe("taskResponseSchema", () => {
  it.each([
    ["an external URL", { navigationIntent: { ...intent, path: "https://evil.example/x" }, generated: true }],
    ["a protocol-relative path", { navigationIntent: { ...intent, path: "//evil.example" }, generated: true }],
    ["a path with a query", { navigationIntent: { ...intent, path: "/a?x=1" }, generated: true }],
    ["a path with a fragment", { navigationIntent: { ...intent, path: "/a#x" }, generated: true }],
    ["a path without a leading slash", { navigationIntent: { ...intent, path: "a/b" }, generated: true }],
    ["a path with a scheme", { navigationIntent: { ...intent, path: "/javascript:alert(1)" }, generated: true }],
    ["a body without generated", { navigationIntent: intent }],
    ["generated false", { navigationIntent: intent, generated: false }],
    ["two bodies at once", { navigationIntent: intent, clarifyingQuestion: "Which?", generated: true }],
    ["an empty clarifying question", { clarifyingQuestion: "", generated: true }],
    ["a nested value in params", { navigationIntent: { ...intent, params: { a: { b: 1 } } }, generated: true }],
  ])("rejects %s", (_name, body) => {
    expect(taskResponseSchema.safeParse(body).success).toBe(false);
  });

  it("accepts a clean absolute app path and a clarifying question with candidates", () => {
    expect(taskResponseSchema.safeParse({ navigationIntent: { ...intent, path: "/purchasing/purchase-orders/new" }, generated: true }).success).toBe(true);
    expect(taskResponseSchema.safeParse({ clarifyingQuestion: "Which?", candidates: ["a", "b"], generated: true }).success).toBe(true);
  });
});
