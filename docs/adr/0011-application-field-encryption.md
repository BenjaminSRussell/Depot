# ADR-0011: Application-level field encryption (`fieldcrypt`) instead of pgcrypto with Vault transit

**Status:** Accepted — 2026-11-17 (records a decision already made in code; SDLC run 1, stage 2, deviation D15)
**Evidence:** `docs/sdlc/run-1/architecture-delta.md` §3 row 8, §4 D15, §5 C14

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
- Services that use it: compliance, hr, metadata, migration, party, payables, receiving and sales. No migration uses
  `pgp_sym_encrypt` or another pgcrypto function.
- Erasure is by pseudonymization (compliance, BR-48.5), not by deleting a key: data keys are derived from a shared
  master key, so no tenant's or person's key can be destroyed on its own.

ADR-0005 froze the three `passage.*` labels: they are wire identifiers, and data at rest depends on them.

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
- Only Azure Key Vault is supported as a wrapping service.
- Backlog: build a re-encryption job that rewrites values sealed under a retired master key, and a runbook for master-key rotation (D15).
- Backlog: decide whether per-subject keys (crypto-shred) are needed for the audit `pii_payload` or for HR data, and get counsel sign-off on pseudonymization as erasure (Q17; D15).
- Backlog: add key-wrapping support for a second KMS (Vault transit or a cloud KMS other than Azure) if a target needs it (D15).
- Backlog: add a CI check that fails if any of the three frozen `passage.*` labels changes or if a new `passage` string appears outside them (ADR-0005).
