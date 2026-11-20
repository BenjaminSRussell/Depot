import { createHash } from "node:crypto";
import type { LogFn } from "../logging/pseudonymize.js";
import { toolAnnotation, type ToolClass } from "../agents/catalog.js";
import type { AiPolicyGuard } from "./aiPolicyGuard.js";
import { AI_POLICY_MODULES } from "../domain/aiDataPolicy.js";
import { MAX_LOOKBEHIND_CHARS, PSEUDONYM_RE, scanPii, type PiiCategory, type PiiFinding, type Pseudonymizer } from "../domain/redaction.js";

export const EGRESS_GUARD_TOOL = "egress.guard";
export const GUARD_POLICY_VERSION = 1;
export const ANALYZER_DEADLINE_MS = 2000;
export const GUARD_REFUSAL_MESSAGE =
  "I could not check this request for sensitive data, so I did not send it to the AI model. Your ERP work is not affected; please try again later.";

export type DataClass = "P0" | "P1" | "P2" | "P3";
export type GuardDecision = "ALLOWED" | "REDACTED" | "BLOCKED" | "BLOCKED_BOUNDARY" | "OUTPUT_BLOCKED";
export type GuardErrorCode = "ANALYZER_ERROR" | "ANALYZER_TIMEOUT" | "RESIDUAL_PII" | "NO_APPROVED_ROUTE" | "POLICY_UNREADABLE";
export type OutputGuardReason = "TOKEN_NOT_IN_INPUT" | "RECORD_NOT_IN_INPUT" | "DUTY_LEAK" | "RAW_IDENTIFIER" | "LINK_IN_ARGS" | "GUARD_TIMEOUT" | "GUARD_ERROR";

export interface GuardResult {
  callId: string;
  highestClass: DataClass;
  routeId: string;
  policyVersion: number;
  policyCell?: "tenant" | "default";
  counts: Record<string, number>;
  excluded: Record<string, number>;
  decision: GuardDecision;
  errorCode?: GuardErrorCode | OutputGuardReason;
}

export interface Analyzer {
  scan(text: string): PiiFinding[] | Promise<PiiFinding[]>;
}

export interface EgressInput {
  tenantId: string;
  callId: string;
  routeId: string;
  pseudo: Pseudonymizer;
  system: string;
  typed: string;
  results: readonly string[];
  recordIds?: ReadonlySet<string>;
  excluded?: Record<string, number>;
  policyCell?: "tenant" | "default";
  policyVersion?: number;
  declared?: DataClass;
}

export interface GuardContext {
  inputTokens: Set<string>;
  inputRecordIds: Set<string>;
  highestClass: DataClass;
}

export interface EgressVerdict {
  user: string;
  context: GuardContext;
  result: GuardResult;
  typedReplaced: number;
}

export interface Admission {
  pass: boolean;
  class: DataClass;
  policyCell: "tenant" | "default";
  policyVersion: number;
  failure?: GuardErrorCode;
  reason?: "TENANT_POLICY";
}

export interface EgressGuard {
  admit(input: { tenantId: string; tool: string; text: string }): Promise<Admission>;
  inspect(input: EgressInput): Promise<EgressVerdict>;
}

const CLASS_ORDER: readonly DataClass[] = ["P0", "P1", "P2", "P3"];

const CATEGORY_CLASS: Readonly<Record<PiiCategory, DataClass>> = {
  EMAIL: "P2",
  IBAN: "P3",
  CARD: "P3",
  SSN: "P3",
  TAXID: "P3",
  BANKACCT: "P3",
  PAY: "P3",
};

export const highestClassOf = (classes: readonly DataClass[]): DataClass =>
  classes.reduce<DataClass>((top, c) => (CLASS_ORDER.indexOf(c) > CLASS_ORDER.indexOf(top) ? c : top), "P0");

class GuardFailure extends Error {
  constructor(readonly code: GuardErrorCode, detail: string = code) {
    super(detail);
    this.name = "GuardFailure";
  }
}

const ERROR_CODE_RE = /^[A-Z][A-Z0-9_-]{1,31}$/;
const ERROR_MESSAGE_MAX = 160;
const ERROR_SCAN_MAX = 4096;

