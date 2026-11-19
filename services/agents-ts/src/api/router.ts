import { randomUUID } from "node:crypto";
import type { IncomingMessage, ServerResponse } from "node:http";
import type { KillSwitchRegistry } from "../domain/killswitch.js";
import { type ApprovalStore, handleApprovalDecision } from "./decision.js";
import { HEADER as SERVICE_TOKEN_HEADER, ServiceTokenError, type ServiceTokenVerifier } from "./serviceToken.js";
import { StepRefusal, type RunSteps } from "../runtime/steps.js";
import { handleKillSwitchCommand, type KillSwitchWiring, killSwitchStatusOf, readKillSwitchStatus } from "./killswitch.js";
import { COPILOT_DUTY, type CopilotService } from "../runtime/copilot.js";
import { parseChunks, parseSearch, type KnowledgeService } from "../runtime/knowledge.js";
import type { ActionService } from "../runtime/actions.js";
import type { RunService } from "../runtime/service.js";
import { handleChat } from "./copilot.js";
import type { AgentBudgetService } from "./agentBudgets.js";
import { type AiPolicyStore, handleGetAiDataPolicy, handlePutAiDataPolicy } from "./aiDataPolicy.js";
import type { Authenticate } from "./auth.js";
import { ApiError, requireDuty, toApiError } from "./principal.js";
import { handleGenerate, handleGet, handleList, handleMarkRead } from "./briefings.js";
import type { BriefingService } from "../runtime/briefing.js";
import { bearerToken, handleGetRun, handleListActions, handleStartRun } from "./runs.js";

/** 64 KiB is far above any control payload in this API. */
export const MAX_BODY_BYTES = 64 * 1024;

export type AgentsApiDeps = AgentsApiBase & KillSwitchWiring;

export interface AgentsApiBase {
  /** Who the caller is: the verified bearer token (auth.ts createAuthenticator), never the edge headers alone. */
  authenticate: Authenticate;
  registry: KillSwitchRegistry;
  approvals: ApprovalStore;
  /** Run start and read endpoints; absent in a process that serves only the control routes. */
  runs?: RunService;
  /** Decline of an ungated action and undo; absent and an action with no approval request answers 404. */
  actions?: ActionService;
  /** The copilot chat endpoint. */
  copilot?: CopilotService;
  /** The knowledge index and its hybrid search; absent and the knowledge routes answer 404. */
  knowledge?: KnowledgeService;
  /** The budgets list and per-agent cap routes; absent and they answer 404. */
  budgets?: AgentBudgetService;
  /** On-demand briefings; absent and their routes answer 404. */
  briefings?: BriefingService;
  /** The tenant AI data policy; absent and its route answers 404. */
  aiPolicy?: AiPolicyStore;
  now?: () => Date;
  /** The run-step routes workflow-ts calls (AGENTS_RUN_ENGINE=temporal); absent and they answer 404. */
  steps?: { service: RunSteps; verifier: ServiceTokenVerifier };
}

const UUID = "[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}";
const KILL_PATH = "/api/v1/agents/kill-switch";
const AVAILABILITY_PATH = "/api/v1/agents/availability";
const AI_POLICY_PATH = "/api/v1/agents/ai-data-policy";
const START_PATH = /^\/api\/v1\/agents\/([a-z][a-z0-9_]{0,63})\/runs$/;
const RUN_PATH = new RegExp(`^/api/v1/agents/runs/(${UUID})$`);
const ACTIONS_PATH = new RegExp(`^/api/v1/agents/runs/(${UUID})/actions$`);
const BRIEFINGS_PATH = "/api/v1/agents/briefings";
const BRIEFING_GENERATE_PATH = "/api/v1/agents/briefings:generate";
const BRIEFING_PATH = new RegExp(`^/api/v1/agents/briefings/(${UUID})$`);
const BRIEFING_READ_PATH = new RegExp(`^/api/v1/agents/briefings/(${UUID}):mark-read$`);
const BUDGETS_PATH = "/api/v1/agents/budgets";
const BUDGET_AGENT_PATH = /^\/api\/v1\/agents\/budgets\/([a-z][a-z0-9_]{0,63})$/;
const CHAT_PATH = "/api/v1/copilot/chat";
const CONVERSATIONS_PATH = "/api/v1/agents/copilot/conversations";
const MESSAGES_PATH = new RegExp(`^/api/v1/agents/copilot/conversations/(${UUID})/messages$`);
const KNOWLEDGE_CHUNKS_PATH = "/api/v1/knowledge/chunks";
const KNOWLEDGE_SEARCH_PATH = "/api/v1/knowledge/search";
const STEP_PATH = new RegExp(`^/api/v1/agents/runs/(${UUID})/steps/(begin|plan|resolve|commit|abort)$`);
const DECISION_PATH = new RegExp(`^/api/v1/agents/runs/(${UUID})/actions/(${UUID})/decision$`);
const UNDO_PATH = new RegExp(`^/api/v1/agents/runs/(${UUID})/actions/(${UUID}):undo$`);

