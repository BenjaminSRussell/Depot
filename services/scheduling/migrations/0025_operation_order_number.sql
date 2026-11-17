-- FR-23.5 fix (backlog B-08b): the order number on each operation.
--
-- The dispatch heuristic sorts orders by (delivery date, priority, order
-- number), but an operation only carried the order's uuid, so ties fell to
-- uuid order. order_number is the order's business number: given on POST
-- /operations, or set by the production order import from the event's
-- orderNumber. Numbers compare naturally (digit runs by value), since
-- production pads its numbers only to a minimum width and legacy numbers have
-- any shape. Operations created before this migration are backfilled from
-- scheduling_order_import by 0026; an order with no number sorts after
-- numbered ones, then by uuid. Expand-only: a nullable column, no rewrite and
-- no new table, so the grants and the tenant policy of 0001 cover it.
SET LOCAL lock_timeout = '5s';

ALTER TABLE scheduling_operation ADD COLUMN IF NOT EXISTS order_number text;
