import { afterEach, describe, expect, it, vi } from "vitest";
import type { DepotModule } from "../../modules/registry";
import {
  AI_REQUEST_TIMEOUT_MS,
  applyRefusal,
  AVAILABILITY_PATH,
  deriveAvailability,
  getAvailability,
  hideAssistantEntries,
  type Availability,
} from "./availability";
import { availableAssistant as enabled } from "../testing/fakeServer";

const reply = (status: number, body: unknown) => vi.fn(async () => new Response(JSON.stringify(body), { status }));

afterEach(() => {
  vi.useRealTimers();
});

describe("deriveAvailability", () => {
  const read = (data: Availability) => ({ ok: true as const, data });
  const kill = (patch: object) => ({ ...enabled, killSwitch: { ...enabled.killSwitch, ...patch } });

  it.each([
    ["enabled", read(enabled), { status: "enabled", budget: "OK" }],
    ["a soft budget", read({ ...enabled, budgetStatus: "SOFT" }), { status: "enabled", budget: "SOFT" }],
    ["a halted kill switch", read(kill({ halted: true })), { status: "unavailable", reason: "paused" }],
    ["a stale kill switch", read(kill({ stale: true })), { status: "unavailable", reason: "paused" }],
    ["the assistant off for the company", read({ ...enabled, copilotEnabled: false }), { status: "unavailable", reason: "off" }],
    ["a hard budget", read({ ...enabled, budgetStatus: "HARD" }), { status: "degraded", reason: "budget" }],
    ["a network error", { ok: false as const, cause: "network" as const }, { status: "degraded", reason: "unreachable" }],
    ["an invalid body", { ok: false as const, cause: "invalid" as const }, { status: "degraded", reason: "unreachable" }],
  ])("maps %s", (_name, input, expected) => {
    expect(deriveAvailability(input)).toEqual(expected);
  });

});

describe("applyRefusal", () => {
  it("degrades on the budget refusal and leaves the state alone for any other code", () => {
    const state = { status: "enabled", budget: "OK" } as const;
    expect(applyRefusal(state, "AGT-0202")).toEqual({ status: "degraded", reason: "budget" });
    expect(applyRefusal(state, "AGT-0001")).toBe(state);
  });
});

describe("getAvailability", () => {
  it("reads the availability path without caching and returns the validated body", async () => {
    const fetchFn = reply(200, { ...enabled, killSwitch: { ...enabled.killSwitch, tenantId: "t1" } });
    expect(await getAvailability(undefined, fetchFn)).toEqual({ ok: true, data: enabled });
    expect(fetchFn).toHaveBeenCalledWith(AVAILABILITY_PATH, expect.objectContaining({ cache: "no-store" }));
  });

  it.each([404, 401, 403, 500, 503])("reports an http failure for status %s without throwing", async (status) => {
    expect(await getAvailability(undefined, reply(status, {}))).toEqual({ ok: false, cause: "http" });
  });

  it.each([
    ["null", null],
    ["a missing copilotEnabled", { killSwitch: enabled.killSwitch, budgetStatus: "OK" }],
  ])("fails closed on %s", async (_name, body) => {
    expect(await getAvailability(undefined, reply(200, body))).toEqual({ ok: false, cause: "invalid" });
  });

  it("aborts and reports a timeout when nothing answers within the request limit", async () => {
    vi.useFakeTimers();
    const fetchFn = vi.fn(
      (_input: string, init?: RequestInit) =>
        new Promise<Response>((_resolve, reject) => init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")))),
    );
    const pending = getAvailability(undefined, fetchFn);
    await vi.advanceTimersByTimeAsync(AI_REQUEST_TIMEOUT_MS - 1);
    expect(fetchFn.mock.calls[0]?.[1]?.signal?.aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(await pending).toEqual({ ok: false, cause: "timeout" });
  });

});

describe("hideAssistantEntries", () => {
  const modules: DepotModule[] = [
    {
      id: "copilot",
      title: "Copilot",
      routes: [],
      nav: [
        { label: "Copilot", to: "/copilot" },
        { label: "Tool catalog", to: "/copilot/tools" },
        { label: "Agent kill switch", to: "/copilot/kill-switch" },
      ],
    },
    { id: "ledger", title: "Ledger", routes: [], nav: [{ label: "Journals", to: "/ledger/journals" }] },
  ];

  it("removes the copilot and tool catalog entries, keeps the kill switch page, and leaves other modules as they are", () => {
    const hidden = hideAssistantEntries(modules);
    expect(hidden[0]?.nav.map((n) => n.to)).toEqual(["/copilot/kill-switch"]);
    expect(hidden[1]).toBe(modules[1]);
  });
});
