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

## Who and what it needs

- A tenant administrator of each tenant: a principal with **no legal-entity claim** that holds `tenant.administer` and
  `shopfloor.setup.maintain`. A caller with a claim gets `403 AUTH-0003`.
- The shop floor API configured with `identityUrl` and `serviceIdentity` (chart values). Every call confirms the legal
  entity with identity: an id the tenant does not have, or an inactive one, is `422 SF-0201`; identity not answering,
  or the check not configured, is `503 SF-0202`. Nothing is written in either case.
- A worker's legal entity freezes as soon as the worker has any registration, day, correction or cost rate. Legacy
  workers usually have history, so **their first assignment is final**. Always run the dry run first and read it.

## Tenant with one legal entity

1. Dry run. It checks the legal entity with identity and reports, writing nothing:
   ```
   POST /v1/shopfloor/workers:assign-legal-entity
   Idempotency-Key: backfill-<tenant>-dry
   {"legalEntityId": "<legal entity uuid>", "allUnassigned": true, "dryRun": true}
   ```
   Check that `assignedCount` plus `remaining` is the number of legacy workers you expect, and that `exportBatches`
   looks right.
2. Assign. Each call assigns at most 1,000 workers; repeat with a new `Idempotency-Key` until `remaining` is 0:
   ```
   POST /v1/shopfloor/workers:assign-legal-entity
   Idempotency-Key: backfill-<tenant>-1
   {"legalEntityId": "<legal entity uuid>", "allUnassigned": true}
   ```

The response reports `assignedCount`, `remaining`, `unchanged`, `conflicts` and `notFound`, and the `exportBatches`
count. Each assigned worker publishes `dev.depot.shopfloor.worker.assigned.v1`, with the administrator in
`assignedBy`. Every call also assigns the old export batches whose lines all belong to the legal entity's workers.

## Tenant with several legal entities

The shop floor has no copy of the tenant's legal entities. Map each worker to its employer from HR or payroll (the
worker's `partyId` is the employee party).

1. List the workers. This needs a read with the all-entity scope: `GET /v1/shopfloor/workers` with
   `X-Depot-LegalEntity-Scope: ALL`, by a caller holding `org.switcher.all-scope`. Workers with `"legalEntityId": null`
   are the ones to assign.
2. Assign each legal entity's workers with one call. A call takes at most 1,000 ids. Send it with `"dryRun": true`
   first and check the `assigned`, `conflicts` and `notFound` lists:
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
