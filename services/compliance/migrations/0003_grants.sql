-- Least-privilege grants for the application role (depot_app, NOBYPASSRLS).
-- sod_mitigation and outbox are append-only; idempotency_keys is claimed then completed.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO depot_app', current_schema());
    EXECUTE 'GRANT INSERT ON outbox TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT ON sod_mitigation TO depot_app';
    EXECUTE 'GRANT SELECT, INSERT, UPDATE ON sod_conflict, idempotency_keys TO depot_app';
  END IF;
END
$$;
