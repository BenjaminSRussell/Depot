# e2e/_shared

## writeGuard

Playwright fixture that records every network request in a scenario and fails the test when a domain write is issued without approval. A domain write is any non-GET request to `/api/v1/*` that is not on the allowlist.

```ts
import { expect, test } from "../_shared/writeGuard";

test("ask path never writes", async ({ page, writeGuard }) => {
  await page.goto("/");
  // drive the scenario
  writeGuard.expectNoWrites();
});

test("act path writes only after Approve", async ({ page, writeGuard }) => {
  await page.goto("/");
  // drive the scenario up to the proposal card
  await writeGuard.expectWritesOnlyAfterApproval(page.getByRole("button", { name: "Approve" }));
});
```

Applying the fixture is the single `writeGuard` fixture option on the test; specs add no `page.route` or request interception for it. `test` extends Playwright's base test, so it replaces the import of `test` from `@playwright/test`.

### API

- `expectNoWrites()` for ask, navigate, contextual-help and briefing paths. Fails on any domain write outside the allowlist, even after an approval.
- `expectWritesOnlyAfterApproval(approveEvent: Locator)` for the act path. Pass the Approve control (proposal card button or inbox one-click approve). The guard stamps the time, clicks it, waits for the network to go idle, then fails if a domain write happened before the click or if there was no approval at all (declined or expired sessions). After the click any non-GET `/api/v1/*` request is permitted, covering `approvals/{id}:decide` with `APPROVED` and the resulting domain commit. The click time is exposed as `writeGuard.approvedAt`.
- `writeGuard.session()` returns a copy of the recorded session.
- A failure lists each offending request as `METHOD url at <ISO time> (+<ms from session start>)`.
- Pure functions `evaluateSession(session, mode)`, `classify`, `isDomainWrite` and `parseSession` are exported for flows where the approval is not a single click: build a session `{ entries: [{ t, method, url, postData }], events: [{ type: "approve", t }] }` and call `evaluateSession(session, "after-approval")`.

### Allowlist

`WRITE_ALLOWLIST` in `writeGuard.ts` is the one allowlist constant. Adding or removing a rule must update the exact-list unit test in `writeGuard.spec.ts`.

| Route | Purpose |
|---|---|
| `POST /api/v1/agents/copilot/conversations` | start a copilot conversation |
| `POST /api/v1/agents/copilot/conversations/{id}/messages` | send a copilot message |
| `POST /api/v1/agents/briefings:generate` | generate a briefing |
| `POST /api/v1/agents/briefings/{id}:mark-read` | mark a briefing read |
| `POST /api/v1/agents/runs` | proposal creation |
| `POST /api/v1/copilot/chat` | interim ask route used by the home prompt until the panel moves to the conversations endpoints |

Decline calls are allowed by a separate rule: `POST /api/v1/agents/approvals/{id}:decide` and `POST /api/v1/agents/runs/{runId}/actions/{id}/decision` count as allowlisted only when the JSON body has `decision` exactly `"REJECTED"`. A missing, non-JSON or differently valued body is treated as a domain write. `HEAD` and `OPTIONS` on `/api/v1/*` are writes too (anything that is not `GET`).

### Self-tests

`writeGuard.spec.ts` replays the recorded sessions in `fixtures/` (`violating.json`, `compliant.json`, `declined.json` with a declined and an expired session, `navigation-only.json`), checks the allowlist, and exercises the fixture on a live page. It runs in the `chromium` Playwright project, so the CI `web` job runs it on every change:

```
cd apps/web && npx playwright test e2e/_shared/writeGuard.spec.ts --project=chromium
```

Making that job a required check is a branch-protection setting.

### Adoption

Each of these stories imports `test` and `expect` from `e2e/_shared/writeGuard` in its own spec and adds no interception code:

- E1-S4, E1-S6, E1-S7: ask, navigate, contextual-help and briefing scenarios call `expectNoWrites()` at the end of the scenario.
- E1-S9, E1-S11: act scenarios call `expectWritesOnlyAfterApproval(<Approve locator>)` instead of clicking Approve themselves.
- E1-S16 runs the cross-surface sweep and does not import the fixture.
