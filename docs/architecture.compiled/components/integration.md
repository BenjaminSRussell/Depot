# Integration

**As built (v1.0):** `services/integration-hub-ts` (TypeScript: webhooks, API clients, API catalogue) and `services/edi`
(Go: EDI, AS2, e-invoicing, punch-out) in place of the designed `integration-ts` (ADR-0008). The migration pipeline is
`services/migration`, grouped under the integration context in architecture §3.2 but owned here by the
migration-framework component. `apps/developer-portal` is a stub.

## Purpose & Boundaries
Partner-facing integration: webhook subscriptions and signed deliveries, dead letters and replays of deliveries, API
client registration and the API catalogue (integration-hub-ts); EDI partner profiles, X12 and EDIFACT inbound,
interchanges, AS2 with MDNs, cXML and punch-out, e-invoicing and Peppol, self-billing (edi); the developer portal
(not built).

## Services
| Directory | Lang | Prefix | Outbox relayed |
|---|---|---|---|
| `services/integration-hub-ts` | TS | `/v1/hub` | no (D2) |
| `services/edi` | Go | `/edi/v1` (D22), `/as2/v1`, `/cxml/v1`, `/punchout/v1` | no |
| `apps/developer-portal` | none | `NOT-IMPLEMENTED.md` only (Backstage scaffold awaits a licensing decision) | n/a |

## Interfaces
- integration-hub-ts: `/v1/hub/subscriptions` (`:test`, `:rotate`, `:resume`, `:replay`), `/v1/hub/deliveries` (`:replay`), `/v1/hub/dead-letters`, `/v1/hub/clients`, `/v1/hub/catalog`.
- Webhooks are signed with the header `Depot-Signature`, HMAC-SHA256 over timestamp and body (`services/integration-hub-ts/src/webhook.ts`); the design named the header `X-Depot-Signature` (pattern `webhook-signature`, drift).
- edi: `/edi/v1/partners`, `/edi/v1/interchanges` (`:reprocess`, `:resend`), `/edi/v1/messages`, `/edi/v1/einvoice/*`, `/edi/v1/peppol/invoices:build`, `/edi/v1/punchout/*`, `/edi/v1/self-billing/*`; AS2 at `/as2/v1/{endpoint}` through its own Gateway `depot-as2`, authenticated by the partner's AS2 signature.
- Designed: `/v1/integration/event-replays`, tenant egress topics and a partner event relay — not built (D24, Q5).

## Data Ownership
Subscriptions, deliveries, dead letters, API clients and catalogue entries (integration-hub-ts, own database, no
legal-entity policy, D17); partner profiles, interchanges, messages, e-invoice mandates, punch-out sessions (edi).

## Events Produced
`depot.hub.subscription`, `depot.hub.webhook`, `depot.hub.apiclient`, `depot.hub.catalogentry`; `depot.edi.message`,
`depot.edi.einvoice`, `depot.edi.delivery`, `depot.edi.dispute`, `depot.edi.punchout`, `depot.edi.selfbilling`. None
relayed (D2).

## Events Consumed
integration-hub-ts consumes domain topics to deliver webhooks (`services/integration-hub-ts/src/consumer.ts`, kafkajs);
edi has consumer code (`deploy/kafka/edi-consumer.yaml`). Neither declares `receive` operations in AsyncAPI.

## Dependencies
internal: every context that publishes events (webhook sources), sales and procurement (EDI documents), platform,
eventing-backbone. external: trading partners (AS2, SFTP, Peppol access points), e-invoice platforms.

## Gotchas & Constraints
- Webhook delivery can only carry events that reach Kafka; most outboxes are not relayed (D2).
- Egress to partner URLs must block RFC 1918, link-local and metadata addresses.
- edi uses a non-`/v1` prefix (D22).

## Target — not built
- Event replay API and longer retention (D24); tenant egress topics and their relay (Q5).
- Relays for integration-hub-ts and edi (D2); legal-entity policy in integration-hub-ts (D17).
- Developer portal (`apps/developer-portal/NOT-IMPLEMENTED.md`).

## Source Sections
- Architecture §5.17 integration endpoints (target), §7.7 external events and webhooks; events §11 webhooks, §4.3 tenant isolation.
- ADR-0008; delta §1.1 (edi, integration-hub-ts), §1.2 (developer-portal), §2.1 (integration row), §2.2 (`webhook-signature`).
