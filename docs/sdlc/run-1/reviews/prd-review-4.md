# Stage 1 review, gate attempt 4: condition-3 check
Reviewer: independent agent (did not produce the audit). Repo commit 0662309dc4ea40c18b391ecb4975d7ceeb1f47d4; date 2026-11-17; sample seed 20261120 (review-sample-4.md). The sample has two parts:
- (a) the 4 rows the gate-3 targeted pass changed;
- (b) 10 fresh random Built Must rows that were not in sample 3.

## Method
Same as review 3. For each row:
- pulled the full FR block with its note lines (extract_fr.py over rv3/ids4.txt);
- checked every clause and note against code, migrations and tests;
- read the owning service's NOT-IMPLEMENTED.md (NI), including "deviation", "later slice" and "handoff" rows;
- for every conditional leg ("then", "tie →", "only when", "defaults to") and every scope clause ("any", "per", lists of modules), found the code path that implements that leg, and a test for it.

DB-free tests I ran, all passing:
- mobile `./internal/gs1`;
- shopfloor `TestRound_BR25_2` and `TestAbsence*`.

I also ran one reviewer probe, `TestReview4_ThirdKeyIsOrderNumber`. It ran on the scratchpad copy probes/scheduling, which I confirmed is identical to the repository's schedule.go. I deleted the probe file afterwards.

The repository was not modified.

**Gate rule:** PASS if no row in (b) is overstated and no row in (a) is wrongly left Built.

## Verdicts

### (a) Rows changed by the gate-3 targeted pass
| FR | Audited | Verdict | Correct status | Evidence |
|---|---|---|---|---|
| FR-26.7 | Partial | Agree (justified) | Partial | `SmartRound` picks the band from the unrounded value, then adds 1 when the candidate falls below it. It never re-checks the band (services/pricing/internal/domain/calc.go:23-35). Worked by hand: 99.96 falls in the 10–100 band, so floor 99 + .95 = 99.95 < 99.96, and +1 gives 100.95. Under the "≥ 100 → whole" band it should be a whole number. The band clause is defective, and price journals call this function (journal.go:155). |
| FR-3.4 | Partial | Agree (justified) | Partial | The note "`version` increments on every update" is not generated. The generated DDL only has `version integer not null default 1` (services/metadata/internal/gen/migration.go:52-58). The generated repository is an interface (gen/go.go:51-56). The generated handler only passes `If-Match` through to `repository.Update` (testdata/golden/Address.handlers.go:27-29). There is no trigger and no generated Update implementation, and grep finds no `version + 1` in services/metadata or platform. |
| FR-8.10 | Partial | Agree (justified) | Partial | "Enforce … module-level blocks (purchase, sales, inventory, production)" needs the four modules to refuse a blocked released product. grep for `process-policy`, `PRD-LC-BLOCKED`, `blocked_*`, `lifecycleState` and `phase_out`/`obsolete` over the Go code of purchasing, sales, inventory, production and fulfillment finds nothing. The flags are evaluated only by product's own process-policy endpoint. |
| FR-1.9 | Partial | Agree (justified, narrowly) | Partial | `make deploy-local` (deploy/Makefile:14-39) never installs the cert-manager CA into the host trust store and never maps the host. It ends with an echo telling the user to add `/etc/hosts` themselves. Trusting the CA is a manual runbook step (docs/runbooks/delivery/deploy-local.md:22-25). So "locally trusted CA" behind `https://app.local.depot.dev` is not delivered by the target. This could be scripted in the repo (for example, mkcert-style trust and hosts entries), so it is an in-repo gap, not only live proof. |

