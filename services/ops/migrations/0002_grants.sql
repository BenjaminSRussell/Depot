DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON ops_region TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON ops_failover TO depot_app';
  END IF;
END
$$;
