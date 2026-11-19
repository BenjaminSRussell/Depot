import type { LogFn } from "../logging/pseudonymize.js";
import { mayViewValue, P3_CATEGORIES, PSEUDONYM_RE, scanPii, type Pseudonymizer } from "../domain/redaction.js";
import { highestClassOf, type Analyzer, type DataClass, type GuardContext, type GuardDecision, type OutputGuardReason } from "./egressGuard.js";

export const OUTPUT_GUARD_TOOL = "output.guard";
export const OUTPUT_GUARD_CODE = "AGT-0220";
export const OUTPUT_GUARD_MESSAGE = "This answer could not be shown. Try again or ask your administrator.";
export const OUTPUT_GUARD_DEADLINE_MS = 2000;

const CITE_RE = /\[\[cite:([^\]]*)\]\]/g;
const SENTENCE_END = /[.!?]\s|\n/g;
const MIN_LEAK_LENGTH = 4;

export interface OutputInput {
  tenantId: string;
  text: string;
  context: GuardContext;
  pseudo: Pseudonymizer;
  duties: ReadonlySet<string>;
  declaredClass: DataClass;
}

export type OutputVerdict = { ok: true; dataClass: DataClass } | { ok: false; reason: OutputGuardReason; dataClass: DataClass };

export interface OutputGuard {
  check(input: OutputInput): Promise<OutputVerdict>;
}

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

  async function violation(input: OutputInput): Promise<OutputGuardReason | undefined> {
    const { text, context, pseudo, duties } = input;
    if ([...text.matchAll(PSEUDONYM_RE)].some((m) => !context.inputTokens.has(m[0]))) return "TOKEN_NOT_IN_INPUT";
    if ([...text.matchAll(CITE_RE)].some((m) => !context.inputRecordIds.has(m[1] ?? ""))) return "RECORD_NOT_IN_INPUT";
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
    async check(input) {
      const started = performance.now();
      const dataClass = highestClassOf([input.declaredClass, input.context.highestClass]);
      let reason: OutputGuardReason | undefined;
      let error: string | undefined;
      try {
        reason = await withDeadline(violation(input), deadlineMs);
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
      return reason === undefined ? { ok: true, dataClass } : { ok: false, reason, dataClass };
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
      let cut = 0;
      for (const m of buffer.matchAll(SENTENCE_END)) cut = (m.index ?? 0) + m[0].length;
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
