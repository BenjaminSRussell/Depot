# Shop floor legal-entity backfill

Shop floor migration `0039_worker_legal_entity.sql` (B-03b, architecture gap D17) scopes a worker's time and pay to
the legal entity that employs it. Workers and payroll export batches created before it have no legal entity: they
and their registrations, days, pay lines, corrections and cost rates are visible only to all-entity reads. From
the moment the migration runs, every scoped user sees no existing workers:

- kiosk badges of existing workers no longer resolve (SF-0001);
- clock-ins on them are refused;
- supervisors see empty lists;
- old payroll exports are hidden.

Run this backfill in the same deploy window, before users come back.

## Who

A tenant administrator of each tenant: a principal with **no legal-entity claim** that holds `tenant.administer` and
`shopfloor.setup.maintain`. A caller with a claim gets `403 AUTH-0003`.

## Tenant with one legal entity

One call per tenant assigns every unassigned worker to the legal entity, then assigns every old export batch whose
lines all belong to its workers:

```
POST /v1/shopfloor/workers:assign-legal-entity
Idempotency-Key: backfill-<tenant>-1
{"legalEntityId": "<legal entity uuid>", "allUnassigned": true}
```

The response lists `assigned`, `unchanged`, `conflicts` and `notFound` worker ids, and the `exportBatches` count. Each
assigned worker publishes `dev.depot.shopfloor.worker.assigned.v1`, with the administrator in `assignedBy`.

## Tenant with several legal entities

The shop floor has no copy of the tenant's legal entities. Map each worker to its employer from HR or payroll (the
worker's `partyId` is the employee party).

1. List the workers. This needs a read with the all-entity scope: `GET /v1/shopfloor/workers` with
   `X-Depot-LegalEntity-Scope: ALL`, by a caller holding `org.switcher.all-scope`. Workers with `"legalEntityId": null`
   are the ones to assign.
2. Assign each legal entity's workers with one call. A call takes at most 1,000 ids:
   ```
   POST /v1/shopfloor/workers:assign-legal-entity
   {"legalEntityId": "<A>", "workerIds": ["<worker>", ...]}
   ```
3. Repeat step 2 for every legal entity, until a read in step 1 shows no worker with `legalEntityId` null.

A worker already in another legal entity is reported in `conflicts` and is not moved. To correct a wrong assignment,
call `POST /v1/shopfloor/workers/{id}:assign-legal-entity` for that worker. This works only while the worker has no
registration, day, correction or cost rate. After that the legal entity never changes (SF-0200).

## Export batches

A batch with lines from workers of several legal entities keeps no legal entity: no single company owns that payroll
file. Its lines stay visible, and are never exported again, through each line's worker. Only an all-entity read lists
the batch itself.

## Check

Run as the migration owner, or as `depot_app` with `depot.legal_entity_scopes` set to `*`. Both queries should return
0, apart from mixed batches:

```sql
select count(*) from worker where legal_entity_id is null;
select count(*) from pay_export_batch where legal_entity_id is null;
```
