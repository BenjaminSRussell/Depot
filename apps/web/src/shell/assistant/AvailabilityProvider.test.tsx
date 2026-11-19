import { act, render, renderHook, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { PrincipalProvider } from "../principal";
import { AssistantNotice, AvailabilityProvider, useAvailabilityController } from "./AvailabilityProvider";
import { AI_REQUEST_TIMEOUT_MS, AVAILABILITY_POLL_MS } from "./availability";
import { availableAssistant as enabled } from "../testing/fakeServer";

let body: unknown = enabled;
let status = 200;
const answer = async (_input: string, _init?: RequestInit) => new Response(JSON.stringify(body), { status });
const hang = (_input: string, init?: RequestInit) =>
  new Promise<Response>((_resolve, reject) => init?.signal?.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError"))));
const fetchFn = vi.fn(answer);
let visibility: DocumentVisibilityState = "visible";

beforeEach(() => {
  vi.useFakeTimers();
  body = enabled;
  status = 200;
  fetchFn.mockReset();
  fetchFn.mockImplementation(answer);
  visibility = "visible";
  vi.spyOn(document, "visibilityState", "get").mockImplementation(() => visibility);
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

const mount = (enable = true) => renderHook(() => useAvailabilityController(enable, fetchFn));
const settle = () => act(() => vi.advanceTimersByTimeAsync(0));
const tick = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms));

describe("availability polling", () => {
  it("reads once on mount, then again every poll interval while the tab is visible", async () => {
    const { result } = mount();
    expect(result.current.state).toEqual({ status: "checking" });
    await settle();
    expect(result.current.state).toEqual({ status: "enabled", budget: "OK" });
    expect(fetchFn).toHaveBeenCalledTimes(1);
    await tick(AVAILABILITY_POLL_MS - 1);
    expect(fetchFn).toHaveBeenCalledTimes(1);
    await tick(1);
    expect(fetchFn).toHaveBeenCalledTimes(2);
    await tick(AVAILABILITY_POLL_MS);
    expect(fetchFn).toHaveBeenCalledTimes(3);
  });

  it("makes no request while the tab is hidden and reads at once when it becomes visible", async () => {
    visibility = "hidden";
    mount();
    await tick(AVAILABILITY_POLL_MS * 3);
    expect(fetchFn).not.toHaveBeenCalled();
    visibility = "visible";
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await settle();
    expect(fetchFn).toHaveBeenCalledTimes(1);
  });

  it("goes from the budget notice back to enabled on the next poll, and from paused back to enabled", async () => {
    body = { ...enabled, budgetStatus: "HARD" };
    const { result } = mount();
    await settle();
    expect(result.current.state).toEqual({ status: "degraded", reason: "budget" });
    body = enabled;
    await tick(AVAILABILITY_POLL_MS);
    expect(result.current.state).toEqual({ status: "enabled", budget: "OK" });
    body = { ...enabled, killSwitch: { ...enabled.killSwitch, halted: true } };
    await tick(AVAILABILITY_POLL_MS);
    expect(result.current.state).toEqual({ status: "unavailable", reason: "paused" });
    body = enabled;
    await tick(AVAILABILITY_POLL_MS);
    expect(result.current.state).toEqual({ status: "enabled", budget: "OK" });
  });

  it("gives up on a read that does not answer within the request limit and treats it as not enabled", async () => {
    fetchFn.mockImplementation(hang);
    const { result } = mount();
    await tick(AI_REQUEST_TIMEOUT_MS);
    expect(result.current.state).toEqual({ status: "degraded", reason: "unreachable" });
  });

  it("flips to degraded at once on a budget refusal and reads once more, without a loop", async () => {
    const { result } = mount();
    await settle();
    body = { ...enabled, budgetStatus: "HARD" };
    act(() => result.current.reportRefusal("AGT-0202"));
    expect(result.current.state).toEqual({ status: "degraded", reason: "budget" });
    await settle();
    expect(fetchFn).toHaveBeenCalledTimes(2);
    await tick(AVAILABILITY_POLL_MS - 1);
    expect(fetchFn).toHaveBeenCalledTimes(2);
  });

  it("aborts the read in flight and leaves no timer or listener after unmount", async () => {
    let signal: AbortSignal | undefined;
    fetchFn.mockImplementationOnce((_input: string, init?: RequestInit) => {
      signal = init?.signal ?? undefined;
      return hang(_input, init);
    });
    const { unmount } = mount();
    await settle();
    unmount();
    await settle();
    expect(signal?.aborted).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe("AssistantNotice", () => {
  function Harness({ duties }: { duties: string[] }) {
    const availability = useAvailabilityController(true, fetchFn);
    return (
      <PrincipalProvider duties={duties}>
        <AvailabilityProvider value={availability}>
          <AssistantNotice />
        </AvailabilityProvider>
      </PrincipalProvider>
    );
  }

  it("announces the paused notice in a status region when the switch is turned on and removes it when it is turned off", async () => {
    render(<Harness duties={[]} />);
    await settle();
    expect(screen.queryByRole("status")).toBeNull();
    body = { ...enabled, killSwitch: { ...enabled.killSwitch, halted: true } };
    await tick(AVAILABILITY_POLL_MS);
    expect(screen.getByRole("status")).toHaveTextContent("The assistant is paused");
    body = enabled;
    await tick(AVAILABILITY_POLL_MS);
    expect(screen.queryByRole("status")).toBeNull();
  });
});
