# NOT-IMPLEMENTED: copilot module (DPT-47, wave 47)

Slice 2 adds the streaming client: `api/stream.ts` parses `text/event-stream`
frames (`token`, `done`, `error`), validates the terminal `done` frame with the
same schema as the JSON path, and fails loudly when a stream ends before a
terminal frame. The panel calls `askCopilot`, which offers both response types:
it renders tokens as they arrive when the server streams, and reads the JSON
body when it does not. Slice 5 adds the agent kill switch page
(`KillSwitchPage.tsx`, `/copilot/kill-switch`, nav gated by agents.admin) on the
agent runtime's new `GET`/`POST /api/v1/agents/kill-switch`.

| Deliverable / criterion | Status | Reason |
|---|---|---|
| Server-sent streaming response from `POST /api/v1/copilot/chat` (FR-47.3, first-token p95 < 2 s, 9.47.10) | built except the live p95 | `services/agents-ts` streams the Anthropic adapter's answer as it is written (see its NOT-IMPLEMENTED); the p95 against a live model is tracked in #213. |
| Screens for the rest of the agent runtime: token budget status, provider and failover status (9.47.6) | later slice of DPT-47 | The kill switch screen is built. `services/agents-ts` serves no budget or provider endpoint yet (9.47.5 `GET /api/v1/agents/budgets/{tenant}` comes with the persisted budgets) |
| `POST /api/v1/copilot/chat` server | built | Served by `services/agents-ts` (`src/runtime/copilot.ts`): read tools as the user, duty gate AGT-0203, 5-minute per-user cache, budget hold, redaction, one audited run per turn. The `explain` mode reads only what the tools return; the knowledge search tool behind AGT-0212 is not built |
| `Ctrl+/` opens the copilot with the current record context (9.47.6) | later slice of DPT-47 | Needs the shell's keyboard and record context hooks (shell owner, wave 5) |
| Explain-this-record reads the record's audit trail and related documents (FR-47.3) | later slice of DPT-47 | The panel sends `recordRef`; the server side does the reading |
| Mode remembered per workspace per user: the panel saves and restores the mode through an injected `memory` store (`domain.ts`, `CopilotPanel`) | handoff | The shell (wave 5) must pass the browser store and the signed-in user id and workspace. Without it the mode is not remembered |
| Draft insertion into a host document with `origin=agent` and run id (BR-47.13) | later slice of DPT-47 | `canInsertDraft` is built; the host documents belong to waves 17 and 30 |
| Summarize workflow history and cases (FR-47.3) | later slice of DPT-47 | `summarize` is a mode with no server handler yet |
| Copilot enablement per legal entity (`copilot.enabled`, 9.47.13) | later slice of DPT-47 | Enabled by the server's response; the module does not check the flag |
| Localized glossary labels from the label framework (9.47.20) | later slice of DPT-47 | Labels are English constants in `domain.ts` |
| Approval decision screen, keyboard approval shortcuts `A`/`R` and agent inbox (FR-47.8, 9.47.6) | handoff | The decision endpoint is built in services/agents-ts (`POST /api/v1/agents/runs/{run_id}/actions/{id}/decision`). The screen that shows the proposal and its diff and calls it belongs to the workflow inbox (wave 4), which receives `approval.requested` |
| Agent runs list and timeline, agent administration (mode, thresholds), eval dashboard and token budget screens (9.47.6, 9.47.14) | later slice of DPT-47 | services/agents-ts serves only the kill switch and the approval decision so far; the runs, budgets and versions endpoints are not built, so there is nothing to list from. 9.47.5 names no read endpoint for agent definitions or versions, so the agent administration screen also needs one added to the API |
| Decline, undo (with `Cannot undo: <reason>` and the settlement confirmation) and the origin=agent marker on the record pages (DEPOT-287) | later slice of DEPOT-287 | The shared suggestion component with its badge, "Why this?" and "Data used" is built; the decision and undo clients, the undo control and the six record-page markers are the next slices. Settlement undo details (`undoPreview`) are populated by DEPOT-313 |
| `rationale`, `confidence` and `dataUsed` per action in `GET /api/v1/agents/runs/{id}` (DEPOT-287) | agents service follow-up | The web run-trace schema accepts them as optional; the agents run view does not return them yet, so "Why this?" shows the agent version and a plain no-rationale line, and "Data used" shows tools only |
| `/api/v1/mcp` route in the web BFF to the gateway's `POST /mcp` | handoff | The BFF belongs to the shell owner (wave 5); the gateway HTTP server is a later slice of DPT-47 (services/mcp-ts/NOT-IMPLEMENTED.md) |
| Ask over the conversations API (DEPOT-279): `createConversation`, then `POST /api/v1/agents/copilot/conversations/{id}/messages`, one answer component on the home and on `/copilot` | built | `postRoute` stays on `/api/v1/copilot/chat` (DEPOT-296). There is no BFF (ADR-0009); the existing gateway route carries the stream |
| Follow-ups remember earlier turns (DEPOT-279) | not built | The server stores nothing, so each follow-up is an independent turn that reuses the conversation id. The client does not claim memory |
| Typed citation refs on the server (DEPOT-279) | agents service follow-up | The server sets `sourceRef` to a tool name today. Only `record:<ctx>/<entity>/<id>` for an entity in `RECORD_PAGES` the user holds the duty for becomes a link (`citationRoute`); every other source is plain text, so nothing links until the server sends typed refs |
| Unverified-claim flags, the tool-call-limit note, the follow-up transcript list, the OpenAPI contract test and the withdrawn/excluded handling for the home answer (DEPOT-279 acceptance criteria 3, 6, 7, 8, 10) | later slice of DEPOT-279 | Slice 1 moves the client to the conversations API and renders the sourced answer inline. Numeric-grounding flags also wait for the server |
| Playwright cases for the ask path with the write-guard fixture, the 8-call-limit path and the two-persona real-stack test (DEPOT-279) | later slice of DEPOT-279 | Not run in slice 1; the ask path has component and client tests only |
| AI controls page (`BudgetsPage.tsx`, `/copilot/budgets`, nav gated by agentbudget.read) and agents page (`AgentsPage.tsx`, `/copilot/agents`, nav gated by agents.admin) (DEPOT-278) | built | Budgets read from `GET /api/v1/agents/budgets` with per-agent caps set through `PUT /api/v1/agents/budgets/{agentId}`; the tenant row is read-only because only the tenant budget stops runs. The agents page disables and enables through `POST /api/v1/agents/definitions/{id}:disable` and `:enable` |
| Admin budget notice link in the shell availability notice (DEPOT-278) | built | The SOFT and HARD notices link administrators to `/copilot/budgets`; non-administrators see no budget notice at SOFT and no link at HARD |
| AI data policy editor at `/copilot/ai-policy` (DEPOT-278, DEPOT-306) | later slice of DEPOT-306 | The AI controls page shows a read-only summary for callers holding aipolicy.read and links to the planned route in `plannedRoutes.json` |
| Audit consumer for kill switch, budget, data policy and agent-disabled changes (DEPOT-278) | later slice | Each change writes its change table row with actor and time; a `services/audit` consumer is out of scope |
| Playwright coverage of the AI controls and agents pages (DEPOT-278) | later slice of DEPOT-278 | The pages are covered by component tests with axe; the browser run against the agents service fixture is not added |
