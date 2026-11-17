DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'debezium_cdc_migration') THEN
    EXECUTE 'GRANT USAGE ON SCHEMA migration TO debezium_cdc_migration';
    EXECUTE 'GRANT SELECT ON migration.outbox TO debezium_cdc_migration';
  ELSE
    RAISE WARNING 'role debezium_cdc_migration does not exist: the outbox-migration connector cannot read migration.outbox; create the role, then delete migration 0023 from schema_migrations and restart to re-run this grant';
  END IF;
END
$$;
