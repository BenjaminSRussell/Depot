# ADR-0011: Application-level field encryption (`fieldcrypt`) instead of pgcrypto with Vault transit

**Status:** Accepted with conditions — 2026-11-17 (records a decision already made in code; SDLC run 1, stage 2, deviation D15)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` §3 row 8, §4 D15, §5 C14; `docs/sdlc/run-1/architecture-review-response.md` R-05, R-09
**Amended:** 2026-11-17 after the independent stage-2 review: status changed to Accepted with conditions; Conditions added; metadata removed from the users

## Context

The architecture (§7.16, data §3 and EN-006, EN-012) designed P3 (sensitive) columns encrypted in Postgres with
pgcrypto, using per-tenant keys from Vault transit, in `*_secure` tables, plus a per-subject key (`pii_key_ref`) for
the audit `pii_payload` so that GDPR erasure is a crypto-shred.

The code encrypts in the application (`platform/go/fieldcrypt/fieldcrypt.go`):

- Master keys come from `FIELDCRYPT_KEYS` (or the mounted file `FIELDCRYPT_KEYS_FILE`) as `id=base64(32 bytes)` pairs,
  with `FIELDCRYPT_CURRENT_KEY_ID` naming the key for new writes. An entry `id=wrapped:<version>:<base64>` is a master
  key wrapped by Azure Key Vault or a Managed HSM (`fieldcrypt/azurekv`, tool `fieldcrypt-wrap`), unwrapped once at
  start and held in memory.
- `Seal` derives a per-tenant data key with HKDF-SHA256 (salt = tenant id, info `passage.fieldcrypt.v1`) and encrypts
  with AES-256-GCM, binding tenant and field name as additional data. The envelope records version and key id, so old
  ciphertexts open after the current key changes.
- `MAC` derives a separate per-tenant key (info `passage.fieldcrypt.mac.v1`) for blind indexes (equality lookups on
  encrypted values) and for log pseudonyms (`platform/go/logpseudo`, domain `passage.log.pseudonym.v1\0`, matched in
  the TypeScript and Python services).
- Services that use it: compliance, hr, migration, party, payables, receiving and sales import `platform/go/fieldcrypt`;
  integration-hub-ts has a byte-compatible TypeScript port (`src/crypt.ts`). These eight charts mount the master keys
  (`FIELDCRYPT_KEYS`). metadata does not encrypt; its code generator emits fieldcrypt-sealed fields for other services.
  No migration uses `pgp_sym_encrypt` or another pgcrypto function.
- Key wrapping is supported in the charts through Azure workload identity (`global.workloadIdentity.fieldcryptClientId`,
  key `fieldcrypt-kek` in `deploy/tofu/cluster/modules/aks`), but no environment under `deploy/envs` enables it, so
  master keys are plain secrets wherever they are deployed. hr has an opt-in re-encryption job (`hr reseal-pii`,
  `reseal.enabled: false`); no other service has one.
- Erasure is by pseudonymization (compliance, BR-48.5), not by deleting a key: data keys are derived from a shared
  master key, so no tenant's or person's key can be destroyed on its own.

ADR-0005 froze the three `passage.*` labels: they are wire identifiers, and data at rest depends on them.

The combination this ADR accepts is risky: there is no crypto-shred, and one master key per installation derives every
tenant's key, so a leaked master key exposes every tenant's sealed fields and no tenant can be erased by destroying a
key. The conditions below are what keep it acceptable.

## Decision

Sensitive fields are encrypted and blind-indexed in the service, with `platform/go/fieldcrypt`, before they reach
Postgres. Keys are master keys supplied as secrets and, where a key-management service is available, wrapped by it
(Azure Key Vault or Managed HSM today). Postgres never sees plaintext or keys.

The three labels below are frozen. They are the only sanctioned uses of the former product name; changing one needs a
`.v2` label and a re-encryption or re-index migration:

| Constant | Value | Used for |
|---|---|---|
| fieldcrypt KDF info | `passage.fieldcrypt.v1` | every field ciphertext |
| fieldcrypt MAC KDF info | `passage.fieldcrypt.mac.v1` | blind indexes and the per-tenant log pseudonym key |
| log pseudonym domain | `passage.log.pseudonym.v1\0` | pseudonyms in retained logs (Go, TypeScript, Python) |

### Conditions

This decision holds only with these controls. Until each is met and evidenced, no stage or prod environment holds real
personal data. The unmet conditions are tracked as D35.

1. **KMS wrapping is mandatory outside dev.** Every master key in stage, prod and customer installs is wrapped by a
   key-management service (Azure Key Vault or Managed HSM today; a second KMS where a target needs one). Plain
   `id=base64` keys are allowed only in dev and CI. Custody is named (who can unwrap, who can rotate), and there is a
   rotation runbook with a re-encryption job for every service that seals data, run as a drill.
2. **Master-key holders are minimized.** Only services that seal or open fields, or compute blind indexes or log
   pseudonyms, mount the keys; the list per environment is generated and reviewed by security; no service gets the
   keys for convenience. Today eight charts mount them.
3. **Erasure reaches every copy.** Pseudonymization covers the source tables and every derived copy: the search index
   (ADR-0013), Kafka topics (retention stated: 7 days today, D24), the lakehouse, logs (pseudonymized at write), and
   backups, whose bounded retention is the figure the erasure SLA cites; the erasure log is re-applied on any restore
   before traffic. A test checks each copy.
4. **Counsel signs off pseudonymization as erasure (Q17)** before personal data of EU subjects is loaded into any
   environment, including whether per-subject keys are needed for the audit `pii_payload` or HR data.

## Alternatives rejected

- **pgcrypto with Vault transit** (the design). Puts keys or plaintext in SQL statements and the database session,
  ties every encrypting service to Vault on the hot path, and does not give blind indexes for lookups.
- **Vault transit encrypt/decrypt per value.** A network round trip per field read or write.

## Consequences

- Encryption works the same on every target (Kubernetes, Windows, any cloud), with or without Vault.
- Searching encrypted fields is equality-only, through blind indexes.
- Crypto-shred is not available, per person or per tenant. Erasure relies on pseudonymization and deletion, and on
  backup expiry for copies in backups.
- Rotating a master key protects new writes; existing values keep the old key until re-encrypted.
- Only Azure Key Vault is supported as a wrapping service; it is supported in the charts but enabled in no environment.
- A leaked master key exposes every tenant's sealed fields in that installation; condition 1 and 2 bound that risk.
- Backlog: enable KMS wrapping in stage and prod, record key custody, and write the rotation runbook (condition 1; D35).
- Backlog: build a re-encryption job for every sealing service (hr has `reseal-pii`), and run a rotation drill (condition 1; D35).
- Backlog: generate and review the per-environment list of master-key holders (condition 2; D35).
- Backlog: extend erasure to the search index, Kafka, the lakehouse and logs, tie backup retention to the erasure SLA, and test each copy (condition 3; D35, D20).
- Backlog: decide whether per-subject keys (crypto-shred) are needed for the audit `pii_payload` or for HR data, and get counsel sign-off on pseudonymization as erasure (condition 4, Q17; D35).
- Backlog: add key-wrapping support for a second KMS (Vault transit or a cloud KMS other than Azure) if a target needs it (condition 1; D35).
- Backlog: add a CI check that fails if any of the three frozen `passage.*` labels changes or if a new `passage` string appears outside them (ADR-0005; D43).
