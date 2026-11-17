-- Application role privileges. depot_app (no BYPASSRLS) works under the row-level
-- security policies. Tables the service updates carry UPDATE; the rest are insert-only.
-- Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT ON outbox, idempotency_keys TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON expense_line TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON expense_report TO depot_app';
  END IF;
END
$$;