// Deliberately the local scanPii, not the injected analyzer: error text must be scrubbed even when the analyzer is the thing that failed.
function describeError(err: unknown): Record<string, string> {
  if (!(err instanceof Error)) return { error: "unknown" };
  const code = (err as { code?: unknown }).code;
  const detail: Record<string, string> = { error: err.name };
  if (typeof code === "string" && ERROR_CODE_RE.test(code)) detail.errorDetailCode = code;
  if (!(err instanceof GuardFailure)) return detail;
  const bounded = err.message.slice(0, ERROR_SCAN_MAX);
  const scrubbed = scanPii(bounded)
    .filter((f) => f.value.length > 0)
    .sort((a, b) => b.value.length - a.value.length)
    .reduce((text, f) => text.split(f.value).join("[REDACTED]"), bounded)
    .replace(/\d{4,}/g, "[N]")
    .slice(0, ERROR_MESSAGE_MAX);
  if (scrubbed.length > 0) detail.errorMessage = scrubbed;
  return detail;
}

function withDeadline<T>(work: Promise<T>, ms: number): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  const late = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new GuardFailure("ANALYZER_TIMEOUT")), ms);
  });
  return Promise.race([work, late]).finally(() => clearTimeout(timer));
}

export const SEAM_WINDOW_CHARS = 4 * MAX_LOOKBEHIND_CHARS;
const SEPARATOR = "\n\n";

function seamWindows(parts: readonly string[]): string[] {
  const joined = parts.join(SEPARATOR);
  const ranges: Array<[number, number]> = [];
  let offset = 0;
  for (const part of parts.slice(0, -1)) {
    offset += part.length;
    const lead = Math.max(0, offset - SEAM_WINDOW_CHARS);
    const leadCut = joined.slice(lead, offset - MAX_LOOKBEHIND_CHARS).search(/\W/);
    let start = lead;
    if (lead > 0 && leadCut >= 0) start = lead + leadCut + 1;
    else while (start > 0 && /\w/.test(joined.charAt(start - 1))) start--;
    const tailStart = offset + SEPARATOR.length + MAX_LOOKBEHIND_CHARS;
    const trail = Math.min(joined.length, offset + SEPARATOR.length + SEAM_WINDOW_CHARS);
    const trailCut = joined.slice(tailStart, trail).search(/\W\w*$/);
    let end = trail;
    if (trail < joined.length && trailCut >= 0) end = tailStart + trailCut;
    else while (end < joined.length && /\w/.test(joined.charAt(end))) end++;
    ranges.push([start, end]);
    offset += SEPARATOR.length;
  }
  const merged: Array<[number, number]> = [];
  for (const [start, end] of ranges.sort((x, y) => x[0] - y[0])) {
    const last = merged[merged.length - 1];
    if (last !== undefined && start < last[1]) last[1] = Math.max(last[1], end);
    else merged.push([start, end]);
  }
  return merged.map(([start, end]) => joined.slice(start, end));
}

const SCAN_MEMO_MAX = 512;
const SCAN_MEMO_MAX_CHARS = 64 * 1024;

type ScanMemo = Map<string, PiiCategory[]>;

const memoKey = (text: string): string | undefined =>
  text.length > SCAN_MEMO_MAX_CHARS ? undefined : createHash("sha256").update(text).digest("hex");

async function memoisedCategories(
  memo: ScanMemo,
  scan: (texts: readonly string[]) => Promise<PiiFinding[][]>,
  segments: readonly string[],
): Promise<PiiCategory[][]> {
  const keys = new Map([...new Set(segments)].map((t) => [t, memoKey(t)] as const));
  const known = new Map<string, PiiCategory[]>();
  for (const [t, key] of keys) {
    const held = key === undefined ? undefined : memo.get(key);
    if (held !== undefined) known.set(t, held);
  }
  const fresh = [...keys.keys()].filter((t) => !known.has(t));
  const scanned = new Map<string, PiiCategory[]>();
  if (fresh.length > 0) {
    const results = await scan(fresh);
    fresh.forEach((t, i) => {
      const findings = results[i];
      if (!Array.isArray(findings)) throw new GuardFailure("ANALYZER_ERROR", "analyzer returned a non-array");
      scanned.set(t, findings.map((f) => f.category));
    });
  }
  const out = segments.map((t) => scanned.get(t) ?? known.get(t) ?? []);
  for (const [t, held] of known) {
    const key = keys.get(t);
    if (key === undefined) continue;
    memo.delete(key);
    memo.set(key, held);
  }
  for (const [t, categories] of scanned) {
    const key = keys.get(t);
    if (key === undefined) continue;
    if (memo.size >= SCAN_MEMO_MAX) {
      const oldest = memo.keys().next();
      if (!oldest.done) memo.delete(oldest.value);
    }
    memo.set(key, categories);
  }
  return out;
}

