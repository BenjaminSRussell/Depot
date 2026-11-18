# Review: B-05c (#666), commit 46ad2ed1

**Verdict: CHANGES REQUIRED**

The check runs, and it catches the cases the builder designed it for. Today's code has no consumer that uses any of the
patterns below, so the current green result is real. But the gate passes silently on several ordinary ways of reading an
event, on a whole consumer group that it fails to find, and on a credit note routed to the wrong topic. The
reconciliation acceptance criterion is only half built. Four Major findings, no Blocker.

## What I ran

- `python -m unittest deploy/scripts/test_event_contracts.py -v`: 14/14 OK (25 s with a warm cache; 113 s with an empty
  GOCACHE and a warm module cache).
- `go vet` and `golangci-lint v2.15.0` on `deploy/ci/eventfields`: 0 issues.
- `services/search`: `go vet ./...` and `go test -p 1 -count=1 ./...` against Postgres (db `rev_b05c_search`, now dropped):
  all OK.
- `test_kafka_consumers.py` (15 OK), `docs/ops/tools` (155 OK), `scripts/compile_architecture.py --check` (up to date).
- Mutation runs in a scratch copy made with `git archive 46ad2ed1` (the scratch copy is deleted and nothing was committed):

| # | Mutation | Result |
|---|----------|--------|
| 1 | search worker struct adds `nickname` | detected |
| 2 | nested `totals.foo` in billingDocumentData | detected |
| 3 | pointer field `extra *struct{bar}` | detected |
| 4 | `var m map[string]any; json.Unmarshal(evt.Data,&m); m["nickname"]` | **missed** |
| 5 | struct gets undeclared `json.RawMessage` field, then a second Unmarshal | detected (first level only) |
| 5b | second `json.Unmarshal(d.Raw,&inner)` of a *declared* field, reading `nope` | **missed** |
| 6 | `data := evt.Data; json.Unmarshal(data,&x)` | **missed** |
| 7 | helper `func decodeBytes(b []byte, v any)` called with evt.Data | **missed** |
| 8 | `json.NewDecoder(strings.NewReader(string(evt.Data))).Decode(&x)` | **missed** |
| 9 | sales `OrderConfirmedData` writes undeclared `secret_margin` | detected |
| 10 | sales renames `order_number` tag | detected (consumer side and producer side) |
| 11a | production shopfloor consumer struct adds `noSuchField` | detected |
| 11b | same, but `ShopfloorConsumerHandlers` reached only through an interface method | **missed**: the whole production-shopfloor group disappears, and no problem is reported |
| 12 | billing enqueues credit notes under aggregate `creditnote` (they would land on depot.billing.creditnote) | **missed** by SearchSourcesTest and by the existing search index test |

Every missed mutation compiled. A type-check failure is reported as a problem, so none of these passes is an accident of
the build.

## Findings

### M1 (Major): the analyzer passes silently on common decode forms
`deploy/ci/eventfields/main.go:451` (`isEventData`), `:603` (`walker.call`), `:1191-1240` (`collector.typ`, where `open`
is set); `deploy/scripts/test_event_contracts.py:308-323` (`go_reads` ignores `open` decodes).

A decode counts only when its source is `evt.Data` itself or a `json.RawMessage` *parameter*. The following yield no
decode and no problem, so the handler reports zero fields and passes (mutations 4, 5b, 6, 7, 8):
- a local copy of evt.Data;
- a `[]byte` helper parameter;
- `json.NewDecoder`;
- a decode into `map[string]any` (the decode is `open`, and go_reads drops open decodes);
- a second Unmarshal of a `json.RawMessage` field that the first decode yielded.

The module docs and CHANGELOG say the check follows reads "to the json.Unmarshal of the event data", but nothing fails
when a read takes another path.

**Fix:**
- Fail closed. In every function a handler entry reaches, report a problem for each use of `events.Event.Data` (a
  selector whose receiver is events.Event) that is not the first argument of a recognized Unmarshal, and is not an
  argument bound to a parameter in `decodeParams` or to a RawMessage parameter that is followed.
- Accept `[]byte` parameters as well as `json.RawMessage`.
- Turn `open` decodes into problems in `go_reads`, unless an allowlist with reasons names them (audit's CDC is already
  NOT_OUTBOX).
- Treat Unmarshal of a value whose expression is a field of a decoded struct as a nested decode under that path, or else
  as a problem.
- Add mutations 4 and 6-8 to `SYNTH_HANDLERS` as negative tests.

### M2 (Major): a consumer group whose handler map is not found passes, as long as its service has another group
`deploy/scripts/test_event_contracts.py:318`

`go_reads` fails only when a *service* has no handlers at all. Production, receiving and scheduling each run several
groups. In mutation 11b the analyzer did not follow interface dispatch, and production-shopfloor's two handlers vanished
with no problem reported. The walker follows only static `*types.Func` identifiers, so the same happens for a map built
in a non-main package's `init()`, behind an interface, or through reflection.