async function readBody(req: IncomingMessage, emptyAllowed = false): Promise<unknown> {
  const chunks: Buffer[] = [];
  let size = 0;
  for await (const chunk of req) {
    const buf = typeof chunk === "string" ? Buffer.from(chunk) : (chunk as Buffer);
    size += buf.length;
    if (size > MAX_BODY_BYTES) throw new ApiError(413, "AGT-0413", "request body exceeds 64 KiB");
    chunks.push(buf);
  }
  if (size === 0) {
    if (emptyAllowed) return {};
    throw new ApiError(400, "AGT-0400", "a JSON body is required");
  }
  try {
    return JSON.parse(Buffer.concat(chunks).toString("utf8")) as unknown;
  } catch {
    throw new ApiError(400, "AGT-0400", "body is not valid JSON");
  }
}

function headerValue(raw: string | string[] | undefined): string | undefined {
  return Array.isArray(raw) ? raw[0] : raw;
}

function send(res: ServerResponse, status: number, body: unknown, extra: Record<string, string> = {}): void {
  const payload = JSON.stringify(body);
  res.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "cache-control": "no-store",
    "content-length": Buffer.byteLength(payload).toString(),
    ...extra,
  });
  res.end(payload);
}

const TITLES: Readonly<Record<number, string>> = {
  400: "Bad Request",
  401: "Unauthorized",
  403: "Forbidden",
  404: "Not Found",
  405: "Method Not Allowed",
  409: "Conflict",
  412: "Precondition Failed",
  413: "Content Too Large",
  422: "Unprocessable Content",
  428: "Precondition Required",
  429: "Too Many Requests",
  500: "Internal Server Error",
  503: "Service Unavailable",
};

/** RFC 9457 problem details in the kernel's shape: type, title, status, code, detail. */
function problem(res: ServerResponse, err: ApiError, path: string, extra: Record<string, string> = {}): void {
  const payload = JSON.stringify({
    ...err.members,
    type: `https://depot.dev/problems/${err.code}`,
    title: TITLES[err.status] ?? "Error",
    status: err.status,
    code: err.code,
    detail: err.message,
    instance: path,
  });
  res.writeHead(err.status, {
    "content-type": "application/problem+json",
    "cache-control": "no-store",
    "content-length": Buffer.byteLength(payload).toString(),
    "x-content-type-options": "nosniff",
    ...err.headers,
    ...extra,
  });
  res.end(payload);
}

class MethodNotAllowed extends ApiError {
  readonly allow: string;

  constructor(allow: string) {
    super(405, "AGT-0405", "method not allowed");
    this.allow = allow;
  }
}

/** A run step for workflow-ts: authenticated by its signed service token (tenant-level, grant `agents.run.workflow`), never by a user token. */
async function handleStep(steps: NonNullable<AgentsApiBase["steps"]>, req: IncomingMessage, runId: string, step: string, body: unknown): Promise<unknown> {
  const raw = req.headers[SERVICE_TOKEN_HEADER];
  const token = Array.isArray(raw) ? raw[0] : raw;
  let tenantId: string;
  let actor: string;
  try {
    const claims = steps.verifier.verify(token ?? "");
    if (!claims.duties.includes("agents.run.workflow")) throw new ServiceTokenError("no run duty");
    tenantId = claims.tid.toLowerCase();
    actor = claims.act;
  } catch {
    throw new ApiError(401, "AGT-0401", "a valid service token is required");
  }
  const obj = typeof body === "object" && body !== null && !Array.isArray(body) ? (body as Record<string, unknown>) : {};
  try {
    // The token names the user who started the run as its actor; a token for another user's run is refused.
    await steps.service.assertActor(tenantId, runId, actor);
    switch (step) {
      case "begin":
        return await steps.service.begin(tenantId, runId);
      case "plan":
        return await steps.service.plan(tenantId, runId);
      case "resolve":
        return await steps.service.resolve(tenantId, runId, obj["expire"] === true);
      case "commit":
        return await steps.service.commit(tenantId, runId);
      default:
        return await steps.service.abort(tenantId, runId, typeof obj["reason"] === "string" ? obj["reason"].slice(0, 500) : "run aborted");
    }
  } catch (err) {
    if (err instanceof StepRefusal) throw new ApiError(err.status as 404, err.code as "AGT-0404", err.message);
    throw err;
  }
}

