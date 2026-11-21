import { WorkflowExecutionAlreadyStartedError, WorkflowIdReusePolicy } from "@temporalio/client";
import type { Client } from "@temporalio/client";
import { ecoApprovalWorkflow, ecoApprovalWorkflowId, validateEcoApprovalInput, type EcoApprovalInput } from "./ecoApprovalWorkflow.js";
import { capaWorkflow, capaWorkflowId, validateCapaInput, type CapaInput } from "./capaWorkflow.js";
import { type PaymentRunInput } from "./paymentRunWorkflow.js";
import { startPaymentRunWorkflow } from "./startPaymentRun.js";

export const ecoSubmittedType = "dev.depot.engineering.eco.submitted.v1";
export const ecoSubmittedSource = "/depot/engineering/engineering";
export const nonconformanceCreatedType = "dev.depot.quality.nonconformance.created.v1";
export const nonconformanceCreatedSource = "quality";
export const paymentProposalApprovedType = "dev.depot.payables.paymentproposal.approved.v1";
export const paymentProposalApprovedSource = "payables";

export const CAPA_TRIGGER_ACTOR = "system:capa-trigger";
const DAY_MS = 24 * 60 * 60 * 1000;
export const ECO_APPROVAL_TIMEOUT_MS = 14 * DAY_MS;
export const CAPA_STEP_TIMEOUT_MS = 14 * DAY_MS;
export const CAPA_EFFECTIVENESS_TIMEOUT_MS = 14 * DAY_MS;
export const CAPA_MAX_REOPENS = 2;

export interface TriggerEvent {
  type: string;
  source: string;
  tenantid: string;
  actor?: unknown;
  data: Record<string, unknown>;
}

export type TriggerResult = { workflowId: string; started: boolean } | { skipped: string };

function expectEvent(event: TriggerEvent, type: string, source: string): void {
  if (event.type !== type) throw new TypeError(`expected ${type}, got ${event.type}`);
  if (event.source !== source) throw new TypeError(`${type} is accepted only from ${source}, got ${event.source}`);
}

function requiredText(value: unknown, name: string): string {
  if (typeof value !== "string" || value.trim() === "") throw new TypeError(`${name} must be a non-empty string`);
  return value;
}

function optionalText(value: unknown, name: string): string | undefined {
  if (value === undefined || value === null || value === "") return undefined;
  if (typeof value !== "string") throw new TypeError(`${name} must be a string`);
  return value;
}

async function startOnce(workflowId: string, start: (options: { workflowId: string; workflowIdReusePolicy: WorkflowIdReusePolicy }) => Promise<unknown>): Promise<{ workflowId: string; started: boolean }> {
  try {
    await start({ workflowId, workflowIdReusePolicy: WorkflowIdReusePolicy.REJECT_DUPLICATE });
    return { workflowId, started: true };
  } catch (error) {
    if (error instanceof WorkflowExecutionAlreadyStartedError) return { workflowId, started: false };
    throw error;
  }
}

export function toEcoApprovalInput(event: TriggerEvent): EcoApprovalInput | { skipped: string } {
  expectEvent(event, ecoSubmittedType, ecoSubmittedSource);
  const ecoId = requiredText(event.data.ecoId, "ecoId");
  const requester = requiredText(event.actor, "actor");
  const legalEntityId = optionalText(event.data.legalEntityId, "legalEntityId");
  if (!legalEntityId) return { skipped: `ECO ${ecoId} was submitted without a single legal entity in scope, so no approval workflow was started` };
  const input: EcoApprovalInput = { tenantId: event.tenantid, legalEntityId, ecoId, requester, approvalTimeoutMs: ECO_APPROVAL_TIMEOUT_MS };
  validateEcoApprovalInput(input);
  return input;
}

export function toCapaInput(event: TriggerEvent): CapaInput | { skipped: string } {
  expectEvent(event, nonconformanceCreatedType, nonconformanceCreatedSource);
  const ncrId = requiredText(event.data.ncr_id, "ncr_id");
  const legalEntityId = optionalText(event.data.legalEntityId, "legalEntityId");
  if (!legalEntityId) return { skipped: `Nonconformance ${ncrId} was raised without a single legal entity in scope, so no corrective-action workflow was started` };
  const input: CapaInput = {
    tenantId: event.tenantid,
    legalEntityId,
    ncrId,
    startedBy: CAPA_TRIGGER_ACTOR,
    stepTimeoutMs: CAPA_STEP_TIMEOUT_MS,
    effectivenessTimeoutMs: CAPA_EFFECTIVENESS_TIMEOUT_MS,
    maxReopens: CAPA_MAX_REOPENS,
  };
  validateCapaInput(input);
  return input;
}

export function toPaymentRunInput(event: TriggerEvent): PaymentRunInput {
  expectEvent(event, paymentProposalApprovedType, paymentProposalApprovedSource);
  return {
    tenantId: event.tenantid,
    legalEntityId: requiredText(event.data.legal_entity_id, "legal_entity_id"),
    proposalId: requiredText(event.data.proposal_id, "proposal_id"),
    executedBy: requiredText(event.data.actor, "actor"),
  };
}

/** Starts the approval workflow of a submitted ECO; a redelivered event finds it running or finished and starts no second one. */
export async function handleEcoSubmitted(client: Pick<Client, "workflow">, taskQueue: string, event: TriggerEvent): Promise<TriggerResult> {
  const input = toEcoApprovalInput(event);
  if ("skipped" in input) return input;
  return startOnce(ecoApprovalWorkflowId(input.ecoId), (options) => client.workflow.start(ecoApprovalWorkflow, { ...options, taskQueue, args: [input] }));
}

/** Starts the corrective-action workflow of a new nonconformance; the workflow itself waits for the NCR to be approved. */
export async function handleNonconformanceCreated(client: Pick<Client, "workflow">, taskQueue: string, event: TriggerEvent): Promise<TriggerResult> {
  const input = toCapaInput(event);
  if ("skipped" in input) return input;
  return startOnce(capaWorkflowId(input.ncrId), (options) => client.workflow.start(capaWorkflow, { ...options, taskQueue, args: [input] }));
}

/** Starts the payment run of an approved proposal; the id comes from entity and proposal, so a redelivery starts nothing. */
export async function handlePaymentProposalApproved(client: Pick<Client, "workflow">, taskQueue: string, event: TriggerEvent): Promise<TriggerResult> {
  return startPaymentRunWorkflow(client, taskQueue, toPaymentRunInput(event));
}
