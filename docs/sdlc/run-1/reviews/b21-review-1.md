# B-21 review 1: commit 11c9a6b1 (UX slice 1, honest screens)

**Verdict: CHANGES REQUIRED**

Reviewed `git show 11c9a6b1` against the B-21 entry in `docs/sdlc/run-1/backlog.md` (line 1059), slice 1 in `docs/ux/rollout.md` (line 83), UXS-01 in `docs/ux/patterns.md` (line 22) and `apps/web/docs/ux-writing.md`.

The web-side work is careful. Fake rows and fake counts are gone. The planned page is honest, accessible and dated. `/admin/import` now opens the working import, and the sequences page handles its loading, empty, 403, failure and retry states. The new tests catch most regressions. The blocking problems are about what the commit leaves true or false *around* the code:

- The sequences list has no backend outside the demo.
- The user docs say it works.
- Planned routes are still offered in the palette and in All features, which UXS-01 forbids.
- The design-system doc the backlog names was not updated.

## What I ran

All of this ran in a scratch copy of the tree at 11c9a6b1. Other agents merged into the shared branch while I worked, so I could not use the shared tree. Nothing was committed, and the scratch copy and `apps/web/node_modules` have been deleted.

| Check | Result |
|---|---|
| `npm ci` | ok |
| `npm run typecheck` | pass |
| `npx vitest --run` (full suite) | 7950/7951 passed. The one failure (`ProblemMessage.test.tsx`, `PTY-NUMBERING-TIMEOUT`) came from another agent's merge (fe2eb629) landing in the shared tree during the run. It passes alone and is unrelated to B-21. |
| Targeted tests at 11c9a6b1 (honestScreens, admin, workflow, Shell, workspaces, registry) | 12 files, 204 tests passed |
| `vite build` (production) and grep of `dist/` | Workflow markers (`rows-1m`, `400000 / 1000000`, `workflow.workitem.escalated`, `outbox.sweep`, `Manager role`, "Sample data", "Coming soon", "No audit entries yet") are absent. **Other demo data is present:** `Cedar Ridge Pumps, Inc.`, the demo user "Jordan Avery" with `jordan.avery@cedarridgepumps.example`, and the demo-tour areas, all through `shell/pages/HomePage.tsx:3` (see M-6). |
| Playwright, chromium: `a11y.spec.ts`, `shell.spec.ts`, `home-setup.spec.ts` (38 tests incl. every admin/workflow route) | 38 passed, `--trace off` |
| Playwright, demo-flows: a throwaway review spec (not committed) | /admin/sequences lists the 5 demo sequences with legal-entity names, and axe (WCAG 2.2 AA tags) reports nothing. The same holds for /admin/import, /admin/audit, /workflow/jobs and /workflow. **The palette still offers "Audit log", "Job monitor", "Designer" and "Alerts", and /features links all 7 planned routes.** |

### Mutation testing of `honestScreens.test.ts`

Each mutation was applied in the scratch copy, the test was run, and the change was reverted.

| # | Mutation | Result |
|---|---|---|
| m1 | `WorkflowHome({ counts = { running: 1, failed: 0 } })` + `StatTile` | caught (checks 1 and 2) |
| m4 | `/workflow/jobs` added to `workspaces.json` | caught |
| m6 | Static `EmptyState` page with no fetch (old `SequencePage`) | caught |
| m7 | "Sample data" badge on `WorkflowHome` | caught |
| m8 | `/workflow/jobs` route renders a real page instead of `PlannedPage` | caught |
| m9 | Module statically imports `../../demo/company` | caught |
| m10 | `rows-1m` in a module `.ts` file | caught |
| m11 | Planned `targetDate` in the past | caught |
| m12 | `AdminHome` stops filtering planned cards | missed by honestScreens; **caught by `admin/module.test.tsx`** |
| m15 | `SequencesPage` `queryFn` returns `[]` without fetching | caught by `admin/module.test.tsx` |
| m2 | `function JobsPage({ runs = SAMPLE_RUNS })` with `const SAMPLE_RUNS = [...]` | **missed** |
| m3 / m13 | Hard-coded `<ol>`/`<ul>` of steps or alerts with no EmptyState (the old `DesignerPage`/`TemplatesPage` pattern) | **missed** |
| m5 | Static `EmptyState` in a page whose only "fetch" is `import type { Foo } from "./api/client"` | **missed** |
| m14 | `StatTile value={props.running ?? 1}` in a file that merely mentions `useQuery` | **missed** |

