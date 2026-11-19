import { afterEach, describe, expect, it, vi } from "vitest";
import { AI_FIRST_TOKEN_TIMEOUT_MS, AI_REQUEST_TIMEOUT_MS } from "../../../shell/assistant/availability";
import { COPILOT_TIMEOUT_MESSAGE, CopilotError, postChat as postChatInConversation, type ChatRequest } from "./client";
import { askCopilot as askInConversation, parseSseFrames, streamChat as streamInConversation, toStreamEvent, type StreamEvent } from "./stream";

const CONVERSATION_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
const postChat = (req: ChatRequest, fetchFn?: typeof fetch) => postChatInConversation(req, fetchFn, CONVERSATION_ID);
const streamChat = (req: ChatRequest, fetchFn?: typeof fetch) => streamInConversation(req, fetchFn, CONVERSATION_ID);
const askCopilot = (req: ChatRequest, onToken?: (text: string) => void, onWithdrawn?: (message: string) => void, fetchFn?: typeof fetch) =>
  askInConversation(req, onToken, onWithdrawn, fetchFn, CONVERSATION_ID);

const donePayload = {
  answer: "Revenue rose 4.2%.",
  citations: [{ sourceType: "METRIC", sourceRef: "revenue", label: "Revenue, Sep vs Aug" }],
  toolCalls: [{ tool: "depot.ledger.statement.get", status: "OK" }],
  refusal: null,
  generated: true,
};

function frames(...items: Array<[string, unknown]>): string {
  return items.map(([event, data]) => `event: ${event}\ndata: ${typeof data === "string" ? data : JSON.stringify(data)}\n\n`).join("");
}

/** A Response-like object whose body yields the given byte chunks in order. */
function streamingFetch(chunks: Uint8Array[], status = 200, contentType = "text/event-stream"): typeof fetch {
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      for (const c of chunks) controller.enqueue(c);
      controller.close();
    },
  });
  const fn = async (): Promise<Response> => new Response(body, { status, headers: { "content-type": contentType } });
  return fn as unknown as typeof fetch;
}

async function collect(gen: AsyncGenerator<StreamEvent>): Promise<StreamEvent[]> {
  const out: StreamEvent[] = [];
  for await (const e of gen) out.push(e);
  return out;
}

describe("parseSseFrames", () => {
  it("joins multi-line data, skips comments and frames without data, and keeps the unterminated tail", () => {
    const text = ": keep-alive\n\nevent: token\ndata: one\ndata: two\n\nid: 9\nevent: token\n\nevent: token\r\ndata: three\r\n\r\nevent: token\ndata: pa";
    const { events, rest } = parseSseFrames(text);
    expect(events).toEqual([
      { event: "token", data: "one\ntwo" },
      { event: "token", data: "three" },
    ]);
    expect(rest).toBe("event: token\ndata: pa");
  });
});

describe("toStreamEvent", () => {
  it("validates done frames with the same schema as the JSON path", () => {
    const ev = toStreamEvent({ event: "done", data: JSON.stringify(donePayload) });
    expect(ev).toMatchObject({ type: "done", response: { answer: "Revenue rose 4.2%.", generated: true } });
  });

  it("rejects a done frame without the generated label (AI labelling, FR-47.12)", () => {
    expect(() => toStreamEvent({ event: "done", data: JSON.stringify({ ...donePayload, generated: false }) })).toThrow();
  });

  it("maps an answer.withdrawn frame and rejects a malformed one", () => {
    expect(toStreamEvent({ event: "answer.withdrawn", data: JSON.stringify({ code: "AGT-0220", message: "safe" }) })).toEqual({ type: "withdrawn", code: "AGT-0220", message: "safe" });
    expect(() => toStreamEvent({ event: "answer.withdrawn", data: JSON.stringify({ code: "AGT-0220" }) })).toThrow();
  });

  it("ignores event names it does not know, so the contract can grow", () => {
    expect(toStreamEvent({ event: "progress", data: "{}" })).toBeUndefined();
  });
});

