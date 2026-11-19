# B-21 review 2: commit 6672a576 (fixes for review 1)

**Verdict: CHANGES REQUIRED** (one Major, in the honest-screens scan; everything else is fixed or minor)

I reviewed `git show 6672a576` against the round-1 findings in `b21-review-1.md`. The work was done in a scratch copy of the tree at 6672a576 (`git archive`). Nothing was committed. The scratch copy, its `node_modules` and the `rev_b21b_num` database are deleted, and `/home/user/Passage/apps/web/node_modules` does not exist.

## What I ran

| Check | Result |
|---|---|
| `opa test --timeout 600s policies/authz` | PASS 323/323 |
| `go test -count=1 ./...` in `services/numbering`, with `DEPOT_TEST_DATABASE_URL` and `DEPOT_TEST_APP_DATABASE_URL` on `rev_b21b_num` | all ok. `-v` confirms that the new DB tests ran and were not skipped: `TestPostgresListScopesPagesAndReportsTheNextValueUnderTheAppRole`, and `TestAllocateAndListOverHTTPUnderAppRole` with its 3 list cases. |
| `go vet`, `golangci-lint run`, `gofmt -l` (numbering) | clean, 0 issues |
| Scratch store test (deleted): cursor tampering, all-scope, paging, mixed claim | pass. Details under Security. |
| Mutation: explicit scope filter removed from `List` | every test still passes. RLS alone keeps the scoping correct (see nit N-1). |
| `npm ci`, `npm run typecheck` | ok |
| `npx vitest --run` (full) | 638 files, 7959 tests, all passed |
| `npm run build`, then `node scripts/check-bundle.mjs dist` | "3 files, no demo-only data". `Cedar Ridge`, `Jordan Avery`, `cedarridgepumps` and `rows-1m` are absent. |
| check-bundle against a production build with `HomePage` mutated back to a static `DemoTour` import | exit 1, names Cedar Ridge, cedarridgepumps.example and Jordan Avery. The guard works. |
| Demo build | still contains the tour ("Cedar Ridge Pumps is a fictional…") |
| Playwright chromium: `a11y.spec.ts`, `shell.spec.ts` (incl. the new "offers no planned page" test), `home-setup.spec.ts` | 75 passed, `--trace off` |
| Playwright demo-flows, throwaway spec (deleted) | see below |

From the demo-flows spec:

- **`/admin/sequences`:** H1 "Number sequences", rows with `SO-024190`, axe (WCAG 2.2 AA tags) shows no violations.
- **`/admin/audit` and `/workflow/jobs` opened as bookmarks:** both show the planned page, with axe clean.
- **Home, `/features`, `/workflow`:** none links a planned route (`/features` has 685 links).
- **Palette:**
  - "job monitor", "alerts" and "standard texts" return nothing.
  - "audit" offers only other modules' audit pages.
  - "designer" offers only "Flow designer".
  - "documents" offers only "Documents, Engineering data".
- **Setup checklist:** shows "Number sequences in place".

## Round-1 findings: status

