import { describe, expect, it } from "vitest";
import { resolveRecord } from "../../modules/copilot/suggestionModel";
import { loadModules } from "../../modules/registry";
import { copy } from "../copy";
import {
  ASSIST_ACTIONS,
  AUDIT_TRAIL_TOOL,
  buildRecordRef,
  DOCUMENT_FLOW_TOOL,
  missingParts,
  RECORD_ASSIST,
  recordAssistEntry,
  WORKFLOW_HISTORY_TOOL,
} from "./recordAssist";

const UUID = "0195f0a0-0000-7000-8000-000000000071";

describe("buildRecordRef", () => {
  it("builds record:<entity>/<id> for a registered entity and a plain id", () => {
    expect(buildRecordRef("purchase-order", UUID)).toBe(`record:purchase-order/${UUID}`);
    expect(buildRecordRef("ap-invoice", "INV_1042")).toBe("record:ap-invoice/INV_1042");
  });

  it.each([
    ["unknown entity", "planned-order", UUID],
    ["prototype key", "__proto__", UUID],
    ["constructor key", "constructor", UUID],
    ["empty id", "purchase-order", ""],
    ["undefined id", "purchase-order", undefined],
    ["number id", "purchase-order", 7],
    ["NaN id", "purchase-order", Number.NaN],
    ["object id", "purchase-order", { id: UUID }],
    ["121 character id", "purchase-order", "a".repeat(121)],
    ["id with a slash", "purchase-order", "a/b"],
    ["id with dots", "purchase-order", ".."],
    ["id with a space", "purchase-order", "a b"],
  ])("returns undefined for %s", (_name, entity, id) => {
    expect(buildRecordRef(entity, id)).toBeUndefined();
  });

  it("accepts a 120 character id", () => {
    expect(buildRecordRef("purchase-order", "a".repeat(120))).toBe(`record:purchase-order/${"a".repeat(120)}`);
  });
});

describe("RECORD_ASSIST registry", () => {
  it("only lists entities the citation resolver knows, so a citation can link to them", () => {
    for (const entity of Object.keys(RECORD_ASSIST)) {
      expect(resolveRecord({ entityType: entity, recordId: "x", label: "x" }, []).entityLabel).not.toBe("A record");
    }
  });

  it("links next steps only to screens in the navigation, with the navigation's duty", () => {
    const nav = loadModules().flatMap((module) => module.nav);
    for (const entry of Object.values(RECORD_ASSIST)) {
      for (const link of entry.nextLinks) {
        const path = link.to(UUID).split("?")[0];
        expect(nav.find((item) => item.to === path)?.duty, link.label).toBe(link.duty);
      }
    }
  });

  it("covers the four record types and ignores inherited keys", () => {
    expect(Object.keys(RECORD_ASSIST).sort()).toEqual(["ap-invoice", "ar-payment", "journal-entry", "purchase-order"]);
    expect(recordAssistEntry("toString")).toBeUndefined();
  });

  it("has a plain sentence for every tool an action needs", () => {
    for (const tools of Object.values(ASSIST_ACTIONS)) {
      for (const tool of tools) expect(copy.recordAssist.missing[tool]).toBeTruthy();
    }
  });
});

describe("missingParts", () => {
  const ok = (tool: string) => ({ tool, status: "OK" as const });

  it("reports every required tool when nothing was read", () => {
    expect(missingParts("explain", [])).toEqual([AUDIT_TRAIL_TOOL, DOCUMENT_FLOW_TOOL, WORKFLOW_HISTORY_TOOL]);
  });

  it("reports nothing when every required tool succeeded", () => {
    expect(missingParts("explain", [ok(WORKFLOW_HISTORY_TOOL), ok(AUDIT_TRAIL_TOOL), ok(DOCUMENT_FLOW_TOOL)])).toEqual([]);
  });

  it("counts a DENIED or ERROR call as missing, once, in a fixed order", () => {
    const calls = [
      { tool: DOCUMENT_FLOW_TOOL, status: "DENIED" as const },
      { tool: DOCUMENT_FLOW_TOOL, status: "ERROR" as const },
      ok(AUDIT_TRAIL_TOOL),
      ok("depot.other.thing.get"),
    ];
    expect(missingParts("next", calls)).toEqual([DOCUMENT_FLOW_TOOL, WORKFLOW_HISTORY_TOOL]);
  });

  it("needs only the audit trail for what changed", () => {
    expect(missingParts("changed", [ok(DOCUMENT_FLOW_TOOL)])).toEqual([AUDIT_TRAIL_TOOL]);
    expect(missingParts("changed", [ok(AUDIT_TRAIL_TOOL)])).toEqual([]);
  });
});
