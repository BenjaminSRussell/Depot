# Review 2: B-05c fixes (#666), commit 78abe4d3

**Verdict: CHANGES REQUIRED.** One Major remains. Everything else is Minor or Nit.

Every round-1 Major is fixed or reasonably re-scoped, and all seven break tests the analyzer missed in round 1 now fail
the check. The fail-closed rule covers `evt.Data` itself but not what happens after the first decode. When a decoded
`json.RawMessage`, map or `any` field is read anywhere other than a direct `json.Unmarshal(d.Field, ...)` in the same
function, the check still passes silently (R2-M1). The fix is small and costs nothing for today's consumers.

## What I ran

- `python -m unittest deploy/scripts/test_event_contracts.py -v` at HEAD (fd95ca7c, which merges 78abe4d3): 20/20 OK in 16 s.
- `golangci-lint v2.15.0` on `deploy/ci/eventfields`: 0 issues.
- `services/search`: `go vet` and `go test -p 1 -count=1 ./...` against Postgres (db `rev_b05c2_search`, dropped): all OK.
- `test_kafka_consumers.py` (OK), `test_argocd_apps.py` (62 OK), `docs/ops/tools` (OK), `test_compile_architecture.py`
  (OK), `compile_architecture.py --check` (up to date).
- No topic name `depot.payables.invoice`, `depot.payables.settlement` or `depot.receiving.policy` remains in code or deploy
  config. The analyzer's receiving routes (vendorinvoice, invoiceadjustment, settlement, auditpolicy, ersvendor,
  erspriceconfirmation, receipt, serviceentrysheet) match the renamed channels. The debezium ACLs list the new topics.
- I ran the break tests in a scratch copy made with `git archive 78abe4d3`. The copy is now deleted and nothing was
  committed.

### Round-1 break tests that were missed

| # | Mutation | Round 1 | Round 2 |
|---|----------|---------|---------|
| 4 | `map[string]any` decode, then a key read | missed | **fails** (open decode, OPEN_DECODES is empty) |
| 5b | second Unmarshal of a declared RawMessage field | missed | **fails** (`worker_type.nope` is not declared) |
| 6 | `data := evt.Data; json.Unmarshal(data, &x)` | missed | **fails** (`nickname` is not declared) |
| 7 | `decodeBytes(b []byte, v any)` helper | missed | **fails** |
| 8 | `json.NewDecoder(...evt.Data...)` | missed | **fails** ("does not follow") |
| 11b | shopfloor handler map reached only through an interface method | missed | **fails** (interface dispatch is followed) |
| 12 | billing `creditnote.posted` enqueued under aggregate `creditnote` | missed | **fails** in the channel check and in SearchSourcesTest |

### New break tests

| # | Mutation | Result |
|---|----------|--------|
| N1 | decoded RawMessage field passed to a helper `parseInner(b json.RawMessage)` that Unmarshals it | **missed** |
| N2 | `raw := d.Raw; json.Unmarshal(raw, &inner)` | **missed** |
| N6 | `reDecode(d.Raw)` where `reDecode(b []byte)` Unmarshals | **missed** |
| N4b | field typed `any` on a declared key (`worker_type`), read as `d.WorkerType.(map[string]any)["nope"]` | **missed** |
| N4 | undeclared field typed `map[string]any` | detected (the key itself is undeclared) |
| N3 | closure `parse := func(b []byte) error {...}; parse(evt.Data)`, plus an unrelated top-level `func sameShape(b []byte) error` | **missed** |
| N3b | the same closure with no same-shape function in the module | detected ("function value") |
| N5 | `json.Marshal(evt)`, then Unmarshal into `struct{ Data struct{Nick} }` | **missed** |

Every missed mutation compiled. A type-check failure is a reported problem, so none of these passes comes from a broken
build.

## Findings

### R2-M1 (Major): decoded data with no fixed shape is not fail-closed
`deploy/ci/eventfields/main.go:1019` (`decodedPath`), `:1921-1968` (`collector.typ` / `leafOrOpen`)

The nested-decode support only fires for `json.Unmarshal(<decoded local>.<field>, ...)` in the same walk, and only when
the decoded variable is the decode target ident. A `json.RawMessage`, map or `any` field of decoded data is otherwise a
plain leaf. Any further read of it is invisible: passed to a helper (N1, N6), copied to a local (N2), or type-asserted
and indexed (N4b). The check passes as long as the top-level key is declared. This is the round-1 M1 failure mode one
level down, in forms just as ordinary as `decodeBytes(evt.Data)`. The builder's claim "Nested Unmarshal is handled" holds
only for the direct form.

**Fix (fail closed, about 15 lines):**
- In `collector.typ` with `decode: true` and `path != ""`, record a json.RawMessage, `[]byte`, map or interface leaf as
  a *dynamic* leaf.
- When `nested()` records a decode for that path, clear the flag.
- After the walk, report each remaining dynamic leaf as an open decode, which `go_reads` fails unless OPEN_DECODES names
  it.

I measured the cost with an instrumented copy over all 18 consumer modules. The only dynamic leaves today are audit's
Debezium `before`/`after`/`schema`/`payload`, which NOT_OUTBOX already skips, so nothing current needs an allowlist
entry. Add N1, N2 and N4b to `SYNTH_HANDLERS`.

