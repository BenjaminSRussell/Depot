import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { Client, Connection } from "@temporalio/client";
import { NativeConnection, Worker } from "@temporalio/worker";
import { createDecisionHandler } from "./decisionApi.js";
import { createVerifier, parsePublicKeys } from "./agentsToken.js";
import { buildActivities } from "./activityRegistry.js";
import { jsonLine } from "./logging/pseudonymize.js";
import { eventConsumerConfigFromEnv, startEventConsumer, type RunningEventConsumer } from "./eventConsumer.js";

const address = process.env.TEMPORAL_ADDRESS ?? "temporal-frontend.temporal.svc:7233";
const taskQueue = process.env.TEMPORAL_TASK_QUEUE ?? "passage-approvals";
// Under the eu-data-boundary overlay (or LOG_PSEUDONYMIZE=true) person values and email addresses are pseudonymized (#605).
const line = jsonLine(process.env);
const warn = (message: string, attributes: Record<string, string>): void => {
  line({ level: "warn", message, ...attributes });
};
const connection = await NativeConnection.connect({ address });
const worker = await Worker.create({
  connection,
  namespace: process.env.TEMPORAL_NAMESPACE ?? "passage",
  taskQueue,
  activities: buildActivities(process.env, (path) => readFileSync(path, "utf8"), warn),
  workflowsPath: fileURLToPath(new URL("./workflows.js", import.meta.url)),
});

const clientConnection = await Connection.connect({ address });
const client = new Client({ connection: clientConnection, namespace: process.env.TEMPORAL_NAMESPACE ?? "passage" });
const decisions = createServer(createDecisionHandler(client, { taskQueue, ...(process.env.PASSAGE_SERVICE_PUBLIC_KEYS ? { agentsVerifier: createVerifier({ keys: parsePublicKeys(process.env.PASSAGE_SERVICE_PUBLIC_KEYS) }) } : {}) })).listen(Number(process.env.HTTP_PORT ?? 8080));
// Starts tenderWorkflow on transport's load.released and signals it on tender.accepted and tender.declined (#426).
const consumerConfig = eventConsumerConfigFromEnv(process.env);
const events: RunningEventConsumer | null = consumerConfig ? await startEventConsumer(consumerConfig, client, taskQueue, warn) : null;

const shutdown = (): void => {
  decisions.close();
  worker.shutdown();
};
process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);

try {
  await worker.run();
} finally {
  // The consumer stops before the Temporal client it starts and signals through.
  await events?.stop();
  await clientConnection.close();
  await connection.close();
}
