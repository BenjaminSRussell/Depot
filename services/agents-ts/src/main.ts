import { readFileSync } from "node:fs";
import { createServer } from "node:http";
import { pathToFileURL } from "node:url";
import { AGENT_IDS, defaultCatalog } from "./agents/catalog.js";
import { createAgentBudgetService } from "./api/agentBudgets.js";
import { createAuthenticator } from "./api/auth.js";
import { PgAgentBudgets } from "./store/agentBudgets.js";
import type { AgentsApiDeps } from "./api/router.js";
import { createProviderFactory, loadConfig, type Config } from "./config.js";
import { CopilotAnswerCache } from "./domain/copilot.js";
import { KILL_PROPAGATION_MS, KillSwitchRegistry } from "./domain/killswitch.js";
import { createLogger } from "./logging/pseudonymize.js";
import { createCopilotService } from "./runtime/copilot.js";
import { createKnowledgeService } from "./runtime/knowledge.js";
import { PgKeywordIndex } from "./store/knowledge.js";
import { createMcpConnector } from "./runtime/mcp.js";
import { createVerifier, parsePublicKeys } from "./api/serviceToken.js";
import { createRunSteps } from "./runtime/steps.js";
import { createWorkflowGateway } from "./runtime/workflowGateway.js";
import { serviceSignerFromEnv } from "./api/serviceSigner.js";
import { PgRunStateStore } from "./store/runstate.js";
import { createRunService } from "./runtime/service.js";
import { createAdminServer, createRequestHandler, listen, Metrics } from "./server.js";
import { PgApprovalStore } from "./store/approvals.js";
import { PgKillSwitchLog } from "./store/killswitch.js";
import { KillSwitchPoller } from "./runtime/killswitch-poller.js";
import { MIGRATE_COMMAND, migrateFromEnv, openAgentStore } from "./store/pg.js";
import { PgVersionStore } from "./store/registry.js";

export { loadConfig };

/** The version-1 catalog with each agent's model pinned: the override for its id, else the default model (FR-47.10). */
export function agentCatalog(cfg: Pick<Config, "model" | "agentModels">) {
  return defaultCatalog(cfg.model).map((a) => ({ ...a, modelId: cfg.agentModels[a.agentId] ?? a.modelId }));
}

// Under the eu-data-boundary overlay (or LOG_PSEUDONYMIZE=true) user and person values are pseudonymized before the line is written (#605).
const log = createLogger(process.env, "agents-ts");

