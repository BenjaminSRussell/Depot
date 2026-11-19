import { describe, expect, it } from "vitest";
import { loadConfig } from "../src/config.js";
import { exclusionNotice, recordsOf, routeCarries, selectRoute, type Attestation, type Boundary, type ModelRoute } from "../src/domain/modelRoute.js";
import type { EffectiveClass } from "../src/runtime/cuiDesignation.js";

const attestation: Attestation = { accreditation: "FedRAMP High", evidenceRef: "ev-1", approvedBy: "operator", approvedAt: "2026-11-01T00:00:00Z" };
const plain: ModelRoute = { routeId: "env:anthropic", adapter: "anthropic", endpointId: "env" };
const attested: ModelRoute = { routeId: "gov", adapter: "bedrock", endpointId: "bedrock-gov", region: "us-gov-west-1", attestation };
const incomplete: ModelRoute = { ...attested, attestation: { ...attestation, evidenceRef: " " } };

describe("route selection", () => {
  const table: Array<[Boundary, string, ModelRoute, EffectiveClass, boolean]> = [
    ["standard", "unattested", plain, "NONE", true],
    ["standard", "unattested", plain, "CUI", false],
    ["standard", "unattested", plain, "UNKNOWN", false],
    ["standard", "attested", attested, "NONE", true],
    ["standard", "attested", attested, "CUI", false],
    ["standard", "attested", attested, "UNKNOWN", false],
    ["cui", "unattested", plain, "NONE", false],
    ["cui", "unattested", plain, "CUI", false],
    ["cui", "unattested", plain, "UNKNOWN", false],
    ["cui", "incomplete attestation", incomplete, "CUI", false],
    ["cui", "attested", attested, "NONE", true],
    ["cui", "attested", attested, "CUI", true],
    ["cui", "attested", attested, "UNKNOWN", true],
  ];
  it.each(table)("%s boundary, %s route, class %s: carries %s", (boundary, _label, route, dataClass, carries) => {
    expect(routeCarries(boundary, route, dataClass)).toBe(carries);
    expect(selectRoute(boundary, [route], dataClass)).toBe(carries ? route : undefined);
  });

  it("picks the first route that carries the class", () => {
    expect(selectRoute("cui", [plain, attested], "CUI")).toBe(attested);
  });
});

describe("recordsOf", () => {
  const GET = "depot.ap.invoice.get";
  it("makes one ref of an object with a string id, typed by the tool's entity", () => {
    expect(recordsOf(GET, { id: "a1", total: "1" })).toEqual([{ entityType: "invoice", recordId: "a1" }]);
  });

  it("makes one ref per element of an array or of items", () => {
    const refs = [{ entityType: "invoice", recordId: "a" }, { entityType: "invoice", recordId: "" }, { entityType: "invoice", recordId: "" }];
    expect(recordsOf(GET, [{ id: "a" }, { name: "x" }, "text"])).toEqual(refs);
    expect(recordsOf(GET, { items: [{ id: "a" }, {}, 3] })).toEqual(refs);
  });

  it("has no refs for an empty list", () => {
    expect(recordsOf(GET, [])).toEqual([]);
    expect(recordsOf(GET, { items: [] })).toEqual([]);
  });

  it.each([null, undefined, "", "text", 7, true, {}, { id: 5 }, { id: { x: 1 } }, { id: "" }, { items: "no" }])("makes one empty-id ref of %j", (output) => {
    expect(recordsOf(GET, output)).toEqual([{ entityType: "invoice", recordId: "" }]);
  });

  it("leaves the entity type empty for a short tool name", () => {
    expect(recordsOf("depot.x", { id: "a" })).toEqual([{ entityType: "", recordId: "a" }]);
  });
});

describe("exclusionNotice", () => {
  it("names the kinds and the count and never a record id", () => {
    const text = exclusionNotice(["invoice", "vendor"], 3);
    expect(text).toContain("3");
    expect(text).toContain("invoice, vendor records");
    expect(text).toContain("no approved AI route");
  });
});

describe("loadConfig boundary and compliance URL", () => {
  const base = { MCP_URL: "http://mcp:8080", AGENTS_JWT_ISSUER: "https://idp.dev.depot.dev/realms/dev", AGENTS_JWKS_URL: "https://idp.dev.depot.dev/realms/dev/protocol/openid-connect/certs" };
  it("defaults to the standard boundary with no resolver", () => {
    expect(loadConfig(base)).toMatchObject({ boundary: "standard", complianceUrl: undefined });
    expect(loadConfig({ ...base, AGENTS_BOUNDARY: "" }).boundary).toBe("standard");
  });
  it("accepts cui and an http(s) compliance URL", () => {
    expect(loadConfig({ ...base, AGENTS_BOUNDARY: "cui", AGENTS_COMPLIANCE_URL: "https://gw" })).toMatchObject({ boundary: "cui", complianceUrl: "https://gw" });
  });
  it.each(["CUI", "other"])("refuses AGENTS_BOUNDARY=%s", (value) => {
    expect(() => loadConfig({ ...base, AGENTS_BOUNDARY: value })).toThrow(/AGENTS_BOUNDARY/);
  });
  it("refuses a non-http compliance URL", () => {
    expect(() => loadConfig({ ...base, AGENTS_COMPLIANCE_URL: "ftp://x" })).toThrow(/AGENTS_COMPLIANCE_URL/);
  });
});
