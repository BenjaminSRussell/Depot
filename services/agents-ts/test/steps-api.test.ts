import { createPrivateKey, createPublicKey, randomBytes, sign } from "node:crypto";
import { readFileSync } from "node:fs";
import { afterEach, describe, expect, it } from "vitest";
import { defaultCatalog } from "../src/agents/catalog.js";
import { createVerifier, DUTY_RUN_WORKFLOW, parsePublicKeys, SERVICE_GRANTS, SERVICE_NAME } from "../src/api/serviceToken.js";
import { createRunService } from "../src/runtime/service.js";
import { createRunSteps } from "../src/runtime/steps.js";
import { WorkflowRefused, type WorkflowGateway } from "../src/runtime/workflowGateway.js";
import { AUDIT_GRANT, COMPLIANCE_GRANT, WORKFLOW_GRANT } from "../src/api/serviceSigner.js";
import { tokenFor } from "./jwt.js";
import { proposal, ScriptedProvider } from "./fakes.js";
import { FakeMcp, headers, MemoryStore, OTHER, serve, T1, tool, USER } from "./surface-fakes.js";
import { MemState } from "./memstate.js";

const AP_DUTIES = "agents.invoke,ap.invoice.read,ap.vendor.read,ledger.account.read,tax.code.read,ap.invoice.propose";
const PROPOSE = "depot.ap.invoice.propose_coding";
const PKCS8 = Buffer.from("302e020100300506032b657004220420", "hex");
const seed = randomBytes(32);
const publicB64 = createPublicKey(createPrivateKey({ key: Buffer.concat([PKCS8, seed]), format: "der", type: "pkcs8" })).export({ format: "der", type: "spki" }).subarray(-32).toString("base64");
const NOW = Date.now();

function mint(over: Record<string, unknown> = {}): string {
  const iat = Math.floor(NOW / 1000);
  const claims = { iss: "workflow-ts", aud: "agents", tid: T1, act: USER, duties: [DUTY_RUN_WORKFLOW], iat, exp: iat + 60, jti: randomBytes(8).toString("hex"), ...over };
  const body = Buffer.from(JSON.stringify(claims)).toString("base64url");
  const sig = sign(null, Buffer.from(`depot.service-token.v1.${body}`), createPrivateKey({ key: Buffer.concat([PKCS8, seed]), format: "der", type: "pkcs8" })).toString("base64url");
  return `${body}.${sig}`;
}

const setup = async () => {
  const store = new MemoryStore();
  const state = new MemState();
  const mcp = new FakeMcp([tool(PROPOSE, "ap.invoice.propose", "suggest")], { [PROPOSE]: { ok: true, output: { id: "coding-1" }, ref: "coding-1" } });
  const provider = new ScriptedProvider([proposal(0.9, "1200", PROPOSE, { invoiceId: "INV-7" })]);
  const agents = defaultCatalog("claude-sonnet-5-5");
  const gateway = { started: [] as string[], woken: [] as string[], failStart: false, startErrors: [] as Error[], wakeFails: false } as WorkflowGateway & { started: string[]; woken: string[]; failStart: boolean; startErrors: Error[]; wakeFails: boolean };
  gateway.start = async (_c, runId) => {
    if (gateway.failStart) throw new WorkflowRefused("the workflow service answered 503");
    const next = gateway.startErrors.shift();
    if (next) throw next;
    gateway.started.push(runId);
  };
  gateway.approvalDecided = async (_c, runId, actionId) => {
    if (gateway.wakeFails) throw new WorkflowRefused("the workflow service answered 403");
    gateway.woken.push(`${runId}/${actionId}`);
  };
  const steps = createRunSteps({
    agents,
    providerFor: () => provider,
    mcp,
    audit: store,
    state,
    inbox: store,
    killSwitch: { isHalted: () => false },
    riskFor: () => ({ assess: () => ({ vendorHighRisk: false, sodConflict: false }) }),
    pseudonymTtlMs: 86_400_000,
  });
  const runs = createRunService({
    agents,
    evalPassed: async () => true,
    providerFor: () => provider,
    mcp,
    store,
    inbox: store,
    killSwitch: { isHalted: () => false },
    config: { runTokenCap: 50_000, selfApprovalLimit: { amount: "0", currency: "USD" }, pseudonymTtlMs: 60_000 },
    workflow: { steps, state, gateway },
  });
  const verifier = createVerifier({ keys: parsePublicKeys(`workflow-ts=${publicB64}`), now: () => NOW });
  const srv = await serve({ approvals: store, runs, steps: { service: steps, verifier } });
  return { store, state, mcp, provider, gateway, srv };
};

