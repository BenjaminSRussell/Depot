# B-21 review 3: commit 98b4b41e (fixes for review 2), plus a look at e74a24b1

**Verdict: APPROVE.** Every round-2 finding is fixed: r2-M1, r2-m1, r2-m2 and all four nits. Two narrow evasions remain. Both are Minor and need not block the merge.

I reviewed `git show 98b4b41e` against `b21-review-2.md`, and also looked at e74a24b1, the identity catalogue's support for the `{,/*}` glob. The work was done in a scratch copy at 98b4b41e. The identity tests ran on a copy of the merge commit 04b81530, because e74a24b1 is a sibling of 98b4b41e, not an ancestor.

Nothing was committed. The scratch copies, `node_modules` and the databases `rev_b21c_num` and `rev_b21c_id` are deleted. `/home/user/Passage/apps/web/node_modules` does not exist.

## What I ran

| Check | Result |
|---|---|
| `npm run typecheck` | pass |
| `npx vitest --run` (full) | 638 files, 7966 tests, all passed. honestScreens now has 24 tests. |
| `npm run build`, then `node scripts/check-bundle.mjs dist` | "3 files, no demo-only data". It works from another directory too, because it resolves `demoMarkers.json` relative to the script. When I planted `"Jordan Avery"` in `dist/assets/leak.js`, it exited 1 and named the file. |
| `go test -count=1 ./...` in `services/numbering` (root and `depot_app` DSNs) | all ok |
| Mutation: explicit scope filter removed from `store.List` | **The new admin-pool assertion fails** (`store_test.go:241`, "want only B's sequence"). The filter is now pinned (N-1). |
| `go test -count=1 .` in `services/identity` at the merge 04b81530 (with DB) | ok (514 s) |
| `go test ./...` in `services/identity` at 98b4b41e alone | fails: `duty "numbering.read": unsupported glob "/v1/numbering/sequences{,/*}"`. See below. |
| `opa test --timeout 600s policies/authz` | PASS 323/323 |

Playwright was not re-run: 98b4b41e changes only test code, `check-bundle.mjs`, a JSON marker file, design-system.md and one Go test. The round-2 Playwright results (75 chromium tests plus the demo checks) still stand.

## Round-2 findings: status

| Round 2 | Status |
|---|---|
| r2-M1 (a false empty state passes when the component declares a prop) | **Fixed.** There are now three rules (component, route, file), and the route-component list is cross-checked against `loadModules()`. n13, n1, n2 and m14 are now caught, including n13 placed in a file that fetches. |
| r2-m1 (static-list gaps) | **Fixed.** m13, n3, n4 and n5 are caught. |
| r2-m2 (memo, forwardRef, anonymous default) | **Fixed.** n11 and n12 are caught. |
| N-1 (scope filter not pinned) | **Fixed.** Verified by mutation (above). |
| N-2 (prompt exemption) | **Fixed.** n8 is caught. |
| N-3 (header comment) | **Fixed.** The header points at `scripts/check-bundle.mjs`. |
| N-4 (two marker lists) | **Fixed.** Both now read `src/shell/demoMarkers.json`. |

## Mutation testing (30 mutations)

All round-1 and round-2 mutations are caught:

- m1 to m14;
- n1 to n5, n7, n8 and n11 to n13. (n6, a fetch whose result is ignored, is out of scope for a source scan, as noted in round 2.)

New mutations for this round:

| # | Mutation | Result |
|---|---|---|
| p1 | n13 (`AuditViewer({ items? })`, "No audit entries yet") **in a file that fetches** (`SequencesPage.tsx`), routed with `createElement(AuditViewer)` | **caught** by the route rule |
| p2 | Same as p1, but the route passes a harmless prop: `createElement(AuditViewer, { title: "Audit" })` | **missed** |
| p3 | Route element via `lazy(() => import("./AuditViewer"))` | caught by the coverage guard and the file rule |
| p4 | Route through a wrapper component in a separate `.tsx` (`AuditRoute` renders `<AuditViewer key={id} />` in JSX), with the page alone in its file | caught by the file rule |
| p5 | Same as p4, but the page's file also exports a hook that fetches | **missed** |
| p6 | Route element built by a helper in module.ts: `page(path, AuditViewer)` calling `createElement(el)` | caught by the coverage guard |

## Allowlists

All entries are justified. Each file I read takes its data as props from a page that fetches it.

**`NO_FETCH_FILE_ALLOWLIST`:**