async function main(): Promise<void> {
  const cfg = loadConfig(process.env);
  const { pool, store } = await openAgentStore(process.env);
  const inbox = new PgApprovalStore(store);
  // Every pod polls kill_switch_change (AC-47.6); a pod that cannot read it for 10 s fails closed, and a pod that has
  // not read it yet is closed from the start (and not ready).
  const registry = new KillSwitchRegistry(() => Date.now(), KILL_PROPAGATION_MS, { startSynced: false });
  const killLog = new PgKillSwitchLog(store, pool);
  const killPoller = new KillSwitchPoller(killLog, registry, (err) => log("warn", "kill switch poll failed", { error: String(err) }));
  await killPoller.start(cfg.killSwitchPollMs);
  const mcp = createMcpConnector({ baseUrl: cfg.mcpUrl });
  const providerFor = createProviderFactory(cfg);
  const agents = agentCatalog(cfg);
  // Every definition this pod runs is in agent_version; a run starts only when its newest eval_run passed (AGT-0204).
  const versions = new PgVersionStore(pool);
  await versions.register(agents);
  // agent_action is partitioned by month (migration 0006): keep this month and the next two in place, checked daily.
  const ensurePartitions = (): Promise<void> =>
    store.ensureActionPartitions(2).then(
      (made) => (made > 0 ? log("info", "agent_action partitions created", { made }) : undefined),
      (err: unknown) => log("warn", "agent_action partitions not created", { error: String(err) }),
    );
  await ensurePartitions();
  const partitionTimer = setInterval(() => void ensurePartitions(), 24 * 3_600_000);
  partitionTimer.unref();
  // AGENTS_RUN_ENGINE=temporal: runs are workflows (workflow-ts) that call these steps; the in-process engine stays the dev default.
  const approvalHours = cfg.approvalExpiryHours ?? 72;
  const serviceSigner = cfg.runEngine === "temporal" ? serviceSignerFromEnv(process.env, (path) => readFileSync(path, "utf8")) : undefined;
  if (cfg.runEngine === "temporal" && serviceSigner === undefined) throw new Error("AGENTS_RUN_ENGINE=temporal needs DEPOT_SERVICE_SIGNING_KEY or DEPOT_SERVICE_SIGNING_KEY_FILE: agents signs its calls to workflow-ts");
  const stepsService =
    cfg.runEngine === "temporal"
      ? createRunSteps({
          agents,
          providerFor,
          mcp,
          audit: store,
          state: new PgRunStateStore(store),
          inbox,
          killSwitch: registry,
          // The pseudonym map must outlive the longest approval wait, or a resumed run could not restore real values.
          pseudonymTtlMs: Math.max(24, approvalHours + 24) * 3_600_000,
        })
      : undefined;
  const runs = createRunService({
    ...(stepsService !== undefined && cfg.workflowUrl !== undefined
      ? { workflow: { steps: stepsService, state: new PgRunStateStore(store), gateway: createWorkflowGateway({ baseUrl: cfg.workflowUrl, signer: serviceSigner! }) } }
      : {}),
    agents,
    evalPassed: (agent) => versions.evalPassed(agent),
    providerFor,
    mcp,
    store,
    inbox,
    killSwitch: registry,
    config: {
      runTokenCap: cfg.runTokenCap,
      selfApprovalLimit: cfg.selfApprovalLimit,
      ...(cfg.approvalExpiryHours !== undefined ? { approvalExpiryHours: cfg.approvalExpiryHours } : {}),
      pseudonymTtlMs: 24 * 3_600_000,
    },
  });
  // Keyword half of the hybrid search only until an embedding port and a vector index are configured (NOT-IMPLEMENTED.md).
  const knowledge = createKnowledgeService({ keywords: new PgKeywordIndex(store) });
  const copilot = createCopilotService({
    knowledge,
    providerFor,
    mcp,
    store,
    killSwitch: registry,
    cache: new CopilotAnswerCache(),
    config: { tokenCap: cfg.copilotTokenCap, maxTurns: cfg.copilotMaxTurns, pseudonymTtlMs: 24 * 3_600_000 },
    modelId: cfg.copilotModel,
  });
  const api: AgentsApiDeps = {
    authenticate: createAuthenticator({
      issuer: cfg.jwt.issuer,
      audience: cfg.jwt.audience,
      jwksUrl: cfg.jwt.jwksUrl,
      jwks: cfg.jwt.jwksFile !== undefined ? (JSON.parse(readFileSync(cfg.jwt.jwksFile, "utf8")) as { keys: [] }) : undefined,
    }),
    registry,
    // The toggle, its version and its killswitch.changed outbox row are stored in one transaction.
    log: killLog,
    approvals: inbox,
    runs,
    ...(stepsService !== undefined ? { steps: { service: stepsService, verifier: createVerifier({ keys: parsePublicKeys(cfg.servicePublicKeys) }) } } : {}),
    copilot,
    knowledge,
    budgets: createAgentBudgetService({ store: new PgAgentBudgets(store) }),
  };
  const metrics = new Metrics();
  const handler = createRequestHandler({
    api,
    metrics,
    ready: async () => {
      await pool.query("select 1");
      return !registry.isStale();
    },
  });
  const admin = await listen(createAdminServer(metrics), cfg.adminHost, cfg.adminPort);
  const server = await listen(createServer(handler), cfg.host, cfg.port);
  log("info", "agents listening", { provider: cfg.provider, model: cfg.model, agents: AGENT_IDS.length });
  process.once("SIGTERM", () => {
    killPoller.stop();
    clearInterval(partitionTimer);
    admin.close();
    server.close(() => void pool.end().finally(() => process.exit(0)));
  });
}

// Only the entrypoint starts the server; importing this module (tests) has no side effects.
// `node dist/main.js migrate` applies the migrations as the owner role and exits: the chart's PreSync migration Job (D28).
const isEntrypoint = process.argv[1] !== undefined && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isEntrypoint && process.argv[2] === MIGRATE_COMMAND) {
  migrateFromEnv(process.env).then(
    () => {
      process.stdout.write(`${JSON.stringify({ level: "info", message: "migrations applied", service: "agents-ts" })}\n`);
      process.exit(0);
    },
    (err: unknown) => {
      process.stderr.write(`${JSON.stringify({ level: "error", message: "migrations failed", service: "agents-ts", error: String(err) })}\n`);
      process.exit(1);
    },
  );
} else if (isEntrypoint) {
  main().catch((err: unknown) => {
    process.stderr.write(`${JSON.stringify({ level: "error", message: "agents failed to start", service: "agents-ts", error: String(err) })}\n`);
    process.exit(1);
  });
}
