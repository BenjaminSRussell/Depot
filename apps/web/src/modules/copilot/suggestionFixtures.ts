import { runTraceSchema, type RunTrace } from "./api/runTrace";
import {
  toSuggestionViewModels,
  type SuggestionHost,
  type SuggestionViewModel,
} from "./suggestionModel";

export const UNRENDERED_MARKER = "UNRENDERED-PROMPT-MARKER";

function parsedTrace(raw: unknown): RunTrace {
  return runTraceSchema.parse(raw);
}

const hiddenFields = {
  inputJson: UNRENDERED_MARKER,
  inputRedacted: UNRENDERED_MARKER,
  outputRedacted: UNRENDERED_MARKER,
  committedEntityRef: UNRENDERED_MARKER,
};

const answerTrace = parsedTrace({
  run: { runId: "run-answer", agentId: "journal_anomaly", agentVersion: 3 },
  actions: [
    {
      seq: 1,
      toolName: "depot.ledger.journal.get",
      actionId: "action-answer",
      rationale: "The posting differs from the usual pattern for this account.",
      confidence: 0.82,
      dataUsed: [
        {
          entityType: "journal-entry",
          recordId: "je-100",
          label: "Journal entry 100",
        },
      ],
      ...hiddenFields,
    },
  ],
});

const proposalTrace = parsedTrace({
  run: { runId: "run-proposal", agentId: "ap_coding", agentVersion: 2 },
  actions: [
    {
      seq: 1,
      toolName: "depot.ap.invoice.propose_coding",
      actionId: "action-proposal",
      dataUsed: [
        { entityType: "ap-invoice", recordId: "inv-7", label: "Invoice INV-7" },
      ],
      ...hiddenFields,
    },
    {
      seq: 2,
      toolName: "depot.ap.invoice.propose_coding",
      actionId: "action-proposal-2",
    },
  ],
});

const feedItemTrace = parsedTrace({
  run: { runId: "run-feed", agentId: "mrp_triage", agentVersion: 1 },
  actions: [
    {
      seq: 1,
      toolName: "depot.production.order.flag",
      actionId: "action-feed",
      ...hiddenFields,
    },
  ],
});

const briefingItemTrace = parsedTrace({
  run: { runId: "run-briefing", agentId: "unknown_agent", agentVersion: 5 },
  actions: [
    {
      seq: 1,
      toolName: "not a tool name",
      actionId: "action-briefing",
      rationale: "Summarized from the records below.",
      confidence: 0.4,
      dataUsed: [
        {
          entityType: "mystery-record",
          recordId: "x-1",
          label: "Hidden label",
        },
      ],
    },
  ],
});

function firstSuggestion(trace: RunTrace): SuggestionViewModel {
  const [first] = toSuggestionViewModels(trace);
  if (!first) throw new Error("Fixture trace has no actions.");
  return first;
}

export const suggestionFixtures: Readonly<
  Record<SuggestionHost, { trace: RunTrace; suggestion: SuggestionViewModel }>
> = {
  answer: { trace: answerTrace, suggestion: firstSuggestion(answerTrace) },
  proposal: {
    trace: proposalTrace,
    suggestion: firstSuggestion(proposalTrace),
  },
  "feed-item": {
    trace: feedItemTrace,
    suggestion: firstSuggestion(feedItemTrace),
  },
  "briefing-item": {
    trace: briefingItemTrace,
    suggestion: firstSuggestion(briefingItemTrace),
  },
};
