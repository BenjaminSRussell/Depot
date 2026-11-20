import type { LogFn } from "../logging/pseudonymize.js";
import { neutraliseLinks, unsafeTail } from "../domain/exfilLinks.js";
import { mayViewValue, P3_CATEGORIES, PSEUDONYM_RE, scanPii, type Pseudonymizer } from "../domain/redaction.js";
import { GUARD_POLICY_VERSION, highestClassOf, type Analyzer, type DataClass, type GuardContext, type GuardDecision, type GuardResult, type OutputGuardReason } from "./egressGuard.js";

export const OUTPUT_GUARD_TOOL = "output.guard";
export const OUTPUT_GUARD_CODE = "AGT-0220";
export const OUTPUT_GUARD_MESSAGE = "This answer could not be shown. Try again or ask your administrator.";
export const OUTPUT_GUARD_DEADLINE_MS = 2000;

const CITE_RE = /\[\[cite:([^\]]*)\]\]/g;
const SENTENCE_END = /[.!?]\s|\n/g;
const MIN_LEAK_LENGTH = 4;
export const UNVERIFIED_MARK = "[unverified]";
export const UNVERIFIED_LIMIT = 0.2;

export function stripUnreadableCitations(text: string, readable: ReadonlySet<string>): string {
  return text.replace(CITE_RE, (marker, id: string) => (readable.has(id) ? marker : UNVERIFIED_MARK));
}

export interface OutputInput {
  tenantId: string;
  text: string;
  context: GuardContext;
  pseudo: Pseudonymizer;
  duties: ReadonlySet<string>;
  declaredClass: DataClass;
  partial?: boolean;
  attachments?: string;
}

export type OutputVerdict = { ok: true; dataClass: DataClass; text: string; linksRemoved: number } | { ok: false; reason: OutputGuardReason; dataClass: DataClass };

export interface OutputGuard {
  check(input: OutputInput): Promise<OutputVerdict>;
  neutralise(text: string): string;
}

export async function checkRunOutput(
  guard: OutputGuard,
  input: { tenantId: string; prompt: string; response: { text: string; proposal?: { args: unknown } | undefined }; pseudo: Pseudonymizer; duties: ReadonlySet<string> },
): Promise<OutputVerdict> {
  return guard.check({
    tenantId: input.tenantId,
    text: input.response.text,
    attachments: JSON.stringify(input.response.proposal?.args ?? {}),
    context: { inputTokens: new Set(input.prompt.match(PSEUDONYM_RE)), inputRecordIds: new Set(), highestClass: "P0" },
    pseudo: input.pseudo,
    duties: input.duties,
    declaredClass: "P0",
  });
}

export const outputBlockedResult = (runId: string, verdict: { reason: OutputGuardReason; dataClass: DataClass }): GuardResult => ({
  callId: `${runId}:output`,
  highestClass: verdict.dataClass,
  routeId: OUTPUT_GUARD_TOOL,
  policyVersion: GUARD_POLICY_VERSION,
  counts: {},
  excluded: {},
  decision: "OUTPUT_BLOCKED",
  errorCode: verdict.reason,
});

class GuardTimeout extends Error {}

function withDeadline<T>(work: Promise<T>, ms: number): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  const late = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new GuardTimeout()), ms);
  });
  return Promise.race([work, late]).finally(() => clearTimeout(timer));
}

function appearsAsValue(text: string, value: string): boolean {
  const word = /[A-Za-z0-9]/;
  for (let at = text.indexOf(value); at !== -1; at = text.indexOf(value, at + 1)) {
    if (!word.test(text[at - 1] ?? "") && !word.test(text[at + value.length] ?? "")) return true;
  }
  return false;
}

