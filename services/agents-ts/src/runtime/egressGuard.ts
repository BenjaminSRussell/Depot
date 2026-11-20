import type { LogFn } from "../logging/pseudonymize.js";
import { toolAnnotation, type ToolClass } from "../agents/catalog.js";
import type { AiPolicyGuard } from "./aiPolicyGuard.js";
import { AI_POLICY_MODULES } from "../domain/aiDataPolicy.js";
import { PSEUDONYM_RE, scanPii, type PiiCategory, type PiiFinding, type Pseudonymizer } from "../domain/redaction.js";

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

function describeError(err: unknown): Record<string, string> {
  if (!(err instanceof Error)) return { error: "unknown" };
  const code = (err as { code?: unknown }).code;
  const detail: Record<string, string> = { error: err.name };
  if (typeof code === "string" && ERROR_CODE_RE.test(code)) detail.errorDetailCode = code;
  try {
    const scrubbed = scanPii(err.message)
      .filter((f) => f.value.length > 0)
      .reduce((text, f) => text.split(f.value).join("[REDACTED]"), err.message)
      .replace(/\d{4,}/g, "[N]")
      .slice(0, ERROR_MESSAGE_MAX);
    if (scrubbed.length > 0) detail.errorMessage = scrubbed;
  } catch {
    return detail;
  }
  return detail;
}

function withDeadline<T>(work: Promise<T>, ms: number): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  const late = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new GuardFailure("ANALYZER_TIMEOUT")), ms);
  });
  return Promise.race([work, late]).finally(() => clearTimeout(timer));
}

const SCAN_MEMO_MAX = 512;
const scanMemo = new WeakMap<Pseudonymizer, Map<string, PiiCategory[]>>();

export function createEgressGuard(
  deps: { analyzer?: Analyzer; policy: AiPolicyGuard; log?: LogFn; observe?: (seconds: number, decision: GuardDecision) => void; deadlineMs?: number },
): EgressGuard {
  const analyzer = deps.analyzer ?? { scan: scanPii };
  const deadlineMs = deps.deadlineMs ?? ANALYZER_DEADLINE_MS;
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
        const memo = scanMemo.get(input.pseudo) ?? new Map<string, PiiCategory[]>();
        scanMemo.set(input.pseudo, memo);
        const categoriesOf = async (segments: readonly string[]): Promise<PiiCategory[][]> => {
          const fresh = [...new Set(segments)].filter((t) => !memo.has(t));
          const scanned = new Map<string, PiiCategory[]>();
          if (fresh.length > 0) {
            const results = await scan(fresh);
            fresh.forEach((t, i) => {
              const findings = results[i];
              if (!Array.isArray(findings)) throw new GuardFailure("ANALYZER_ERROR", "analyzer returned a non-array");
              scanned.set(t, findings.map((f) => f.category));
            });
          }
          const out = segments.map((t) => memo.get(t) ?? scanned.get(t) ?? []);
          for (const [t, categories] of scanned) {
            if (memo.size >= SCAN_MEMO_MAX) memo.delete(memo.keys().next().value as string);
            memo.set(t, categories);
          }
          return out;
        };
        const perText = await categoriesOf(texts);
        const found = perText.flat();
        for (const category of found) result.counts[category] = (result.counts[category] ?? 0) + 1;
        result.highestClass = highestClassOf([input.declared ?? "P0", ...found.map((c) => CATEGORY_CLASS[c])]);
        const redacted = texts.map((t) => input.pseudo.redact(t));
        const user = redacted.join("\n\n");
        const residual = await categoriesOf([input.system, ...redacted]);
        if (residual.some((categories) => categories.length > 0)) throw new GuardFailure("RESIDUAL_PII");
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