let close: (() => Promise<void>) | undefined;
afterEach(async () => {
  await close?.();
  close = undefined;
});

const step = (base: string, runId: string, name: string, token: string | undefined, body: unknown = {}) =>
  fetch(`${base}/api/v1/agents/runs/${runId}/steps/${name}`, { method: "POST", headers: token === undefined ? {} : { "x-depot-service-token": token }, body: JSON.stringify(body) });

describe("agent runs as workflows (AGENTS_RUN_ENGINE=temporal)", () => {
  it("start answers 202 with the run id and starts the workflow; the steps then run, an approval wakes the workflow and the commit runs as the approver", async () => {
    const s = await setup();
    close = s.srv.close;
    const res = await fetch(`${s.srv.base}/api/v1/agents/ap_coding/runs`, { method: "POST", headers: headers(AP_DUTIES), body: JSON.stringify({ prompt: "Code invoice INV-7 for Acme" }) });
    expect(res.status).toBe(202);
    const { runId, status } = (await res.json()) as { runId: string; status: string };
    expect(status).toBe("RUNNING");
    expect(s.gateway.started).toEqual([runId]);
    expect(s.mcp.connects).toHaveLength(0); // nothing runs in this process
    expect(s.provider.seen).toHaveLength(0);

    // What the workflow does, one activity at a time.
    const t = mint();
    expect(await (await step(s.srv.base, runId, "begin", t)).json()).toEqual({ next: "plan" });
    const planned = (await (await step(s.srv.base, runId, "plan", mint())).json()) as { next: string; actionId: string };
    expect(planned.next).toBe("await_approval");
    expect(s.mcp.connects[0]).toMatchObject({ userId: USER, token: tokenFor(T1, USER, AP_DUTIES) });

    const decide = await fetch(`${s.srv.base}/api/v1/agents/runs/${runId}/actions/${planned.actionId}/decision`, { method: "POST", headers: headers("ap.invoice.propose", OTHER), body: JSON.stringify({ decision: "APPROVED" }) });
    expect(decide.status).toBe(200);
    expect(s.gateway.woken).toEqual([`${runId}/${planned.actionId}`]);
    expect(await (await step(s.srv.base, runId, "resolve", mint(), { expire: false })).json()).toEqual({ next: "commit" });
    expect(await (await step(s.srv.base, runId, "commit", mint())).json()).toEqual({ next: "done", status: "COMMITTED" });
    expect(s.mcp.connects.at(-1)).toMatchObject({ userId: OTHER, token: tokenFor(T1, OTHER, "ap.invoice.propose") });
    expect(s.mcp.calls.at(-1)).toEqual({ name: PROPOSE, args: { invoiceId: "INV-7" } });

    const view = (await (await fetch(`${s.srv.base}/api/v1/agents/runs/${runId}`, { headers: headers("agents.run.read") })).json()) as { run: { status: string } };
    expect(view.run.status).toBe("COMMITTED");
    const actions = (await (await fetch(`${s.srv.base}/api/v1/agents/runs/${runId}/actions`, { headers: headers("agents.run.read") })).json()) as { items: unknown[] };
    expect(actions.items).toEqual([expect.objectContaining({ toolName: PROPOSE, decision: "APPROVED", approverUserId: OTHER, committedEntityRef: "coding-1" })]);
    expect(s.state.creds.size).toBe(0);
  });

  it("the prompt and the starter's token stay in the run's own rows, not in anything sent to the workflow", async () => {
    const s = await setup();
    close = s.srv.close;
    await fetch(`${s.srv.base}/api/v1/agents/ap_coding/runs`, { method: "POST", headers: headers(AP_DUTIES), body: JSON.stringify({ prompt: "Remit to DE89370400440532013000" }) });
    expect(s.gateway.started.join()).not.toContain("DE89");
    const [row] = [...s.state.rows.values()];
    expect((row?.input as { prompt: string }).prompt).toContain("DE89370400440532013000");
  });

  const startRun = (base: string) => fetch(`${base}/api/v1/agents/ap_coding/runs`, { method: "POST", headers: headers(AP_DUTIES), body: JSON.stringify({ prompt: "x" }) });
  const decide = (base: string, runId: string, actionId: string, user: string, decision = "APPROVED") =>
    fetch(`${base}/api/v1/agents/runs/${runId}/actions/${actionId}/decision`, { method: "POST", headers: headers("ap.invoice.propose", user), body: JSON.stringify({ decision }) });
  async function awaiting(s: Awaited<ReturnType<typeof setup>>) {
    const { runId } = (await (await startRun(s.srv.base)).json()) as { runId: string };
    await step(s.srv.base, runId, "begin", mint());
    const planned = (await (await step(s.srv.base, runId, "plan", mint())).json()) as { actionId: string };
    return { runId, actionId: planned.actionId };
  }

  it("a start the workflow service refuses answers 502 AGT-0214 and leaves no state or token behind", async () => {
    const s = await setup();
    close = s.srv.close;
    s.gateway.failStart = true;
    const res = await startRun(s.srv.base);
    expect(res.status).toBe(502);
    expect(((await res.json()) as { code: string }).code).toBe("AGT-0214");
    expect(s.state.rows.size).toBe(0);
    expect(s.state.creds.size).toBe(0);
  });

  it("a start that timed out is tried again with the same run id, and answers 202 once the workflow service has it", async () => {
    const s = await setup();
    close = s.srv.close;
    s.gateway.startErrors = [new Error("timeout"), new Error("socket hang up")];
    const res = await startRun(s.srv.base);
    expect(res.status).toBe(202);
    const { runId } = (await res.json()) as { runId: string };
    expect(s.gateway.started).toEqual([runId]);
    expect(s.state.rows.size).toBe(1);
  });

  it("a start that never gets an answer is given up after three tries and cleaned up", async () => {
    const s = await setup();
    close = s.srv.close;
    s.gateway.startErrors = [new Error("t"), new Error("t"), new Error("t")];
    expect((await startRun(s.srv.base)).status).toBe(502);
    expect(s.state.rows.size).toBe(0);
  });

  it("a wake the workflow service refuses (403) or loses does not fail the run: the decision is stored, the answer is 200 and the run still finds it", async () => {
    const s = await setup();
    close = s.srv.close;
    const { runId, actionId } = await awaiting(s);
    s.gateway.wakeFails = true;
    expect((await decide(s.srv.base, runId, actionId, OTHER)).status).toBe(200);
    expect(s.gateway.woken).toEqual([]);
    // The workflow's timer re-reads the stored decision; the approver's session was kept, so the commit goes ahead.
    expect(await (await step(s.srv.base, runId, "resolve", mint(), { expire: false })).json()).toEqual({ next: "commit" });
    expect(await (await step(s.srv.base, runId, "commit", mint())).json()).toEqual({ next: "done", status: "COMMITTED" });
  });

  it("the invoker replaying the approver's decision does not put their own token in the approver's place", async () => {
    const s = await setup();
    close = s.srv.close;
    const { runId, actionId } = await awaiting(s);
    expect((await decide(s.srv.base, runId, actionId, OTHER)).status).toBe(200);
    expect((await decide(s.srv.base, runId, actionId, USER)).status).toBe(200); // a replay of the stored decision
    expect(s.state.creds.get(`${T1}/${runId}/approver`)?.userId).toBe(OTHER);
    await step(s.srv.base, runId, "resolve", mint(), { expire: false });
    await step(s.srv.base, runId, "commit", mint());
    expect(s.mcp.connects.at(-1)?.userId).toBe(OTHER);
    const actions = (await (await fetch(`${s.srv.base}/api/v1/agents/runs/${runId}/actions`, { headers: headers("agents.run.read") })).json()) as { items: Array<{ approverUserId: string }> };
    expect(actions.items[0]?.approverUserId).toBe(OTHER);
  });

  it("a decision on a run that has ended keeps no token", async () => {
    const s = await setup();
    close = s.srv.close;
    const { runId, actionId } = await awaiting(s);
    await step(s.srv.base, runId, "abort", mint(), { reason: "gave up" });
    await decide(s.srv.base, runId, actionId, OTHER, "REJECTED");
    await decide(s.srv.base, runId, actionId, OTHER, "APPROVED");
    expect(s.state.creds.size).toBe(0);
  });

  it("a service token whose actor did not start the run is refused", async () => {
    const s = await setup();
    close = s.srv.close;
    const { runId } = (await (await startRun(s.srv.base)).json()) as { runId: string };
    expect((await step(s.srv.base, runId, "begin", mint({ act: OTHER }))).status).toBe(403);
    expect((await step(s.srv.base, runId, "begin", mint())).status).toBe(200);
  });

  it("the step routes answer only to a valid service token for the agents service", async () => {
    const s = await setup();
    close = s.srv.close;
    const run = "00000000-0000-7000-8000-00000000f001";
    for (const token of [undefined, "garbage", tokenFor(T1, USER, AP_DUTIES), mint({ aud: "payables" }), mint({ duties: ["agents.invoke"] }), mint({ le: "00000000-0000-7000-8000-0000000000a1" }), mint({ exp: Math.floor(NOW / 1000) - 5 })]) {
      expect((await step(s.srv.base, run, "plan", token)).status).toBe(401);
    }
    expect((await step(s.srv.base, run, "plan", mint())).status).toBe(404); // valid token, no such run
    const replay = mint({ jti: "same" });
    expect((await step(s.srv.base, run, "plan", replay)).status).toBe(404);
    expect((await step(s.srv.base, run, "plan", replay)).status).toBe(401);
    expect((await fetch(`${s.srv.base}/api/v1/agents/runs/${run}/steps/plan`, { method: "GET", headers: { "x-depot-service-token": mint() } })).status).toBe(405);
  });

  it("another tenant's token cannot reach a run", async () => {
    const s = await setup();
    close = s.srv.close;
    const res = await fetch(`${s.srv.base}/api/v1/agents/ap_coding/runs`, { method: "POST", headers: headers(AP_DUTIES), body: JSON.stringify({ prompt: "x" }) });
    const { runId } = (await res.json()) as { runId: string };
    expect((await step(s.srv.base, runId, "begin", mint({ tid: "00000000-0000-7000-8000-0000000000ff" }))).status).toBe(404);
  });
});