| Round 1 | Status | Evidence |
|---|---|---|
| M-1 no list backend | **Fixed** | Route, store, OpenAPI, policy and tests are in place, and the setup check reads the same list. See Security. |
| M-2 docs untrue | **Fixed** | `docs/user/whats-new.md` and `tenant-admin.md` now match the shipped behaviour. |
| M-3 planned routes in nav and palette | **Fixed** | `withoutPlannedNav` in `Shell.tsx:76`. Tenant workspace overrides are covered too. e2e and unit tests assert that no planned route appears anywhere, and bookmarks still open them. |
| M-4 design-system.md | **Fixed** | Sections 5, 7 and 8 are updated, and section 8 names `check-bundle.mjs`. |
| m-1 constant defaults | **Fixed** | Mutation m2 is now caught. |
| m-2 loose fetch detection | **Fixed** | m5 (type import) is now caught, and comments and unused imports no longer count as fetches. |
| m-3 hard-coded lists | **Partly fixed** | m3 is caught. Variants still escape (Minor r2-m1 below). |
| m-4 405 retry and list tools | **Fixed** | The info state has no retry, search, views or export, and a test asserts this. |
| m-5 build test | **Fixed** | `scripts/check-bundle.mjs` runs in CI (`.github/workflows/ci.yml:234`, working directory `apps/web`). Verified to fail on a leak. |
| m-6 demo data in the production bundle | **Fixed** | `DemoTour` is now lazy-loaded. |
| m-7 nav duty | **Fixed** | `numbering.read`, `admin/module.ts:40`. |
| m-8 PlannedPage location | **Fixed** | Moved to `shell/pages/PlannedPage.tsx`. |
| Nits n-1 to n-7 | **Fixed** | n-1: the admin-card test now renders `AdminHome`. n-3: the page has one name, "Import records". n-6: "Workflow". n-7: the empty state links to the setup checklist. |

## Security of `GET /v1/numbering/sequences`

I found no security problems.

- **RLS.** `number_sequence` and `number_sequence_state` both have FORCE RLS, with a permissive tenant policy and a restrictive `depot.legal_entity_scopes` policy (`migrations/0001_init.sql:16-38`). `List` runs in `dbx.WithTenantTx`, which sets both GUCs from the principal. The join to `number_sequence_state` is filtered by RLS too, so the next value cannot come from another entity. `depot_app` holds only SELECT, INSERT and UPDATE on these tables.
- **Explicit scope filter.** `store.go` (`$2::uuid[] is null or s.legal_entity_id = any($2)`) repeats RLS:
  - `scopeEntities` returns nil only for a caller with no claim who holds `org.switcher.all-scope`, which `tenant.Middleware` keeps only on an explicit ALL-scope read.
  - A caller with no claim and no all-scope duty gets 403 `AUTH-0003`.
  - A claim with no valid UUID also gets 403.
  - With the explicit filter mutated away, every test still passed, which shows that RLS alone already scopes correctly.
- **Cursor tampering.** The cursor is validated as a UUID at the handler (`api.go`, 400 otherwise) and used only as `s.id > $3`, which is ANDed with the tenant filter, the scope filter and RLS. In my scratch test, a caller scoped to entity A sent:
  - the nil UUID: got A's 3 rows;
  - `ffff…`: got 0 rows;
  - entity B's sequence ids: got 0 rows;
  - another tenant's sequence id: got 0 rows.

  Paging A one row at a time returned exactly A's 3 rows, never B's. A mixed claim (`not-a-uuid,B`) returned only B. An all-scope caller saw both entities of its own tenant and none of tenant 2's. A cursor can only shift the window within rows the caller may already see.
- **Limits.** `limit` 1 to 500 (400 otherwise), `limit+1` lookahead, and keyset order on the primary key `(tenant_id, id)`. The exact-boundary case (limit equal to the row count) returns no `nextCursor`. A store error returns `COM-0500` without leaking the error text (tested).
- **`nextValue`.** `coalesce(st.next_value + 1, s.start_from)` is correct: `Allocate` stores the last issued value in `next_value` and inserts `start_from` on the first allocation. `nextFormatted` is omitted when the padded width is exhausted, and the UI falls back to the raw value.

## Policy glob `numbering.read`: `/v1/numbering/sequences{,/*}`

The grant is correct and no deeper than before. `path_matches` uses `glob.match(pattern, ["/", ":"], path)`, so `*` crosses neither `/` nor `:`. I probed the decision with the duty assigned:

| Path | Result |
|---|---|
| `/v1/numbering/sequences` | allow |
| `/v1/numbering/sequences/INV` | allow |
| `/v1/numbering/sequences/` (empty segment) | allow, as the old `/sequences/*` already did |
| `/sequences/INV/next`, `/sequences/a:b`, `/sequencesX`, `/sequences/a/b/c`, `/v1/numbering`, `/v1/numbering/allocations` | deny |

