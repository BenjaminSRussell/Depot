import { describe, expect, it, vi } from "vitest";
import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy, type Client } from "@temporalio/client";
import {
  CAPA_TRIGGER_ACTOR,
  ECO_APPROVAL_TIMEOUT_MS,
  ecoSubmittedSource,
  ecoSubmittedType,
  handleEcoSubmitted,
  handleNonconformanceCreated,
  handlePaymentProposalApproved,
  nonconformanceCreatedSource,
  nonconformanceCreatedType,
  paymentProposalApprovedSource,
  paymentProposalApprovedType,
  type TriggerEvent,
} from "../src/workflowTriggers.js";
import { capaWorkflow } from "../src/capaWorkflow.js";
import { ecoApprovalWorkflow } from "../src/ecoApprovalWorkflow.js";
import { paymentRunWorkflow } from "../src/paymentRunWorkflow.js";

const TENANT = "00000000-0000-0000-0000-000000000001";
const ENTITY = "00000000-0000-0000-0000-0000000000a1";
const ECO = "00000000-0000-0000-0000-0000000000e1";
const NCR = "00000000-0000-0000-0000-0000000000c1";
const PROPOSAL = "00000000-0000-0000-0000-0000000000d1";
const QUEUE = "depot-approvals";

const eco = (overrides: Partial<TriggerEvent> = {}, data: Record<string, unknown> = {}): TriggerEvent => ({
  type: ecoSubmittedType,
  source: ecoSubmittedSource,
  tenantid: TENANT,
  actor: "engineer",
  data: { ecoId: ECO, ecoNumber: "ECO-1", affectedVersions: [], legalEntityId: ENTITY, ...data },
  ...overrides,
});

const ncr = (overrides: Partial<TriggerEvent> = {}, data: Record<string, unknown> = {}): TriggerEvent => ({
  type: nonconformanceCreatedType,
  source: nonconformanceCreatedSource,
  tenantid: TENANT,
  data: { ncr_id: NCR, source: "INTERNAL", legalEntityId: ENTITY, ...data },
  ...overrides,
});

const proposal = (overrides: Partial<TriggerEvent> = {}, data: Record<string, unknown> = {}): TriggerEvent => ({
  type: paymentProposalApprovedType,
  source: paymentProposalApprovedSource,
  tenantid: TENANT,
  data: { proposal_id: PROPOSAL, legal_entity_id: ENTITY, status: "APPROVED", actor: "approver", ...data },
  ...overrides,
});

function fakeClient(start = vi.fn().mockResolvedValue({})) {
  return { start, client: { workflow: { start } } as unknown as Pick<Client, "workflow"> };
}

const alreadyStarted = () => new WorkflowExecutionAlreadyStartedError("running", "wf", "type");

describe("handleEcoSubmitted", () => {
  it("starts the approval workflow with the requester, the event's entity and a 14 day window, once per ECO", async () => {
    const { start, client } = fakeClient();
    expect(await handleEcoSubmitted(client, QUEUE, eco())).toEqual({ workflowId: `eco-approval:${ECO}`, started: true });
    expect(start).toHaveBeenCalledWith(ecoApprovalWorkflow, {
      workflowId: `eco-approval:${ECO}`,
      taskQueue: QUEUE,
      args: [{ tenantId: TENANT, legalEntityId: ENTITY, ecoId: ECO, requester: "engineer", approvalTimeoutMs: ECO_APPROVAL_TIMEOUT_MS }],
      workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE,
    });
  });

  it("reports started:false on a redelivery while the workflow runs or after it finished", async () => {
    const { client } = fakeClient(vi.fn().mockRejectedValue(alreadyStarted()));
    expect(await handleEcoSubmitted(client, QUEUE, eco())).toEqual({ workflowId: `eco-approval:${ECO}`, started: false });
  });

  it.each([
    ["absent", undefined],
    ["empty", ""],
    ["null", null],
  ])("skips with a reason and starts nothing when legalEntityId is %s", async (_name, value) => {
    const { start, client } = fakeClient();
    const result = await handleEcoSubmitted(client, QUEUE, eco({}, { legalEntityId: value }));
    expect(result).toEqual({ skipped: expect.stringContaining(ECO) });
    expect(start).not.toHaveBeenCalled();
  });

  it.each([
    ["wrong source", eco({ source: "billing" })],
    ["wrong type", eco({ type: "dev.depot.engineering.eco.approved.v1" })],
    ["missing actor", eco({ actor: undefined })],
    ["empty actor", eco({ actor: "" })],
    ["numeric actor", eco({ actor: 7 })],
    ["numeric entity", eco({}, { legalEntityId: 7 })],
    ["object entity", eco({}, { legalEntityId: {} })],
    ["missing ecoId", eco({}, { ecoId: undefined })],
    ["numeric ecoId", eco({}, { ecoId: 3 })],
    ["missing tenant", eco({ tenantid: "" })],
  ])("refuses with a TypeError and starts nothing for %s", async (_name, event) => {
    const { start, client } = fakeClient();
    await expect(handleEcoSubmitted(client, QUEUE, event)).rejects.toBeInstanceOf(TypeError);
    expect(start).not.toHaveBeenCalled();
  });

  it("rethrows a Temporal outage so the message is retried", async () => {
    const { client } = fakeClient(vi.fn().mockRejectedValue(new Error("temporal unavailable")));
    await expect(handleEcoSubmitted(client, QUEUE, eco())).rejects.toThrow("temporal unavailable");
  });
});