describe("agents service grants", () => {
  it("the grant agents holds on workflow-ts matches policies/authz/data.json and the Go table", () => {
    const root = new URL("../../../", import.meta.url);
    const data = JSON.parse(readFileSync(new URL("policies/authz/data.json", root), "utf8")) as { service_grants: Record<string, Record<string, string[]>> };
    expect(data.service_grants["agents"]?.["workflow-ts"]).toEqual([...WORKFLOW_GRANT]);
    const go = readFileSync(new URL("platform/go/tenant/service.go", root), "utf8");
    expect(go).toContain('"workflow-ts": {"agents.run.signal"}');
  });

  it("the grant agents holds on compliance matches policies/authz/data.json and the Go table", () => {
    const root = new URL("../../../", import.meta.url);
    const data = JSON.parse(readFileSync(new URL("policies/authz/data.json", root), "utf8")) as { service_grants: Record<string, Record<string, string[]>> };
    expect(data.service_grants["agents"]?.["compliance"]).toEqual([...COMPLIANCE_GRANT]);
    expect(readFileSync(new URL("platform/go/tenant/service.go", root), "utf8")).toContain('"compliance":  {"compliance.data-designation.resolve", "compliance.need-to-know.read"}');
  });

  it("the grant agents holds on audit matches policies/authz/data.json and the Go table", () => {
    const root = new URL("../../../", import.meta.url);
    const data = JSON.parse(readFileSync(new URL("policies/authz/data.json", root), "utf8")) as { service_grants: Record<string, Record<string, string[]>> };
    expect(data.service_grants["agents"]?.["audit"]).toEqual([...AUDIT_GRANT]);
    expect(readFileSync(new URL("platform/go/tenant/service.go", root), "utf8")).toContain('"audit":       {"audit.ai-access.write"}');
  });

  it("match policies/authz/data.json and the Go table", () => {
    const root = new URL("../../../", import.meta.url);
    const data = JSON.parse(readFileSync(new URL("policies/authz/data.json", root), "utf8")) as { service_grants: Record<string, Record<string, string[]>> };
    const fromData: Record<string, string[]> = {};
    for (const [iss, targets] of Object.entries(data.service_grants)) if (targets[SERVICE_NAME]) fromData[iss] = targets[SERVICE_NAME];
    expect(fromData).toEqual(SERVICE_GRANTS);
    const go = readFileSync(new URL("platform/go/tenant/service.go", root), "utf8");
    const table = go.slice(go.indexOf("var serviceGrants = "), go.indexOf("// ServiceGrantTable"));
    const fromGo: Record<string, string[]> = {};
    let issuer = "";
    for (const line of table.split("\n")) {
      const iss = /^\t"([^"]+)": \{$/.exec(line);
      if (iss) issuer = iss[1]!;
      const aud = /^\t\t"([^"]+)":\s*\{(.*)\},$/.exec(line);
      if (aud && aud[1] === SERVICE_NAME) fromGo[issuer] = [...aud[2]!.matchAll(/"([^"]+)"/g)].map((m) => m[1]!);
    }
    expect(fromGo).toEqual(SERVICE_GRANTS);
  });
});