So the scan catches the regressions the spec names. It can be dodged in ways that are realistic (see m-1 and m-2 below).

## Findings

### Major

**M-1. `/admin/sequences` has no backend outside the demo, and acceptance criterion 2 is not met.**
Files: `apps/web/src/modules/admin/SequencesPage.tsx:8,28` and `services/numbering/internal/api/api.go:53`. The authz gap is at `policies/authz/data.json:2950`.

- The numbering service registers only `POST /v1/numbering/sequences`, `GET /v1/numbering/sequences/{code}` and `POST .../next`. There is no list route.
- `numbering.read` grants only `/v1/numbering/sequences/*`, so a reader gets 403 at the gateway. Anyone who gets past it gets chi's 405.
- In every real environment, therefore, the page shows either "You can't view number sequences… Ask an administrator" (false: no administrator can fix it) or "Depot can't list number sequences yet".
- The setup checklist's sequences step (`modules/setup/checks.ts:35`) calls the same missing route.
- So "TA5 and the setup checklist's sequences step complete in Round 0" holds only in the demo build. UX-005 ("sequences exist, the page says none") is not closed for real tenants.
- The CHANGELOG discloses the gap, but the backlog was not updated. No item owns a list endpoint: B-35 covers create and edit only.

Fix (one of):
- (a) Add `GET /v1/numbering/sequences` (list, scoped to the caller's legal entities, `{items}` page), its OpenAPI entry and the `numbering.read` grant for the collection path, as a B-21 sub-task or a new dependency item.
- (b) Keep the web page as is, but record AC2 as "met in demo only, blocked on <new item>" in `backlog.md`, add that item as a dependency of B-21's closure, and correct the docs (M-2).

Either way, also expose a next/current value. Slice 1 asks for "code, legal entity, format, next value", and the page shows "Starts at" instead of the next value without saying why.

**M-2. The user docs and What's new say the sequences list works.**
Files: `docs/user/whats-new.md:7` and `docs/user/tenant-admin.md:54`.

"Admin, then Sequences lists the number sequences of each legal entity…" is false in production (M-1). The release notes would break UXS-01's own promise.

Fix: until a list route ships, say what is true. For example: "Admin, then Sequences will list the number sequences of each legal entity. Until Depot can list them, the page says so; documents are still numbered as before." Alternatively, hold these entries until M-1(a) lands.

**M-3. Planned routes are still in module nav, the command palette and All features.**
Files: `apps/web/src/modules/admin/module.ts:35-45` and `apps/web/src/modules/workflow/module.ts:18-25`.

- The UXS-01 rule says a page with no working data source "is not in the navigation, workspaces, command palette or home page", and acceptance 4 says "No nav entry, workspace entry or palette entry points to a route in `plannedRoutes.json`".
- Build-or-reuse says the shell "strips them from nav, workspaces and the palette".
- The commit keeps the nav entries on purpose. In the demo build, typing "audit", "job monitor", "designer" or "alerts" in the palette offers those pages, and `/features` links all 7 planned routes.
- The backlog and rollout wording ("Hide … or render NotAvailablePage") leaves room for this choice, but patterns.md is the standard B-21 traces to, and its wording is unambiguous.
- A tenant workspace override can also still list a planned route at runtime (`shell/workspaces/model.test.ts:106` now asserts that it can).

Fix:
- Filter `plannedRoutes` paths out in `resolveNavEntries`, or wherever the palette, `/features` and workspaces are built, in the shell. Bookmarks keep working because the routes stay.
- Change `e2e/shell.spec.ts:52-60` ("aud" opens Audit) to a live target.
- Extend check 4 in `honestScreens.test.ts` to module nav, palette entries and resolved (overridden) workspaces.
- If the team prefers the current reading, record that decision in `docs/ux/patterns.md` UXS-01 acceptance 4 in the same change, so spec and code agree.

**M-4. `apps/web/docs/design-system.md` was not updated, though the backlog names it in scope.**
File: `apps/web/docs/design-system.md:275,286` (section 8 "Preserve") and `:177-193` (section 5 page inventory).

- B-21 scope-in lists "design-system.md section 8", and the rollout alignment table (row `design-system.md`, slice 1) asks to "drop the two fake-data 'preserve' assertions".
- Section 8 still requires the tests the commit deleted: "table 'Audit entries' contains `user-1`…" and "table 'Batch runs' contains rows-1m", plus "Workflow tiles … Pending work items 4".
- Section 5 still documents `AuditViewer` with `items=[]`, `JobMonitorPage`, the static sequences/documents/texts empty states and the CSV textarea import.
- This also leaves AC4 ("docs in rollout.md's alignment table are updated") unmet.

Fix:
- Drop or replace those two preserve lines with the new assertions (planned page, sequences fetch).
- Update the section 5 rows for `/admin/*` and `/workflow/*`.
- Add `PlannedPage` to the inventory.

### Minor

**m-1. A named-constant default prop dodges check 2.**
File: `apps/web/src/shell/honestScreens.test.ts:71-73`.

`({ runs = SAMPLE_RUNS })`, with `const SAMPLE_RUNS = [...]` at module level, is not flagged (mutation m2). It is the most natural way someone would reintroduce default rows.

Fix: when `right.type === "Identifier"`, resolve it to a top-level `VariableDeclarator` in the same file and apply the same array/non-empty-object test. Add the case to the inline `sample` fixture.

**m-2. `FETCHES` is satisfied by things that do not fetch.**
File: `apps/web/src/shell/honestScreens.test.ts:54-55`.

- `from "./api` matches `import type { … } from "./api/client"` (m5).
- The bare words `useQuery`/`fetch(` also match inside comments or unused imports (m14).
- The check is per file, so one fetching component can launder a static `EmptyState` in a sibling component.

Fix: strip comments and `import type` lines before testing. Better, use the AST already parsed for check 2 to require a call to a fetch hook, or an import of a non-type binding from an api module.

**m-3. The scan does not cover hard-coded lists, which were 2 of the 7 original offenders.**
File: `apps/web/src/shell/honestScreens.test.ts:53`.

`SHOWS_STATE` covers `EmptyState`, `empty=`, `StatTile` and `<progress>` only. The removed `DesignerPage` (static approval steps) and `TemplatesPage` (static template codes) would both pass a fresh copy of themselves (m3/m13). That matches the letter of UXS-01 acceptance 1, so this is not a spec breach, but it is a gap in the "rows" half of the rule.

Fix: also flag a page that maps over a module-level array literal of objects or strings into `<li>`/`DataTable rows=` without fetching, with the allowlist for genuine static content (for example option lists).

**m-4. The 405 state still offers "Try again" and the list tools.**
Files: `apps/web/src/modules/admin/SequencesPage.tsx:44-56,71-78` (render in `shell/ui/ListPage.tsx:430`).

The commit message and CHANGELOG say a 404/405 "says the list is not available instead of 'try again'", but the rendered page still shows:

```
Could not load number sequences / Depot can't list number sequences yet… / [Try again]
```

It also shows Search, "0 number sequences loaded", Views, Columns and Export CSV.

- "Could not load" plus a retry button frames a permanent state as a transient failure. This is the very thing `explainFailure` set out to avoid.
- `lists.md:34` hides the list tools for 403, and the same reasoning applies here.

Fix: treat this case like `forbidden`. Either add an `unavailable` flag to `ListData` that renders an info `EmptyState` with no retry and no list tools, or render the planned-style notice ("Number sequences can't be listed yet") from `SequencesPage` before `ListPage`. Add a test that `Try again` is absent.

**m-5. The build test is a source scan, not a build.**
File: `apps/web/src/shell/honestScreens.test.ts:177-198`.

- AC3 says "No production build contains the workflow sample rows (build test)". The test checks sources and the import graph from `main.tsx`.
- I confirmed by running `vite build` that the markers are absent today, so the AC holds in fact. The proxy can drift from the bundler, though; see M-6, where the proxy knowingly allowlists a leak.

Fix: add a CI step (or a script the gate runs) that runs `vite build` and greps `dist/` for the markers, plus `Cedar Ridge` and `cedarridgepumps.example`. Otherwise get the backlog owner to accept the source scan as the build test and record that in the AC.

**m-6. The production bundle contains demo data, and the commit allowlists the leak instead of fixing it.**
Files: `apps/web/src/shell/pages/HomePage.tsx:3,66` and `apps/web/src/shell/honestScreens.test.ts:31-34`.

- The static `import { DemoTour }` puts the demo company, the demo user's name and email, and the tour blurbs into `index-*.js` of the production build. This was verified.
- UXS-01 says sample data "lives only under `src/demo/` and only runs in demo builds". It does not run, but it ships.
- The fix is one line, and the allowlist reason already describes it.

Fix: `const DemoTour = import.meta.env.MODE === "demo" ? lazy(() => import("../../demo/DemoTour").then((m) => ({ default: m.DemoTour }))) : null;` with a `Suspense` boundary, then delete the allowlist entry. `HomePage.tsx` is outside the listed files, so it needs a one-line note in the commit. This is pre-existing, so treat it as a follow-up if the scope must stay tight.

**m-7. The Sequences nav entry requires `numbering.write` for a read-only page.**
File: `apps/web/src/modules/admin/module.ts:40`.

The setup checklist uses `numbering.read` for the same page, and the page now writes nothing. Nav is not duty-filtered yet (`gated` only affects workspace scoring), but it will be.

Fix: `duty: "numbering.read"`.

**m-8. `PlannedPage` lives in `modules/admin` and is imported by the workflow module.**
Files: `apps/web/src/modules/admin/PlannedPage.tsx` and `apps/web/src/modules/workflow/module.ts:3`.

- `plannedRoutes.json` is a shell concept, and UXS-01 says "Reuse `shell/pages/NotAvailablePage.tsx`… The shell renders NotAvailablePage for them".
- Placing it in `admin` makes workflow (B-34), close (slice 4) and every later slice depend on the admin module.
- I understand that `shell/pages/**` is not in the files-touched list.

Fix: move `PlannedPage`, `plannedRoutes`, `isPlanned` and `plannedRoute` to `shell/pages/PlannedPage.tsx` (or give `NotAvailablePage` a `planned` variant) when M-3 touches the shell anyway.

### Nit

- **n-1.** `apps/web/src/shell/honestScreens.test.ts:170-174`: "gives no planned route a card on the admin home" only asserts that `adminCards` *contains* planned cards (> 0). It cannot fail when the filter is removed (m12); `admin/module.test.tsx` does that job. Rename it to "admin cards include planned routes, which AdminHome filters", or render `AdminHome` here and assert the links.
- **n-2.** Spec naming: UXS-01 names the allowlist file `shell/honestScreensAllowlist.json`, and the commit keeps the allowlists inline in the test. That is fine, but either note it in patterns.md or move them so other slices find them.
- **n-3.** Three names for one page: nav "Import / export", workspace and palette "Import and export data" (from `navAudit.json`), page header "Import records" (`ImportPage.tsx:14`). Sequences likewise has h1 "Sequences" and h2 "Number sequences". Align them, for example "Import records" everywhere for a page that no longer exports.
- **n-4.** `apps/web/src/modules/admin/pages.tsx:12`: "Export from the list itself." is terse. Try "To export, use Export CSV on the list you need." The muted note in `ImportPage.tsx:20` already says it better.
- **n-5.** `apps/web/src/modules/admin/ImportPage.tsx:15`: put the step name in quotes or bold, as `docs/user` does: "…the same import as the "Import your first records" step of the setup guide."
- **n-6.** `apps/web/src/modules/workflow/pages.tsx:13`: the page is still titled "Workflow and batch", but every batch page is planned now. Consider "Workflow".
- **n-7.** `SequencesPage.tsx:97-100`: per `lists.md:24`, a first-run empty state gives the next step as `action`. Nothing can be created yet, so point to the setup guide or say who sets sequences up.
- **n-8.** The rollout alignment table lists a new `apps/web/docs/at-pass-template.md` for slice 1 (UXS-15), but no backlog item owns it. This is a backlog gap, not a defect of this commit; flag it to the backlog owner.

## Per-question answers

1. **Acceptance criteria.**
   - AC1: honest-screens scan passes and no workspace links a planned route. Met for workspaces; see M-3 for nav and the palette.
   - AC2: TA5 met (the import works). The sequences step is met only in the demo (M-1).
   - AC3: met in fact, verified by building. The test is a source-scan proxy (m-5).
   - AC4: What's new, guide and CHANGELOG present. What's new and the guide are inaccurate (M-2), and design-system.md was not updated (M-4). No routes moved, so no redirects are needed.
2. **honestScreens.test.ts.** It catches workspace links, planned-route rendering and dates, inline literal defaults, static empty states, "Sample data"/"Coming soon", static demo imports and the workflow markers (8 of 8 targeted mutations). It misses named-constant defaults, type-only "fetches" and static lists (m-1 to m-3). The admin-home card check lives in `module.test.tsx`, not here (n-1).
3. **Bundle and demo gating.** Demo gating in `main.tsx` is intact and tested (dynamic import behind `demo ?`), and `demo/handlers/index.ts` registers the new workflow handler. The workflow rows are absent from the production bundle, but other demo data ships through `HomePage` (m-6).
4. **SequencesPage and ImportPage states.**
   - Sequences: loading, empty, 403, 5xx with retry, and 405 explained are all tested and correct. The 405 still shows retry and list tools (m-4), and the 403 in production is caused by the missing authz grant, not by the person's duties (M-1).
   - Import: it reuses `ImportStep` wholesale, including its no-duty state ("Your account can't import files") and the integration fetch, so it is correct.
5. **Copy.** Mostly good: sentence case, "isn't available yet", no spec ids, plain error detail. The plannedRoutes descriptions are tested for case, final period and SPEC/DELIVERY refs. Issues: M-2 (false claims), m-4 (failure framing), n-3 to n-6.
6. **Accessibility.** One h1 per page (from TopBar), with the planned page's h2 under it. axe (chromium suite plus WCAG 2.2 AA tags in the demo) found nothing on all admin/workflow routes, and `pageViolations` is asserted in the unit tests. No issues found.
7. **Removed pages and nav.** Nothing a user relied on is lost.
   - The removed pages showed only fake or empty-by-design data.
   - The CSV column counter is replaced by the real import.
   - All routes still resolve, so bookmarks work. `checkCsv` and the page exports had no other callers (typecheck passes).
   - `/admin/audit` leaving the tenant-admin workspace removes only a page that never worked.
8. **Edits outside the file list.** All are justified.
   - `workspaces.json`: removing `/admin/audit` is required by AC1.
   - `Shell.test.tsx`, `workspaces.test.tsx` and `model.test.ts`: the fixtures use the real `workspaces.json`, so without an extra entry the Administration workspace would vanish from the fixture.
   - `docs/user/*` and `CHANGELOG.md`: required by the rollout "Done when".
   - `apps/web/docs/design-system.md` *should* have been edited and was not (M-4). `HomePage.tsx` would be a justified addition (m-6).
