import { useCallback, useEffect, useRef, useState } from "react";
import { describeProblem, type Problem } from "./api";

/** Loads a value (or a list) when the page opens and again on `reload`; a failure is kept as a problem to show. */
export function useLoad<T>(load: (signal: AbortSignal) => Promise<T>, key = "") {
  const [state, setState] = useState<{ data: T | null; error: Problem | null }>({ data: null, error: null });
  const loader = useRef(load);
  loader.current = load;
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setState((s) => ({ data: s.data, error: null }));
    loader.current(controller.signal).then(
      (data) => setState({ data, error: null }),
      (failure: unknown) => {
        if (!controller.signal.aborted) setState({ data: null, error: describeProblem(failure) });
      },
    );
    return () => controller.abort();
  }, [attempt, key]);
  return { ...state, loading: state.data === null && state.error === null, reload: useCallback(() => setAttempt((n) => n + 1), []) };
}
