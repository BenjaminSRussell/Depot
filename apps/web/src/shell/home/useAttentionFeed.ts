import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { listPendingApprovals, type PendingApproval } from "../../modules/copilot/api/proposals";
import { listMessages, type Message } from "../../modules/planning/api/client";
import type { ApiError } from "../api/http";
import { copy } from "../copy";
import { holdsDuty } from "../dutyGate";
import { sourceList } from "../inbox/inbox";
import { useSharedInbox } from "../inbox/InboxProvider";
import { prioritizeFeed } from "../personal/homeOrder";
import { usePrincipalDuties } from "../principal";
import { APPROVALS_DUTY, approvalToFeedItem, buildFeed, FEED_PLAN_CODE, inboxToFeedItem, messageToFeedItem, PLANNING_DUTY, PLANNING_FETCH_LIMIT, type FeedItem } from "./feedSources";

type SourceState<T> = { status: "off" | "loading" | "ready" | "error"; rows: T[] };

function useSource<T>(enabled: boolean, attempt: number, load: (signal: AbortSignal) => Promise<T[]>): SourceState<T> {
  const [state, setState] = useState<SourceState<T>>({ status: enabled ? "loading" : "off", rows: [] });
  const latest = useRef(0);
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    const token = ++latest.current;
    if (!enabled) {
      setState({ status: "off", rows: [] });
      return;
    }
    const controller = new AbortController();
    setState((prev) => ({ ...prev, status: "loading" }));
    loadRef.current(controller.signal).then(
      (rows) => {
        if (token === latest.current) setState({ status: "ready", rows });
      },
      () => {
        if (token === latest.current && !controller.signal.aborted) setState({ status: "error", rows: [] });
      },
    );
    return () => controller.abort();
  }, [enabled, attempt]);

  return state;
}

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
  const duties = usePrincipalDuties();
  const planningEnabled = holdsDuty(duties, PLANNING_DUTY);
  const approvalsEnabled = holdsDuty(duties, APPROVALS_DUTY);
  const [attempt, setAttempt] = useState(0);
  const planning = useSource<Message>(planningEnabled, attempt, (signal) => listMessages(FEED_PLAN_CODE, undefined, { signal }, "priority", "open", PLANNING_FETCH_LIMIT));
  const approvals = useSource<PendingApproval>(approvalsEnabled, attempt, (signal) => listPendingApprovals(signal));

  const inboxReload = inbox?.reload;
  const reload = useCallback(() => {
    inboxReload?.();
    setAttempt((n) => n + 1);
  }, [inboxReload]);

  const inboxStatus = inbox?.status ?? "off";
  const inboxFailed = inboxStatus === "error";
  const inboxItems = inbox?.items;
  const messages = planning.rows;
  const pendingApprovals = approvals.rows;
  const items = useMemo(
    () =>
      prioritizeFeed(
        buildFeed([
          ...(inboxItems ?? []).map((i) => inboxToFeedItem(i, now)),
          ...messages.map((m) => messageToFeedItem(m, now)),
          ...pendingApprovals.map((a) => approvalToFeedItem(a, now)),
        ]),
        duties,
      ),
    [inboxItems, messages, pendingApprovals, duties, now.toDateString()],
  );

  const unavailable = [
    ...(inboxFailed ? [copy.attention.sourceInbox] : []),
    ...(inbox && inbox.unavailable.length > 0 ? [sourceList(inbox.unavailable)] : []),
    ...(planning.status === "error" ? [copy.attention.sourcePlanning] : []),
    ...(approvals.status === "error" ? [copy.attention.sourceApprovals] : []),
  ];

  return {
    enabled: inbox !== null || planningEnabled || approvalsEnabled,
    loading: inboxStatus === "loading" || planning.status === "loading" || approvals.status === "loading",
    items,
    unavailable,
    hasMore: inbox !== null && (inbox.hasMore || inbox.truncated.length > 0),
    needsEntity: inbox?.needsEntity ?? false,
    soleInboxProblem: inboxFailed && !planningEnabled && !approvalsEnabled ? (inbox?.problem ?? null) : null,
    reload,
  };
}
