import { describe, expect, test } from "vitest";
import { ApiError, legalEntityFromHeaders } from "../src/server.ts";

// The kernel's legal-entity selection rule (platform/go/tenant requestLegalEntity; D33, ADR-0006 rule 4, B-03a).
const A = "0190a5f2-7b1c-7d3e-8a4f-00000000000a";
const B = "0190a5f2-7b1c-7d3e-8a4f-00000000000b";
const C = "0190a5f2-7b1c-7d3e-8a4f-00000000000c";
const none = new Set<string>(["hub.subscription.write"]);
const admin = new Set<string>(["tenant.administer", "hub.subscription.write"]);

function refusal(fn: () => unknown): { status: number; code: string } | undefined {
  try {
    fn();
  } catch (e) {
    if (e instanceof ApiError) {
      return { status: e.status, code: e.code };
    }
    throw e;
  }
  return undefined;
}

describe("legalEntityFromHeaders", () => {
  test("no claim and a selection is refused", () => {
    expect(refusal(() => legalEntityFromHeaders({ "x-depot-legalentity": A }, none))).toEqual({ status: 403, code: "HUB-0403" });
    expect(refusal(() => legalEntityFromHeaders({ "x-depot-legalentity": `${A},${B}` }, new Set(["org.switcher.all-scope"])))).toEqual({ status: 403, code: "HUB-0403" });
  });

  test("a tenant administrator without a claim may still select, as in the kernel", () => {
    expect(legalEntityFromHeaders({ "x-depot-legalentity": A.toUpperCase() }, admin)).toBe(A);
    expect(legalEntityFromHeaders({ "x-depot-legalentity": `${A},${B}` }, admin)).toBeUndefined();
    expect(legalEntityFromHeaders({}, admin)).toBeUndefined();
  });

  test("a selection inside the claim list narrows to it", () => {
    expect(legalEntityFromHeaders({ "x-depot-legal-entity": `${A},${B},${C}`, "x-depot-legalentity": B }, none)).toBe(B);
    expect(legalEntityFromHeaders({ "x-depot-legal-entity": A, "x-depot-legalentity": ` ${A.toUpperCase()} ` }, none)).toBe(A);
  });

  test("a selection outside the claim is refused", () => {
    expect(refusal(() => legalEntityFromHeaders({ "x-depot-legal-entity": `${A},${B}`, "x-depot-legalentity": C }, none))).toEqual({ status: 403, code: "HUB-0403" });
    expect(refusal(() => legalEntityFromHeaders({ "x-depot-legal-entity": `${A},${B}`, "x-depot-legalentity": `${A},${C}` }, none))).toEqual({ status: 403, code: "HUB-0403" });
    expect(refusal(() => legalEntityFromHeaders({ "x-depot-legal-entity": A, "x-depot-legalentity": B }, admin))).toEqual({ status: 403, code: "HUB-0403" });
  });

  test("without a selection the claim is unchanged; a list names no single entity", () => {
    expect(legalEntityFromHeaders({ "x-depot-legal-entity": A }, none)).toBe(A);
    expect(legalEntityFromHeaders({ "x-depot-legal-entity": `${A},${B}` }, none)).toBeUndefined();
    expect(legalEntityFromHeaders({}, none)).toBeUndefined();
  });

  test("a malformed header is 400", () => {
    for (const bad of ["*", "not-a-uuid", "00000000-0000-0000-0000-000000000000", `${A},*`]) {
      expect(refusal(() => legalEntityFromHeaders({ "x-depot-legalentity": bad }, admin))).toEqual({ status: 400, code: "HUB-0400" });
      expect(refusal(() => legalEntityFromHeaders({ "x-depot-legal-entity": bad }, none))).toEqual({ status: 400, code: "HUB-0400" });
    }
  });
});
