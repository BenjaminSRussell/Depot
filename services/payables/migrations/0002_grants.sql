DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ap_open_item TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON outbox, idempotency_keys TO depot_app';
  END IF;
END
$$;
