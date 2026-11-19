-- The egress guard result of a model call, stored on agent_action (DEPOT-304): ids, classes, counts and reasons only, never values.
-- Migrations must run as the owner role, as in 0001.

DO $do$
BEGIN
  IF current_user = 'depot_app' THEN
    RAISE EXCEPTION 'agents migrations must run as the owner role, not depot_app';
  END IF;
END
$do$;

ALTER TABLE agent_action ADD COLUMN guard_result jsonb;
