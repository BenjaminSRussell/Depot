import type { LogFn } from "../logging/pseudonymize.js";
import { scanPii, type PiiCategory, type PiiFinding, type Pseudonymizer } from "../domain/redaction.js";

export const EGRESS_GUARD_TOOL = "egress.guard";
export const GUARD_POLICY_VERSION = 1;
export const ANALYZER_DEADLINE_MS = 2000;
export const GUARD_REFUSAL_MESSAGE =
  "I could not check this request for sensitive data, so I did not send it to the AI model. Your ERP work is not affected; please try again later.";

export type DataClass = "P0" | "P1" | "P2" | "P3";
export type GuardDecision = "ALLOWED" | "REDACTED" | "BLOCKED";
export type GuardErrorCode = "ANALYZER_ERROR" | "ANALYZER_TIMEOUT" | "RESIDUAL_PII";

export interface GuardResult {
  callId: string;
  highestClass: DataClass;
  routeId: string;
  policyVersion: number;
  counts: Record<string, number>;
  excluded: Record<string, number>;
  decision: GuardDecision;
  errorCode?: GuardErrorCode;
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
}

export interface EgressVerdict {
  user: string;
  result: GuardResult;
  typedReplaced: number;
}

export interface EgressGuard {
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
  constructor(readonly code: GuardErrorCode) {
    super(code);
    this.name = "GuardFailure";
  }
}

function withDeadline<T>(work: Promise<T>, ms: number): Promise<T> {
  let timer: NodeJS.Timeout | undefined;
  const late = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new GuardFailure("ANALYZER_TIMEOUT")), ms);
  });
  return Promise.race([work, late]).finally(() => clearTimeout(timer));
}

export function createEgressGuard(
  deps: { analyzer?: Analyzer; log?: LogFn; observe?: (seconds: number, decision: GuardDecision) => void; deadlineMs?: number } = {},
): EgressGuard {
  const analyzer = deps.analyzer ?? { scan: scanPii };
  const deadlineMs = deps.deadlineMs ?? ANALYZER_DEADLINE_MS;
  const scan = (texts: readonly string[]): Promise<PiiFinding[][]> => withDeadline(Promise.all(texts.map(async (t) => analyzer.scan(t))), deadlineMs);

  return {
    async inspect(input) {
      const started = performance.now();
      const result: GuardResult = {
        callId: input.callId,
        highestClass: "P0",
        routeId: input.routeId,
        policyVersion: GUARD_POLICY_VERSION,
        counts: {},
        excluded: {},
        decision: "BLOCKED",
      };
      try {
        const perText = await scan([input.typed, ...input.results]);
        const found = perText.flat();
        for (const f of found) result.counts[f.category] = (result.counts[f.category] ?? 0) + 1;
        result.highestClass = highestClassOf(found.map((f) => CATEGORY_CLASS[f.category]));
        const user = [input.typed, ...input.results].map((t) => input.pseudo.redact(t)).join("\n\n");
        const [left = []] = await scan([`${input.system}\n\n${user}`]);
        if (left.length > 0) throw new GuardFailure("RESIDUAL_PII");
        result.decision = found.length > 0 ? "REDACTED" : "ALLOWED";
        return { user, result, typedReplaced: (perText[0] ?? []).filter((f) => f.category !== "EMAIL").length };
      } catch (err) {
        result.errorCode = err instanceof GuardFailure ? err.code : "ANALYZER_ERROR";
        deps.log?.("warn", "egress guard blocked the provider call", {
          tenantId: input.tenantId,
          callId: input.callId,
          errorCode: result.errorCode,
          error: err instanceof Error ? err.name : "unknown",
          counts: result.counts,
        });
        return { user: "", result, typedReplaced: 0 };
      } finally {
        deps.observe?.((performance.now() - started) / 1000, result.decision);
      }
    },
  };
}
