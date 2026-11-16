import { AgentError } from "../errors.js";

/**
 * PII redaction before any provider call (FR-47.12). Categories here are the
 * global core; country packs extend them later (PRD 9.47.20).
 */
export type PiiCategory = "EMAIL" | "IBAN" | "SSN";

const PATTERNS: Readonly<Record<PiiCategory, RegExp>> = {
  // The lookbehind starts a match only at the beginning of a local part, so a long
  // run of word characters is scanned once, not once per position (9.47.19 budget).
  EMAIL: /(?<![A-Za-z0-9._%+-])[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g,
  IBAN: /\b[A-Z]{2}\d{2}(?: ?[A-Z0-9]{4}){2,7}(?: ?[A-Z0-9]{1,3})?\b/g,
  SSN: /\b\d{3}-\d{2}-\d{4}\b/g,
};

export interface PiiFinding {
  category: PiiCategory;
  value: string;
}

/** Returns every PII match in the text. Used as a write-time scanner. */
export function scanPii(text: string): PiiFinding[] {
  const findings: PiiFinding[] = [];
  for (const category of Object.keys(PATTERNS) as PiiCategory[]) {
    for (const match of text.matchAll(PATTERNS[category])) {
      findings.push({ category, value: match[0] });
    }
  }
  return findings;
}

/** BR/invariant: agent_action.input_json never contains raw PII. */
export function assertNoPii(text: string): void {
  if (scanPii(text).length > 0) {
    throw new AgentError("AGT-0210", "unredacted PII found in text bound for storage or provider");
  }
}

interface Entry {
  pseudonym: string;
  expiresAt: number;
}

/** The pseudonym map as stored with a run that waits for approval, so a resumed run restores values the same way (FR-47.4). */
export interface PseudonymSnapshot {
  entries: Array<{ category: PiiCategory; value: string; pseudonym: string; expiresAt: number }>;
  counters: Partial<Record<PiiCategory, number>>;
}

/**
 * Replaces PII with stable pseudonyms per (tenant, value) for `ttlMs`
 * (BR-47.7). The mapping lives only in this in-memory instance; the caller
 * decides whether to persist it (Vault transit-encrypted, per the PRD).
 */
export class Pseudonymizer {
  private readonly byValue = new Map<string, Entry>();
  private readonly byPseudonym = new Map<string, string>();
  private readonly counters: Partial<Record<PiiCategory, number>> = {};

  constructor(
    private readonly tenantId: string,
    private readonly ttlMs: number,
    private readonly now: () => number,
  ) {}

  /** The live mappings and counters, for persisting between the steps of a resumable run. */
  snapshot(): PseudonymSnapshot {
    this.sweep();
    const entries: PseudonymSnapshot["entries"] = [];
    for (const [key, entry] of this.byValue) {
      const [, category = "", value = ""] = key.split("\u0000");
      entries.push({ category: category as PiiCategory, value, pseudonym: entry.pseudonym, expiresAt: entry.expiresAt });
    }
    return { entries, counters: { ...this.counters } };
  }

  /** Loads a snapshot taken by `snapshot()`; mappings already past their TTL are dropped by the next sweep. */
  load(snapshot: PseudonymSnapshot): void {
    for (const e of snapshot.entries) {
      this.byValue.set(`${this.tenantId}\u0000${e.category}\u0000${e.value}`, { pseudonym: e.pseudonym, expiresAt: e.expiresAt });
      this.byPseudonym.set(e.pseudonym, e.value);
    }
    Object.assign(this.counters, snapshot.counters);
  }

  redact(text: string): string {
    let out = text;
    for (const category of Object.keys(PATTERNS) as PiiCategory[]) {
      out = out.replace(PATTERNS[category], (value) => this.pseudonymFor(category, value));
    }
    return out;
  }

  /** Resolves pseudonyms in a model response back to real values for the UI only. */
  restore(text: string): string {
    this.sweep();
    let out = text;
    for (const [pseudonym, value] of this.byPseudonym) {
      out = out.split(pseudonym).join(value);
    }
    return out;
  }

  /** Drops mappings past their TTL. */
  sweep(): void {
    const t = this.now();
    for (const [key, entry] of this.byValue) {
      if (entry.expiresAt <= t) {
        this.byValue.delete(key);
        this.byPseudonym.delete(entry.pseudonym);
      }
    }
  }

  private pseudonymFor(category: PiiCategory, value: string): string {
    this.sweep();
    const key = `${this.tenantId}\u0000${category}\u0000${value}`;
    const existing = this.byValue.get(key);
    if (existing) return existing.pseudonym;
    const n = (this.counters[category] ?? 0) + 1;
    this.counters[category] = n;
    const pseudonym = `[${category}-${n}]`;
    this.byValue.set(key, { pseudonym, expiresAt: this.now() + this.ttlMs });
    this.byPseudonym.set(pseudonym, value);
    return pseudonym;
  }
}
