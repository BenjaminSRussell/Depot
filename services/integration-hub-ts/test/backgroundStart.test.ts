import { describe, expect, it, vi } from "vitest";
import { startInBackground } from "../src/backgroundStart.ts";

// The consumer start must never fail the hub (D34): a missing source topic or a broker that is down is retried.
describe("startInBackground", () => {
  it("retries a failed start with doubling backoff, capped, until it succeeds", async () => {
    const stop = vi.fn().mockResolvedValue(undefined);
    const start = vi
      .fn()
      .mockRejectedValueOnce(new Error("UNKNOWN_TOPIC_OR_PARTITION"))
      .mockRejectedValueOnce(new Error("UNKNOWN_TOPIC_OR_PARTITION"))
      .mockRejectedValueOnce(new Error("broker down"))
      .mockResolvedValue({ stop });
    const waits: number[] = [];
    const errors: string[] = [];
    const started = startInBackground(start, {
      sleep: (ms) => {
        waits.push(ms);
        return Promise.resolve();
      },
      minBackoffMs: 5_000,
      maxBackoffMs: 12_000,
      onError: (err) => errors.push((err as Error).message),
    });
    expect(await started.ready).toBe(true);
    expect(start).toHaveBeenCalledTimes(4);
    expect(waits).toEqual([5_000, 10_000, 12_000]);
    expect(errors).toEqual(["UNKNOWN_TOPIC_OR_PARTITION", "UNKNOWN_TOPIC_OR_PARTITION", "broker down"]);
    await started.stop();
    expect(stop).toHaveBeenCalledTimes(1);
  });

  it("does not throw to the caller when every start fails, and stop() ends the retries", async () => {
    const start = vi.fn().mockRejectedValue(new Error("UNKNOWN_TOPIC_OR_PARTITION"));
    const started = startInBackground(start, { minBackoffMs: 60_000 });
    await vi.waitFor(() => expect(start).toHaveBeenCalledTimes(1));
    await started.stop();
    expect(await started.ready).toBe(false);
    expect(start).toHaveBeenCalledTimes(1);
  });

  it("stops a consumer whose start finishes after stop()", async () => {
    const stop = vi.fn().mockResolvedValue(undefined);
    let finish: (v: { stop: () => Promise<void> }) => void = () => undefined;
    const started = startInBackground(() => new Promise((resolve) => (finish = resolve)));
    const stopping = started.stop();
    finish({ stop });
    await stopping;
    expect(await started.ready).toBe(false);
    expect(stop).toHaveBeenCalledTimes(1);
  });
});
