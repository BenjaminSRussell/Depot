DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON dock_appointment, location_directive TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON dock_door, location, receipt, receipt_line, work, work_line TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE, DELETE ON location_stock, idempotency_keys TO depot_app';
  END IF;
END
$$;
