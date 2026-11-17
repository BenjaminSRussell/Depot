DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON print_device, print_routing_rule TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON print_job TO depot_app';
  END IF;
END
$$;
