# Not implemented in DPT-5 slice 1

| Deliverable or AC | Status | Reason |
|---|---|---|
| Deliverable 3: non-continuous block preallocation and continuous return-to-pool | partial | All three modes persist and allocate under a row lock (gap-free semantics). AX-style segment caching and unused-number return are slice 2 |
| Shared and fiscal scopes, manual number override with reason (FR-5.8), `check_voucher_used` and the ledger journal number on the shared service (FR-11.6), sequence edit and the `/admin/sequences` create and edit page (B-35 slice 2) | slice 2 | line budget; per-LE scoped sequences cover the proof |
| Argo CD Application, ingress, image registry entry, Kubernetes Secret `numbering-db`, database `depot_numbering` | handoff | `deploy/` outside the service chart is wave 1 |
