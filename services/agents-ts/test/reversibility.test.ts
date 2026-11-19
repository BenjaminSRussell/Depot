import { describe, expect, it } from "vitest";
import { defaultCatalog } from "../src/agents/catalog.js";
import type { ActionRecord } from "../src/domain/audit.js";
import { REVERSIBILITY, reversibilityOf, toActionView } from "../src/domain/reversibility.js";

const record = (toolName: string, over: Partial<ActionRecord> = {}): ActionRecord => ({
  runId: "run-1",
  seq: 1,
  toolName,
  inputJson: "{}",
  outputHash: "0".repeat(64),
  outputJson: "{}",
  outputTruncated: false,
  approverUserId: null,
  decision: "AUTO",
  decidedAt: "2026-11-18T10:00:00.000Z",
  committedEntityRef: null,
  reason: null,
  ...over,
});

const proposeTools = defaultCatalog("m")
  .flatMap((a) => a.allowedTools)
  .filter((t) => /propose|draft/.test(t));

describe("reversibility registry", () => {
  it("lists every propose or draft tool of the catalog, with exactly two reversible kinds", () => {
    expect([...new Set(proposeTools)].sort()).toEqual(Object.keys(REVERSIBILITY).sort());
    const reversible = Object.entries(REVERSIBILITY).filter(([, e]) => e.kind !== "none");
    expect(reversible.map(([name]) => name).sort()).toEqual(["depot.ap.invoice.propose_coding", "depot.ar.settlement.propose"]);
  });

  it("is fail closed: no row is reversible in this slice and every row carries a reason", () => {
    for (const name of Object.keys(REVERSIBILITY)) {
      const view = toActionView(record(name, { decision: "APPROVED", committedEntityRef: "inv-1" }));
      expect(view.reversible).toBe(false);
      expect(view.reversibleReason).toMatch(/^cannot undo: .+/);
    }
  });

  it("gives the ap_coding action the reason for the missing AP coding update API", () => {
    expect(reversibilityOf(record("depot.ap.invoice.propose_coding", { decision: "APPROVED", committedEntityRef: "inv-1" }))).toEqual({
      reversible: false,
      reversibleReason: "cannot undo: AP invoice coding update is not available yet",
    });
  });

  it("refuses an unknown tool and keeps the record's own fields", () => {
    const view = toActionView(record("depot.unknown.tool"));
    expect(view).toMatchObject({ toolName: "depot.unknown.tool", seq: 1, reversible: false, reversibleReason: "cannot undo: this tool is not registered as reversible" });
  });
});
