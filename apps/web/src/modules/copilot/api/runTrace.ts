import { z } from "zod";

export const RUN_TRACE_PATH = "/api/v1/agents/runs";

const dataUsedSchema = z.object({
  entityType: z.string().min(1),
  recordId: z.string().min(1),
  label: z.string(),
});

const actionSchema = z.object({
  seq: z.number().int().positive(),
  toolName: z.string(),
  actionId: z.string().min(1),
  rationale: z.string().optional(),
  confidence: z.number().min(0).max(1).optional(),
  dataUsed: z.array(dataUsedSchema).optional(),
});

export const runTraceSchema = z.object({
  run: z.object({
    runId: z.string().min(1),
    agentId: z.string().min(1),
    agentVersion: z.number().int().positive(),
  }),
  actions: z.array(actionSchema),
});

export type RunTrace = z.infer<typeof runTraceSchema>;
export type RunTraceAction = RunTrace["actions"][number];
export type DataUsedRecord = z.infer<typeof dataUsedSchema>;
