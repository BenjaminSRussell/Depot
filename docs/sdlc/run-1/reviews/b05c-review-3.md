# Review 3: B-05c (#666), commit 70efe589

**Verdict: APPROVE**

R2-M1 is closed. I mutated real service code (search's worker, credit-note and invoice handlers, and tax's
packing-slip mapper), not only the synthetic module. Every N1–N6 and N4b variant now fails the check, and so do the
remaining round-2 Minors and Nits.

I found two new holes. Both need code no consumer has today and that would be unusual to write, so they are Minor and
do not block. Fix them in a follow-up; they need not hold this batch.

## What I ran

- `python -m unittest deploy/scripts/test_event_contracts.py` at HEAD: 20/20 OK. `golangci-lint` on eventfields: 0 issues.
- `services/search`: `go vet` and `go test -p 1 -count=1 ./...` against Postgres (db `rev_b05c3_search`, dropped): all OK.
- `test_kafka_consumers.py`: OK. `compile_architecture.py --check`: up to date.
- #731 is cited in the D6 row, NOT-IMPLEMENTED, README, both reconcile files and the CHANGELOG. Part of this came from
  d27e75bc, which landed between rounds.
- I ran the mutations in a scratch copy made with `git archive 70efe589`. The copy is now deleted and nothing was
  committed.

## Mutations on real code

| # | Mutation (service code) | Result |
|---|-------------------------|--------|
| N1 | search worker: RawMessage `worker_type` passed to helper `parseInner(b json.RawMessage)` that decodes `nope` | **fails** (`worker_type.nope` not declared) |
| N2 | `raw := d.Raw; json.Unmarshal(raw, &inner)` | **fails** |
| N3 | closure `parse := func(b []byte)` plus an unrelated same-shape top-level function | **fails** (the closure is followed) |
| N3b | the same closure alone | **fails** |
| N4 | undeclared `map[string]any` field | **fails** (dynamic, and undeclared) |
| N4b | `worker_type` retyped `any`, read as `.(map[string]any)["nope"]` | **fails** (dynamic field) |
| N5 | `json.Marshal(evt)` then decode into an envelope | **fails** ("marshals the whole event") |
| N6 | `reDecode(b []byte)` helper on the decoded field | **fails** |
| T1b | tax `fromPackingSlip`: `partner_country` retyped `json.RawMessage`, used only as `string(...)` | **fails** (dynamic field) |
| T2 | tax: RawMessage field decoded by helper `innerLE(b []byte)` reading `nope` | **fails** (`legal_entity_id.nope`) |
| N8b | search billing `totals` retyped RawMessage, decoded and also read through `NewDecoder` in a method on the struct | **fails** (fail-closed: the receiver is not tracked, so the field stays dynamic) |
| N9 | production builds `eventing.Config` by field assignment in a helper | **fails** ("no eventing.Config named production-planning") |
| N7 | search worker field of a module type whose `UnmarshalJSON` decodes an undeclared `nope` | **missed** (R3-m1) |
| N7b | search worker decode target whose own `UnmarshalJSON` decodes an undeclared `nickname` | **missed** (R3-m1) |
| N8c | invoice handler: `totals` RawMessage decoded into `{gross}` *and* read through `json.NewDecoder(...)` into a map, `m["nope"]` | **missed** (R3-m2) |

Every missed mutation compiled; the analyzer reports a type-check failure as a problem. Two mutations had flaws and are
excluded:
- The first T1 gave a second field the tag `legal_entity_id`. Go then ignores both fields, so it was not a fair test.
  T1b replaces it.
- A map-typed N4b did not compile. The `any` variant replaces it.

## Findings

### R3-m1 (Minor): a module type with its own `UnmarshalJSON` is accepted as an opaque leaf, or read as plain struct fields
`deploy/ci/eventfields/main.go:2327` (`codec`), `:2352`

- For a field, any type with `UnmarshalJSON`/`UnmarshalText` ends the path (N7).
- For the decode target itself, the custom method is ignored and the struct's fields are listed instead (N7b).

A module-defined codec that decodes an object therefore reads keys the check never sees. Today the module codecs in
consumer modules are all scalars (tax `Dec`/`Date`, purchasing `Day`, receiving `fieldDecimal`/`wireDecimal`), so
nothing is affected yet.

**Fix:** when the type's `UnmarshalJSON` is declared in the analyzed module, follow it the way `followNested` follows a
helper. Its `[]byte` parameter is the data at that path, and the target at path "" is the type's own decode. Treat an
`UnmarshalJSON` that cannot be followed as dynamic. Keep stdlib and dependency codecs (time, decimal) as leaves. Add N7
and N7b to `SYNTH_HANDLERS`.

### R3-m2 (Minor): once a nested decode clears a field's dynamic mark, other reads of that field go unchecked
`deploy/ci/eventfields/main.go:1336-1341` (`nested`) and `:939` (`sink`, which applies only to `evt.Data`)

`nested()` clears the mark as soon as one followed decode reads the field. The field can then also go to
`json.NewDecoder`, `len`, an unfollowed function or a map decode, and those reads stay invisible (N8c reads
`totals.nope`). `evt.Data` itself is checked fail-closed by `sink`, but decoded dynamic fields are not.

**Fix:** run the same sink walk on every expression that `decodedPath` resolves to a dynamic path. Allowed uses are
`json.Unmarshal` argument 0, a followed helper's `[]byte`/`json.RawMessage` parameter, a local copy and a conversion.
Anything else is a handler problem. Add N8c as a synthetic case.

### R3-n1 (Nit): calls with identical source text in one function share an allowlist key
`deploy/scripts/test_event_contracts.py` (`unresolved_problems`, key `(file, via file, func, text)`)

A second, textually identical unresolved call in the same function is still absorbed by its sibling's entry. One
example is a second `outbox.Enqueue(ctx, tx, aggregate, aggregateID, evt)` in `AcceptSheet`. This is unlikely, and
much narrower than round 2's key.

**Fix (optional):** add an occurrence count to the key.

## Round-2 findings, closed

- **R2-M1:** closed. Dynamic RawMessage, `[]byte`, map and interface leaves are marked, cleared by direct, local-copy and
  helper decodes, and fail unless OPEN_DECODES names them. OPEN_DECODES is empty. Confirmed on search and tax code.
  The residual hole is R3-m2.
- **R2-m1:** closed. Func values are resolved to their closure or function first, and signature matching is used only
  for values that cannot be resolved (N3 fails).
- **R2-m2:** closed. Allowlist keys include the call text, with a negative test for a second call in a listed function.
  The leftover edge is R3-n1.
- **R2-m3:** closed. #731 is cited everywhere.
- **R2-n1:** closed. Marshal, MarshalIndent and Encode of an `events.Event` is a problem (N5).
- **R2-n2:** closed. Groups are tied to their own `eventing.Config` maps, with a negative test for moved reads and for a
  missing Config. N9 also fails.
- **R2-n3:** closed. Field-store failures are reported in `unresolvedRoutes`.
