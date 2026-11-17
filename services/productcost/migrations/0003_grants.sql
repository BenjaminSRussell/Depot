DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON cost_group, cost_component TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON costing_version, idempotency_keys TO depot_app';
  END IF;
END
$$;