- **`eam/parts.tsx`:** `PagedTable` renders `state.rows`, passed in by pages that use `usePaged`: AssetCare, LocationPages, PermitPages, SequencePages.
- **`integration/parts.tsx`:** read-only views of a fetched EDI document. Its empty states ("The 850 carried no valid PO1 lines.") describe the passed-in document.
- **`ledger/BatchLinesEditor.tsx`:** the empty state is a draft batch with no lines, followed by "Add a line…".
- **`mobile-flows/FlowSimulator.tsx`:** an in-browser simulation; "No steps run yet" describes the simulation, not stored data.
- **`productcost/shared.tsx`:** costing result tables passed in by the page.

The per-component and route rules still apply to these five files.

**`STATIC_LIST_ALLOWLIST`, `sales/OneTimeFields.tsx:FIELDS`:** the address fields rendered as `FormField` inputs. It is form layout, not data.

The round-2 entries (`NoLinkedWorker`, `overrideReasons`, `DirectiveSimulatorPage.initial`) are unchanged, and each was justified in round 2. The self-check that every allowlist entry is still needed covers the new lists too.

## e74a24b1: identity catalogue support for `{,/*}`

The change is correct, and it fixes a regression from round 2.

- **Regex.** `compileGlob` cuts `{,/*}` only when `{,/**}` was not cut, and yields `^<base>(/[^/:]*)?$`. That matches OPA's `glob.match(pattern, ["/", ":"], path)`:
  - the path itself and exactly one segment below match;
  - `*` crosses neither `/` nor `:`;
  - the empty segment after a trailing slash (`/v1/x/`) matches.
- **Error handling.** Combined or misplaced suffixes (`/v1/a{,/*}/b`, `{,/*}{,/**}`, `{,/**}{,/*}`) leave braces in the base, so they still raise "unsupported glob". The new test pins that.
- **Contract test.** The `opa_contract_test.go` matcher replaces `{,/**}` before `{,/*}`, so the two can't collide, and `concreteResource` handles the new suffix.
- **Regression.** 6672a576, the B-21 round-1 fix, changed `numbering.read` to `{,/*}` in `policies/authz/data.json`. That broke `LoadCatalogue`, and with it every identity test that loads the policy file. I confirmed this at 98b4b41e alone: `tenant_setup_test.go:95: unsupported glob`. My round-2 review didn't run the identity tests and missed it. e74a24b1 fixes it, and the identity package passes at the merge 04b81530. Any branch that takes 6672a576 without e74a24b1 will have red identity CI.

## Findings

### Minor

**r3-m1. The route rule only follows routes rendered with no props at all, so one harmless prop exempts a fake page in a file that fetches.**
File: `apps/web/src/shell/honestScreens.test.ts`, `routeComponentsWithoutProps` (the `noProps` test in `typeOf`).

Mutation p2: n13 sits in a file with an unrelated fetch and is routed as `createElement(AuditViewer, { title: "Audit" })`. All three rules pass:

- the file fetches, so the file rule passes;
- the component has props, so the component rule passes;
- the route passes a prop, so the route rule never sees it.

Fix: treat a route element as data-less unless its props object carries a data-shaped value. For example, count only props whose value is not a string, number or boolean literal, or arrow function, or check the specific prop names the component reads into `rows=`/`empty=`. Add p2 as a fixture.

**r3-m2. Route wrappers are followed only inside `module.ts`.**
File: `apps/web/src/shell/honestScreens.test.ts`, `routeComponentsWithoutProps` (`followWrapper` only walks local wrappers).

Mutation p5: an `AuditRoute` in its own `.tsx` renders `<AuditViewer key={id} />` in JSX, and AuditViewer's file also exports a fetching hook. The route rule checks `AuditRoute`, which shows no state itself, and the file rule is satisfied by the sibling hook. No module uses this pattern today (0 `export function …Route(` in `src/modules/**/*.tsx`), so the gap is latent.

Fix: when a route component's own file renders, with no props, a component imported from another scanned file, follow it as a route component too. Or reuse `followWrapper` on the route component's file through `resolveImport`.

### Nit

- **n-1.** The route rule's fixture list in `honestScreens.test.ts` would read better with p2 and p5 added as *known-missed* cases, each with a comment, so the next reader knows the boundary of the scan. Optional.

## Verdict

**APPROVE.** The numbering list route, the policy and identity glob support, the shell's planned-route filtering, the bundle guard and the docs were already approved in round 2 and are unchanged here. The honest-screens scan now catches every regression the review rounds tried except p2 and p5. Both need a fake page that sits in a file with an unrelated fetch *and* is reached through a prop or a cross-file wrapper. They can be follow-ups (r3-m1, r3-m2) and need not block the merge.
