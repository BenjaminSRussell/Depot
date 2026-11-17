// Starts a long-lived client (the Kafka consumer) in the background and retries a failed start with backoff, so a
// dependency that is not there yet never stops the process that hosts it (D34, B-05b). A client that fails for good after
// it started (kafkajs's consumer crash with restart=false) goes back into the same retry loop.

export interface Stoppable {
  stop(): Promise<void>;
}

export interface Started extends Stoppable {
  /**
   * Resolves, never rejects, with the error once the client has failed for good after starting (kafkajs's CRASH event
   * with restart=false); stays pending while it runs. The loop then stops the client and starts it again after a backoff.
   */
  failed?: Promise<unknown>;
}

export interface BackgroundStartOptions {
  /**
   * Called after each failed start, or failure after a start, with the error, the wait before the next attempt and the
   * number of consecutive failures (1 for the first). Not called once stop() has been called.
   */
  onError?: (err: unknown, retryInMs: number, failures: number) => void;
  /** Called with true when the client has started and with false when it stops running (a failure, or stop()). */
  onRunning?: (running: boolean) => void;
  /** Waits ms, or less when the signal aborts. */
  sleep?: (ms: number, signal: AbortSignal) => Promise<void>;
  /** The clock, in ms. */
  now?: () => number;
  minBackoffMs?: number;
  maxBackoffMs?: number;
}

export interface BackgroundStarted extends Stoppable {
  /** Resolves true once the first start succeeded, false when stop() ended the retries first. */
  ready: Promise<boolean>;
}

/** Waits ms; returns at once when the signal has aborted or aborts. */
export const sleepUnlessAborted = (ms: number, signal: AbortSignal): Promise<void> =>
  new Promise((resolve) => {
    if (signal.aborted) {
      resolve();
      return;
    }
    const timer = setTimeout(done, ms);
    function done(): void {
      clearTimeout(timer);
      signal.removeEventListener("abort", done);
      resolve();
    }
    signal.addEventListener("abort", done, { once: true });
  });

/**
 * Calls start until it succeeds, and again whenever the started client fails: backoff from minBackoffMs (default 5 s),
 * doubling to maxBackoffMs (default 2 min). A client that ran for at least maxBackoffMs before failing resets the backoff
 * and the failure count.
 */
export function startInBackground(start: () => Promise<Started>, options: BackgroundStartOptions = {}): BackgroundStarted {
  const controller = new AbortController();
  const { signal } = controller;
  const sleep = options.sleep ?? sleepUnlessAborted;
  const now = options.now ?? Date.now;
  const minBackoff = options.minBackoffMs ?? 5_000;
  const maxBackoff = options.maxBackoffMs ?? 120_000;
  let running: Started | null = null;
  let markReady: (started: boolean) => void = () => undefined;
  const ready = new Promise<boolean>((resolve) => (markReady = resolve));
  const loop = (async (): Promise<void> => {
    let backoff = minBackoff;
    let failures = 0;
    while (!signal.aborted) {
      let err: unknown;
      try {
        const started = await start();
        if (signal.aborted) {
          await started.stop();
          break;
        }
        running = started;
        markReady(true);
        options.onRunning?.(true);
        const startedAt = now();
        let onAbort = (): void => undefined;
        const aborted = new Promise<void>((resolve) => {
          onAbort = resolve;
          signal.addEventListener("abort", onAbort, { once: true });
        });
        try {
          err = await Promise.race([started.failed ?? new Promise<never>(() => undefined), aborted]);
        } finally {
          signal.removeEventListener("abort", onAbort);
        }
        if (signal.aborted) {
          break; // stop() stops the running client.
        }
        running = null;
        options.onRunning?.(false);
        await started.stop().catch(() => undefined);
        if (now() - startedAt >= maxBackoff) {
          backoff = minBackoff;
          failures = 0;
        }
      } catch (e) {
        if (signal.aborted) {
          break; // A start that fails during shutdown is not retried, logged or waited for.
        }
        err = e;
      }
      failures += 1;
      options.onError?.(err, backoff, failures);
      await sleep(backoff, signal);
      backoff = Math.min(backoff * 2, maxBackoff);
    }
    markReady(false);
  })();
  return {
    ready,
    async stop() {
      controller.abort();
      await loop;
      const client = running;
      running = null;
      if (client) {
        options.onRunning?.(false);
        await client.stop();
      }
    },
  };
}
