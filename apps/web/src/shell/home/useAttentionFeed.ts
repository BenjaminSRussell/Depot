import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { listMessages, type Message } from "../../modules/planning/api/client";
import type { ApiError } from "../api/http";
import { copy } from "../copy";
import { holdsDuty } from "../dutyGate";
import { sourceList } from "../inbox/inbox";
import { useSharedInbox } from "../inbox/InboxProvider";
import { usePrincipalDuties } from "../principal";
import { buildFeed, FEED_PLAN_CODE, inboxToFeedItem, messageToFeedItem, PLANNING_DUTY, PLANNING_FETCH_LIMIT, type FeedItem } from "./feedSources";

type PlanningState = { status: "off" | "loading" | "ready" | "error"; messages: Message[] };

export interface AttentionFeed {
  enabled: boolean;
  loading: boolean;
  items: FeedItem[];
  unavailable: string[];
  hasMore: boolean;
  needsEntity: boolean;
  soleInboxProblem: ApiError | null;
  reload(): void;
}

export function useAttentionFeed(now: Date = new Date()): AttentionFeed {
  const inbox = useSharedInbox();
  const planningEnabled = holdsDuty(usePrincipalDuties(), PLANNING_DUTY);
  const [planning, setPlanning] = useState<PlanningState>({ status: planningEnabled ? "loading" : "off", messages: [] });
  const [attempt, setAttempt] = useState(0);
  const latest = useRef(0);

  useEffect(() => {
    const token = ++latest.current;
    if (!planningEnabled) {
      setPlanning({ status: "off", messages: [] });
      return;
    }
    const controller = new AbortController();
    setPlanning((prev) => ({ ...prev, status: "loading" }));
    listMessages(FEED_PLAN_CODE, undefined, { signal: controller.signal }, "priority", "open", PLANNING_FETCH_LIMIT).then(
      (messages) => {
        if (token === latest.current) setPlanning({ status: "ready", messages });
      },
      () => {
        if (token === latest.current && !controller.signal.aborted) setPlanning({ status: "error", messages: [] });
      },
    );
    return () => controller.abort();
  }, [planningEnabled, attempt]);

  const inboxReload = inbox?.reload;
  const reload = useCallback(() => {
    inboxReload?.();
    setAttempt((n) => n + 1);
  }, [inboxReload]);

  const inboxStatus = inbox?.status ?? "off";
  const inboxFailed = inboxStatus === "error";
  const inboxItems = inbox?.items;
  const messages = planning.messages;
  const items = useMemo(
    () => buildFeed([...(inboxItems ?? []).map((i) => inboxToFeedItem(i, now)), ...messages.map((m) => messageToFeedItem(m, now))]),
    [inboxItems, messages, now.toDateString()],
  );

  const unavailable = [
    ...(inboxFailed ? [copy.attention.sourceInbox] : []),
    ...(inbox && inbox.unavailable.length > 0 ? [sourceList(inbox.unavailable)] : []),
    ...(planning.status === "error" ? [copy.attention.sourcePlanning] : []),
  ];

  return {
    enabled: inbox !== null || planningEnabled,
    loading: inboxStatus === "loading" || planning.status === "loading",
    items,
    unavailable,
    hasMore: inbox !== null && (inbox.hasMore || inbox.truncated.length > 0),
    needsEntity: inbox?.needsEntity ?? false,
    soleInboxProblem: inboxFailed && !planningEnabled ? (inbox?.problem ?? null) : null,
    reload,
  };
}