**Fix:** check per consumer, not per service. Each `kc.go_consumers()` entry has `topics`. Require every subscribed topic
to be the `events.Topic`/routed topic of at least one handler type the analyzer found for that service, and fail
otherwise ("consumer X subscribes to T but no handler the check finds reads it"). Also add a synthetic negative test that
uses an interface-dispatched map.

### M3 (Major): the search connector check does not tie an event type to its aggregate type
`deploy/scripts/test_event_contracts.py:661-667`; `deploy/ci/eventfields/main.go:1051-1058`

For aggregate-routed producers (billing, receiving), the check passes when the producer enqueues *any* event under the
aggregate whose topic the index reads. It does not check that the indexed type is enqueued under that aggregate. In
mutation 12 I moved credit notes to aggregate `creditnote` (`store.go:657`, `glposting.go:283`): they would land on
`depot.billing.creditnote`, the index would never see them, and both `SearchSourcesTest` and the existing
`TestAggregateRoutedTopicsMatchTheSourceOutboxAggregate` (which matches only `inv.ID`) still pass. ADR-0013 backlog #1
asks to "verify that every indexed type's source ... relays" it. The CHANGELOG wording ("an aggregate type the
producer's code enqueues") describes the weaker check that was built.

**Fix:**
- In `producers()`, for each `outbox.Enqueue`, resolve the `evt` argument back to its `events.New` call: a local
  assigned in the same function, or a parameter bound at call sites, using the same frame machinery as the type.
- Emit `(eventType, aggregate)` pairs, and in Python require `agg in aggregates_by_type[(service, typ)]`.
- Make an unresolvable evt for a type the index reads a problem.

### M4 (Major): the reconciliation compares with a hand-made report, and nothing gathers source counts
`services/search/internal/reconcile/reconcile.go:1-8,94`; `services/search/NOT-IMPLEMENTED.md:9`; D6 row in
`docs/architecture.md:93`

The acceptance criterion is "Search index reconciliation compares counts with sources (ADR-0013)". The commit compares
the index with a JSON file that nothing produces. It defers "gathering that report live" to #213. But #213 is the
dev-cluster smoke test, and the backlog lists only "the live smoke test on a dev cluster" as blocked-external. Counting
source records (each source's database or list API) needs no Kafka cluster.

**Fix:** pick one and state it consistently.
- (a) Add the source side: a `search reconcile --gather` step, or a per-source counter interface, that queries each
  source's count (party, product, purchasing, receiving, sales, fulfillment, billing, production, hr) with fakes in
  tests.
- (b) Record it as an explicit open item under a new issue (not #213). Mark ADR-0013 #1's reconciliation half as
  partial in the D6 row, the CHANGELOG and NOT-IMPLEMENTED, and do not present the acceptance criterion as met.

### m1 (Minor): unresolved `events.New` sites act as unlisted exemptions
`deploy/scripts/test_event_contracts.py` (report `unresolved` is never asserted); sales `store.go:430` via `rma.go:960`,
purchasing `store.go:286` via `catalog.go:402`.

If a consumed type is also written at an unresolved site, that site's payload is never compared. The docstring admits
this ("listed in its report but not checked").

**Fix:** list the known unresolved sites in a dict with a reason, the way PRODUCER_CODE_UNSEEN works. Fail on any new
unresolved site in a producer module of a consumed type, and fail on a stale entry.

### m2 (Minor): the check skips instead of failing when Go is missing; analyzer errors hide stderr
`deploy/scripts/test_event_contracts.py:675,893` (`skipUnless(go_command())`); `:281-283` (`check=True,
capture_output=True`).

If setup-go breaks, the job goes green with every test skipped. When `go run`/`go list` fails, `CalledProcessError`
prints only the exit status, so the CI log carries no reason.

**Fix:** skip only when `os.environ.get("CI")` is unset, and fail otherwise. Wrap the subprocess, re-raising with
`out.stderr` in the message.

### m3 (Minor): reconcile exit codes and coverage gaps
`services/search/cmd/search/main.go:32-36`; `services/search/internal/reconcile/reconcile.go:94-133`

- `search reconcile` with no path, or with extra arguments, falls through to "unknown command" with exit 2. Exit 2 is
  documented as "the index cannot be counted". A usage error should exit 3 (bad input).
- A report with `"counts": []` returns exit 0 and `compared: 0`, so it reads as a pass. Refuse an empty report in
  `ReadSource`.
- For each legal entity the report names, types that the index holds but the report omits are dropped silently, as are
  index rows of legal entities the report does not name. Run already counts them in each pass, so list them in the
  Result as `uncompared` (or as diffs with source 0 when the report claims to be complete).

RLS use is correct. Each pass runs as `ServicePrincipal(search-index, tenant, le)` through `WithTenantTx`; an empty
legal entity gives scopes `""`, so the tenant-wide pass sees only `search_record_tenant`, and the filter at line 112 is
redundant but harmless. `Store.Counts` excludes removed rows, and its store test covers other legal entities and other
tenants.

### m4 (Minor): the credit-note line schema descriptions are wrong
`services/billing/events/asyncapi.yaml:149-152`

They say "Copied from the credited invoice line". No credit-note path sets `Line.Source`: `api/credit.go:103` and
`api/modification.go:266` build lines without it, and `api/shipment.go:310-312` looks the source up from the original
invoice for that reason. So these four fields are never written for a credit note today. The schema is right to declare
them, because the shared `InvoicePostedLine` struct can write them, but the text misleads consumers.

**Fix:** say "Not set today: credit-note lines carry no source references; read them from the original invoice line
(original_line_no). Declared because the payload shares the invoice line shape." Or give `CreditNotePostedLine` its own
struct without these fields and drop them from the schema.

### m5 (Minor): a kafkajs consumer with no `as <X>Event` cast reports no reads and no problem
`deploy/scripts/test_event_contracts.py:387-394`

Only integration-hub-ts is asserted to read nothing. A new TS consumer that reads `event.data.x` without a cast passes.
Nested named types (`lines: Line[]`) stop at `lines[]`.

**Fix:** fail when a TS consumer has no recognized cast, unless an allowlist with a reason names it (integration-hub-ts).
Resolve a named member type when it is an interface in the same module.

### m6 (Minor): the producer's declared channel is never compared with where the event lands
`services/receiving/events/asyncapi.yaml:22`

Receiving's send channel is `depot.payables.invoice`, but outbox-receiving routes vendor invoices to
`depot.receiving.vendorinvoice`. The search AsyncAPI documents this mismatch instead of fixing it, and
`contracts.json` now lists `depot.receiving.vendorinvoice` with no publisher. D6 includes "some subscriptions have no
relayed producer".

**Fix:** correct receiving's channel address (and its other aggregate-routed channels), and add a check in
`search_source_problems` that the producer's `send` channel address equals the routed topic.

### n1 (Nit): "four schemas" should be three
`CHANGELOG.md:31` and the D6 row in `docs/architecture.md:93`

Three AsyncAPI documents and three messages changed (fulfillment packingslip.posted, sales order.confirmed, billing
creditnote.posted). Say "three messages".

### n2 (Nit): the D6 row puts its status note in the problem column
`docs/architecture.md:93`

D2 and D3 put "(part a ... done in B-05a)" in the exit-criterion column. D6 puts a long parenthetical in the problem
column. Move it to match, and shorten it.

### n3 (Nit): no `timeout-minutes` on the job
`.github/workflows/ci.yml:555`

A cold run compiles export data for about 30 modules (about 2 minutes here, more on a 4-vCPU runner). Add
`timeout-minutes: 20` so a hung `go list` does not hold a runner for 6 hours. No other job sets one, so this is optional.

## Checked and fine

- **CI job:** pinned actions, Go 1.26 matches every `go.mod` (`go 1.26.0`), and `cache-dependency-path: '**/go.sum'`.
  eventfields is stdlib-only (no go.sum), so the only downloads are the target modules' dependencies, which setup-go
  caches. The only pip dependency is pyyaml, and every transitively imported test module (test_argocd_apps,
  test_kafka_consumers, test_migrate_jobs, gen_migrate_jobs) needs only the stdlib and yaml. The synthetic module test
  rewrites the search `go.mod` replace to an absolute path and copies `go.sum`, so it works offline once the cache is
  warm. The lint matrix entry passes.
- **Search handlers:** the split structs decode the same values as before. Embedded unexported structs promote their
  fields in encoding/json. A credit note never carried `invoice_id` (it carries `original_invoice_id`), so indexing
  under `credit_note_id` is unchanged. PO-cancel `UpdateOnly` and the production status mapping are preserved, and the
  index tests cover each type, including malformed events.
- **Search AsyncAPI:** the ten channels, the eighteen messages and the payloads are `$ref`s that resolve to the
  producers' payload nodes, and the compiled-architecture output is regenerated.
- **Schema additions:** fulfillment `legal_entity_id` and sales `order_number`/`legal_entity_id` match the
  `omitempty` fields the producers write. Billing is covered by m4. No JSON-schema mirror needs updating.
- **Exemptions:** NOT_OUTBOX (audit `cdc:context`, `cdc:row-change`) is justified, and staleness is tested.
  PRODUCER_CODE_UNSEEN is empty. Pricing's four handlers legitimately read only `evt.TenantID`/`evt.Time`.
- **Open schemas:** with `is_open` forced to False, no current read fails, so nothing passes today only because a
  schema is open.
