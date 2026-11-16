import { fileURLToPath } from "node:url";
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { bundleWorkflowCode, Worker, type WorkflowBundle } from "@temporalio/worker";
import { TestWorkflowEnvironment } from "@temporalio/testing";
import { createAgentRunActivities } from "../src/agentRunActivities.js";
import { createServiceTokenSigner } from "../src/serviceToken.js";
import { agentApprovalDecidedSignal, agentRunStateQuery, agentRunWorkflow, agentRunWorkflowId, type AgentRunInput, validateAgentRunInput } from "../src/workflows.js";
import { FakeAgents } from "./fakeAgents.js";

const input: AgentRunInput = { tenantId: "00000000-0000-0000-0000-000000000001", runId: "00000000-0000-0000-0000-0000000000f1", actor: "alice" };
const HOUR = 3_600_000;
const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 7).toString("base64") });

// Needs the Temporal time-skipping test server (downloaded by @temporalio/testing); CI runs it.
describe("agent run workflow", () => {
  let environment: TestWorkflowEnvironment;
  let workflowBundle: WorkflowBundle;
  let sequence = 0;

  beforeAll(async () => {
    environment = await TestWorkflowEnvironment.createTimeSkipping();
    workflowBundle = await bundleWorkflowCode({ workflowsPath: fileURLToPath(new URL("../src/workflows.ts", import.meta.url)) });
  }, 120_000);

  afterAll(async () => {
    await environment?.teardown();
  });

  function worker(fake: FakeAgents, taskQueue: string) {
    return Worker.create({
      connection: environment.nativeConnection,
      taskQueue,
      workflowBundle,
      maxCachedWorkflows: 0,
      activities: { ...createAgentRunActivities("http://agents", signer, fake.fetch) },
    });
  }

  const startRun = (taskQueue: string) =>
    environment.client.workflow.start(agentRunWorkflow, { workflowId: `${agentRunWorkflowId(input.tenantId, input.runId)}:${taskQueue}`, taskQueue, args: [input] });

  async function until(check: () => boolean | Promise<boolean>): Promise<void> {
    for (let i = 0; i < 200 && !(await check()); i += 1) await new Promise((r) => setTimeout(r, 50));
    expect(await check()).toBe(true);
  }

  async function fresh(): Promise<{ fake: FakeAgents; taskQueue: string }> {
    sequence += 1;
    const fake = new FakeAgents();
    fake.expiresAtMs = (await environment.currentTimeMs()) + 72 * HOUR;
    return { fake, taskQueue: `agent-run-${sequence}` };
  }

  it("resumes after the approval and commits as one run, with a tenant-level token for the run's user", async () => {
    const { fake, taskQueue } = await fresh();
    const result = await (await worker(fake, taskQueue)).runUntil(async () => {
      const handle = await startRun(taskQueue);
      await until(async () => (await handle.query(agentRunStateQuery)).status === "awaiting_approval");
      expect(fake.commitsPosted).toBe(0);
      fake.decision = { decision: "APPROVED" };
      await handle.signal(agentApprovalDecidedSignal, { actionId: "action-1" });
      return handle.result();
    });
    expect(result).toMatchObject({ status: "done", actionId: "action-1", outcome: { status: "COMMITTED" } });
    expect(fake.calls).toMatchObject({ begin: 1, plan: 1, commit: 1, abort: 0 });
    expect(fake.tokens.every((t) => t.act === "alice" && t.tid === input.tenantId && t.le === undefined && t.duties.join() === "agents.run.workflow")).toBe(true);
  }, 60_000);

  it("survives the worker being replaced while it waits: the new worker finishes the same run", async () => {
    const { fake, taskQueue } = await fresh();
    const first = await worker(fake, taskQueue);
    const handle = await first.runUntil(async () => {
      const h = await startRun(taskQueue);
      await until(async () => (await h.query(agentRunStateQuery)).status === "awaiting_approval");
      return h;
    });
    expect(fake.commitsPosted).toBe(0);
    fake.decision = { decision: "APPROVED" };
    const result = await (await worker(fake, taskQueue)).runUntil(async () => {
      await handle.signal(agentApprovalDecidedSignal, { actionId: "action-1" });
      return handle.result();
    });
    expect(result.outcome).toEqual({ status: "COMMITTED" });
    expect(fake.commitsPosted).toBe(1);
  }, 60_000);

  it("expires after the approval window with nobody deciding: REJECTED, AGT-0208, nothing committed", async () => {
    const { fake, taskQueue } = await fresh();
    const result = await (await worker(fake, taskQueue)).runUntil(async () => (await startRun(taskQueue)).result());
    expect(result.outcome).toEqual({ status: "REJECTED", code: "AGT-0208", reason: "EXPIRED" });
    expect(fake.commitsPosted).toBe(0);
    expect(fake.calls.resolve).toBeGreaterThan(1);
  }, 60_000);

  it("a rejection ends the run without a commit", async () => {
    const { fake, taskQueue } = await fresh();
    const result = await (await worker(fake, taskQueue)).runUntil(async () => {
      const handle = await startRun(taskQueue);
      await until(async () => (await handle.query(agentRunStateQuery)).status === "awaiting_approval");
      fake.decision = { decision: "REJECTED" };
      await handle.signal(agentApprovalDecidedSignal, { actionId: "action-1" });
      return handle.result();
    });
    expect(result.outcome).toMatchObject({ status: "REJECTED", reason: "approver rejected" });
    expect(fake.commitsPosted).toBe(0);
  }, 60_000);

  it("a decision whose signal was lost is found by the hourly re-read", async () => {
    const { fake, taskQueue } = await fresh();
    const result = await (await worker(fake, taskQueue)).runUntil(async () => {
      const handle = await startRun(taskQueue);
      await until(async () => (await handle.query(agentRunStateQuery)).status === "awaiting_approval");
      fake.decision = { decision: "APPROVED" };
      return handle.result();
    });
    expect(result.outcome).toEqual({ status: "COMMITTED" });
  }, 60_000);

  it("a signal for another action is ignored", async () => {
    const { fake, taskQueue } = await fresh();
    const result = await (await worker(fake, taskQueue)).runUntil(async () => {
      const handle = await startRun(taskQueue);
      await until(async () => (await handle.query(agentRunStateQuery)).status === "awaiting_approval");
      await handle.signal(agentApprovalDecidedSignal, { actionId: "someone-elses" });
      return handle.result();
    });
    expect(result.outcome).toMatchObject({ status: "REJECTED", code: "AGT-0208" });
  }, 60_000);

  it("a run the kill switch stopped ends KILLED and never reaches approval or commit", async () => {
    const { fake, taskQueue } = await fresh();
    fake.planOutcome = () => ({ next: "done", status: "KILLED", reason: "kill switch activated" });
    const result = await (await worker(fake, taskQueue)).runUntil(async () => (await startRun(taskQueue)).result());
    expect(result.outcome).toMatchObject({ status: "KILLED" });
    expect(fake.calls).toMatchObject({ plan: 1, resolve: 0, commit: 0 });
  }, 60_000);

  it("a step the agents service refuses for good ends the run FAILED through abort", async () => {
    const { fake, taskQueue } = await fresh();
    fake.refuse = { step: "plan", status: 422 };
    const result = await (await worker(fake, taskQueue)).runUntil(async () => (await startRun(taskQueue)).result());
    expect(result.outcome?.status).toBe("FAILED");
    expect(fake.calls).toMatchObject({ plan: 1, abort: 1, commit: 0 });
    expect(fake.aborted).toContain("422");
  }, 60_000);

  it("a plan that fails with a server error is retried until it lands, and the run is not failed", async () => {
    const { fake, taskQueue } = await fresh();
    fake.transient.plan = 3;
    const result = await (await worker(fake, taskQueue)).runUntil(async () => {
      const handle = await startRun(taskQueue);
      await until(async () => (await handle.query(agentRunStateQuery)).status === "awaiting_approval");
      fake.decision = { decision: "APPROVED" };
      await handle.signal(agentApprovalDecidedSignal, { actionId: "action-1" });
      return handle.result();
    });
    expect(fake.calls.plan).toBe(4);
    expect(result.outcome).toEqual({ status: "COMMITTED" });
    expect(fake.calls.abort).toBe(0);
  }, 60_000);

  it("an outage during the hourly re-read is ridden out, and abort retries until it lands", async () => {
    const { fake, taskQueue } = await fresh();
    fake.transient.resolve = 5;
    fake.transient.abort = 3;
    fake.refuse = undefined;
    const result = await (await worker(fake, taskQueue)).runUntil(async () => {
      const handle = await startRun(taskQueue);
      await until(async () => (await handle.query(agentRunStateQuery)).status === "awaiting_approval");
      fake.decision = { decision: "APPROVED" };
      return handle.result();
    });
    expect(result.outcome).toEqual({ status: "COMMITTED" });
  }, 60_000);

  it("abort is retried through its own outage after a step is refused", async () => {
    const { fake, taskQueue } = await fresh();
    fake.refuse = { step: "plan", status: 422 };
    fake.transient.abort = 4;
    const result = await (await worker(fake, taskQueue)).runUntil(async () => (await startRun(taskQueue)).result());
    expect(result.outcome?.status).toBe("FAILED");
    expect(fake.calls.abort).toBe(5);
  }, 60_000);

  it("an approval whose session expired is asked for again: the workflow waits on the new request and commits after the second approval", async () => {
    const { fake, taskQueue } = await fresh();
    fake.reapproveOnce = true;
    const result = await (await worker(fake, taskQueue)).runUntil(async () => {
      const handle = await startRun(taskQueue);
      await until(async () => (await handle.query(agentRunStateQuery)).status === "awaiting_approval");
      fake.decision = { decision: "APPROVED" };
      await handle.signal(agentApprovalDecidedSignal, { actionId: "action-1" });
      await until(async () => (await handle.query(agentRunStateQuery)).actionId === "action-2");
      expect(fake.commitsPosted).toBe(0);
      fake.decision = { decision: "APPROVED" };
      await handle.signal(agentApprovalDecidedSignal, { actionId: "action-2" });
      return handle.result();
    });
    expect(result.outcome).toEqual({ status: "COMMITTED" });
    expect(fake.commitsPosted).toBe(1);
  }, 60_000);

  it("rejects an input without ids", () => {
    expect(() => validateAgentRunInput({ ...input, runId: "" })).toThrow(TypeError);
  });
});
