export const MODEL_KINDS = ["ask", "navigate", "act"] as const;
export type ModelKind = (typeof MODEL_KINDS)[number];
export type PromptKind = ModelKind | "clarify";

export interface RouteResult {
  kind: PromptKind;
  confidence: number;
  candidates?: [ModelKind, ModelKind];
}

export interface Rule {
  kind: ModelKind;
  re: RegExp;
}

export type RuleOutcome = { status: "decided"; kind: ModelKind } | { status: "ambiguous"; kinds: ModelKind[] } | { status: "none" };

export const MIN_CONFIDENCE = 0.6;
export const MIN_CONFIDENCE_GAP = 0.15;

const ACT_VERBS = "apply|cancel|release|pay|post|approve|reject|settle|reverse|postpone|advance|send";
const RECORD_CUE = "(?:\\$\\s?\\d|\\b\\d|\\b[a-z]{2,5}-\\d+\\b|\\b(?:invoice|payment|bill|order|po|receipt|journal|credit memo|statement|amount|balance)s?\\b)";
const FILL_ORDER: readonly ModelKind[] = ["navigate", "ask", "act"];

export const RULES: readonly Rule[] = [
  { kind: "act", re: new RegExp(`\\b(?:${ACT_VERBS})\\b(?!-)[^?]*?${RECORD_CUE}`) },
  { kind: "navigate", re: /^(?:show me|list|open|go to|find|create|new|start)\b/ },
  { kind: "ask", re: /\?$|^(?:what|which|who|why|how many|how much|when|compare|explain|total|trend)\b/ },
];

export function classifyByRules(text: string, rules: readonly Rule[] = RULES): RuleOutcome {
  const prompt = text.trim().toLowerCase();
  const hits = rules.flatMap((rule) => {
    const match = rule.re.exec(prompt);
    return match === null ? [] : [{ kind: rule.kind, at: match.index }];
  });
  const first = hits[0];
  if (first === undefined) return { status: "none" };
  const tied = hits.filter((h) => h.at === first.at && h.kind !== first.kind);
  return tied.length === 0 ? { status: "decided", kind: first.kind } : { status: "ambiguous", kinds: [first.kind, ...tied.map((h) => h.kind)] };
}

interface Scored {
  kind: ModelKind;
  confidence: number;
}

function scored(value: unknown): Scored | undefined {
  if (typeof value !== "object" || value === null) return undefined;
  const { kind, confidence } = value as Record<string, unknown>;
  if (typeof kind !== "string" || !(MODEL_KINDS as readonly string[]).includes(kind)) return undefined;
  if (typeof confidence !== "number" || !Number.isFinite(confidence) || confidence < 0 || confidence > 1) return undefined;
  return { kind: kind as ModelKind, confidence };
}

function parseModel(text: string): { top?: Scored; runnerUp?: Scored } {
  try {
    const raw = JSON.parse(text) as unknown;
    const top = scored(raw);
    if (top === undefined) return {};
    const runnerUp = scored((raw as Record<string, unknown>)["runnerUp"]);
    return { top, ...(runnerUp !== undefined ? { runnerUp } : {}) };
  } catch {
    return {};
  }
}

export function decideRoute(rules: RuleOutcome, modelText: string): RouteResult {
  if (rules.status === "decided") return { kind: rules.kind, confidence: 1 };
  const { top, runnerUp } = parseModel(modelText);
  if (top !== undefined && top.confidence >= MIN_CONFIDENCE && (runnerUp === undefined || top.confidence - runnerUp.confidence >= MIN_CONFIDENCE_GAP)) {
    return { kind: top.kind, confidence: top.confidence };
  }
  const known = rules.status === "ambiguous" ? rules.kinds : [];
  const ordered = [top?.kind, runnerUp?.kind, ...known, ...FILL_ORDER].filter((k): k is ModelKind => k !== undefined);
  const unique = [...new Set(ordered)];
  return { kind: "clarify", confidence: top?.confidence ?? 0, candidates: [unique[0] ?? "navigate", unique[1] ?? "ask"] };
}
