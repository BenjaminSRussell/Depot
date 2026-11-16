import { describe, expect, it } from "vitest";
import { MockActivityEnvironment } from "@temporalio/testing";
import { createAgentRunActivities, type AgentRunRef } from "../src/agentRunActivities.js";
import { createServiceTokenSigner } from "../src/serviceToken.js";
import { REJECTED } from "../src/serviceCall.js";
import { FakeAgents } from "./fakeAgents.js";
import { decodeClaims } from "./fakePayables.js";

const ref: AgentRunRef = { tenantId: "00000000-0000-0000-0000-000000000001", runId: "00000000-0000-0000-0000-0000000000f1", actor: "alice" };
const signer = createServiceTokenSigner({ seedBase64: Buffer.alloc(32, 7).toString("base64"), now: () => 1_700_000_000_000, newJti: () => "jti-1" });
const run = <T>(fn: () => Promise<T>): Promise<T> => new MockActivityEnvironment().run(fn);

describe("agent run activities", () => {
  it("call the run step routes of agents with a tenant-level token for the run's user", async () => {
    const fake = new FakeAgents();
    fake.expiresAtMs = 5;
    const a = createAgentRunActivities("http://agents", signer, fake.fetch);
    expect(await run(() => a.agentBegin(ref))).toEqual({ next: "plan" });
    expect(await run(() => a.agentPlan(ref))).toMatchObject({ next: "await_approval", actionId: "action-1" });
    expect(await run(() => a.agentResolve({ ...ref, expire: false }))).toMatchObject({ next: "await_approval" });
    fake.decision = { decision: "APPROVED" };
    expect(await run(() => a.agentResolve({ ...ref, expire: false }))).toEqual({ next: "commit" });
    expect(await run(() => a.agentCommit(ref))).toEqual({ next: "done", status: "COMMITTED" });
    expect(fake.calls).toMatchObject({ begin: 1, plan: 1, resolve: 2, commit: 1 });
    expect(fake.tokens[0]).toMatchObject({ iss: "workflow-ts", aud: "agents", tid: ref.tenantId, act: "alice", duties: ["agents.run.workflow"] });
    expect(fake.tokens[0]?.le).toBeUndefined();
  });

  it("send expire to resolve and the reason to abort", async () => {
    const seen: unknown[] = [];
    const fetchImpl = (async (_url: unknown, init?: RequestInit) => {
      seen.push(JSON.parse(String(init?.body)));
      return new Response(JSON.stringify({ next: "done", status: "FAILED" }), { status: 200 });
    }) as typeof fetch;
    const a = createAgentRunActivities("http://agents", signer, fetchImpl);
    await run(() => a.agentResolve({ ...ref, expire: true }));
    await run(() => a.agentAbort({ ...ref, reason: "gave up" }));
    expect(seen).toEqual([{ expire: true }, { reason: "gave up" }]);
  });

  it("do not retry a refusal of the run, and retry a server error", async () => {
    const fake = new FakeAgents();
    const a = createAgentRunActivities("http://agents", signer, fake.fetch);
    fake.refuse = { step: "plan", status: 422 };
    await expect(run(() => a.agentPlan(ref))).rejects.toMatchObject({ type: REJECTED, nonRetryable: true });
    fake.refuse = { step: "plan", status: 503 };
    await expect(run(() => a.agentPlan(ref))).rejects.not.toMatchObject({ nonRetryable: true });
  });

  it("the signed token names no legal entity and is addressed to agents", () => {
    const claims = decodeClaims(signer.mintForAgents({ tenantId: ref.tenantId, actor: "alice" }));
    expect(claims).toMatchObject({ aud: "agents", duties: ["agents.run.workflow"], exp: 1_700_000_060 });
    expect(claims?.le).toBeUndefined();
    expect(() => signer.mintForAgents({ tenantId: "", actor: "alice" })).toThrow(TypeError);
  });
});
