DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON idempotency_keys TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON consolidation_run, consolidation_balance, consolidation_entry, consolidation_statement_line TO depot_app';
  END IF;
END
$$;
