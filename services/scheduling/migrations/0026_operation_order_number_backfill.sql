-- FR-23.5 fix (backlog B-08b), part 2: backfill operation order numbers.
--
-- 0025 added scheduling_operation.order_number. Operations imported from
-- production before it have no number of their own; this copies the number
-- the order import recorded, so GET /operations, runs and campaign sequencing
-- all read the same column. Operations made through POST /operations without
-- a number, and orders never imported, stay NULL (sorted after numbered
-- orders). An operation imported by a pod still on the old code between 0025
-- and this migration's deploy gets its number on the order's next import.
-- Data only: no new table, so the grants and the tenant policy of 0001 cover it.
SET LOCAL lock_timeout = '5s';

UPDATE scheduling_operation o
SET order_number = i.order_number
FROM scheduling_order_import i
WHERE o.order_number IS NULL
  AND i.tenant_id = o.tenant_id
  AND i.order_id = o.order_id
  AND i.order_number <> '';