describe("streamChat", () => {
  it("yields tokens then the validated final answer, even when bytes split inside frames", async () => {
    const text = frames(["token", { text: "Revenue " }], ["token", { text: "rose." }], ["done", donePayload]);
    const bytes = new TextEncoder().encode(text);
    // One byte per chunk: every frame boundary and multi-byte split is exercised.
    const chunks = Array.from(bytes, (b) => new Uint8Array([b]));
    const events = await collect(streamChat({ mode: "ask", message: "revenue?" }, streamingFetch(chunks)));
    expect(events.map((e) => e.type)).toEqual(["token", "token", "done"]);
    expect(events[0]).toEqual({ type: "token", text: "Revenue " });
  });

  it("yields a terminal error frame and stops", async () => {
    const text = frames(["token", { text: "partial" }], ["error", { code: "AGT-0206", message: "Provider unavailable." }], ["token", { text: "never" }]);
    const events = await collect(streamChat({ mode: "explain", message: "why", recordRef: "INV-1" }, streamingFetch([new TextEncoder().encode(text)])));
    expect(events.at(-1)).toEqual({ type: "error", code: "AGT-0206", message: "Provider unavailable." });
    expect(events).toHaveLength(2);
  });

  it("throws when the stream closes before a terminal frame, so a truncated answer is never complete", async () => {
    const text = frames(["token", { text: "half an answer" }]);
    await expect(collect(streamChat({ mode: "summarize", message: "summarize" }, streamingFetch([new TextEncoder().encode(text)])))).rejects.toThrow(
      "ended before the answer was complete",
    );
  });

  it("reports a non-OK status without reading a body", async () => {
    await expect(collect(streamChat({ mode: "ask", message: "x" }, streamingFetch([], 503)))).rejects.toThrow("HTTP 503");
  });
});

describe("askCopilot (the panel's client)", () => {
  const encode = (text: string) => [new TextEncoder().encode(text)];

  it("streams tokens to the callback and returns the validated final answer", async () => {
    const tokens: string[] = [];
    const text = frames(["token", { text: "Revenue " }], ["token", { text: "rose." }], ["done", donePayload]);
    const res = await askCopilot({ mode: "ask", message: "revenue?" }, (t) => tokens.push(t), undefined, streamingFetch(encode(text), 200, "text/event-stream; charset=utf-8"));
    expect(tokens).toEqual(["Revenue ", "rose."]);
    expect(res.answer).toBe("Revenue rose 4.2%.");
  });

  it("reads a JSON answer when the server does not stream", async () => {
    const onToken = (): void => {
      throw new Error("no tokens on the JSON path");
    };
    const res = await askCopilot({ mode: "ask", message: "x" }, onToken, undefined, streamingFetch(encode(JSON.stringify(donePayload)), 200, "application/json"));
    expect(res).toMatchObject({ answer: "Revenue rose 4.2%.", generated: true });
  });

  it("asks for either response type", async () => {
    let accept = "";
    const fetchFn = (async (_url: string, init?: RequestInit) => {
      accept = String((init?.headers as Record<string, string>).accept);
      return new Response(JSON.stringify(donePayload), { status: 200, headers: { "content-type": "application/json" } });
    }) as unknown as typeof fetch;
    await askCopilot({ mode: "ask", message: "x" }, undefined, undefined, fetchFn);
    expect(accept).toContain("text/event-stream");
    expect(accept).toContain("application/json");
  });

  it("throws the code and message of a terminal error frame", async () => {
    const text = frames(["token", { text: "partial" }], ["error", { code: "AGT-0202", message: "Monthly token budget exhausted." }]);
    await expect(askCopilot({ mode: "ask", message: "x" }, undefined, undefined, streamingFetch(encode(text)))).rejects.toThrow(
      "AGT-0202: Monthly token budget exhausted.",
    );
  });

  it("rejects a JSON answer without the generated label", async () => {
    const body = JSON.stringify({ ...donePayload, generated: false });
    await expect(askCopilot({ mode: "ask", message: "x" }, undefined, undefined, streamingFetch(encode(body), 200, "application/json"))).rejects.toThrow();
  });
});

