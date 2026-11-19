import type { LogFn } from "../logging/pseudonymize.js";
import { toolAnnotation, type ToolClass } from "../agents/catalog.js";
import type { AiPolicyGuard } from "./aiPolicyGuard.js";
import { AI_POLICY_MODULES } from "../domain/aiDataPolicy.js";
import { scanPii, type PiiCategory, type PiiFinding, type Pseudonymizer } from "../domain/redaction.js";

export const EGRESS_GUARD_TOOL = "egress.guard";
export const GUARD_POLICY_VERSION = 1;
export const ANALYZER_DEADLINE_MS = 2000;
export const GUARD_REFUSAL_MESSAGE =
  "I could not check this request for sensitive data, so I did not send it to the AI model. Your ERP work is not affected; please try again later.";

export type DataClass = "P0" | "P1" | "P2" | "P3";
export type GuardDecision = "ALLOWED" | "REDACTED" | "BLOCKED" | "BLOCKED_BOUNDARY";
export type GuardErrorCode = "ANALYZER_ERROR" | "ANALYZER_TIMEOUT" | "RESIDUAL_PII" | "NO_APPROVED_ROUTE" | "POLICY_UNREADABLE";

export interface GuardResult {
  callId: string;
  highestClass: DataClass;
  routeId: string;
  policyVersion: number;
  policyCell?: "tenant" | "default";
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
  excluded?: Record<string, number>;
  policyCell?: "tenant" | "default";
  policyVersion?: number;
  declared?: DataClass;
}

export interface EgressVerdict {
  user: string;
  result: GuardResult;
  typedReplaced: number;
}

export interface Admission {
  pass: boolean;
  class: DataClass;
  policyCell: "tenant" | "default";
  policyVersion: number;
  failure?: GuardErrorCode;
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
  deps: { analyzer?: Analyzer; policy?: AiPolicyGuard; log?: LogFn; observe?: (seconds: number, decision: GuardDecision) => void; deadlineMs?: number } = {},
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
        if (policyCell === "default" || deps.policy === undefined || cls === "P2" || cls === "P3") return { ...base, pass: true, class: cls };
        const decision = await deps.policy.check({ tenantId, module, dataClass: cls });
        return { ...base, pass: decision.allowed, class: cls, policyVersion: decision.policyVersion };
      } catch (err) {
        const failure = err instanceof GuardFailure ? err.code : err instanceof Error && err.name === "AgentError" ? "POLICY_UNREADABLE" : "ANALYZER_ERROR";
        deps.log?.("warn", "egress guard could not admit a tool result", { tenantId, tool, errorCode: failure, error: err instanceof Error ? err.name : "unknown" });
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
        const perText = await scan([input.typed, ...input.results]);
        const found = perText.flat();
        for (const f of found) result.counts[f.category] = (result.counts[f.category] ?? 0) + 1;
        result.highestClass = highestClassOf([input.declared ?? "P0", ...found.map((f) => CATEGORY_CLASS[f.category])]);
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
