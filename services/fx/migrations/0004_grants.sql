DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON exchange_rate_type, provider_import_run TO depot_app';
    EXECUTE 'REVOKE UPDATE, DELETE, TRUNCATE ON exchange_rate, idempotency_keys FROM depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON exchange_rate, idempotency_keys TO depot_app';
    EXECUTE 'GRANT UPDATE (superseded_by) ON exchange_rate TO depot_app';
  END IF;
END
$$;
