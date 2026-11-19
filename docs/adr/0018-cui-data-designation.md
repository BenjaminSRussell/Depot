# ADR-0018: CUI data designation: a class overlay on P0-P3 held centrally in services/compliance

**Status:** Accepted — 2026-11-18 (DEPOT-301; change, event, history, client, web control and `cui.view` delivered, entity-type registry still partial)
**Evidence:** `services/compliance/internal/domain/data_designation.go` (`Effective`, `Boundary`),
`services/compliance/internal/api/api_designation.go` (`createDesignation`, `changeDesignation`, `resolveDesignations`, `designationHistory`),
`services/compliance/internal/store/data_designation_change.go` (`ChangeDesignation`),
`services/compliance/internal/api/owner_lookup.go` (`ownerCallBudget`), `services/compliance/events/asyncapi.yaml`,
`services/agents-ts/src/runtime/cuiDesignation.ts`, `apps/web/src/modules/compliance/DataDesignationPanel.tsx`,
`policies/authz/data.json` (`datadesignation.read`, `datadesignation.write`, `cui.view`)

## Context

Architecture 7.16 classifies data P0-P3 by sensitivity and says nothing about Controlled Unclassified Information (CUI).
CUI handling is a separate axis: the same record can be a P2 project and CUI, and CUI tenants run in an isolated
deployment (GovCloud) with an in-boundary AI model. Services and agents need one place to ask whether a record is CUI.

## Decision

1. **CUI is an overlay on P0-P3, not a fifth tier.** A record keeps its P-class; the data class `CUI` or `NONE` is added
   next to it. Nothing in P0-P3 handling is weakened or reinterpreted.
2. **CUI lives in a separate, isolated deployment.** `COMPLIANCE_BOUNDARY` is `standard` (default) or `cui`. The standard
   deployment refuses to create a CUI designation (409 `COM-1301`) so CUI data and its designations never enter
   it; the isolated CUI deployment accepts them.
3. **One central designation table in `services/compliance`.** A designation has a scope (TENANT, LEGAL_ENTITY or RECORD)
   and a class. Owning services do not store the class; they call `POST /v1/compliance/data-designations:resolve`.
4. **Effective-class precedence is most specific first:** RECORD, then LEGAL_ENTITY, then TENANT, otherwise NONE
   (`domain.Effective`). The answer carries its `source`.
5. **UNKNOWN is treated as CUI.** When the owning service cannot say which legal entity a record belongs to (lookup
   failure, timeout, record outside the caller's scope, a caller with no legal-entity scope), the resolver answers
   `UNKNOWN` and every consumer must treat it as CUI. A deadline on the resolve call (`resolveDeadline`) turns slow
   owners into UNKNOWN rather than an unbounded wait.
6. **Record scope takes the legal entity from the owning service,** never from the caller, so a caller cannot designate
   or probe a record outside its legal-entity scope. Only `project` records have an owning endpoint today.
7. **TENANT scope is tenant-wide only.** Creating or changing a TENANT designation needs `tenant.administer` and no
   legal-entity claim (403 `AUTH-0003`), checked in the API and again in the store.
8. **Rows are never deleted.** `PATCH /v1/compliance/data-designations/{id}` (If-Match, reason required) moves a row
   between `CUI` and `NONE`; the stored class is `CUI` or `NONE`. A `NONE` row is ignored by `Effective`, so a downgraded
   record falls back to what its legal entity and tenant say. Every change writes an `audit_event` (action `UPGRADE` or
   `DOWNGRADE`, with before and after) and one outbox event in the same transaction; an idempotent replay writes neither twice.
9. **The change is announced** as `dev.depot.compliance.datadesignation.changed.v1` on topic `depot.compliance.datadesignation`
   (the `compliance` context is added to the Kafka Connect route and ACLs).
10. **History is the audit trail:** `GET /v1/compliance/data-designations/{id}/history`, newest first.
11. **`cui.view` is a flag duty** that grants no route. It marks who may see CUI content; holding a designation duty does
    not imply it.
12. **AI consumers use `createDesignationClient`** (agents-ts): any failure to learn the class is `UNKNOWN`, which is
    treated as CUI (`treatAsCui`); only `NONE` is usable by AI (`aiMayUse`). The client never throws.
13. **The resolver is bounded:** 8 s deadline, concurrency 8, and a per-request cap of `2 * 500` outbound owner calls
    (`ownerCallBudget`); once spent, remaining refs are `UNKNOWN`.

## Consequences

- Consumers pay one resolve call per batch (at most 500 refs); the owning service lookup is one call per ref and
  legal-entity scope until a bulk lookup exists on the owning service.
- The web control (`DataDesignationPanel`, shown on the project page) is hidden without `datadesignation.read`, and its
  change action without `datadesignation.write`.
- Remaining: entity types `party`, `item`, `contract` and `attachment` (no owning endpoint that returns a legal entity),
  so `PARENT_RECORD` inheritance for attachments; the service grant letting agents call compliance and wiring the client
  into the agent runtime; the identity-provider mapping for `cui.view`; enforcement of `cui.view` on CUI content (E1-S36).