/**
 * The agents HTTP surface served in this slice: the kill switch (read and
 * toggle) and the approval decision. Every other route answers 404. A toggle's
 * event is published before the 200 is sent.
 */
export function createAgentsHandler(deps: AgentsApiDeps): (req: IncomingMessage, res: ServerResponse) => Promise<void> {
  const now = deps.now ?? (() => new Date());
  return async (req, res) => {
    const path = new URL(req.url ?? "/", "http://agents.internal").pathname;
    try {
      if (path === KILL_PATH) {
        if (req.method === "GET") {
          send(res, 200, readKillSwitchStatus(await deps.authenticate(req.headers), deps.registry));
          return;
        }
        if (req.method !== "POST") throw new MethodNotAllowed("GET, POST");
        const principal = await deps.authenticate(req.headers);
        const out = await handleKillSwitchCommand({
          principal,
          body: await readBody(req),
          registry: deps.registry,
          now,
          ...("log" in deps ? { log: deps.log } : { versions: deps.versions, publish: deps.publish }),
        });
        send(res, 200, out.body);
        return;
      }
      if (path === AVAILABILITY_PATH) {
        if (req.method !== "GET") throw new MethodNotAllowed("GET");
        const principal = await deps.authenticate(req.headers);
        requireDuty(principal, COPILOT_DUTY, "the assistant availability");
        const { halted, stale } = killSwitchStatusOf(principal, deps.registry);
        send(res, 200, {
          killSwitch: { halted, stale },
          budgetStatus: deps.budgets === undefined ? "OK" : await deps.budgets.status(principal),
          copilotEnabled: deps.copilot !== undefined,
        });
        return;
      }
      if (path === AI_POLICY_PATH) {
        if (deps.aiPolicy === undefined) throw new ApiError(404, "AGT-0404", "no such route");
        if (req.method !== "GET" && req.method !== "PUT") throw new MethodNotAllowed("GET, PUT");
        const principal = await deps.authenticate(req.headers);
        const policy =
          req.method === "GET"
            ? await handleGetAiDataPolicy({ principal, store: deps.aiPolicy })
            : await handlePutAiDataPolicy({ principal, ifMatch: headerValue(req.headers["if-match"]), body: await readBody(req), store: deps.aiPolicy, now });
        send(res, 200, policy, { etag: `"${policy.version}"` });
        return;
      }
      if (path === CHAT_PATH) {
        if (deps.copilot === undefined) throw new ApiError(404, "AGT-0404", "no such route");
        if (req.method !== "POST") throw new MethodNotAllowed("POST");
        const principal = await deps.authenticate(req.headers);
        await handleChat({ principal, token: bearerToken(req.headers), body: await readBody(req), accept: String(req.headers["accept"] ?? ""), copilot: deps.copilot, res });
        return;
      }
      if (path === CONVERSATIONS_PATH) {
        if (deps.copilot === undefined) throw new ApiError(404, "AGT-0404", "no such route");
        if (req.method !== "POST") throw new MethodNotAllowed("POST");
        requireDuty(await deps.authenticate(req.headers), COPILOT_DUTY, "the copilot");
        send(res, 201, { conversationId: randomUUID() });
        return;
      }
      if (MESSAGES_PATH.test(path)) {
        if (deps.copilot === undefined) throw new ApiError(404, "AGT-0404", "no such route");
        if (req.method !== "POST") throw new MethodNotAllowed("POST");
        const principal = await deps.authenticate(req.headers);
        await handleChat({ principal, token: bearerToken(req.headers), body: await readBody(req), accept: String(req.headers["accept"] ?? ""), copilot: deps.copilot, res });
        return;
      }
      if (path === KNOWLEDGE_CHUNKS_PATH || path === KNOWLEDGE_SEARCH_PATH) {
        if (deps.knowledge === undefined) throw new ApiError(404, "AGT-0404", "no such route");
        if (req.method !== "POST") throw new MethodNotAllowed("POST");
        const principal = await deps.authenticate(req.headers);
        if (path === KNOWLEDGE_CHUNKS_PATH) {
          requireDuty(principal, "knowledge.write", "the knowledge index");
          send(res, 200, await deps.knowledge.index(principal.tenantId, parseChunks(await readBody(req))));
        } else {
          requireDuty(principal, COPILOT_DUTY, "the knowledge search");
          const q = parseSearch(await readBody(req));
          send(res, 200, await deps.knowledge.search(principal.tenantId, q.query, q.limit));
        }
        return;
      }
      const briefingGet = BRIEFING_PATH.exec(path);
      const briefingRead = BRIEFING_READ_PATH.exec(path);
      if (path === BRIEFINGS_PATH || path === BRIEFING_GENERATE_PATH || briefingGet !== null || briefingRead !== null) {
        if (deps.briefings === undefined) throw new ApiError(404, "AGT-0404", "no such route");
        const method = path === BRIEFING_GENERATE_PATH || briefingRead !== null ? "POST" : "GET";
        if (req.method !== method) throw new MethodNotAllowed(method);
        const principal = await deps.authenticate(req.headers);
        const briefings = deps.briefings;
        if (path === BRIEFING_GENERATE_PATH) send(res, 202, await handleGenerate({ principal, briefings }));
        else if (briefingRead !== null) send(res, 200, await handleMarkRead({ principal, id: (briefingRead[1] ?? "").toLowerCase(), briefings }));
        else if (briefingGet !== null) send(res, 200, await handleGet({ principal, id: (briefingGet[1] ?? "").toLowerCase(), briefings }));
        else send(res, 200, await handleList({ principal, params: new URL(req.url ?? "/", "http://agents.internal").searchParams, briefings }));
        return;
      }
      const budgetAgent = BUDGET_AGENT_PATH.exec(path);
      if (path === BUDGETS_PATH || budgetAgent !== null) {
        if (deps.budgets === undefined) throw new ApiError(404, "AGT-0404", "no such route");
        if (budgetAgent === null) {
          if (req.method !== "GET") throw new MethodNotAllowed("GET");
          send(res, 200, await deps.budgets.list(await deps.authenticate(req.headers)));
        } else {
          if (req.method !== "PUT") throw new MethodNotAllowed("PUT");
          const principal = await deps.authenticate(req.headers);
          send(res, 200, await deps.budgets.setCap(principal, budgetAgent[1] ?? "", await readBody(req)));
        }
        return;
      }
      const start = START_PATH.exec(path);
      if (start !== null && deps.runs !== undefined) {
        if (req.method !== "POST") throw new MethodNotAllowed("POST");
        const principal = await deps.authenticate(req.headers);
        const started = await handleStartRun({ principal, token: bearerToken(req.headers), agentId: start[1] ?? "", body: await readBody(req), runs: deps.runs });
        send(res, 202, started);
        return;
      }
      const step = STEP_PATH.exec(path);
      if (step !== null) {
        if (deps.steps === undefined) throw new ApiError(404, "AGT-0404", "no such route");
        if (req.method !== "POST") throw new MethodNotAllowed("POST");
        send(res, 200, await handleStep(deps.steps, req, (step[1] ?? "").toLowerCase(), step[2] ?? "", await readBody(req).catch(() => ({}))));
        return;
      }
      const runMatch = RUN_PATH.exec(path) ?? ACTIONS_PATH.exec(path);
      if (runMatch !== null && deps.runs !== undefined) {
        if (req.method !== "GET") throw new MethodNotAllowed("GET");
        const principal = await deps.authenticate(req.headers);
        const runId = (runMatch[1] ?? "").toLowerCase();
        send(res, 200, path.endsWith("/actions") ? await handleListActions({ principal, runId, runs: deps.runs }) : await handleGetRun({ principal, runId, runs: deps.runs }));
        return;
      }
      const undo = UNDO_PATH.exec(path);
      if (undo !== null) {
        if (deps.actions === undefined) throw new ApiError(404, "AGT-0404", "no such route");
        if (req.method !== "POST") throw new MethodNotAllowed("POST");
        const principal = await deps.authenticate(req.headers);
        await deps.actions.undo(principal, (undo[1] ?? "").toLowerCase(), (undo[2] ?? "").toLowerCase(), await readBody(req, true));
        return;
      }
      const match = DECISION_PATH.exec(path);
      if (match === null) throw new ApiError(404, "AGT-0404", "no such route");
      if (req.method !== "POST") throw new MethodNotAllowed("POST");
      const principal = await deps.authenticate(req.headers);
      const runId = (match[1] ?? "").toLowerCase();
      const actionId = (match[2] ?? "").toLowerCase();
      const body = await readBody(req);
      if (deps.actions !== undefined && (await deps.approvals.find(principal.tenantId, runId, actionId)) === undefined) {
        send(res, 200, await deps.actions.decline(principal, runId, actionId, body));
        return;
      }
      const out = await handleApprovalDecision({
        principal,
        runId,
        actionId,
        body,
        store: deps.approvals,
        now,
      });
      await deps.runs?.decided(principal, bearerToken(req.headers), out);
      send(res, 200, out);
    } catch (err) {
      // Internal errors are never echoed to the caller.
      if (res.headersSent) {
        res.destroy();
        return;
      }
      const apiErr = toApiError(err);
      problem(res, apiErr, path, err instanceof MethodNotAllowed ? { allow: err.allow } : {});
    }
  };
}
