import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { Client, Connection } from "@temporalio/client";
import { NativeConnection, Worker } from "@temporalio/worker";
import { createDecisionHandler } from "./decisionApi.js";
import { createVerifier, parsePublicKeys } from "./agentsToken.js";
import { buildActivities } from "./activityRegistry.js";
import { jsonLine } from "./logging/pseudonymize.js";
import { eventConsumerConfigFromEnv, startEventConsumerInBackground } from "./eventConsumer.js";
import type { Stoppable } from "./backgroundStart.js";

const address = process.env.TEMPORAL_ADDRESS ?? "temporal-frontend.temporal.svc:7233";
const taskQueue = process.env.TEMPORAL_TASK_QUEUE ?? "depot-approvals";
// Under the eu-data-boundary overlay (or LOG_PSEUDONYMIZE=true) person values and email addresses are pseudonymized (#605).
const line = jsonLine(process.env);
const logAt =
  (level: "info" | "warn" | "error") =>
  (message: string, attributes: Record<string, string>): void => {
    line({ level, message, ...attributes });
  };
const warn = logAt("warn");
const connection = await NativeConnection.connect({ address });
const worker = await Worker.create({
  connection,
  namespace: process.env.TEMPORAL_NAMESPACE ?? "depot",
  taskQueue,
  activities: buildActivities(process.env, (path) => readFileSync(path, "utf8"), warn),
  workflowsPath: fileURLToPath(new URL("./workflows.js", import.meta.url)),
});

const clientConnection = await Connection.connect({ address });
const client = new Client({ connection: clientConnection, namespace: process.env.TEMPORAL_NAMESPACE ?? "depot" });
const decisions = createServer(createDecisionHandler(client, { taskQueue, ...(process.env.DEPOT_SERVICE_PUBLIC_KEYS ? { agentsVerifier: createVerifier({ keys: parsePublicKeys(process.env.DEPOT_SERVICE_PUBLIC_KEYS) }) } : {}) })).listen(Number(process.env.HTTP_PORT ?? 8080));
// Starts tenderWorkflow on transport's load.released and signals it on tender.accepted and tender.declined (#426). The consumer
// starts in the background and retries until its topics exist and the brokers answer, so it never stops the worker (D34).
const consumerConfig = eventConsumerConfigFromEnv(process.env);
const events: Stoppable | null = consumerConfig
  ? startEventConsumerInBackground(consumerConfig, client, taskQueue, { info: logAt("info"), warn, error: logAt("error") })
  : null;

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
