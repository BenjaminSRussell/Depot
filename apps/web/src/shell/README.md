# Web shell (wave 5)

Keyboard-first host at `apps/web/src/shell`. Workspaces come from `modules/*/module.ts`. The metadata-driven form renderer consumes the wave-3 `MetadataCatalog` interface; this slice ships `fakeCatalog()` with `platform.note`.

Design system, tokens, primitives and accessibility rules: see `apps/web/docs/design-system.md`.

User-facing text follows `apps/web/docs/ux-writing.md`. Server failures render through `ProblemMessage` (`src/shell/errors`), which maps problem codes to plain messages from `src/shell/errors/catalog.ts` and keeps the code and request id under "Details".

A test (`errors/bareCode.test.ts`) fails when a module renders a failure's code as text, and `errors/catalogCoverage.test.ts` fails when a purchase-to-pay, order-to-cash or master-data service defines a problem code with no catalog message. Domain words get a tooltip from `<Term id="...">` (`src/shell/glossary`), and the full list is the Glossary page at `/glossary`.

Command palette (#506): typing two or more characters also calls `GET /api/v1/search` and lists records by type (`src/shell/search`): customers and vendors, products, purchase orders, receipts, vendor invoices, sales orders, packing slips, customer invoices, production orders and workers. Alt+F favourites the active record; recent and favourite records are kept per user in this browser. `src/shell/search/match.ts` holds the service's matching rules (a number by its start, a name by what it holds or a near miss) for rows filtered in the browser; see `docs/lists.md`, "Text search". `recordLinks.test.ts` fails when the service returns a record type the palette does not know or links to a page that does not exist.

## Needs

- `@module-federation/vite` and the OTel web SDK are still pending. Federation remotes are declared in `vite.config.ts` as `federationRemotes`.
- Playwright and `@axe-core/playwright` are now present. `e2e/` holds the end-to-end suite (`a11y.spec.ts` scans every route plus the open palette and mobile drawer for serious/critical WCAG 2.1 AA violations; `shell.spec.ts` covers skip link, landmarks and keyboard choreography; `forms.spec.ts` covers Notes validation). Run it with `make test-e2e` or `npx playwright test` in `apps/web`; first-time setup is `npx playwright install chromium`. Unit-level a11y proofs still run under vitest + the in-repo checker (`a11y.ts`).

## Workspaces and navigation (#498)

The sidebar shows one persona workspace at a time, not every module. All of it is data under `src/shell/workspaces/`:

- `workspaces.json`: the 8 business areas and the 10 persona workspaces. A workspace's `entries` are nav paths ordered by how often the persona uses them; the first five are its top tasks. Keep each workspace to 10-20 entries.
- `navAudit.json`: one row for every `modules/*/module.ts` nav entry with its `area`, a `keep`, `merge` or `advanced` decision, an optional task-language `taskLabel`, and `mergeInto` for merges. `navAudit.test.ts` fails when a module entry has no row, so classify a new entry in the same change that adds it. `keep` entries are listed under their area in All features, `advanced` and `merge` entries under the area's Advanced group; nothing is unreachable.
- `model.ts` loads and validates both files. `loadWorkspaceConfig(override)` merges a per-tenant override (replace a workspace or area by id, add new ones, `hide` ids); pass the result to `<Shell workspaceConfig={...}>`. Keys are strict and ids must be unique; an invalid override is recorded as an error and the shipped config is used. Nothing fetches an override yet.
- Duty gating runs first: the shell hands the workspace code only the modules the principal's duties allow, so a curated entry the user may not open never appears in a workspace, All features or search.
- A user holds several workspaces (every one with a visible entry, minus the ones they dropped, so workspaces gained later appear on their own) and switches with the Workspace selector. The default is the workspace with the most duty-gated entries the user can see; home shows at most four. The choice is kept in this browser only (`localStorage`).
- Labels in the sidebar, All features and search are task labels. The page title stays the module's own label.

## Pinned items (DEPOT-289)

`shell/pins/` lets a person pin the current page or record from the top bar. Pins show in the Pinned region on the home, can be moved up, moved down and removed with buttons (each change is announced in the shell live region), and persist in this browser only.

- Key `depot.pins.v1.<identity>` over `localPreferenceStorage`, one key per signed-in identity. An empty identity stores nothing.
- Value: a JSON array of strict objects, `{kind: "page", route, at}` or `{kind: "record", recordType, id, at}`. Only the route key, the record type and id, and the timestamp are stored. Names are never stored: the region resolves them at render time (`resolveOption`), shows the record type's plural name when the type has no lookup, and hides a pin whose lookup returns nothing or whose page or record the person can no longer open.
- At most `MAX_PINS` (20) pins. A 21st is refused with a message and nothing is dropped. A corrupt stored value, or an item that does not match the schema, is ignored on read.
- `probeStorage` writes, reads and removes a test key once. When it fails, the pin control and the region are not rendered.
- `RECORD_ROUTES` in `pinTargets.ts` copies the search service's record links; `pins.test.tsx` fails when the service changes them.
- Saved list views and assistant prompts are not pinnable yet (follow-up slice of DEPOT-289). The assistant store's own `pins` kind (`depot.assistant.pins.v1.<identity>`) is separate and untouched.

## Assistant storage (#309)

Prompts, answers, citations, tool results, proposals and briefings are held in memory only (the home's briefing card reads the list into component state; the only thing it stores is the per-user local-date marker of the automatic daily generation, as a `briefingDay` row in `shell/assistant/assistantStorage.ts`). They are never written to `localStorage`, `sessionStorage`, IndexedDB or the Cache API, never placed in a URL, history state or document title, and never sent in a telemetry event. The home page hands a typed prompt to the copilot through `shell/assistant/session.ts` (memory, read once, expires after 10 seconds) instead of router state.

The only browser storage an assistant surface may use is `shell/assistant/assistantStorage.ts`:

- `createAssistantStore(storage?)` returns `read(kind, identity)` and `write(kind, identity, rows)`. `kind` is `pins` (`{id, at}`) or `activity` (`{id, route, at, count}`); keys are `depot.assistant.<kind>.v1.<identity>`.
- Rows are strict objects: an `id` is a token with no spaces (up to 128 characters), `route` a path with no query, `at` an ISO timestamp, `count` a non-negative integer. A write with any other field or shape returns `{ ok: false, reason: "schema" }` and stores nothing; an empty identity returns `"no-identity"`.
- A corrupt or wrong-shaped stored value reads as an empty list. When the browser refuses storage, the store keeps working from memory and shows no error.
- Copilot requests send `cache: "no-store"`; the agent runtime answers `Cache-Control: no-store`. The app registers no service worker.

`shell/assistant/storageScan.test.ts` fails the build when a non-test source under `modules/copilot`, `shell/assistant` or `shell/home` uses those browser stores directly or imports `preferenceStorage` outside the wrapper, or when a service worker appears.

## Assistant availability (DEPOT-277)

No ERP flow depends on the assistant (ADR-0001). `shell/assistant/AvailabilityProvider.tsx` reads `GET /api/v1/agents/availability` for users who hold `mcp.copilot.use` and keeps one state in memory: `enabled`, `degraded` (budget used up, or the read failed) or `unavailable` (paused, or turned off for the company). Anything but `enabled` shows the classic home with a status notice, hides the Copilot and Tool catalog entries, and leaves every other page alone. A failed, slow, unreadable or 404 read counts as not enabled.

- `AVAILABILITY_POLL_MS` (10 seconds) is the single poll interval. The read repeats while the tab is visible, not while it is hidden, and runs at once on window focus, when the tab becomes visible and after a copilot refusal or failure. Each read replaces the whole state, so a lifted pause or a raised budget returns the assistant-led home on the next read.
- `AI_REQUEST_TIMEOUT_MS` (10 seconds) bounds the availability read and non-streamed chat; `AI_FIRST_TOKEN_TIMEOUT_MS` (30 seconds) bounds the wait for the first streamed token.
- A copilot refusal with code `AGT-0202` shows the code and a plain message in the answer and switches the home to classic at once. Nothing retries.

## Assistant prompt router (DEPOT-296)

`shell/assistant/router.ts` is the only code path from prompt text to a copilot call. `createPromptRouter({handlers, getAvailability, hasDuty, postRoute, searchFallback})` returns `submit(text, mode?)`. The home prompt submits through it; the command bar's Assistant row calls the same `submit`. `phrases.ts` exports the canonical phrases for the E1-S4, S6 and S7 fixtures.

- With no mode, `postRoute` sends `POST /api/v1/copilot/chat` with `{mode: "route", message}` (10 second deadline) and dispatches by the returned kind. An explicit mode (`ask`, `navigate`, `act`) skips the call.
- When availability is not `enabled`, nothing is sent: `searchFallback(text)` runs. A timeout, network error, HTTP error or unreadable body also falls back to search, never to act. A refusal such as AGT-0202 shows its code and message.
- `clarify` dispatches nothing and returns two options. `act` needs `agents.invoke` (the domain write duty is checked on the server); without it the outcome is `blocked` with the navigate alternative. Holding only `agentapproval.decide` does not unlock act.
- A kind with no registered handler falls back to the `ask` handler (the CopilotPanel hand-off). Navigate and act handlers are typed slots: DEPOT-281 and DEPOT-282 register them.

| Phrase shape (`services/agents-ts/src/domain/promptClass.ts`) | Kind |
|---|---|
| An act verb plus a record cue, such as "apply this payment to invoice INV-1042" or "cancel this planned order" | act |
| Opens with `show me`, `list`, `open`, `go to`, `find`, `create`, `new` or `start`, such as "show me overdue invoices over $10k" or "create a PO for 50 widgets from Acme" | navigate |
| Ends with `?` or starts with `what`, `which`, `who`, `why`, `how many`, `how much`, `when`, `compare`, `explain`, `total` or `trend`, such as "what is the total of our overdue invoices over $10k?" | ask |
| Anything else goes to the model; confidence under 0.6, or under 0.15 ahead of the runner-up | clarify |

## Attention feed (DEPOT-280)

The home card "Needs your attention" (`shell/home/AttentionFeed.tsx`, rules in `shell/home/feedSources.ts`) replaces the My work strip. Each source is requested only when the user holds its duty, and fails on its own: the card names the unavailable source in a warning with a Retry button.

| Source | API | Duty | Dedupe key | Category |
|---|---|---|---|---|
| Workflow inbox tasks | `GET /api/v1/inbox` (the shared inbox read) | `inbox.read` | `inbox:<source>:<id>` | inbox |
| MRP messages | `GET /api/v1/planning/messages?planCode=MAIN&status=open&sort=priority&limit=50` | `planning.read` | `planning:<id>` | exception |

- Ranking: overdue first (a due date before today, or a planning delay above zero), then amount, then age in days, then key. Planning messages carry no amount.
- De-duplication: one row per key; when categories collide the exception wins over the inbox task.
- One-click: not built. Every row is Review-only, so no posting or payment action is reachable from home. The default-deny inline Approve and Reject rule is a later slice.
- Deferred sources, none queried or shown: three-way match exceptions (DEPOT-342), anomaly review tasks (DEPOT-343), agent approvals in the inbox (DEPOT-344). Agent approvals as a source are a later slice of DEPOT-280. See `modules/copilot/NOT-IMPLEMENTED.md`.

## Record assistant opt-in (DEPOT-284)

`PageHeader` takes one optional prop, `askAbout={{ entity, id }}`, and shows an "Ask about this record" button on a record page. A list page never passes it, so the button is never there.

To opt a module in:

1. Add the entity to `RECORD_ASSIST` in `shell/assist/recordAssist.ts` with its plain label and the next-step screens (`nextLinks`, each with the duty its navigation entry carries). The key must also exist in `RECORD_PAGES` in `modules/copilot/suggestionModel.ts`, so citations can link to it; `recordAssist.test.ts` checks both.
2. Pass `askAbout={{ entity: "<key>", id: record.id }}` to the record page's `PageHeader` once the record has loaded.

Opted in today: `purchase-order`, `ap-invoice`, `ar-payment`, `journal-entry`.

- The button renders only when the user holds `mcp.copilot.use` and availability is `enabled`. Otherwise nothing renders and the page is unchanged.
- The panel sends `record:<entity>/<id>` in `explain` mode with one of three fixed requests. It reads the answer through `AskAnswer`, so the AI badge, tool calls, sources and "left out" notice are the shared ones.
- The panel names every history source the answer did not read (change history, related documents, approval and task history) from `toolCalls`, whatever the model wrote.
- Next steps are ordinary links filtered by duty. The panel never inserts text into the page and never performs an action. The proposal-start button waits for the act handler (DEPOT-281).
- Closing the panel (Escape or Close) discards the conversation and returns focus to the button.
