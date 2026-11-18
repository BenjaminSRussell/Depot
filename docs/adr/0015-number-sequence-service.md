# ADR-0015: Number sequences are allocated by a central `numbering` service over HTTP

**Status:** Accepted with conditions — 2026-11-17 (records a decision already made in code; SDLC run 1, stage 2, deviation D21)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` §3 row 18, §4 D21, §5 C17, §6.2 item 1

## Context

The architecture (§7.18, Q2; data EN-005; consolidated open question 4, "Resolved (C2)") decided that `platform` owns
number-sequence definitions while every posting database keeps its own `number_sequence_counter`, operated by a
kernel `sequence` package and locked inside the posting transaction. Its reason: "a gap-free counter cannot live only
in `depot_platform` and be called over HTTP (an abort after allocation would burn a number)".

What is built:

- `services/numbering` (`services/numbering/README.md`) issues per-legal-entity numbers in `gap_free`, `continuous` and
  `non_continuous` modes through `POST /v1/numbering/sequences/{code}/next`, under a row lock, with an optional
  `Idempotency-Key`; a 10k-concurrent test shows no duplicates or gaps inside the service.
- hr and party call it over HTTP. Party (`services/party/internal/numbering`) signs a service token with the grant
  `numbering.sequence.allocate` when a signing key is configured (`cmd/party/main.go`), and forwards the user's
  `X-Depot-*` headers only as a fallback when no key is set; hr uses the service grant `numbering.sequence.allocate`.
- There is no `platform/go/sequence` package. Other services number documents themselves (for example inventory's
  journal numbers in `services/inventory/migrations/0042_journal_number.sql`).
- The service now offers reserve, confirm and void with a voided-number log, and refuses `next` on `gap_free` for service callers (B-35). No database or Secret is provisioned for it in any
  environment (`services/numbering/NOT-IMPLEMENTED.md`).

## Decision

`numbering` is the shared allocator for sequences that do not have to be gap-free across a business transaction:
master-data codes (party and worker numbers) and `continuous` / `non_continuous` document numbers.

The condition: a `gap_free` number for a fiscal document (invoice, journal, credit note, where law requires no gaps)
must not be taken from `numbering` over HTTP while that is a plain `next` call, because the caller's transaction can
abort after the number is committed. Until `numbering` offers reserve, confirm and void with an audit trail of voided
numbers, such numbers stay in the owning service's database, allocated in its posting transaction, as the architecture
designed.

## Alternatives rejected

- **Only per-service counters** (the design). Fine for fiscal documents, but every service would reimplement
  administration of sequence definitions and formats.
- **Allow `gap_free` over HTTP as built.** Burns a number on every abort after allocation; that breaks NFR-D3 and the
  statutory requirement in some countries.

## Consequences

- One place to define and administer sequences, and one more synchronous dependency (and network hop) for hr and party.
- `gap_free` in `numbering` currently means "no gaps inside the counter", not "no gaps in issued documents"; the mode
  name overstates it.
- Done (B-35): reserve / confirm / void with a voided-number log in `numbering`, and `gap_free` `next` refused for service callers (D21).
- Done (B-35): every fiscal document number allocation is listed in `services/numbering/internal/store/fiscal_numbers.json` and `TestFiscalNumberInventory` fails on an unclassified one (D21).
- Backlog: provision `depot_numbering` and its Secret in every environment, and give hr and party a timeout and a clear error when numbering is unavailable (D4, D21).
- Backlog: remove party's header-forwarding fallback, so the call always uses a service token (D1).