export function createOutputGuard(deps: { analyzer?: Analyzer; log?: LogFn; observe?: (seconds: number, decision: GuardDecision) => void; deadlineMs?: number } = {}): OutputGuard {
  const analyzer = deps.analyzer ?? { scan: scanPii };
  const deadlineMs = deps.deadlineMs ?? OUTPUT_GUARD_DEADLINE_MS;

  async function violation(input: OutputInput, answer: string): Promise<OutputGuardReason | undefined> {
    const { context, pseudo, duties } = input;
    const text = input.attachments === undefined ? answer : `${answer}\n${input.attachments}`;
    if (input.attachments !== undefined && neutraliseLinks(input.attachments).removed > 0) return "LINK_IN_ARGS";
    if ([...text.matchAll(PSEUDONYM_RE)].some((m) => !context.inputTokens.has(m[0]))) return "TOKEN_NOT_IN_INPUT";
    if (input.partial !== true) {
      const cited = [...text.matchAll(CITE_RE)].map((m) => m[1] ?? "");
      const unverified = cited.filter((id) => !context.inputRecordIds.has(id)).length;
      if (unverified > 0 && unverified / cited.length > UNVERIFIED_LIMIT) return "RECORD_NOT_IN_INPUT";
    }
    const entries = pseudo.snapshot().entries;
    const shown = pseudo.restoreFor(text, duties);
    const hidden = entries.filter((e) => !mayViewValue(e.category, duties) && e.value.length >= MIN_LEAK_LENGTH).map((e) => e.value);
    if (hidden.some((v) => appearsAsValue(text.replace(PSEUDONYM_RE, ""), v) || appearsAsValue(shown, v))) return "DUTY_LEAK";
    const allowed = new Set(entries.filter((e) => mayViewValue(e.category, duties)).map((e) => e.value));
    const findings = await analyzer.scan(text);
    if (findings.some((f) => P3_CATEGORIES.includes(f.category) && !allowed.has(f.value))) return "RAW_IDENTIFIER";
    return undefined;
  }

  return {
    neutralise: (text) => neutraliseLinks(text).text,
    async check(input) {
      const started = performance.now();
      const dataClass = highestClassOf([input.declaredClass, input.context.highestClass]);
      let reason: OutputGuardReason | undefined;
      let error: string | undefined;
      let neutral = { text: "", removed: 0 };
      try {
        neutral = neutraliseLinks(input.text);
        reason = await withDeadline(violation(input, neutral.text), deadlineMs);
      } catch (err) {
        reason = err instanceof GuardTimeout ? "GUARD_TIMEOUT" : "GUARD_ERROR";
        error = err instanceof Error ? err.name : "unknown";
      }
      if (reason !== undefined) {
        deps.log?.("warn", "output guard withheld the answer", {
          tenantId: input.tenantId,
          reason,
          inputTokens: input.context.inputTokens.size,
          inputRecordIds: input.context.inputRecordIds.size,
          ...(error !== undefined ? { error } : {}),
        });
      }
      deps.observe?.((performance.now() - started) / 1000, reason === undefined ? "ALLOWED" : "OUTPUT_BLOCKED");
      if (reason !== undefined) return { ok: false, reason, dataClass };
      if (neutral.removed > 0) deps.log?.("warn", "output guard removed links", { tenantId: input.tenantId, removed: neutral.removed });
      return { ok: true, dataClass, text: neutral.text, linksRemoved: neutral.removed };
    },
  };
}

export function createChunkReleaser(release: (chunk: string) => Promise<boolean>) {
  let buffer = "";
  let chain: Promise<void> = Promise.resolve();
  let stopped = false;
  const send = (chunk: string): void => {
    chain = chain.then(async () => {
      if (!stopped && !(await release(chunk))) stopped = true;
    });
  };
  return {
    push(delta: string): void {
      buffer += delta;
      const safe = unsafeTail(buffer);
      let cut = 0;
      for (const m of buffer.matchAll(SENTENCE_END)) {
        const end = (m.index ?? 0) + m[0].length;
        if (end > safe) break;
        cut = end;
      }
      if (cut === 0) return;
      send(buffer.slice(0, cut));
      buffer = buffer.slice(cut);
    },
    async end(): Promise<boolean> {
      if (buffer !== "") send(buffer);
      buffer = "";
      await chain;
      return !stopped;
    },
  };
}
