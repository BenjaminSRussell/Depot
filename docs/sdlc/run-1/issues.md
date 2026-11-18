# SDLC run 1 — GitHub issues

GitHub issues for the run 1 backlog; filed 2026-11-17.

| Backlog id | Issue | Priority | Title |
|---|---|---|---|
| B-01 | [#660](https://github.com/BenjaminSRussell/Depot/issues/660) | P0 | Close the forged-identity path: default-deny NetworkPolicies and signed service calls between peers |
| B-02 | [#661](https://github.com/BenjaminSRussell/Depot/issues/661) | P0 | Make the Kubernetes request path work: shell served, every API routed, real tokens accepted |
| B-03 | [#662](https://github.com/BenjaminSRussell/Depot/issues/662) | P0 | Enforce legal-entity scope end to end and let multi-company users switch company safely |
| B-04 | [#663](https://github.com/BenjaminSRussell/Depot/issues/663) | P0 | One authorization enforcement model across the three gateways, with a logged allow/deny record per decision |
| B-12 | [#664](https://github.com/BenjaminSRussell/Depot/issues/664) | P1 | Access follows employment: HR hire/terminate provisions Keycloak, admins can end sessions, realm and Temporal guard rails |
| B-19 | [#665](https://github.com/BenjaminSRussell/Depot/issues/665) | P1 | Dimension security: readers see only the cost centres and dimensions they are entitled to |
| B-05 | [#666](https://github.com/BenjaminSRussell/Depot/issues/666) | P0 | Make events flow: an outbox connector for every service, a Kafka user for every consumer, and payloads consumers can use |
| B-11 | [#667](https://github.com/BenjaminSRussell/Depot/issues/667) | P1 | Follow one user action end to end: OpenTelemetry in services and trace context in every event |
| B-06 | [#668](https://github.com/BenjaminSRussell/Depot/issues/668) | P0 | Audit service: record only the verified actor; refuse a `userId` in the request body |
| B-07 | [#669](https://github.com/BenjaminSRussell/Depot/issues/669) | P0 | CDC-fed, hash-chained audit trail with verified actor and trace, proven on the A7 set |
| B-08 | [#670](https://github.com/BenjaminSRussell/Depot/issues/670) | P0 | Fix two correctness defects: period close order (FR-14.2) and smart price rounding at band edges (FR-26.7) |
| B-08b | [#671](https://github.com/BenjaminSRussell/Depot/issues/671) | P0 | Fix the scheduling tie-breaks: dispatch (FR-23.5) and changeover sequencing (BR-23.12) break ties on order number, not order uuid |
| B-08c | [#672](https://github.com/BenjaminSRussell/Depot/issues/672) | P0 | Close the closed-period race: a posting must not land in a period that has just closed (FR-14.1) |
| B-35 | [#673](https://github.com/BenjaminSRussell/Depot/issues/673) | P1 | Gap-free fiscal numbers cannot be burned (D21), and administrators can maintain number sequences |
| B-36 | [#674](https://github.com/BenjaminSRussell/Depot/issues/674) | P1 | D3 completed: audit ingestion for every remaining entity with a mutable table, one contract test per owning service |
| B-09 | [#676](https://github.com/BenjaminSRussell/Depot/issues/676) | P0 | Make stage and prod deployable: platform services, every database and secret, migration Jobs, pinned charts and digests |
| B-10 | [#677](https://github.com/BenjaminSRussell/Depot/issues/677) | P0 | Meet ADR-0011: key custody and rotation for field encryption, and erasure that reaches every copy |
| B-13 | [#678](https://github.com/BenjaminSRussell/Depot/issues/678) | P1 | Receiving and vendor invoices update stock and post purchase price variance |
| B-14 | [#679](https://github.com/BenjaminSRussell/Depot/issues/679) | P1 | Shipments and production move stock and reach the ledger: packing slip and production issues, finished goods, COGS |
| B-15 | [#680](https://github.com/BenjaminSRussell/Depot/issues/680) | P1 | Order to cash from the source: sales orders priced by the pricing engine, invoices created from shipments and taxed by the tax engine |
| B-16 | [#681](https://github.com/BenjaminSRussell/Depot/issues/681) | P1 | Planning and promising read real data: MRP from live demand and supply, a stored ATP projection |
| B-39 | [#682](https://github.com/BenjaminSRussell/Depot/issues/682) | P1 | Blocks are enforced: blocked products cannot be bought, sold, moved or produced, blocked vendors cannot be ordered from |
| B-17 | [#683](https://github.com/BenjaminSRussell/Depot/issues/683) | P1 | Foreign-currency postings are correct: three-currency journal lines, rounding residuals and posted FX revaluation |
| B-18 | [#684](https://github.com/BenjaminSRussell/Depot/issues/684) | P1 | Postings that never reach the ledger or cannot be undone: tax liability and settlement, freight accrual, payment reversal and bank returns |
| B-52 | [#685](https://github.com/BenjaminSRussell/Depot/issues/685) | P1 | Subledger values reach the ledger and tie out: standard-cost revaluation and adjustment vouchers, opening balances in period 0, daily inventory, GR/IR and asset reconciliations |
| B-20 | [#686](https://github.com/BenjaminSRussell/Depot/issues/686) | P1 | UX slice 0: usability baseline stage A, task scripts, and a release gate that cannot pass by default |
| B-21 | [#687](https://github.com/BenjaminSRussell/Depot/issues/687) | P1 | UX slice 1: honest screens, with first real admin pages (sequences, import) |
| B-22 | [#688](https://github.com/BenjaminSRussell/Depot/issues/688) | P1 | UX slice 2: guard rails — the shell works on a phone, no codes or UUID prompts in text, accessibility scan on every workspace route |
| B-23 | [#689](https://github.com/BenjaminSRussell/Depot/issues/689) | P1 | UX slice 3a: History on every key record and an audit page that answers "who changed what, when and why" |
| B-24 | [#690](https://github.com/BenjaminSRussell/Depot/issues/690) | P1 | UX slice 3b: audited actions — reasons on sensitive actions, second-person approval for vendor bank accounts and other protected fields, step-up |
| B-25 | [#691](https://github.com/BenjaminSRussell/Depot/issues/691) | P1 | UX slice 4: close workspace, first cut — period status by module, what blocks it, and the checklist run |
| B-26 | [#692](https://github.com/BenjaminSRussell/Depot/issues/692) | P1 | UX slice 5: defaults that fit, and start documents from their source (AP invoice from receipt, receipt from PO, drop-ship from the order line) |
| B-27 | [#693](https://github.com/BenjaminSRussell/Depot/issues/693) | P1 | UX slice 6: names, not ids — pickers instead of UUID fields and names instead of id columns |
| B-28 | [#694](https://github.com/BenjaminSRussell/Depot/issues/694) | P1 | UX slice 7: select many rows and act once — bulk actions, paste from a spreadsheet, undo, shared saved views |
| B-29 | [#695](https://github.com/BenjaminSRussell/Depot/issues/695) | P1 | UX slice 8: a full-screen kiosk for the shop floor — badge in, my jobs, good/scrap with reason buttons, andon |
| B-30 | [#696](https://github.com/BenjaminSRussell/Depot/issues/696) | P1 | UX slice 9: warehouse handheld, first cut — device sign-in and directed receive, put-away and pick |
| B-31 | [#697](https://github.com/BenjaminSRussell/Depot/issues/697) | P2 | UX slices 10 and 13: keyboard line grid for documents, and the form standard (early validation, never lose work) everywhere |
| B-32 | [#698](https://github.com/BenjaminSRussell/Depot/issues/698) | P2 | UX slices 11 and 14: guided flows, first-run journeys, help on every page and a guide for every role |
| B-33 | [#699](https://github.com/BenjaminSRussell/Depot/issues/699) | P2 | UX slices 15 and 20: portals on the shared standard, trimmed navigation, server-stored workspaces, accessibility CI for portals and technician |
| B-34 | [#700](https://github.com/BenjaminSRussell/Depot/issues/700) | P2 | UX slice 16 + D29: long-running work is tracked — one job contract, a real job monitor, cron scheduling |
| B-53 | [#701](https://github.com/BenjaminSRussell/Depot/issues/701) | P2 | UX slices 17 and 18: complete admin (alerts, documents, texts) and complete close (reopen approval, year-end, allocations, accruals) |
| B-54 | [#702](https://github.com/BenjaminSRussell/Depot/issues/702) | P2 | UX slice 19: audited actions and History in every module |
| B-37 | [#703](https://github.com/BenjaminSRussell/Depot/issues/703) | P2 | Document-driven warehousing: work templates, waves, loads, transfer flow, license plates — and the complete handheld (UX slice 12) |
| B-44 | [#704](https://github.com/BenjaminSRussell/Depot/issues/704) | P2 | Plant and maintenance seams: document store, kanban production orders, instructions on the kiosk, maintenance parts and scheduling, automatic quality inspections |
| B-38 | [#705](https://github.com/BenjaminSRussell/Depot/issues/705) | P2 | Time to pay and bill: weekly timesheets, resource bookings, payroll on a calendar, project purchasing and project/service invoicing |
| B-40 | [#706](https://github.com/BenjaminSRussell/Depot/issues/706) | P2 | Intercompany trade without double keying, and production settlement with variances: mirror documents saga, settlement saga, variance workspace, workflow triggers |
| B-41 | [#707](https://github.com/BenjaminSRussell/Depot/issues/707) | P2 | Payments and treasury complete: terms and due dates, payment calendars, prepayments, NACHA/check/positive pay from runs, SEPA direct debit, bank transfers, reconciliation from stored balances |
| B-45 | [#708](https://github.com/BenjaminSRussell/Depot/issues/708) | P2 | Procure-to-pay and receivables controls: matching policies, an invoice exception queue, charges, purchasing policies, rebate accrual and settlement, phase-exit simulation |
| B-51 | [#709](https://github.com/BenjaminSRussell/Depot/issues/709) | P2 | AI assistants an approver can actually use: AP coding and cash application agents end to end, with an approval inbox |
| B-42 | [#710](https://github.com/BenjaminSRussell/Depot/issues/710) | P2 | Regulatory controls: electronic signatures, an evidence store auditors can use, SOC 2 control mapping, statutory e-filing |
| B-43 | [#711](https://github.com/BenjaminSRussell/Depot/issues/711) | P2 | Master data stays clean: duplicate check on create, stewardship queue, electronic addresses, one-time parties, code lists, lifecycle hooks, numbers from sequences |
| B-46 | [#712](https://github.com/BenjaminSRussell/Depot/issues/712) | P2 | Migration compliance and the data platform: PII-safe staging, mapping packs, rehearsal environments, Iceberg landing and historical load |
| B-47 | [#713](https://github.com/BenjaminSRussell/Depot/issues/713) | P2 | Posting and API conformance: one posting key, crash-safe valuation postings, one problem shape, kernel idempotency, consistent If-Match and custom methods |
| B-48 | [#714](https://github.com/BenjaminSRussell/Depot/issues/714) | P2 | Production readiness: partitioned journals and the NFR-P1 load test, production Postgres topology, Windows single-host proof, supply-chain gates |
| B-49 | [#715](https://github.com/BenjaminSRussell/Depot/issues/715) | P3 | Hygiene and low-severity guard rails: stale NOT-IMPLEMENTED lines, mislabeled FR ids, the flaky one-time-party test, retention classes and CI checks |
| B-50 | [#716](https://github.com/BenjaminSRussell/Depot/issues/716) | P3 | Should-level correspondence and confirmations: vendor and customer balance confirmations, AP correspondence, payments to one-time payees |
| B-55 | [#717](https://github.com/BenjaminSRussell/Depot/issues/717) | P3 | UX-GAP-1: fewer clicks for repeat orders — order templates, repeat last order, customer-specific quick entry (gated on the product-owner decision) |