### R2-m1 (Minor): a func-value data sink is accepted because some unrelated function has the same signature
`deploy/ci/eventfields/main.go:850-860` (`sink`), `:636-643` (`solveDataParams`)

When event data goes to a func value with a `[]byte` parameter, `solveDataParams` marks every top-level module function
of that shape as a data parameter. `sink` then accepts the call if any such function exists. A closure, whose decode is
never recorded because closure parameters are not `fn` params, passes whenever an unrelated function of the same shape
exists (N3), and fails otherwise (N3b). Whether a closure is caught depends on coincidence.

**Fix:** resolve the func value first. Use the existing `funcLit` helper, or the locals' assignments to a
`*types.Func`. Walk a FuncLit with its `[]byte` parameter treated as data, and accept a resolved FuncDecl only by its own
`dataParams`. Fall back to "matching signature" only for a value with no resolvable assignment, such as a struct field or
a parameter that never gets bound, and report a problem when no match is found.

### R2-m2 (Minor): an unresolved-call allowlist entry covers every unresolved call in that function
`deploy/scripts/test_event_contracts.py:740`, `UNRESOLVED_EMITS` (`:103`) and `UNRESOLVED_ROUTES`

Entries are keyed by (call file, binding file, function), with no line and no count. A second unresolved `events.New` or
`outbox.Enqueue` added to `moveReturn`, `AcknowledgePurchaseOrder` or `AcceptSheet` is absorbed by the existing entry.
For UNRESOLVED_ROUTES, the hand-written `{type: aggregate}` map then stands in for the analyzer, and nothing checks it.
I checked the current AcceptSheet mapping (receipt.posted to `receipt`, everything else to `serviceentrysheet`): it is
accurate today.

**Fix:** add the expected number of unresolved calls per key, or key on the call expression's source text, so that a
new call fails.

### R2-m3 (Minor): the reconciliation's follow-up issue is "to be filed"
The D6 row in `docs/architecture.md:93`, `services/search/NOT-IMPLEMENTED.md:9`, `services/search/README.md:26`,
`services/search/internal/reconcile/reconcile.go:8`, `CHANGELOG.md:43`

Re-scoping M4 as partial is acceptable. The docs now say it plainly, and NOT-IMPLEMENTED explains why it needs no cluster.
But nothing points to an issue yet.

**Fix:** file the issue before merge and replace "follow-up issue to be filed" with its number, in all five places.

### R2-n1 (Nit): a whole event re-marshalled is not tracked
In N5, a handler that runs `json.Marshal(evt)` and decodes the bytes into an envelope struct passes. This is contrived.

**Fix:** report a problem when handler-reached code passes `json.Marshal` or `json.NewEncoder` a value of type
`events.Event`.

### R2-n2 (Nit): the group check unions the reads of all groups in a service
`deploy/scripts/test_event_contracts.py:398-405`

If one group's handler map is missed, the check still passes when a sibling group of the same service reads a type on
the same topic. No service has two groups on one topic today.

**Fix:** tie each group to its handler map (the `consumerSpec.handlers` value at the `kafka.ConfigFromEnv` call) when
the analyzer can, and otherwise document the limit.

### R2-n3 (Nit): failures behind a field store are dropped
`deploy/ci/eventfields/main.go:1657`

In `evalEvent`, a field store whose value cannot be evaluated is discarded (`got, _ :=`). The route resolves from the
other stores, so a type that reaches the field only through the failing store is lost without a trace.

**Fix:** append those failures to `UnresolvedRoutes`.

## Round-1 findings, closed

- **M1:** closed for `evt.Data`. A local alias, `[]byte`/`json.RawMessage` parameters, `json.NewDecoder`, `len`, an
  unfollowed call, map decodes and the direct nested decode are all covered, with synthetic tests. The remainder is
  R2-M1.
- **M2:** closed. Interface dispatch is followed, and the per-group topic check exists with a negative test. Its limit
  is R2-n2.
- **M3:** closed. (type, aggregate) routes are traced from Enqueue to New, checked per type in search, and producer
  channels are compared with the routes.
- **M4:** re-scoped to partial, as above. Leaving the source side for later is accepted once the issue is filed.
- **m1:** UNRESOLVED_EMITS and UNRESOLVED_ROUTES fail on new and stale entries. Their keying is R2-m2.
- **m2:** `needs_go` fails in CI (`CI` is set on GitHub runners), and analyzer stderr is surfaced.
- **m3:** usage error exits 4, an empty report exits 3, index rows the report omits are listed as `uncompared` and give
  exit 1. The behaviour is tested and the README is updated.
- **m4:** the credit-note descriptions now say the fields are not set today, which is accurate.
- **m5:** TS_NO_DATA is in place, and members typed by interfaces are expanded.
- **m6:** receiving's channels are corrected, and the channel-versus-route check exists with a negative test.
  CHANNEL_MISMATCH is empty.
- **n1–n3:** fixed. "Three messages", the D6 note is in the exit column, `timeout-minutes: 20`.
