alter table batch_run add column if not exists lease_token uuid;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'depot_app') THEN
    EXECUTE 'GRANT UPDATE (lease_token) ON batch_run TO depot_app';
  END IF;
END
$$;
