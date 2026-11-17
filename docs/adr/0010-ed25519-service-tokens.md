# ADR-0010: Short-lived Ed25519 service tokens for service-to-service calls

**Status:** Accepted — 2026-11-17 (records a decision already made in code; SDLC run 1, stage 2, deviations D14 and §3 row 7)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` §3 row 7, §4 D14, §5 C3

## Context

The architecture (§6.3, §7.1, decision D4) designed internal calls with Keycloak client-credentials tokens from an
`_internal` realm plus mTLS, with the user's token forwarded in `X-Depot-Subject-Token` when a user started the work.

The code signs its own tokens (`platform/go/tenant/service.go`):

- Each calling service holds an Ed25519 seed (`DEPOT_SERVICE_SIGNING_KEY` or `_FILE`, optional `DEPOT_SERVICE_KEY_ID`
  for rotation); each target holds the callers' public keys (`DEPOT_SERVICE_PUBLIC_KEYS`).
- `ServiceSigner` mints a token with claims `iss` (caller), `aud` (target), `tid`, `le`, `act` (the end user), optional
  `vnd`/`cus` (only the supplier and customer portals may sign them), `duties`, `iat`, `exp`, `jti`. The signature
  covers the context string `depot.service-token.v1.` plus the body. Maximum life 60 s, clock skew 5 s, 4 KiB cap.
- The duties in a token are the intersection of what was asked for and a **static grant table in code**
  (`serviceGrant(caller, target)`), never the user's own duties. Tenant-level duties are signed only when named, and
  only for a tenant administrator or for tenant reads.
- Targets refuse a replayed `jti` through an in-memory cache, shared across replicas by the table
  `service_token_replay` (`dbx.ServiceTokenReplayDDL`).
- The token travels in `X-Depot-Service-Token` and replaces every user header for that call. TypeScript services carry
  a compatible implementation (for example `services/workflow-ts/src/serviceToken.ts`) pinned by cross-language test
  vectors. ADR-0005 already treats the signing context as a wire identifier.

## Decision

Service-to-service calls authenticate with these Ed25519 service tokens. There is no `_internal` Keycloak realm and
no Keycloak client per service. A token names the calling service, the target, the tenant, one legal entity, the end
user as `act`, and only the duties the grant table allows that caller at that target. Tokens live at most 60 s and are
single-use per target. The context string `depot.service-token.v1.` is versioned; changing the format means `.v2`.

## Alternatives rejected

- **Keycloak client credentials in an `_internal` realm** (the design). One more Keycloak round trip per token, a
  client per service, and the end user would still have to be carried separately.
- **Mesh identity (mTLS only).** Proves which workload called, but carries neither the tenant, the legal entity nor
  the user, and there is no service mesh in the deployment.

## Consequences

- No identity-provider dependency on the hot path of a peer call; verification is a local signature check.
- Least privilege between services is explicit and reviewable in one table, and the ledger sees the user (`act`) and
  the calling service on every posting.
- Key distribution and rotation are Depot's job, not Keycloak's: every service needs its seed and every target the
  public keys of its callers.
- The grant table is code: adding a peer call needs a kernel change.
- Not every peer call uses it yet: some services forward user headers instead (ADR-0006).
- Backlog: document key generation, distribution (ExternalSecret per service) and rotation with `DEPOT_SERVICE_KEY_ID`, and add a rotation drill to the runbooks (D14).
- Backlog: generate the NetworkPolicy peer allow-list of ADR-0006 from the grant table (D1).
- Backlog: add a metric and alert for `ErrReplayStoreUnavailable` (the replay check fails closed, so a database outage refuses every peer call to that service) (D14).
- Backlog: replace the remaining user-header forwarding between services with service tokens (D1).
