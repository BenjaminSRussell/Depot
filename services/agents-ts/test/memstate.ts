import type { PseudonymSnapshot } from "../src/domain/redaction.js";
import type { RunCredential, RunStateStore } from "../src/store/runstate.js";

/** In-memory `agent_run_state` and credentials; the clock decides when a credential has expired. */
export class MemState implements RunStateStore {
  rows = new Map<string, { input: unknown; state: unknown; pseudonyms: PseudonymSnapshot }>();
  creds = new Map<string, RunCredential & { expiresAtMs: number }>();
  clock = { ms: Date.UTC(2026, 9, 21) };
  async create(t: string, r: string, input: unknown, state: unknown) {
    this.rows.set(`${t}/${r}`, { input: structuredClone(input), state: structuredClone(state), pseudonyms: { entries: [], counters: {} } });
  }
  async load<I, S>(t: string, r: string) {
    const row = this.rows.get(`${t}/${r}`);
    return row === undefined ? undefined : (structuredClone(row) as { input: I; state: S; pseudonyms: PseudonymSnapshot });
  }
  async save<S>(t: string, r: string, state: S, pseudonyms: PseudonymSnapshot) {
    const row = this.rows.get(`${t}/${r}`)!;
    row.state = structuredClone(state);
    row.pseudonyms = structuredClone(pseudonyms);
  }
  async putCredential(t: string, r: string, c: RunCredential, expiresAtMs: number) {
    this.creds.set(`${t}/${r}/${c.kind}`, { ...c, expiresAtMs });
  }
  async credential(t: string, r: string, kind: RunCredential["kind"]) {
    const c = this.creds.get(`${t}/${r}/${kind}`);
    return c !== undefined && c.expiresAtMs > this.clock.ms ? c : undefined;
  }
  async purgeCredentials(t: string, r: string) {
    for (const k of [...this.creds.keys()]) if (k.startsWith(`${t}/${r}/`)) this.creds.delete(k);
  }
  async scrub(t: string, r: string) {
    const row = this.rows.get(`${t}/${r}`);
    if (row === undefined) return;
    (row.input as { prompt: string }).prompt = "";
    row.pseudonyms = { entries: [], counters: {} };
  }
  async discard(t: string, r: string) {
    this.rows.delete(`${t}/${r}`);
    await this.purgeCredentials(t, r);
  }
  async sweep() {
    return 0;
  }
}
