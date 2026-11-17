# Agents

**As built (v1.0):** `services/agents-ts` (agent runtime, copilot, knowledge retrieval under `/api/v1/*`) and
`services/mcp-ts` (MCP gateway, `POST /mcp` on `mcp.depot.<tenant>`). `agents-py` and `model-serving-py` do not exist;
`services/prediction-py` is a separate service (sales component). Agent runs execute as Temporal workflows in
`workflow-ts`. The AI section was not audited beyond these deployables; the rest below is design.

## Purpose & Boundaries
Governed agents that act for a user and never exceed the user's duties; a read-only copilot; an MCP gateway that
exposes generated tools under the caller's duties; retrieval over help, metadata and messages; evals in `evals/`.
Agents are advisory: no ERP process depends on the model.

## Services
| Directory | Lang | Prefix | Outbox |
|---|---|---|---|
| `services/agents-ts` | TS | `/api/v1/agents`, `/api/v1/copilot`, `/api/v1/knowledge` (D22) | outbox table, not relayed (D2) |
| `services/mcp-ts` | TS | `/mcp` (D22) | none |

## Interfaces
- agents-ts: `/api/v1/agents/runs`, `/api/v1/agents/{agentId}`, `/api/v1/agents/kill-switch`, `/api/v1/copilot/chat`, `/api/v1/knowledge/search`, `/api/v1/knowledge/chunks`.
- mcp-ts: `POST /mcp` (the design said `/v1/agents/mcp` inside agents-ts; pattern `mcp-gateway-path`, drift). `x-depot-tool` and `x-depot-approval` annotations appear in the agents-ts, mcp-ts, payables, receivables and prediction-py specs; writes go through approval-gated `propose_*` tools (design).
- Design (not audited): about 400 generated tools `depot.<domain>.<entity>.<verb>`, MCP tools/list filtered by duty; copilot read-only with a fixed core tool set; five business agents (`ap_coding`, `cash_application`, `mrp_triage`, `collections_outreach`, `journal_anomaly`).

## Data Ownership
agents-ts database (runs, actions, kill switches, budgets, knowledge chunks with pgvector retrieval); no legal-entity
policy (D17). Designed: `agent_run`, `agent_action` (7-year retention), `tool_definition`, `prompt_version`,
`eval_run`, `retrieval_chunk` (pgvector, HNSW), budgets.

## Events Produced
agents-ts names five event types in prose and declares no channel. *Target:* `agents.action.executed.v1`,
`agents.approval.requested.v1`, `agents.run.completed.v1`, `agents.killswitch.changed.v1`, `agents.budget.threshold.v1`.

## Events Consumed
None declared. Agent runs and their triggers run in `workflow-ts` (`agentRunWorkflow`).

## Dependencies
internal: platform (workflow-ts, identity), edge-identity (gateway, duties), every context (tools), analytics.
external: LLM provider through a provider-agnostic layer (Anthropic default), pgvector, Temporal.

## Gotchas & Constraints
- Permission inheritance: the agent acts as `agent:<id>@user:<sub>` and never exceeds the user (NFR-SEC6); agent on-behalf-of delegation for event-triggered runs is an open question, and Ed25519 service tokens carry the user as `act` (ADR-0010).
- Run states (design, matched by agents-ts per delta §2.2): PLANNING → ACTING → (AWAITING_APPROVAL → COMMITTED | REJECTED) | COMMITTED | FAILED | KILLED; kill switch effective within 10 s.
- Model request parameters (design): set `output_config.effort` explicitly and never send sampling overrides or forced tool choice; agents-ts sends no `output_config` today (pattern `llm-request-params`, missing).
- PII is pseudonymized before provider calls; untrusted data is wrapped and disables auto mode; AP auto mode codes a draft invoice only.
- Eval thresholds are marked assumed in the design (companion PRD).

## Target — not built
- `agents-py` eval service and `model-serving-py` (embedding, reranker, Presidio, anomaly scorer); evals live in `evals/` as a TS package with its own `NOT-IMPLEMENTED.md`.
- Agent events and relay (D2); `/v1` prefixes (D22); legal-entity policy (D17).

## Source Sections
- Architecture AI Feature Design §3 components and deployment (as built note), §5 MCP gateway and tool design, §9 confidence and auto mode, §12 model selection; backend §5.19 agents endpoints (target).
- ADR-0008, ADR-0010; delta §1.1 (agents-ts, mcp-ts, prediction-py), §2.1 (agents row), §2.2 (`status-agent-run`, `mcp-gateway-path`, `llm-request-params`).
