-- Application role privileges. depot_app (no BYPASSRLS) works through the RLS
-- policies; it never owns the tables. recognition_run is append-only, the outbox is
-- insert-only (it is excluded from RLS and only the relay reads it), and nothing is
-- deleted. Skipped when the role does not exist (developer databases without the role split).
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON revenue_contract, performance_obligation, revenue_schedule TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON recognition_run TO depot_app';
    EXECUTE 'GRANT INSERT ON outbox TO depot_app';
  END IF;
END
$$;