export function createEgressGuard(
  deps: { analyzer?: Analyzer; policy: AiPolicyGuard; log?: LogFn; observe?: (seconds: number, decision: GuardDecision) => void; deadlineMs?: number },
): EgressGuard {
  const analyzer = deps.analyzer ?? { scan: scanPii };
  const deadlineMs = deps.deadlineMs ?? ANALYZER_DEADLINE_MS;
  const memos = new WeakMap<Pseudonymizer, ScanMemo>();
  const scan = (texts: readonly string[]): Promise<PiiFinding[][]> => withDeadline(Promise.all(texts.map(async (t) => analyzer.scan(t))), deadlineMs);

  return {
    async admit({ tenantId, tool, text }) {
      const { module, declared } = toolAnnotation(tool);
      const policyCell = (AI_POLICY_MODULES as readonly string[]).includes(module) ? "tenant" : "default";
      const base = { policyCell, policyVersion: GUARD_POLICY_VERSION } as const;
      try {
        const [found = []] = await scan([text]);
        const cls = highestClassOf([declared satisfies ToolClass, ...found.map((f) => CATEGORY_CLASS[f.category])]);
        if (policyCell === "default") return { ...base, pass: true, class: cls };
        const [p0, p1] = await Promise.all([deps.policy.check({ tenantId, module, dataClass: "P0" }), deps.policy.check({ tenantId, module, dataClass: "P1" })]);
        if (!p0.allowed && !p1.allowed) return { ...base, pass: false, class: cls, policyVersion: p1.policyVersion, reason: "TENANT_POLICY" };
        // P2 and P3 values are pseudonymized later in inspect(), which throws RESIDUAL_PII on any leftover.
        if (cls === "P2" || cls === "P3") return { ...base, pass: true, class: cls, policyVersion: p1.policyVersion };
        const decision = cls === "P0" ? p0 : p1;
        return { ...base, pass: decision.allowed, class: cls, policyVersion: decision.policyVersion };
      } catch (err) {
        const failure = err instanceof GuardFailure ? err.code : err instanceof Error && err.name === "AgentError" ? "POLICY_UNREADABLE" : "ANALYZER_ERROR";
        deps.log?.("warn", "egress guard could not admit a tool result", { tenantId, tool, errorCode: failure, ...describeError(err) });
        return { ...base, pass: false, class: "P3", failure };
      }
    },
    async inspect(input) {
      const started = performance.now();
      const result: GuardResult = {
        callId: input.callId,
        highestClass: "P0",
        routeId: input.routeId,
        policyVersion: input.policyVersion ?? GUARD_POLICY_VERSION,
        ...(input.policyCell !== undefined ? { policyCell: input.policyCell } : {}),
        counts: {},
        excluded: { ...input.excluded },
        decision: "BLOCKED",
      };
      try {
        const texts = [input.typed, ...input.results];
        const memo = memos.get(input.pseudo) ?? new Map<string, PiiCategory[]>();
        memos.set(input.pseudo, memo);
        const categoriesOf = (segments: readonly string[]) => memoisedCategories(memo, scan, segments);
        const perText = await categoriesOf(texts);
        const found = perText.flat();
        for (const category of found) result.counts[category] = (result.counts[category] ?? 0) + 1;
        result.highestClass = highestClassOf([input.declared ?? "P0", ...found.map((c) => CATEGORY_CLASS[c])]);
        const redacted = texts.map((t) => input.pseudo.redact(t));
        const user = redacted.join(SEPARATOR);
        const parts = [input.system, ...redacted];
        const residual = await categoriesOf(parts);
        if (residual.some((categories) => categories.length > 0)) throw new GuardFailure("RESIDUAL_PII");
        const seams = await scan(seamWindows(parts));
        if (seams.some((findings) => !Array.isArray(findings))) throw new GuardFailure("ANALYZER_ERROR", "analyzer returned a non-array");
        if (seams.some((findings) => findings.length > 0)) throw new GuardFailure("RESIDUAL_PII");
        result.decision = found.length > 0 ? "REDACTED" : "ALLOWED";
        return { user, context: { inputTokens: new Set(user.match(PSEUDONYM_RE)), inputRecordIds: new Set(input.recordIds), highestClass: result.highestClass }, result, typedReplaced: (perText[0] ?? []).filter((c) => c !== "EMAIL").length };
      } catch (err) {
        result.errorCode = err instanceof GuardFailure ? err.code : "ANALYZER_ERROR";
        deps.log?.("warn", "egress guard blocked the provider call", {
          tenantId: input.tenantId,
          callId: input.callId,
          errorCode: result.errorCode,
          ...describeError(err),
          counts: result.counts,
        });
        return { user: "", context: { inputTokens: new Set(), inputRecordIds: new Set(), highestClass: result.highestClass }, result, typedReplaced: 0 };
      } finally {
        deps.observe?.((performance.now() - started) / 1000, result.decision);
      }
    },
  };
}
