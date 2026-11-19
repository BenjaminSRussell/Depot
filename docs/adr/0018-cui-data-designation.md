# ADR-0018: CUI data designation: a class overlay on P0-P3 held centrally in services/compliance

**Status:** Accepted — 2026-11-18 (DEPOT-301, slice 2 of 5)
**Evidence:** `services/compliance/internal/domain/data_designation.go` (`Effective`, `Boundary`),
`services/compliance/internal/api/api_designation.go` (`createDesignation`, `resolveDesignations`),
`services/compliance/internal/api/owner_lookup.go`, `policies/authz/data.json` (`datadesignation.read`, `datadesignation.write`)

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

## Consequences

- Consumers pay one resolve call per batch (at most 500 refs); the owning service lookup is one call per ref and
  legal-entity scope until a bulk lookup exists on the owning service.
- Entity types `party`, `item`, `contract` and `attachment`, PATCH and downgrade, the changed event, the client
  library, the web control, history and the `cui.view` duty are the remaining slices of DEPOT-301.