### (b) Fresh random Built Must rows
| FR | Audited | Verdict | Correct status | Evidence |
|---|---|---|---|---|
| FR-26.9 | Built | Agree | Built | `price_journal_line` supports CREATE/UPDATE/DELETE/RESTORE with before/after state and expected version (services/pricing/migrations/0002_journals_rebates.sql:30-45). `ValidateJournal` validates every line, including overlaps and duplicate records, before the post (domain/journal.go:71-121). `Reverse` builds a new journal in reverse line order: CREATE→DELETE, UPDATE→UPDATE(before), DELETE→RESTORE(before). Its expected versions refuse a record that changed since the post (journal.go:217-237). `reverses_journal_id` links back to the original. The atomic and 100k tests exist. FR-26.7's rounding defect affects journals only through FR-26.7's own clause. |
| FR-41.5 | Built | Agree | Built | `domain.Resolve` tries exact, then (g,*,*), then (*,*,*), and otherwise fails with PROJ-POST-001 naming the key (services/projects/internal/domain/posting.go:31-49). The table carries all five accounts, and CHECKs allow only resolvable keys (migrations/0003_setup_plans.sql:29-45). Both GL posting paths use it: transactions by (group, category, property) at store/glposting.go:264, and estimate postings by (group,*,*) at :398. A failed resolve blocks the posting with PROJ-POST-001 (:400). |
| FR-32.3 | Built | Agree | Built | The server parser accepts the `C1`/`d2`/`Q3` symbology identifiers (services/mobile/internal/gs1/gs1.go:115, 213-265) and uses GS 0x1D (:14). The web parser does the same (gs1.ts:6, 162-170). Routing is in scanRouting.ts with its tests. The vectors suite runs in CI (vitest at ci.yml:231, Go test shards). The Go tests pass. |
| FR-25.2 | Built | Agree | Built | `time_registration` has kind clock-in/out/absence, `device_id` and `server_received_at` default now() (services/shopfloor/migrations/0001_init.sql:30-53). BR-25.2 rounding covers both conditional legs: a clock-in within the tolerance before shift start snaps to start, a clock-out within the tolerance after shift end snaps to end, and anything else rounds to `rounding_minutes` (internal/domain/time.go:83-104). Rounding uses the worker's stored profile, with `rounding_minutes` limited to 1–15 (migrations/0010_pay_setup.sql:37; store/setup.go:116-157). The tests pass. |
| FR-22.6 | Built | Agree | Built | The `pegging` table links supply to demand (services/planning/migrations/0001_init.sql:70-84). `reqLess` orders by date, then priority, then ref (internal/domain/engine.go:406-414), with priorities sales 1 < forecast 2 < safety 3 (:70-73). The generic supply ledger pegs every supply layer, on hand and receipts as well as planned orders (engine.go:523-525, 540, 560). The explain tree is built (planning NI:326). |
| FR-23.5 | Built | **Disagree: overstated, Must** | Partial | The third sort key is not the order number. `orderPlan` sorts by (due, priority, **OrderID**), and its own comment says "(delivery date, priority, order id)" (services/scheduling/internal/domain/schedule.go:440-472). `OrderID` is the order's uuid (`scheduling_operation.order_id uuid`, migrations/0001_scheduling.sql:33), and `Operation` has no order-number field at all (schedule.go:57-90). The number exists only in `scheduling_order_import.order_number` (migrations/0013). My probe gave ORD-0001 (id fff…) and ORD-0002 (id 000…) the same due date and priority. `orderPlan` placed ORD-0002 first, so ties are broken in uuid order, not by order number. The gate-3 pass recorded this leg as "(delivery date, priority, order number)", which is wrong. The rest is correct: best resource by least finish, then least load (betterCandidate, schedule.go:563-574; gate-3 probe `TestProbe_TieFallsToLeastLoad`), then code. |
| FR-6.6 | Built | Agree | Built | `postal_address` has every structured field, lat/long, `valid_from`/`valid_to`, and one open version per address (services/org/migrations/0010_postal_address.sql:1-29). `address_purpose` has the seven purposes, with one primary per (owner, purpose) enforced by a partial unique index (:31-42). Seven country formats (internal/domain/address.go:75-84). Per-country postal regex from the country reference data, enforced on write (store/address.go:183; domain/address.go:46-58; migration 0030 for CA). `Geocoder` returns lat/long with Confidence, and `NullGeocoder` is the default (domain/address.go:29-41; api/api.go:157). NI:57-58 (future-dated versions, purpose history per version) are outside the FR text. |
| FR-45.8 | Built | Agree | Built | apps/technician plus apps/web/src/modules/technician cover route, navigate, travel/work, checklist, photos, truck parts, signature, customer summary, an encrypted offline queue and the service worker (fieldservice NI:210). The authz seed for the execute duty is present (policies/authz/data.json:2154-2158). Push and real devices are outside the FR text or live proof. |
| FR-6.2 | Built | Agree (with a note) | Built | Four types; code unique per (tenant, type); tenant-scoped; optional `default_legal_entity_id` FK; `manager_party_id` only on cost_center via a CHECK (services/org/migrations/0006_operating_unit.sql:1-19). Note: wave 7 (services/party) exists, but `manager_party_id` is only UUID-checked and never resolved against party (api/operatingunit.go:98-100, 214; NI:66 manager picker is a later slice). I read "resolved after wave 7" as a timing note, the same way I read the FR-41.5 key FK note, so the row stays Built. Log it as backlog. |
| FR-11.8 | Built | Agree | Built | `POST /journal-entries/{id}/reverse`; the date defaults to the original and may override to any open date ≥ original (`LEDGER_DATE_INVALID`); sides are swapped and `reversal_of_line_id` is set (migrations/0003_journal.sql:73); once-only; reinstatement is allowed by default (`allow_reinstate` default true, migrations/0030_journal_name_controls.sql:16). All of this is in ledger NI:84, and the three cited tests exist. |

## Result counts
| Group | Rows | Agree | Disagree | Overstated | Over-corrected |
|---|---|---|---|---|---|
| (a) changed by gate-3 pass | 4 | 4 | 0 | 0 | 0 |
| (b) fresh random Built Must | 10 | 9 | 1 (FR-23.5) | 1 | 0 |
| **Total** | **14** | **13** | **1** | **1** | **0** |

Across the four gates, overstated Built rows in random Built samples have fallen: 9 of 20 at gate 1, 4 of 30 at gate 2, 1 of 10 at gate 3, and 1 of 10 at gate 4.

## Observations
1. **Tie-break legs were checked for the behaviour, not the key.** For FR-23.5 the gate-3 pass probed the resource tie-break, but it read the order sort key from the FR text instead of from the code. *How to find:* for every Built row whose FR lists a sort or priority key such as "(x, y, z)", compare each key with the field the comparator actually reads. Check whether that field is the named business attribute (an order number, a code) or a surrogate id.
2. All four gate-3 downgrades are sound, and none is over-corrected.
3. Backlog, non-blocking:
   - FR-6.2: resolve `manager_party_id` against the party service.
   - FR-23.5: also add a repository test for the least-load tie-break.

## Gate recommendation: **FAIL** (narrowly)
One row in (b) is overstated: **FR-23.5 Dispatch heuristic (Must)**. The FR orders by (delivery date, priority, **order number**). The engine breaks ties by uuid, has no order-number field, and the probe shows it ordering against the order numbers. No row in (a) is wrongly left Built.

To pass:
1. Downgrade FR-23.5 to Partial, with the gap "third sort key is the order uuid, not the order number; `Operation` carries no number (schedule.go:440-472, 57-90)".
2. Run the observation-1 key check over the remaining Built rows and log the rows checked.
3. Have a reviewer check only the rows that check changes. Given four gates of falling error rates and this single narrow miss, I recommend not drawing another random sample unless step 2 changes more than two rows.
