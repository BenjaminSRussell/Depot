# ADR-0014: Valkey for gateway rate-limit counters and portal sessions (addendum to ADR-0001)

**Status:** Accepted — 2026-11-17 (records a decision already made in code; SDLC run 1, stage 2, deviation D26; closes architecture Q4)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` §3 row 19, §4 D26, §5 C20

## Context

ADR-0001 names no cache or key-value store. The architecture (§7.9, Q4; data OQ-11) proposed in-cluster Valkey, never
the Redis product, for Envoy Gateway's global rate-limit counters, pending an ADR-0001 addendum.

The code uses it in two places:

- **Gateway rate limits.** `deploy/envoy-gateway/ratelimit-valkey.yaml` runs one digest-pinned
  `valkey/valkey:8.1.9-alpine` pod with no persistence and no auth, reachable only from `envoy-gateway-system`
  (NetworkPolicy). `deploy/envs/dev/apps/envoy-gateway.yaml` points Envoy Gateway's rate-limit service at it. It backs
  the per-tenant tier limits (starter 600/min, professional 3000/min, enterprise 12000/min, unknown tier falls back to
  starter) and the per-address limits of the AS2, region and portal edges (`deploy/gateway/README.md`).
- **Portal sessions.** `customer-portal-ts` and `supplier-portal-ts` keep sessions and their per-session rate limit in
  a Redis-protocol store when `sessionStore.url` is set (`redis://` or `rediss://`); without it they keep sessions in
  memory and the chart refuses more than one replica. No environment sets the URL yet.

## Decision

Valkey is an approved in-cluster dependency of Depot, for exactly two uses:

1. the Envoy Gateway global rate-limit counters (ephemeral; losing them resets the counters, which is acceptable);
2. the portal BFFs' shared session store, when a portal runs more than one replica.

Valkey is never a system of record and never a cache of authorization decisions or P3 data. Any other use needs its
own decision. The Redis product is not used; the clients speak the Redis protocol, so a Valkey server is the target.

## Alternatives rejected

- **Local (per-pod) rate limits in Envoy only.** No shared store, but each gateway replica counts on its own, so a
  tenant's effective limit grows with the replica count and tier limits cannot be enforced as published (D.7).
- **Rate limits in each service.** Duplicates the logic in about 60 services and counts per service, not per tenant
  across the API.
- **The Redis product.** Licence change (RSAL/SSPL) conflicts with ADR-0001's open-source, in-stack rule; Valkey is
  the protocol-compatible fork.
- **Sticky sessions for the portal BFFs instead of a shared store.** Loses sessions on every pod restart or scale-in
  and needs session affinity in the gateway; kept only as the single-replica in-memory default.
- **Portal sessions in Postgres.** Workable, but adds a database per portal for short-lived data and a write per
  request for the per-session rate limit.

## Consequences

- Global tier limits work across gateway replicas; burst allowances are not expressible in the Envoy Gateway API and
  are not configured.
- Portal sessions held in Valkey are bearer secrets (the BFF session maps to tokens); that store needs auth, TLS and a
  NetworkPolicy, unlike the counter store.
- The rate-limit Valkey is a single pod; if it is down, Envoy Gateway's behaviour (fail open or closed) decides
  whether requests pass.
- Backlog: deploy a session Valkey with auth, TLS (`rediss://`), persistence off and a NetworkPolicy for the portals in stage and prod, and set `sessionStore.url` (D26). *Stage in B-09 part b:* `deploy/session-valkey` (`session-valkey-stage`, checked by `test_session_valkey.py`); `sessionStore.url` is not set yet, because the portal charts cannot mount the store's CA; prod is part c.
- Backlog: decide and test the gateway behaviour when the rate-limit Valkey is unavailable, and document it in the runbook (D26). *Done in B-09 part b:* fail open (`failClosed: false`, 20 ms timeout) in dev and stage, tested in `test_argocd_apps.py`; runbook `docs/ops/runbooks/ratelimit-valkey-down.md`.
- Backlog: add the rate-limit Valkey to stage and prod environments with the gateway (D4). *Stage in B-09 part b* (`envoy-gateway-stage`); prod is part c.
