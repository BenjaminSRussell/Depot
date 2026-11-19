import { describe, expect, it, vi } from "vitest";
import { getAvailability } from "../../../shell/assistant/availability";
import { getKillSwitch, setKillSwitch } from "./agents";
import { createConversation, postChat } from "./client";
import { listTools } from "./mcp";
import { askCopilot, streamChat } from "./stream";

function capture() {
  const inits: RequestInit[] = [];
  const fetchFn = vi.fn(async (_input: unknown, init: RequestInit = {}) => {
    inits.push(init);
    return new Response("{}", { status: 500, headers: { "content-type": "application/json" } });
  });
  return { inits, fetchFn: fetchFn as unknown as typeof fetch };
}

const CONVERSATION_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
const request = { mode: "ask" as const, message: "how is cash" };

describe("copilot requests are never stored by the browser HTTP cache", () => {
  it.each([
    ["createConversation", (f: typeof fetch) => createConversation(f)],
    ["postChat", (f: typeof fetch) => postChat(request, f, CONVERSATION_ID)],
    ["streamChat", (f: typeof fetch) => streamChat(request, f, CONVERSATION_ID).next()],
    ["askCopilot", (f: typeof fetch) => askCopilot(request, undefined, undefined, f, CONVERSATION_ID)],
    ["getKillSwitch", (f: typeof fetch) => getKillSwitch(undefined, f)],
    ["setKillSwitch", (f: typeof fetch) => setKillSwitch({ scope: "GLOBAL", active: true }, f)],
    ["getAvailability", (f: typeof fetch) => getAvailability(undefined, f)],
    ["listTools", (f: typeof fetch) => listTools(undefined, f)],
  ])("%s sends cache: no-store", async (_name, call) => {
    const { inits, fetchFn } = capture();
    await call(fetchFn).catch(() => undefined);
    expect(inits).toHaveLength(1);
    expect(inits[0].cache).toBe("no-store");
  });
});
