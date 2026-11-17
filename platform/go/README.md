# platform/go

The Go kernel every Depot service builds on: `httpx` (router, problem details, serve, shared metrics), `tenant`
(principal from the gateway's headers), `authz` (duty checks), `dbx`, `events`, `outbox`, `logpseudo` and the rest. Each
package's doc comment describes it; this file records the contracts that reach beyond one package.

## Authorization decision record (`authz.decision`)

Every authorization decision writes one structured log record (B-04, D36, ADR-0017):

- in every Go service, from `authz.Require`, `authz.RequireDuty` and `authz.RequireAnyDuty` (source: `DEPOT_SERVICE_NAME`,
  else `service`);
- in identity and the single-host gateways, from `identity.RequireOPA`, `RequireOPAForRequest` and `RequireOPAForGateway`
  (source `identity`, `devgateway`, `depot-server`), plus the gateways' own legal-entity-selection and
  non-canonical-path denials;
- on Envoy, as `result.record` of the ext_authz OPA's decision log (`data.authz.envoy` in `policies/authz/authz.rego`,
  source `envoy`).

The slog message and the `event` field are `authz.decision`. Schema 1:

| Field | Value |
|---|---|
| `event` | `authz.decision` |
| `schema` | `1`; a change that is not an added field bumps it |
| `log_class` | `audit.other`: the retention class, architecture E.8's "3 y other" audit class |
| `source` | the gateway (`envoy`, `devgateway`, `depot-server`) or the service that decided |
| `tenant` | the tenant id (empty when the request had no verified principal) |
| `subject` | the user's pseudonym, `logpseudo.Subject(tenant, sub)`: `pn_` and 16 hex digits, the HMAC of the id under the tenant's key derived from the pseudonymization key (below), the same value the whole-log pseudonymizer writes. The Envoy record computes the same value in rego (`data.authz.log_pseudonym`). `[redacted]` without a key or a tenant. Never the raw id |
| `duty` | the route's duty (a service), the duty that granted or was missing (a gateway; `a\|b` for `RequireAnyDuty`'s alternatives); may be empty |
| `route` | method and route template: the chi pattern in a service (`GET /v1/ledger/journal-entries/{id}`), the prefix at a gateway (`GET /v1/ledger/**`), `unmatched` when none is known or the gateway routes no service at that prefix. Never the raw path, which can carry ids |
| `outcome` | `allow` or `deny` |
| `reason` | `duty_held`, `duty_missing`, `no_principal`, `policy_error` (a service); `policy_allowed`, `policy_denied`, `policy_unavailable`, `unauthenticated`, `legal_entity_selection`, `not_canonical` (a gateway) |
| `request_id` | chi's request id; the single-host gateways make their own (a client's `X-Request-Id` is dropped) and forward it, and Envoy removes a client's `X-Request-Id`, generates its own and forwards it, so a gateway's record and the service's record of one request share it |

Allow records are written at INFO, deny records at WARN.

**Pseudonym key.** `logpseudo.KeyFromEnv` picks the key:
- the dedicated pseudonymization key `DEPOT_LOG_PSEUDONYM_KEY` (or the file named by `DEPOT_LOG_PSEUDONYM_KEY_FILE`), base64 of 32 bytes, used only for this MAC;
- else fieldcrypt's current master key (`FIELDCRYPT_KEYS`).

Both go through fieldcrypt's MAC derivation. Give the dedicated key fresh bytes; never fieldcrypt's master key, because the same Secret is mounted in the ext_authz OPA pods (ADR-0017). Switching a fieldcrypt-keyed service to the dedicated key changes its pseudonyms from then on. `httpx.Serve` and devgateway call `logpseudo.LoadSubjectKey`, which uses it whatever the cloud profile says (whole-log pseudonymization, `logpseudo.Install`, uses the same key and still follows the profile). `depot-server` reads only the dedicated key for its own records. It passes `FIELDCRYPT_*` only to the seven services that load field-encryption keys (`fieldcryptServices`: compliance, hr, migration, party, payables, receiving, sales), and passes the pseudonym key to none, unless a service's own configuration sets it. A key configuration that cannot be read leaves every subject `[redacted]` and is logged; it does not stop the service. Most service charts give no key yet, so their records carry `[redacted]` until Secret `depot-log-pseudonym` is wired into them (follow-up). Until then the fieldcrypt-keyed services pseudonymize under fieldcrypt's current key, so Secret `depot-log-pseudonym` (the Envoy OPA's key) must hold the bytes of fieldcrypt's current key, or Envoy and those services name the same user with different pseudonyms.

`authz.Record` writes the subject with `logpseudo.SubjectAttr`, which marks it as already pseudonymized. The pseudonymizer passes only that mark and the `[redacted]` marker through: a string that merely looks like a pseudonym is pseudonymized like any personal value.

**Shipping and retention.** Records go to stdout as JSON with the rest of the log. A log shipper must keep records with
`log_class: audit.other` for three years. No shipper is deployed yet (#213): the field is the tag it keeps them by.

**Envoy.** The ext_authz OPA (`deploy/gateway/authz-opa.yaml`) logs every decision to the console with
`decision_logs.mask_decision: /system/log/mask`. That mask is a local policy in the OPA's ConfigMap, not part of identity's
bundle, and erases the whole input (raw subject, the token's claims as headers, the client address, the path). The logged
decision keeps its result, whose `record` has the fields above, and OPA's `labels` carry `log_class: audit.other`. The
OPA reads `DEPOT_LOG_PSEUDONYM_KEY` from Secret `depot-log-pseudonym` (optional).

**Metric.** `depot_authz_denied_total{source,duty,reason}` counts every deny of a Go process. `authz` registers it with
`httpx.RegisterMetrics`, so every service's `/metrics` (`httpx.WithMetrics`, `MetricsHandler`, `ServeWithMetrics`)
carries it. It has no user or tenant label. Alert `DepotAuthzDenySpike`
(`deploy/observability/charts/observability/templates/alerts.yaml`) fires on a spike per source and reason. A request
Envoy refuses reaches no Go process; Envoy's own ext_authz counters (`envoy_cluster_ext_authz_denied`, `_error`, scraped by
`deploy/gateway/envoy-podmonitor.yaml`) count it, with alerts `DepotAuthzEnvoyDenySpike`, `DepotAuthzEnvoyCheckErrors`
and `DepotAuthzOPAUnavailable`.
