import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { WorkflowExecutionAlreadyStartedError, WorkflowNotFoundError } from "@temporalio/client";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { createPrivateKey, createPublicKey, randomBytes, sign } from "node:crypto";
import { readFileSync } from "node:fs";
import { createDecisionHandler } from "../src/decisionApi.js";
import { createVerifier, parsePublicKeys, SERVICE_GRANTS } from "../src/agentsToken.js";

const PKCS8 = Buffer.from("302e020100300506032b657004220420", "hex");
const seed = randomBytes(32);
const privateKey = createPrivateKey({ key: Buffer.concat([PKCS8, seed]), format: "der", type: "pkcs8" });
const publicB64 = createPublicKey(privateKey).export({ format: "der", type: "spki" }).subarray(-32).toString("base64");
const NOW = Date.now();
/** A token as agents signs it (services/agents-ts src/api/serviceSigner.ts). */
function mint(over: Record<string, unknown> = {}): string {
  const iat = Math.floor(NOW / 1000);
  const claims = { iss: "agents", aud: "workflow-ts", tid: "00000000-0000-0000-0000-000000000001", act: "alice", duties: ["agents.run.signal"], iat, exp: iat + 60, jti: randomBytes(8).toString("hex"), ...over };
  const body = Buffer.from(JSON.stringify(claims)).toString("base64url");
  return `${body}.${sign(null, Buffer.from(`passage.service-token.v1.${body}`), privateKey).toString("base64url")}`;
}

const TENANT = "00000000-0000-0000-0000-000000000001";
const RUN = "00000000-0000-0000-0000-0000000000f1";