describe("copilot failures and deadlines", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  const hanging = ((_input: string, init?: RequestInit) =>
    new Promise<Response>((_resolve, reject) => init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))))) as unknown as typeof fetch;
  const problem = (status: number, code: string) =>
    (async () => new Response(JSON.stringify({ code }), { status, headers: { "content-type": "application/problem+json" } })) as unknown as typeof fetch;
  const request = { mode: "ask" as const, message: "how is cash" };

  it("carries the problem code of a refused call on the error", async () => {
    const error = await postChat(request, problem(422, "AGT-0202")).catch((e: unknown) => e);
    expect(error).toBeInstanceOf(CopilotError);
    expect(error).toMatchObject({ code: "AGT-0202", message: "Copilot is unavailable (HTTP 422)." });
  });

  it("keeps the message and has no code when the failure body cannot be read", async () => {
    const error = await askCopilot(request, undefined, undefined, (async () => new Response("<html>", { status: 502 })) as unknown as typeof fetch).catch((e: unknown) => e);
    expect(error).toMatchObject({ code: undefined, message: "Copilot is unavailable (HTTP 502)." });
  });

  it("gives up on a non-streamed chat after the request limit", async () => {
    vi.useFakeTimers();
    const outcome = postChat(request, hanging).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(AI_REQUEST_TIMEOUT_MS);
    expect(await outcome).toMatchObject({ message: COPILOT_TIMEOUT_MESSAGE });
  });

  it("waits up to thirty seconds for the first token and gives up after that", async () => {
    vi.useFakeTimers();
    const outcome = askCopilot(request, undefined, undefined, hanging).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(AI_FIRST_TOKEN_TIMEOUT_MS - 1);
    expect(vi.getTimerCount()).toBe(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(await outcome).toMatchObject({ message: COPILOT_TIMEOUT_MESSAGE });
  });

  it("gives up after the request limit when a non-streamed body never finishes", async () => {
    vi.useFakeTimers();
    const slowJson = (async () => ({ ok: true, headers: new Headers({ "content-type": "application/json" }), json: () => new Promise(() => undefined) })) as unknown as typeof fetch;
    const outcome = askCopilot(request, undefined, undefined, slowJson).catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(AI_REQUEST_TIMEOUT_MS);
    expect(await outcome).toMatchObject({ message: COPILOT_TIMEOUT_MESSAGE });
  });

  it("stops the first-token timer once a frame arrives, so a long answer is not cut off", async () => {
    vi.useFakeTimers();
    const fetchFn = streamingFetch([new TextEncoder().encode(frames(["token", { text: "Rev" }], ["done", donePayload]))]);
    const tokens: string[] = [];
    const response = await askCopilot(request, (t) => tokens.push(t), undefined, fetchFn);
    expect(response.answer).toBe(donePayload.answer);
    expect(tokens).toEqual(["Rev"]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it("passes the safe message of a withdrawn frame to the callback and still returns the refusal from done", async () => {
    const refusal = { ...donePayload, answer: "safe", refusal: { code: "AGT-0220", message: "safe" } };
    const fetchFn = streamingFetch([new TextEncoder().encode(frames(["token", { text: "Rev. " }], ["answer.withdrawn", { code: "AGT-0220", message: "safe" }], ["done", refusal]))]);
    const withdrawn: string[] = [];
    const response = await askCopilot(request, undefined, (m) => withdrawn.push(m), fetchFn);
    expect(withdrawn).toEqual(["safe"]);
    expect(response.refusal?.code).toBe("AGT-0220");
  });

  it("throws a terminal error frame with its code", async () => {
    const fetchFn = streamingFetch([new TextEncoder().encode(frames(["error", { code: "AGT-0202", message: "budget" }]))]);
    expect(await askCopilot(request, undefined, undefined, fetchFn).catch((e: unknown) => e)).toMatchObject({ code: "AGT-0202", message: "AGT-0202: budget" });
  });
});

describe("conversations transport", () => {
  const created = () => new Response(JSON.stringify({ conversationId: CONVERSATION_ID }), { status: 201, headers: { "content-type": "application/json" } });
  const answered = () => new Response(JSON.stringify(donePayload), { status: 200, headers: { "content-type": "application/json" } });
  const recorder = (reply: () => Response) => {
    const urls: string[] = [];
    const fetchFn = (async (url: string) => {
      urls.push(url);
      return urls.length === 1 ? reply() : answered();
    }) as unknown as typeof fetch;
    return { urls, fetchFn };
  };

  it("posts to the messages path of the given conversation and creates none", async () => {
    const { urls, fetchFn } = recorder(answered);
    await askInConversation({ mode: "ask", message: "x" }, undefined, undefined, fetchFn, CONVERSATION_ID);
    expect(urls).toEqual([`/api/v1/agents/copilot/conversations/${CONVERSATION_ID}/messages`]);
  });

  it("creates the conversation first when none is given, then posts to its messages path", async () => {
    const { urls, fetchFn } = recorder(created);
    await askInConversation({ mode: "ask", message: "x" }, undefined, undefined, fetchFn);
    expect(urls).toEqual(["/api/v1/agents/copilot/conversations", `/api/v1/agents/copilot/conversations/${CONVERSATION_ID}/messages`]);
  });

  it.each([
    ["no conversation id", {}],
    ["a number", { conversationId: 7 }],
    ["a string that is not a uuid", { conversationId: "../../admin" }],
  ])("sends no message when the new conversation comes back with %s", async (_name, body) => {
    const urls: string[] = [];
    const fetchFn = (async (url: string) => {
      urls.push(url);
      return new Response(JSON.stringify(body), { status: 201, headers: { "content-type": "application/json" } });
    }) as unknown as typeof fetch;
    await expect(askInConversation({ mode: "ask", message: "x" }, undefined, undefined, fetchFn)).rejects.toThrow();
    expect(urls).toEqual(["/api/v1/agents/copilot/conversations"]);
  });

  it("carries the problem code when the conversation is refused", async () => {
    const fetchFn = (async () => new Response(JSON.stringify({ code: "AGT-0203" }), { status: 403 })) as unknown as typeof fetch;
    expect(await askInConversation({ mode: "ask", message: "x" }, undefined, undefined, fetchFn).catch((e: unknown) => e)).toMatchObject({ code: "AGT-0203" });
  });
});
