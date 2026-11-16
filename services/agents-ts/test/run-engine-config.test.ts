import { describe, expect, it } from "vitest";
import { loadConfig } from "../src/config.js";
import { Pseudonymizer } from "../src/domain/redaction.js";
import { createServiceSigner } from "../src/api/serviceSigner.js";
import { createWorkflowGateway } from "../src/runtime/workflowGateway.js";

const signer = createServiceSigner({ seedBase64: Buffer.alloc(32, 9).toString("base64"), now: () => 1_700_000_000_000, newJti: () => "j1" });

const base = { MCP_URL: "http://mcp:8080", AGENTS_JWT_ISSUER: "https://idp.dev.passage.dev/realms/dev", AGENTS_JWKS_URL: "https://idp.dev.passage.dev/realms/dev/protocol/openid-connect/certs" };

describe("run engine config", () => {
  it("runs in-process by default, the dev fallback, and needs nothing from Temporal", () => {
    expect(loadConfig(base)).toMatchObject({ runEngine: "inprocess", workflowUrl: undefined });
  });

  it("temporal mode needs the workflow URL and the callers' public keys", () => {
    const ok = { ...base, AGENTS_RUN_ENGINE: "temporal", AGENTS_WORKFLOW_URL: "http://workflow:8080", PASSAGE_SERVICE_PUBLIC_KEYS: "workflow-ts=AAAA" };
    expect(loadConfig(ok)).toMatchObject({ runEngine: "temporal", workflowUrl: "http://workflow:8080" });
    expect(() => loadConfig({ ...ok, AGENTS_WORKFLOW_URL: "" })).toThrow(/AGENTS_WORKFLOW_URL/);
    expect(() => loadConfig({ ...ok, PASSAGE_SERVICE_PUBLIC_KEYS: "" })).toThrow(/PASSAGE_SERVICE_PUBLIC_KEYS/);
    expect(() => loadConfig({ ...base, AGENTS_RUN_ENGINE: "kafka" })).toThrow(/inprocess or temporal/);
  });
});

describe("workflow gateway", () => {
  it("starts a run and wakes it under agents' own signed service token, never the caller's identity headers", async () => {
    const seen: Array<{ url: string; headers: Record<string, string>; body: string }> = [];
    const fetchFn = (async (url: string, init: RequestInit) => {
      seen.push({ url, headers: init.headers as Record<string, string>, body: String(init.body) });
      return new Response("{}", { status: 202 });
    }) as unknown as typeof fetch;
    const g = createWorkflowGateway({ baseUrl: "http://workflow:8080/", signer, fetchFn });
    const caller = { tenantId: "t1", userId: "u1", duties: new Set(["agents.invoke", "x"]) };
    await g.start(caller, "r1");
    await g.approvalDecided(caller, "r1", "a1");
    expect(seen.map((s) => s.url)).toEqual(["http://workflow:8080/v1/workflows/agent-runs/r1:start", "http://workflow:8080/v1/workflows/agent-runs/r1:approval-decided"]);
    const claims = JSON.parse(Buffer.from(String(seen[0]?.headers["x-passage-service-token"]).split(".")[0]!, "base64url").toString());
    expect(claims).toMatchObject({ iss: "agents", aud: "workflow-ts", tid: "t1", act: "u1", duties: ["agents.run.signal"], exp: 1_700_000_060 });
    expect(claims.le).toBeUndefined();
    expect(Object.keys(seen[0]!.headers).filter((h) => h.startsWith("x-passage-") && h !== "x-passage-service-token")).toEqual([]);
    expect(JSON.parse(seen[1]?.body ?? "")).toEqual({ actionId: "a1" });
    await expect(createWorkflowGateway({ baseUrl: "http://w", signer, fetchFn: (async () => new Response("", { status: 502 })) as unknown as typeof fetch }).start(caller, "r")).rejects.toThrow(/502/);
  });
});

describe("pseudonym snapshot", () => {
  it("round-trips: a loaded map restores and re-redacts with the same pseudonyms and counters", () => {
    const clock = { ms: 1000 };
    const a = new Pseudonymizer("t", 5000, () => clock.ms);
    const text = a.redact("a@x.io and DE89370400440532013000 and b@y.io");
    const b = new Pseudonymizer("t", 5000, () => clock.ms);
    b.load(JSON.parse(JSON.stringify(a.snapshot())));
    expect(b.restore(text)).toBe("a@x.io and DE89370400440532013000 and b@y.io");
    expect(b.redact("c@z.io a@x.io")).toBe("[EMAIL-3] [EMAIL-1]");
    clock.ms = 9000;
    expect(b.restore(text)).toBe(text); // past its TTL the mapping is gone
  });
});
