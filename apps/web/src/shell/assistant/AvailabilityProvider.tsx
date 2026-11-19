import { createContext, useCallback, useContext, useEffect, useRef, useState } from "react";
import { copy } from "../copy";
import { holdsDuty } from "../dutyGate";
import { usePrincipalDuties } from "../principal";
import type { FetchLike } from "../api/http";
import { Alert } from "../ui";
import {
  applyRefusal,
  AVAILABILITY_POLL_MS,
  CHECKING,
  deriveAvailability,
  getAvailability,
  NO_ACCESS,
  type AvailabilityState,
} from "./availability";

const ADMIN_DUTY = "agents.admin";

export interface AiAvailability {
  state: AvailabilityState;
  reportRefusal: (code: string) => void;
  reportFailure: () => void;
}

const AvailabilityContext = createContext<AiAvailability>({ state: NO_ACCESS, reportRefusal: () => undefined, reportFailure: () => undefined });

export const AvailabilityProvider = AvailabilityContext.Provider;

export function useAiAvailability(): AiAvailability {
  return useContext(AvailabilityContext);
}

export function useAvailabilityController(enabled: boolean, fetchFn?: FetchLike): AiAvailability {
  const [state, setState] = useState<AvailabilityState>(enabled ? CHECKING : NO_ACCESS);
  const reads = useRef(new Set<AbortController>());
  const issued = useRef(0);
  const applied = useRef(0);

  const replace = useCallback((next: AvailabilityState) => {
    setState((prev) => (JSON.stringify(prev) === JSON.stringify(next) ? prev : next));
  }, []);

  const refresh = useCallback(async () => {
    const id = ++issued.current;
    const controller = new AbortController();
    reads.current.add(controller);
    const read = await getAvailability(controller.signal, fetchFn);
    reads.current.delete(controller);
    if (controller.signal.aborted || id < applied.current) return;
    applied.current = id;
    replace(deriveAvailability(read));
  }, [fetchFn, replace]);

  useEffect(() => {
    if (!enabled) {
      replace(NO_ACCESS);
      return;
    }
    const run = () => {
      if (document.visibilityState === "visible") void refresh();
    };
    run();
    const timer = setInterval(run, AVAILABILITY_POLL_MS);
    window.addEventListener("focus", run);
    document.addEventListener("visibilitychange", run);
    return () => {
      clearInterval(timer);
      window.removeEventListener("focus", run);
      document.removeEventListener("visibilitychange", run);
      reads.current.forEach((read) => read.abort());
    };
  }, [enabled, refresh, replace]);

  const reportRefusal = useCallback(
    (code: string) => {
      setState((prev) => applyRefusal(prev, code));
      void refresh();
    },
    [refresh],
  );
  const reportFailure = useCallback(() => void refresh(), [refresh]);

  return { state, reportRefusal, reportFailure };
}

function noticeFor(state: AvailabilityState, admin: boolean): { title: string; body: string } | undefined {
  const text = copy.assistantStatus;
  if (state.status === "enabled") return state.budget === "SOFT" && admin ? { title: text.softTitle, body: text.softBody } : undefined;
  if (state.status === "degraded") {
    return state.reason === "budget" ? { title: text.budgetTitle, body: text.budgetBody } : { title: text.unreachableTitle, body: text.unreachableBody };
  }
  if (state.status === "unavailable" && state.reason !== "noAccess") {
    if (state.reason === "paused") return { title: text.pausedTitle, body: text.pausedBody };
    if (state.reason === "signoff") return { title: text.signoffTitle, body: text.signoffBody };
    return { title: text.offTitle, body: text.offBody };
  }
  return undefined;
}

export function AssistantNotice() {
  const { state } = useAiAvailability();
  const notice = noticeFor(state, holdsDuty(usePrincipalDuties(), ADMIN_DUTY));
  if (!notice) return null;
  return (
    <Alert tone={state.status === "enabled" ? "info" : "warning"} live="status" title={notice.title}>
      {notice.body}
    </Alert>
  );
}
