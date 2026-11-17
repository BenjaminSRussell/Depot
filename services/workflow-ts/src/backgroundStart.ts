// Starts a long-lived client (the Kafka event consumer) in the background and retries a failed start with backoff, so a
// dependency that is not there yet never stops the process that hosts it (D34, B-05b).

export interface Stoppable {
  stop(): Promise<void>;
}

export interface BackgroundStartOptions {
  /** Called after each failed start with the error and the wait before the next attempt. */
  onError?: (err: unknown, retryInMs: number) => void;
  /** Waits ms, or less when the signal aborts. */
  sleep?: (ms: number, signal: AbortSignal) => Promise<void>;
  minBackoffMs?: number;
  maxBackoffMs?: number;
}

export interface BackgroundStarted extends Stoppable {
  /** Resolves true once the start succeeded, false when stop() ended the retries first. */
  ready: Promise<boolean>;
}

export const sleepUnlessAborted = (ms: number, signal: AbortSignal): Promise<void> =>
  new Promise((resolve) => {
    const timer = setTimeout(done, ms);
    function done(): void {
      clearTimeout(timer);
      signal.removeEventListener("abort", done);
      resolve();
    }
    signal.addEventListener("abort", done, { once: true });
  });

/** Calls start until it succeeds: backoff from minBackoffMs (default 5 s), doubling to maxBackoffMs (default 2 min). */
export function startInBackground(start: () => Promise<Stoppable>, options: BackgroundStartOptions = {}): BackgroundStarted {
  const controller = new AbortController();
  const sleep = options.sleep ?? sleepUnlessAborted;
  const maxBackoff = options.maxBackoffMs ?? 120_000;
  let running: Stoppable | null = null;
  const ready = (async (): Promise<boolean> => {
    let backoff = options.minBackoffMs ?? 5_000;
    while (!controller.signal.aborted) {
      try {
        const started = await start();
        if (controller.signal.aborted) {
          await started.stop();
          return false;
        }
        running = started;
        return true;
      } catch (err) {
        options.onError?.(err, backoff);
        await sleep(backoff, controller.signal);
        backoff = Math.min(backoff * 2, maxBackoff);
      }
    }
    return false;
  })();
  return {
    ready,
    async stop() {
      controller.abort();
      await ready;
      await running?.stop();
    },
  };
}
