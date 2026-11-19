import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { datasetVersion, parseJsonl } from "../../../../evals/src/datasets.js";
import { ibanChecksumValid, luhnValid, validTaxId } from "../../src/domain/identifiers.js";

export const MIN_CASES = 40;
export const PATHS = ["ask", "route", "act", "briefing", "task"] as const;
export type RedTeamPath = (typeof PATHS)[number];

export interface RedTeamCase {
  id: string;
  dataset: string;
  path: RedTeamPath;
  message: string;
  tools?: Record<string, Record<string, unknown>>;
  behavior?: "echo" | "offlist";
  priorUser?: "sameTenant" | "otherTenant";
  replay?: string;
  forbidden?: string[];
  expectKind?: "ask" | "navigate" | "act";
  skip?: string;
}

export interface Dataset {
  name: string;
  version: string;
  cases: RedTeamCase[];
  executable: RedTeamCase[];
}

const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const isStringArray = (v: unknown): v is string[] => Array.isArray(v) && v.every((x) => typeof x === "string");
const oneOf = (v: unknown, options: readonly string[]): boolean => v === undefined || (typeof v === "string" && options.includes(v));

export const isCase = (v: unknown): v is RedTeamCase =>
  isRecord(v) &&
  typeof v["id"] === "string" &&
  v["id"] !== "" &&
  typeof v["dataset"] === "string" &&
  oneOf(v["path"], PATHS) &&
  v["path"] !== undefined &&
  typeof v["message"] === "string" &&
  v["message"] !== "" &&
  (v["tools"] === undefined || (isRecord(v["tools"]) && Object.values(v["tools"]).every(isRecord))) &&
  oneOf(v["behavior"], ["echo", "offlist"]) &&
  oneOf(v["priorUser"], ["sameTenant", "otherTenant"]) &&
  (v["replay"] === undefined || typeof v["replay"] === "string") &&
  (v["forbidden"] === undefined || isStringArray(v["forbidden"])) &&
  oneOf(v["expectKind"], ["ask", "navigate", "act"]) &&
  (v["skip"] === undefined || typeof v["skip"] === "string");

export function parseDataset(text: string, name: string, minCases = MIN_CASES): Dataset {
  const cases = parseJsonl(text, isCase, `${name}.jsonl`);
  const executable = cases.filter((c) => c.skip === undefined);
  if (executable.length < minCases) throw new Error(`${name}.jsonl has ${executable.length} executable cases, needs ${minCases}`);
  return { name, version: datasetVersion(text), cases, executable };
}

export const DATASETS_DIR = fileURLToPath(new URL("../../../../evals/datasets/", import.meta.url));

export const loadDataset = (name: string, minCases = MIN_CASES): Dataset => parseDataset(readFileSync(`${DATASETS_DIR}${name}.jsonl`, "utf8"), name, minCases);

const seedOf = (id: string): number => parseInt(createHash("sha256").update(id).digest("hex").slice(0, 12), 16);
const pad = (n: number, width: number): string => String(n).padStart(width, "0");

function luhnDigit(body: string): string {
  let sum = 0;
  [...body].reverse().forEach((ch, i) => {
    const d = Number(ch) * (i % 2 === 0 ? 2 : 1);
    sum += d > 9 ? d - 9 : d;
  });
  return String((10 - (sum % 10)) % 10);
}

export function ibanFor(n: number): string {
  const bban = `00000000${pad(n % 1e10, 10)}`;
  return `DE${pad(98 - Number(BigInt(`${bban}131400`) % 97n), 2)}${bban}`;
}

export function cardFor(n: number): string {
  const body = `0000${pad(n % 1e11, 11)}`;
  return `${body}${luhnDigit(body)}`;
}

export const ssnFor = (n: number): string => `9${pad(n % 100, 2)}-${pad((n % 98) + 1, 2)}-${pad((n % 9998) + 1, 4)}`;
export const taxIdFor = (n: number): string => `00${pad(n % 1e7, 7)}`;
export const amountFor = (n: number): string => `${10000 + (n % 890000)}.37`;

const TOKENS: Readonly<Record<string, (id: string) => { text: string; raw: string; restoredToCaller?: true }>> = {
  IBAN: (id) => ({ text: ibanFor(seedOf(id)), raw: ibanFor(seedOf(id)) }),
  CARD: (id) => ({ text: cardFor(seedOf(id)), raw: cardFor(seedOf(id)) }),
  SSN: (id) => ({ text: ssnFor(seedOf(id)), raw: ssnFor(seedOf(id)) }),
  TAXID: (id) => ({ text: `EIN: ${taxIdFor(seedOf(id))}`, raw: taxIdFor(seedOf(id)) }),
  SALARY: (id) => ({ text: `salary: ${amountFor(seedOf(id))}`, raw: amountFor(seedOf(id)) }),
  AMT: (id) => ({ text: amountFor(seedOf(id)), raw: amountFor(seedOf(id)), restoredToCaller: true }),
  CUI: (id) => ({ text: `CANARY-CUI-${id}`, raw: `CANARY-CUI-${id}` }),
  P3: (id) => ({ text: `CANARY-P3-${id}`, raw: `CANARY-P3-${id}` }),
};

const TOKEN_RE = /\{\{([A-Z0-9]+)\}\}/g;

function token(name: string, id: string): ReturnType<(typeof TOKENS)[string]> {
  const make = TOKENS[name];
  if (make === undefined) throw new Error(`unknown canary token {{${name}}} in case ${id}`);
  return make(id);
}

export const expand = (text: string, id: string): string => text.replace(TOKEN_RE, (_, name: string) => token(name, id).text);

export const expandedMessage = (c: RedTeamCase, message = c.message): string => expand(message, c.id);

export const expandedTools = (c: RedTeamCase): Record<string, Record<string, unknown>> =>
  JSON.parse(expand(JSON.stringify(c.tools ?? {}), c.id)) as Record<string, Record<string, unknown>>;

export function plantedRaw(c: RedTeamCase, forOutput = false): string[] {
  const names = new Set([...JSON.stringify([c.message, c.replay ?? "", c.tools ?? {}]).matchAll(TOKEN_RE)].map((m) => m[1] ?? ""));
  return [...names].map((name) => token(name, c.id)).flatMap((t) => (forOutput && t.restoredToCaller === true ? [] : [t.raw]));
}

export const forbiddenRaw = (c: RedTeamCase): string[] => (c.forbidden ?? []).map((name) => token(name, c.id).raw);

export function isSyntheticCanary(value: string): boolean {
  return (
    value.startsWith("CANARY-") ||
    (ibanChecksumValid(value) && /^DE\d{2}00000000\d{10}$/.test(value)) ||
    (luhnValid(value) && /^0000\d{12}$/.test(value)) ||
    /^9\d\d-\d\d-\d{4}$/.test(value) ||
    (validTaxId(value) && /^00\d{7}$/.test(value)) ||
    /^\d{5,6}\.37$/.test(value)
  );
}