describe("agent run workflow routes", () => {
  let server: Server;
  let base = "";
  let started: Array<{ workflowId: string; taskQueue: string; args: unknown[] }> = [];
  let signals: Array<{ workflowId: string; name: string; args: unknown[] }> = [];
  let alreadyRunning = false;
  let missing = false;

  const client = {
    workflow: {
      start: async (_wf: unknown, opts: { workflowId: string; taskQueue: string; args: unknown[] }) => {
        if (alreadyRunning) throw new WorkflowExecutionAlreadyStartedError("running", opts.workflowId, "agentRunWorkflow");
        started.push(opts);
      },
      getHandle: (workflowId: string) => ({
        signal: async (definition: { name: string }, ...args: unknown[]) => {
          if (missing) throw new WorkflowNotFoundError("not found", workflowId, undefined);
          signals.push({ workflowId, name: definition.name, args });
        },
      }),
    },
  } as never;

  beforeAll(async () => {
    const agentsVerifier = createVerifier({ keys: parsePublicKeys(`agents=${publicB64}`), now: () => NOW });
    server = createServer(createDecisionHandler(client, { taskQueue: "passage-approvals", agentsVerifier }));
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  });
  afterAll(() => void server.close());
  beforeEach(() => {
    started = [];
    signals = [];
    alreadyRunning = false;
    missing = false;
  });

  const post = (path: string, body: unknown, headers: Record<string, string>) => fetch(`${base}${path}`, { method: "POST", headers, body: JSON.stringify(body) });
  const asAgents = (over: Record<string, unknown> = {}) => ({ "x-passage-service-token": mint(over) });

  it("starts the run's workflow for the token's tenant and user with ids only, under a workflow id that carries the tenant", async () => {
    const res = await post(`/v1/workflows/agent-runs/${RUN}:start`, {}, asAgents());
    expect(res.status).toBe(202);
    expect(started).toEqual([{ workflowId: `agent-run:${TENANT}:${RUN}`, taskQueue: "passage-approvals", args: [{ tenantId: TENANT, runId: RUN, actor: "alice" }] }]);
  });

  it("the same run id of another tenant is another workflow", async () => {
    await post(`/v1/workflows/agent-runs/${RUN}:start`, {}, asAgents({ tid: "00000000-0000-0000-0000-000000000002" }));
    expect(started[0]?.workflowId).toBe(`agent-run:00000000-0000-0000-0000-000000000002:${RUN}`);
  });

  it("a second start of the same run is accepted and starts nothing", async () => {
    alreadyRunning = true;
    expect((await post(`/v1/workflows/agent-runs/${RUN}:start`, {}, asAgents())).status).toBe(202);
    expect(started).toHaveLength(0);
  });

  it("wakes the workflow of the token's tenant, under agents' token and not the approver's identity", async () => {
    const res = await post(`/v1/workflows/agent-runs/${RUN}:approval-decided`, { actionId: "a1" }, asAgents());
    expect(res.status).toBe(202);
    expect(signals).toEqual([{ workflowId: `agent-run:${TENANT}:${RUN}`, name: "agent-approval-decided", args: [{ actionId: "a1" }] }]);
  });

  it("refuses everything that is not a valid agents service token: edge headers, a user's duties, a wrong audience, grant, issuer, tenant-less or legal-entity token", async () => {
    const edge = { "X-Passage-Tenant": TENANT, "X-Passage-User": "alice", "X-Passage-Duties": "agents.invoke,agents.run.read,agents.run.signal" };
    for (const headers of [edge, {}, { "x-passage-service-token": "garbage" }, asAgents({ aud: "payables" }), asAgents({ duties: ["agents.run.read"] }), asAgents({ iss: "payables" }), asAgents({ le: "00000000-0000-0000-0000-0000000000a1" }), asAgents({ act: "service:other" }), asAgents({ exp: Math.floor(NOW / 1000) - 5 })]) {
      expect((await post(`/v1/workflows/agent-runs/${RUN}:start`, {}, headers)).status).toBe(401);
      expect((await post(`/v1/workflows/agent-runs/${RUN}:approval-decided`, { actionId: "a1" }, headers)).status).toBe(401);
    }
    expect(started).toHaveLength(0);
    expect(signals).toHaveLength(0);
  });

  it("a token cannot be replayed", async () => {
    const headers = asAgents({ jti: "same" });
    expect((await post(`/v1/workflows/agent-runs/${RUN}:start`, {}, headers)).status).toBe(202);
    expect((await post(`/v1/workflows/agent-runs/${RUN}:start`, {}, headers)).status).toBe(401);
  });

  it("answers 404 for an unknown run and 400 without an action id", async () => {
    missing = true;
    expect((await post(`/v1/workflows/agent-runs/${RUN}:approval-decided`, { actionId: "a1" }, asAgents())).status).toBe(404);
    missing = false;
    expect((await post(`/v1/workflows/agent-runs/${RUN}:approval-decided`, {}, asAgents())).status).toBe(400);
  });

  it("without a configured verifier the routes do not exist", async () => {
    const bare = createServer(createDecisionHandler(client, { taskQueue: "q" }));
    await new Promise<void>((resolve) => bare.listen(0, "127.0.0.1", resolve));
    const url = `http://127.0.0.1:${(bare.address() as AddressInfo).port}`;
    try {
      const res = await fetch(`${url}/v1/workflows/agent-runs/${RUN}:start`, { method: "POST", headers: asAgents(), body: "{}" });
      expect(res.status).toBe(404);
    } finally {
      bare.close();
    }
  });

  it("the grant matches policies/authz/data.json and the Go table", () => {
    const root = new URL("../../../", import.meta.url);
    const data = JSON.parse(readFileSync(new URL("policies/authz/data.json", root), "utf8")) as { service_grants: Record<string, Record<string, string[]>> };
    expect(data.service_grants["agents"]?.["workflow-ts"]).toEqual(SERVICE_GRANTS["agents"]);
    expect(readFileSync(new URL("platform/go/tenant/service.go", root), "utf8")).toContain('"workflow-ts": {"agents.run.signal"}');
  });
});