describe("handleNonconformanceCreated", () => {
  it("starts the corrective-action workflow once per NCR as the trigger actor, with the 14 day timeouts and two reopens", async () => {
    const { start, client } = fakeClient();
    expect(await handleNonconformanceCreated(client, QUEUE, ncr())).toEqual({ workflowId: `capa:${NCR}`, started: true });
    expect(start).toHaveBeenCalledWith(capaWorkflow, {
      workflowId: `capa:${NCR}`,
      taskQueue: QUEUE,
      args: [
        {
          tenantId: TENANT,
          legalEntityId: ENTITY,
          ncrId: NCR,
          startedBy: CAPA_TRIGGER_ACTOR,
          stepTimeoutMs: 14 * 24 * 60 * 60 * 1000,
          effectivenessTimeoutMs: 14 * 24 * 60 * 60 * 1000,
          maxReopens: 2,
        },
      ],
      workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE,
    });
  });

  it("reports started:false on a redelivery", async () => {
    const { client } = fakeClient(vi.fn().mockRejectedValue(alreadyStarted()));
    expect(await handleNonconformanceCreated(client, QUEUE, ncr())).toEqual({ workflowId: `capa:${NCR}`, started: false });
  });

  it("skips with a reason when no single legal entity was in scope", async () => {
    const { start, client } = fakeClient();
    expect(await handleNonconformanceCreated(client, QUEUE, ncr({}, { legalEntityId: undefined }))).toEqual({ skipped: expect.stringContaining(NCR) });
    expect(start).not.toHaveBeenCalled();
  });

  it.each([
    ["wrong source", ncr({ source: "engineering" })],
    ["wrong type", ncr({ type: "dev.depot.quality.nonconformance.updated.v1" })],
    ["missing ncr_id", ncr({}, { ncr_id: undefined })],
    ["numeric ncr_id", ncr({}, { ncr_id: 5 })],
    ["numeric entity", ncr({}, { legalEntityId: 5 })],
  ])("refuses with a TypeError for %s", async (_name, event) => {
    const { start, client } = fakeClient();
    await expect(handleNonconformanceCreated(client, QUEUE, event)).rejects.toBeInstanceOf(TypeError);
    expect(start).not.toHaveBeenCalled();
  });
});

describe("handlePaymentProposalApproved", () => {
  it("starts the payment run for the approved proposal, executed by its approver", async () => {
    const { start, client } = fakeClient();
    expect(await handlePaymentProposalApproved(client, QUEUE, proposal())).toEqual({ workflowId: `payment-run:${ENTITY}:${PROPOSAL}`, started: true });
    expect(start).toHaveBeenCalledWith(paymentRunWorkflow, {
      workflowId: `payment-run:${ENTITY}:${PROPOSAL}`,
      taskQueue: QUEUE,
      args: [{ tenantId: TENANT, legalEntityId: ENTITY, proposalId: PROPOSAL, executedBy: "approver" }],
      workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE,
    });
  });

  it("reports started:false on a redelivery of the same proposal", async () => {
    const { client } = fakeClient(vi.fn().mockRejectedValue(alreadyStarted()));
    expect(await handlePaymentProposalApproved(client, QUEUE, proposal())).toEqual({ workflowId: `payment-run:${ENTITY}:${PROPOSAL}`, started: false });
  });

  it.each([
    ["wrong source", proposal({ source: "receivables" })],
    ["wrong type", proposal({ type: "dev.depot.payables.paymentproposal.created.v1" })],
    ["empty entity", proposal({}, { legal_entity_id: "" })],
    ["missing proposal", proposal({}, { proposal_id: undefined })],
    ["numeric proposal", proposal({}, { proposal_id: 9 })],
    ["object actor", proposal({}, { actor: {} })],
    ["empty actor", proposal({}, { actor: "" })],
  ])("refuses with a TypeError for %s", async (_name, event) => {
    const { start, client } = fakeClient();
    await expect(handlePaymentProposalApproved(client, QUEUE, event)).rejects.toBeInstanceOf(TypeError);
    expect(start).not.toHaveBeenCalled();
  });
});
