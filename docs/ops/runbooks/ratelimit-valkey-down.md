# Runbook: rate-limit Valkey down (gateway rate limits fail open)

Symptoms: tenants exceed their published tier limits without getting `429`, Envoy's rate-limit stats show errors or
timeouts towards the rate-limit service, the `ratelimit-valkey` pod in `envoy-gateway-system` is not Ready or restarting,
or the `envoy-ratelimit` Deployment Envoy Gateway runs there is not Ready.

## The decision: fail open

Every global rate limit (`deploy/gateway/backendtrafficpolicy.yaml`, the per-tenant tier limits, and the per-address
limits of the AS2, region and portal edges) is counted by Envoy Gateway's rate-limit service in one Valkey pod,
`ratelimit-valkey` (`deploy/envoy-gateway/ratelimit-valkey.yaml`, no persistence, ADR-0014). Envoy Gateway is configured
with `failClosed: false` and `timeout: 20ms` under `config.envoyGateway.rateLimit` (`deploy/envs/dev/apps/envoy-gateway.yaml`,
copied to stage by `deploy/scripts/stage_apps.py`; `deploy/scripts/test_argocd_apps.py` checks both). When the rate-limit
service or its Valkey cannot answer within 20 ms, Envoy lets the request through, unlimited, instead of answering 500.

Why fail open (B-09 part b):

- The limits share capacity fairly between tenants (ADR-0014 decision 1). They are not an access control: every API
  request is still authenticated by the JWT filter and authorized by the ext_authz OPA, and both of those fail closed
  (`deploy/gateway/README.md`, Authorization). A Valkey outage therefore removes fairness, not security.
- The store is a single pod with no persistence. Failing closed would turn a pod restart, a node drain or an eviction into
  a full API outage for every tenant, a far larger impact than a few minutes without limits.
- Losing the counters is already accepted (ADR-0014): the limits restart from zero when the pod restarts.
- The cost is bounded: each request waits at most the 20 ms timeout while the service is down, and the outage is visible
  (below). Brute-force protection on sign-in is Keycloak's own (brute-force detection), not these limits, and the portal
  BFFs keep their own per-session limit in their session store (`deploy/session-valkey`).

What fails open with it: the per-tenant tier limits, and the per-address limits of the AS2 edge (edi keeps its own token
bucket per AS2 endpoint behind it), the region health edge and the portal edge.

The architecture's target (section 12.7) falls back to a local per-pod limiter; none is configured, so the fallback is
unlimited until one is added.

## 1. Confirm

1. `kubectl -n envoy-gateway-system get pods -l app.kubernetes.io/name=ratelimit-valkey` and the `envoy-ratelimit`
   pods: not Ready, CrashLoopBackOff, Pending or evicted.
2. `kubectl -n envoy-gateway-system logs deploy/envoy-ratelimit` shows Redis connection errors.
3. Envoy's rate-limit filter counters (`ratelimit.error`, `ratelimit.failure_mode_allowed`) rise on the `depot-api`
   Envoy pods.

## 2. Restore

1. The Valkey pod holds nothing worth keeping: delete it (`kubectl -n envoy-gateway-system delete pod -l
   app.kubernetes.io/name=ratelimit-valkey`) and let the Deployment recreate it. Counters restart from zero.
2. If it is Pending, check node capacity and the pod's requests (10m CPU, 32Mi); if it is OOM-killed, the 64mb
   `maxmemory` with `allkeys-lru` should prevent that: check for a changed limit in `deploy/envoy-gateway/ratelimit-valkey.yaml`.
3. If the `envoy-ratelimit` pods stay unready after Valkey is Ready, restart them
   (`kubectl -n envoy-gateway-system rollout restart deploy/envoy-ratelimit`).
4. Do not switch to `failClosed: true` to stop abuse during the outage: that refuses every API call. To slow one abusive
   tenant, disable its users or service accounts in Keycloak or identity instead.

## 3. Verify

1. A request over a starter tenant's 600/min limit gets `429` again.
2. `ratelimit.failure_mode_allowed` stops rising.
3. Record the outage window: limits were not enforced during it.

## Changing the decision

Fail closed would need a highly available store first (replicas with Sentinel or a managed Valkey) and a review of the
outage impact above. Change `failClosed` in the dev Application, regenerate stage with `python3 deploy/scripts/stage_apps.py`,
and update `test_the_rate_limit_service_fails_open_with_a_timeout` and this page.
