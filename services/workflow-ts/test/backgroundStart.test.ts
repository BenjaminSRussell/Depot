import { describe, expect, it, vi } from "vitest";
import { sleepUnlessAborted, startInBackground, type Started } from "../src/backgroundStart.js";

// The event consumer's start must never fail the worker (D34): a missing source topic or a broker that is down is retried.
describe("startInBackground", () => {
  const noWait = (): Promise<void> => Promise.resolve();

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
    const counts: number[] = [];
    const running: boolean[] = [];
    const started = startInBackground(start, {
      sleep: (ms) => {
        waits.push(ms);
        return noWait();
      },
      minBackoffMs: 5_000,
      maxBackoffMs: 12_000,
      onError: (err, _retryInMs, failures) => {
        errors.push((err as Error).message);
        counts.push(failures);
      },
      onRunning: (r) => running.push(r),
    });
    expect(await started.ready).toBe(true);
    expect(start).toHaveBeenCalledTimes(4);
    expect(waits).toEqual([5_000, 10_000, 12_000]);
    expect(errors).toEqual(["UNKNOWN_TOPIC_OR_PARTITION", "UNKNOWN_TOPIC_OR_PARTITION", "broker down"]);
    expect(counts).toEqual([1, 2, 3]);
    await started.stop();
    expect(stop).toHaveBeenCalledTimes(1);
    expect(running).toEqual([true, false]);
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
    let finish: (v: Started) => void = () => undefined;
    const started = startInBackground(() => new Promise((resolve) => (finish = resolve)));
    const stopping = started.stop();
    finish({ stop });
    await stopping;
    expect(await started.ready).toBe(false);
    expect(stop).toHaveBeenCalledTimes(1);
  });

  it("returns from stop() at once when the pending start fails during shutdown, without logging or waiting", async () => {
    let fail: (err: Error) => void = () => undefined;
    const onError = vi.fn();
    const started = startInBackground(() => new Promise<Started>((_, reject) => (fail = reject)), {
      minBackoffMs: 60_000,
      maxBackoffMs: 120_000,
      onError,
    });
    const t0 = Date.now();
    const stopping = started.stop();
    fail(new Error("UNKNOWN_TOPIC_OR_PARTITION"));
    await stopping;
    expect(Date.now() - t0).toBeLessThan(1_000);
    expect(onError).not.toHaveBeenCalled();
    expect(await started.ready).toBe(false);
  });

  it("sleepUnlessAborted returns at once on a signal that has already aborted", async () => {
    const controller = new AbortController();
    controller.abort();
    const t0 = Date.now();
    await sleepUnlessAborted(60_000, controller.signal);
    expect(Date.now() - t0).toBeLessThan(1_000);
  });

  it("retries a client that fails for good after it started, and counts it as a failure", async () => {
    const crashes: Array<(err: Error) => void> = [];
    const stops: number[] = [];
    let n = 0;
    const start = vi.fn(async (): Promise<Started> => {
      const id = n++;
      return {
        failed: new Promise((resolve) => crashes.push(resolve)),
        stop: async () => {
          stops.push(id);
        },
      };
    });
    const running: boolean[] = [];
    const errors: Array<[string, number]> = [];
    const started = startInBackground(start, {
      sleep: noWait,
      now: () => 0,
      onRunning: (r) => running.push(r),
      onError: (err, _ms, failures) => errors.push([(err as Error).message, failures]),
    });
    expect(await started.ready).toBe(true);
    crashes[0]!(new Error("GROUP_AUTHORIZATION_FAILED"));
    await vi.waitFor(() => expect(start).toHaveBeenCalledTimes(2));
    expect(stops).toEqual([0]); // The crashed client's connections are released before the restart.
    expect(errors).toEqual([["GROUP_AUTHORIZATION_FAILED", 1]]);
    crashes[1]!(new Error("GROUP_AUTHORIZATION_FAILED"));
    await vi.waitFor(() => expect(start).toHaveBeenCalledTimes(3));
    expect(errors.map(([, f]) => f)).toEqual([1, 2]); // A client that crashes at once does not reset the count.
    await started.stop();
    expect(stops).toEqual([0, 1, 2]);
    expect(running).toEqual([true, false, true, false, true, false]);
  });

  it("resets the backoff and the failure count after a client ran for maxBackoffMs", async () => {
    let clock = 0;
    const crashes: Array<(err: Error) => void> = [];
    const start = vi
      .fn()
      .mockRejectedValueOnce(new Error("broker down"))
      .mockImplementation(async (): Promise<Started> => ({ failed: new Promise((r) => crashes.push(r)), stop: async () => undefined }));
    const waits: number[] = [];
    const counts: number[] = [];
    const started = startInBackground(start, {
      sleep: (ms) => {
        waits.push(ms);
        return noWait();
      },
      now: () => clock,
      minBackoffMs: 5_000,
      maxBackoffMs: 120_000,
      onError: (_err, _ms, failures) => counts.push(failures),
    });
    await vi.waitFor(() => expect(crashes).toHaveLength(1));
    clock += 120_000;
    crashes[0]!(new Error("coordinator moved"));
    await vi.waitFor(() => expect(crashes).toHaveLength(2));
    expect(waits).toEqual([5_000, 5_000]);
    expect(counts).toEqual([1, 1]);
    await started.stop();
  });
});
