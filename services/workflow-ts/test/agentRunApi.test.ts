import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";
import { WorkflowExecutionAlreadyStartedError, WorkflowNotFoundError } from "@temporalio/client";
import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { createDecisionHandler } from "../src/decisionApi.js";

const TENANT = "00000000-0000-0000-0000-000000000001";
const RUN = "00000000-0000-0000-0000-0000000000f1";

describe("agent run workflow routes", () => {
  let server: Server;
  let base = "";
  let state: Record<string, unknown> | "missing" = { tenantId: TENANT, status: "awaiting_approval" };
  let started: Array<{ workflowId: string; taskQueue: string; args: unknown[] }> = [];
  let signals: Array<{ workflowId: string; name: string; args: unknown[] }> = [];
  let alreadyRunning = false;

  const client = {
    workflow: {
      start: async (_wf: unknown, opts: { workflowId: string; taskQueue: string; args: unknown[] }) => {
        if (alreadyRunning) throw new WorkflowExecutionAlreadyStartedError("running", opts.workflowId, "agentRunWorkflow");
        started.push(opts);
      },
      getHandle: (workflowId: string) => ({
        query: async () => {
          if (state === "missing") throw new WorkflowNotFoundError("not found", workflowId, undefined);
          return state;
        },
        signal: async (definition: { name: string }, ...args: unknown[]) => void signals.push({ workflowId, name: definition.name, args }),
      }),
    },
  } as never;

  beforeAll(async () => {
    server = createServer(createDecisionHandler(client, { taskQueue: "passage-approvals" }));
    await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
    base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  });
  afterAll(() => void server.close());
  beforeEach(() => {
    state = { tenantId: TENANT, status: "awaiting_approval" };
    started = [];
    signals = [];
    alreadyRunning = false;
  });

  const post = (path: string, body: unknown, duties: string, tenant = TENANT) =>
    fetch(`${base}${path}`, { method: "POST", headers: { "X-Passage-Tenant": tenant, "X-Passage-User": "alice", "X-Passage-Duties": duties }, body: JSON.stringify(body) });

  it("starts the run's workflow for the caller's tenant and user, with ids only", async () => {
    const res = await post(`/v1/workflows/agent-runs/${RUN}:start`, {}, "agents.invoke");
    expect(res.status).toBe(202);
    expect(started).toEqual([{ workflowId: `agent-run:${RUN}`, taskQueue: "passage-approvals", args: [{ tenantId: TENANT, runId: RUN, actor: "alice" }] }]);
  });

  it("a second start of the same run is accepted and starts nothing", async () => {
    alreadyRunning = true;
    expect((await post(`/v1/workflows/agent-runs/${RUN}:start`, {}, "agents.invoke")).status).toBe(202);
    expect(started).toHaveLength(0);
  });

  it("refuses a start without agents.invoke", async () => {
    expect((await post(`/v1/workflows/agent-runs/${RUN}:start`, {}, "agents.run.read")).status).toBe(403);
    expect(started).toHaveLength(0);
  });

  it("wakes the workflow for a decided approval", async () => {
    const res = await post(`/v1/workflows/agent-runs/${RUN}:approval-decided`, { actionId: "a1" }, "agents.run.read");
    expect(res.status).toBe(202);
    expect(signals).toEqual([{ workflowId: `agent-run:${RUN}`, name: "agent-approval-decided", args: [{ actionId: "a1" }] }]);
  });

  it("refuses a wake-up without the duty, for another tenant's run, for an unknown run, or without an action id", async () => {
    expect((await post(`/v1/workflows/agent-runs/${RUN}:approval-decided`, { actionId: "a1" }, "agents.invoke")).status).toBe(403);
    expect((await post(`/v1/workflows/agent-runs/${RUN}:approval-decided`, { actionId: "a1" }, "agents.run.read", "00000000-0000-0000-0000-000000000002")).status).toBe(404);
    state = "missing";
    expect((await post(`/v1/workflows/agent-runs/${RUN}:approval-decided`, { actionId: "a1" }, "agents.run.read")).status).toBe(404);
    state = { tenantId: TENANT };
    expect((await post(`/v1/workflows/agent-runs/${RUN}:approval-decided`, {}, "agents.run.read")).status).toBe(400);
    expect(signals).toHaveLength(0);
  });
});
