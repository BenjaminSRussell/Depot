DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_projects') THEN
    EXECUTE format('GRANT USAGE ON SCHEMA %I TO debezium_cdc_projects', current_schema());
    EXECUTE 'GRANT SELECT ON outbox TO debezium_cdc_projects';
  ELSE
    RAISE WARNING 'role debezium_cdc_projects does not exist: the outbox-projects connector cannot read outbox; create the role, then delete migration 0012 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