The raw decision also allows `/sequences/a%2Fb` and `/sequences/..`, as the old pattern did. `gateway_resource` refuses both (`canonical_path` rejects `%`, `..` and `//`), so they never reach a decision through a gateway.

The new `concrete()` helper replaces `{,/**}` before `{,/*}`, so the two do not interfere. The route-table cases cover the list with `numbering.read` (allow) and with only `numbering.write` (deny). `deploy/local/seed.json` gives the admin `numbering.read`.

## honestScreens allowlists

All three entries are justified.

- **`hr/PerformancePages.tsx:NoLinkedWorker`.** It is rendered only from the error branch of the goals and reviews fetches (lines 56 and 136), when the worker lookup came back empty, so its message reports a fetched fact.
- **`mobile-flows/ExceptionsPanel.tsx:ExceptionsPanel.overrideReasons`.** It is a pick-list of reasons the supervisor chooses from (`DEFAULT_OVERRIDE_REASONS`), used for validation, not shown as data.
- **`warehouse/pages.tsx:DirectiveSimulatorPage.initial`.** It is an editable example draft in `useState`; the result comes from the service.

The round-1 `FlowSimulator` entry is gone because it now takes props, which is consistent with the new rule. The self-check that every allowlist entry is still needed is in place.

## Mutation testing

All 12 round-1 mutations that the scan targeted are now caught: m1 to m12, including m2 and m5, which round 1 missed. m13 and m14 are still missed. Fourteen new mutations follow (n1 to n13; n13 is the old `AuditViewer`).

| # | Mutation | Result |
|---|---|---|
| m13 | One-element hard-coded alert list mapped into `<li>` | **missed** |
| m14 | `Tiles(props)` with `value={props.running ?? 1}` | **missed** |
| n1 | `DocsPage(_props: { note?: string })` with a static "No documents yet" `EmptyState` | **missed** (round 1 caught this) |
| n2 | `WorkflowHome({ title = "Workflow" })` with `<StatTile value={3}>` | **missed** (round 1 caught this) |
| **n13** | The old `AuditViewer({ items }: { items?: Entry[] })`, `rows={items ?? []}`, `empty={{ title: "No audit entries yet" }}`, registered as a route with `createElement(AuditViewer)` | **missed** (round 1 caught this) |
| n3 | Module-level list of 2 objects mapped into `<Card>` | **missed** |
| n4 | Inline `rows={[{…},{…}]}` | **missed** |
| n5 | `const [rows] = useState(SAMPLE)` passed to `rows=` with no `empty=` | **missed** |
| n6 | `useQuery` called, result ignored, static `EmptyState` | missed. A scan can't see this; it is the review checklist's job. |
| n7 | `withoutPlannedNav` removed from `Shell.tsx` | caught |
| n8 | `EmptyState title="Select a job: none have run yet"` (prompt exemption) | missed |
| n9 | A path added to `plannedRoutes.json` while its route renders a real page | caught |
| n10 | Anonymous function assigned to a capitalized const | caught |
| n11 | `export default function () {…}` with a static `EmptyState` | missed |
| n12 | `memo(() => <EmptyState …/>)` | missed |

## Findings

### Major

**r2-M1. A false empty state now passes the scan whenever the component declares any prop. That includes UX-001 itself.**
File: `apps/web/src/shell/honestScreens.test.ts:220-221`.

Check 1 used to run per file: a file that shows a state and fetches nothing failed. It now flags only components with **zero parameters**:

```ts
if (params.length === 0 && some(fn, rendersState) && !some(fn, isFetchCall)) paramlessWithoutFetch.push(component);
```

