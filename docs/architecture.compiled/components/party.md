# Party

**As built (v1.0):** one Go service, `services/party`, in place of the designed TypeScript `party-ts` (ADR-0008). Its
outbox is relayed, and the merge event payload matches the designed contract.

## Purpose & Boundaries
Tenant-level parties (organizations and persons) with layered roles; customers and vendors per legal entity; groups,
payment terms, posting profiles, tax registrations, vendor bank accounts, vendor onboarding (prospects, requests,
questionnaires, documents) and portal profile data; duplicate detection and governed merge.

## Services
| Directory | Lang | Datastore | Outbox relayed |
|---|---|---|---|
| `services/party` | Go | Postgres, own database | yes (`outbox-party`) |

## Interfaces
- Prefix `/v1/party` (resources: parties, customers, vendors, customer-groups, vendor-groups, payment-terms, posting-profiles, tax-registrations, merge-requests, pending-changes, vendor-prospects, vendor-requests, vendor-questionnaires, vendor-documents, vendor-audit, customer-portal, vendor-portal, partners, controlled-documents). Every operation and duty is in `contracts.json`.
- Merge is a governed request (`merge-requests`) approved through `workflow-ts` (party-merge activities); sensitive changes go through `pending-changes`.
- Party numbers come from `services/numbering` over HTTP (ADR-0015).
- Designed custom methods (`parties:match`, `duplicate-candidates`, `{id}:merge`, `bank-accounts/{id}:reveal`, `/v1/party/portal/profile`) are the target shape; the built route names differ.

## Data Ownership
Party, roles, customer and vendor per legal entity, groups, terms, tax registrations, bank accounts and merge state
(designed EN-013..017). Sensitive fields (bank details, national ids) are sealed with `platform/go/fieldcrypt` and
looked up by blind index (ADR-0011), not stored in designed `*_secure` pgcrypto tables. Party is tenant-level; customer
and vendor rows are per legal entity.

## Events Produced
`depot.party.party`, `depot.party.customer`, `depot.party.merge`, `depot.party.change`, `depot.party.vendorbank`,
`depot.party.vendorgroup`, `depot.party.vendorinvite`, `depot.party.protectedfields` (types in `contracts.json`). The
merge event carries the designed fields (mergeId, survivorPartyId, mergedPartyIds, roleIdMap, mergedAt).
*Target:* `party.customer.creditchanged.v1` with the designed payload; snapshot topics.

## Events Consumed
None declared in AsyncAPI. Designed consumers (`party-approvals`, `party-legalentity-ref`, `party-numbersequence`) are
replaced by synchronous calls to identity/org and numbering and by `workflow-ts` approvals. Other services are meant to
rewrite `party_id` on the merge event; no AsyncAPI document declares a `receive` on `depot.party.merge`.

## Dependencies
internal: platform (numbering, org legal entities, workflow-ts approvals), ledger (posting profiles), edge-identity,
platform-kernel (`fieldcrypt`, `tenant`). external: tax-id validators (localization pack), none in the critical path.

## Gotchas & Constraints
- P3 fields never go into events, logs or exports; the frozen `passage.fieldcrypt.*` labels protect existing ciphertext (ADR-0005).
- Bank-detail changes from portals route to approval, never a direct write.
- Merged parties keep a pointer; fiscal rows in other services keep the old id.
- `registration_number` is NULL for person parties.

## Target — not built
- Per-subject keys and crypto-shred on erasure: superseded by pseudonymization (ADR-0011).
- `<ctx>-partymerge` consumers in the subledger services; credit-limit change event with the designed payload.
- Designed TypeScript deployable `party-ts`: superseded by ADR-0008.

## Source Sections
- Architecture §3.2 (party row), §5.6 party endpoints (target), §7.16 field encryption; data EN-013..017; events §3.3.
- ADR-0008, ADR-0011, ADR-0015; delta §1.1 (`services/party`), §2.1 (party row), §2.2 (`payload-party.party.merged`).
