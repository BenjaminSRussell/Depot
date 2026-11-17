# ADR-0005: Rename the product from Passage to Depot

**Status:** Accepted — 2026-11-17

## Context

The product was built as **Passage**. The owners renamed it **Depot**. The name appeared in about 43,000 places across
6,400 files. Some of those were prose. Others were identifiers: the Go module path
`github.com/BenjaminSRussell/passage/...`, the `passage_app` RLS role, Kubernetes namespaces and chart names, the
Keycloak realm, `PASSAGE_*` environment variables, `X-Passage-*` and `Passage-*` HTTP headers, CloudEvents types
`dev.passage.*`, Kafka topics `passage.*`, the Java package `dev.passage`, the Python package `passage_dispatch`, image
names `ghcr.io/benjaminsrussell/passage-*` and the `PSG-` ticket prefix.

Nothing runs in production yet. Go-live waits on the live-cluster steps in #213. Dev and stage can be redeployed from
scratch.

## Decision

Rename everything at once, with no compatibility aliases:

- `passage`, `Passage` and `PASSAGE` become `depot`, `Depot` and `DEPOT` in every tracked text file and path. That
  covers code, SQL migrations, OPA data, Helm values, Argo CD applications, docs, the PRD, the architecture and the
  roadmap.
- The `PSG-<n>` ticket prefix becomes `DPT-<n>` (`roadmap/tickets/DPT-*.md`).
- Go modules are `github.com/BenjaminSRussell/depot/...`. The repository itself becomes `BenjaminSRussell/Depot`, a
  rename the owner makes in GitHub settings. GitHub redirects the old URL.
- The service-token signing context becomes `depot.service-token.v1.`. Tokens live for minutes, so no compatibility
  is needed. The cross-language test vectors were re-signed with their committed seeds.
- The print service's golden PDFs were regenerated because their metadata carries the product name.

### Frozen identifiers

Three cryptographic labels keep the former name. Data at rest was derived with them, so they are wire identifiers,
not branding:

| Constant | Value | What depends on it |
|---|---|---|
| `fieldcrypt` KDF info | `passage.fieldcrypt.v1` | Every field-level ciphertext (`platform/go/fieldcrypt`) |
| `fieldcrypt` MAC KDF info | `passage.fieldcrypt.mac.v1` | Blind indexes stored for lookups, and the per-tenant log pseudonym key |
| Log pseudonym domain | `passage.log.pseudonym.v1\0` | Pseudonyms in retained logs, matched across Go, TypeScript and Python |

Changing any of them needs a versioned label (`.v2`) and a re-encryption or re-index migration, not a rename.

## Consequences

- **Redeploy dev and stage from scratch.** Databases grant to `depot_app`, Debezium uses `debezium_cdc_<service>`
  under the renamed databases, the data namespace is `depot-data`, charts and images are `depot-*`, the realm is `depot` (`deploy/keycloak/realm-depot.json`),
  topics are `depot.*` and images are `ghcr.io/benjaminsrussell/depot-*`. Vault paths and ExternalSecrets follow the
  same names, so secrets must be re-seeded under the new paths.
- **Local and CI test environments** use `DEPOT_TEST_DATABASE_URL`, `DEPOT_TEST_APP_DATABASE_URL` and the `depot_app`
  role (`.github/postgres-test-setup.sql`).
- **Browser storage keys** (`depot.workspaces.v1`, `depot.density.v1.*` and others) start empty. Users lose saved
  workspace and density preferences once.
- **GitHub Pages** serves the demo under `/Depot/`. Rename the repository before the next Pages deploy, or the demo's
  asset paths will not match the site path.
- **Links** to `github.com/BenjaminSRussell/Depot/...` resolve once the repository is renamed.
- **Homonym:** "depot" is also a transport term, the vehicle start and end location in route planning (`services/transport`,
  `services/routing-py`). That meaning is unchanged. Read it from context: the product is always "Depot" as a proper
  noun, and the routing term is the lowercase `depot` field of a route plan.