Mutation n13 is the original `AuditViewer` with `items = []` replaced by an optional `items?` and `rows={items ?? []}`. Registered as a route with `createElement(AuditViewer)`, it renders "No audit entries yet" forever, which is exactly UX-001, and it passes all 17 tests. n1 and n2, which round 1 caught, now pass too, and so does round-1 m14.

Keeping a file-level rule is cheap. Under the new fetch detector, only 5 module files show a state with no fetch anywhere in the file, and all are shared presentational parts:

- `modules/eam/parts.tsx`
- `modules/integration/parts.tsx`
- `modules/ledger/BatchLinesEditor.tsx`
- `modules/mobile-flows/FlowSimulator.tsx`
- `modules/productcost/shared.tsx`

Fix (either):

- (a) Treat every component that a module route renders with no props as paramless. The test already calls `loadModules()`. Collect `r.element.type` where `r.element.props` holds no data props, map each to its `file:Component` in `facts`, and require `!some(fn, rendersState) || some(fn, isFetchCall)` for those. Or:
- (b) Restore the file-level rule (`showsState && !fetches`) alongside the per-component one, with the 5 files above in an allowlist, each with a reason.

Add n13 as a fixture: a component with an optional data prop, rendered as a route element.

### Minor

**r2-m1. The static-list detector misses common shapes.**
File: `apps/web/src/shell/honestScreens.test.ts:227-251`.

Four shapes escape:

- a one-element list (m13; `elements.length >= 2`);
- an inline `rows={[{…},{…}]}` (n4; `rowsConstant` requires an Identifier);
- a list mapped into anything but `<li>`/`<tr>`, such as `<Card>` per row (n3);
- a constant routed through `useState(SAMPLE)` (n5).

Fix:

- accept one or more elements;
- treat an `ArrayExpression` of object literals in `rows=` as static;
- count any JSX returned from the `.map` callback, keeping the link and `<option>` exemptions;
- resolve `useState(CONST)` and `useMemo(() => CONST)` to the constant.

**r2-m2. Component discovery misses `memo()`, `forwardRef()` and anonymous default exports.**
File: `apps/web/src/shell/honestScreens.test.ts:187-192`.

Mutations n11 and n12 escape. No module uses these forms today (0 files), so this is a latent gap. Fix: also take a `VariableDeclarator` whose init is a `CallExpression` to `memo`/`forwardRef` wrapping a function, and an `ExportDefaultDeclaration` of a function, named `default`.

### Nit

- **N-1.** `services/numbering/internal/store/store_test.go` (new list test): the tests run only as `depot_app`, so RLS hides the explicit scope filter in `List`. With the filter mutated away, everything still passes. Add one assertion with `store.New(admin)` (the owner role, which bypasses RLS) and a principal scoped to B, expecting only B's rows. That pins the defence-in-depth filter in case the service ever runs as the owner.
- **N-2.** `apps/web/src/shell/honestScreens.test.ts:169-170`: `PROMPT` exempts any title starting with Choose/Select/Enter/Pick, so "Select a job: none have run yet" (n8) passes. Exempt only titles that are a single imperative clause (no ":", no "none", "no ", "yet"), or require the exempted `EmptyState` to have no `description`.
- **N-3.** `apps/web/src/shell/honestScreens.test.ts:15-16`: the header says the build is "checked by the production-build grep in apps/web/docs/design-system.md, section 8". Point straight at `apps/web/scripts/check-bundle.mjs` and the CI step.
- **N-4.** The marker lists in `scripts/check-bundle.mjs:11` and `honestScreens.test.ts:505` differ (`Jordan Avery` is in one only). Export one list, for example a JSON next to the script that both read, so they can't drift.

## Verdict

**CHANGES REQUIRED.** The fix for r2-M1 is small: either use route elements as paramless, or restore the file-level rule with a 5-entry allowlist, plus the n13 fixture. Once it lands, the remaining minors and nits do not block. The numbering endpoint, the policy change, the shell filtering, the bundle guard, the docs and the copy are all approved as they stand.
