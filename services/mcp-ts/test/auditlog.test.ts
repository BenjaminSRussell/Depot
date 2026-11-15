import { describe, expect, it } from "vitest";
import { stdoutAuditSink } from "../src/main.js";

const TENANT = "11111111-1111-4111-8111-111111111111";
const KEY_B64 = Buffer.from(Array.from({ length: 32 }, (_, i) => i)).toString("base64");
const entry = { at: "2026-01-01T00:00:00Z", tenantId: TENANT, userId: "jane.doe@example.com", tool: "list_vendors", outcome: "OK" as const, status: 200 };

describe("the tool-call audit line", () => {
  it("pseudonymizes the user under the eu-data-boundary overlay", async () => {
    const lines: string[] = [];
    await stdoutAuditSink({ PASSAGE_CLOUD_PROFILE: "commercial+eu-data-boundary", FIELDCRYPT_KEYS: `k1=${KEY_B64}`, FIELDCRYPT_CURRENT_KEY_ID: "k1" }, (l) => lines.push(l)).record(entry);
    expect(lines[0]).not.toContain("jane.doe");
    expect(JSON.parse(lines[0]!)).toMatchObject({ message: "mcp.tool_call", service: "mcp-ts", tenantId: TENANT, tool: "list_vendors" });
    expect(JSON.parse(lines[0]!).userId).toMatch(/^pn_[0-9a-f]{16}$/);
  });

  it("is unchanged when pseudonymization is off", async () => {
    const lines: string[] = [];
    await stdoutAuditSink({}, (l) => lines.push(l)).record(entry);
    expect(JSON.parse(lines[0]!).userId).toBe("jane.doe@example.com");
  });
});
